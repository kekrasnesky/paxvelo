package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// A member's own page, opened from the "My membership" link in every club
// email. The link carries the same secret as their unsubscribe link, so there
// are no passwords: it shows whether this year's dues are paid and lets them
// update their phone, emergency contact and riding answers.

type membershipData struct {
	State  string // page, adult, find (no link), invalid, sent, test (the link in a test email)
	Token  string
	Member Member
	Form   memberDetails // what was typed, to show again after a mistake
	// What's on file, in part: club emails carry this page's link and get
	// forwarded, so it never shows a whole phone number or emergency contact.
	PhoneOnFile, EmergencyOnFile string
	Year                         int
	Paid                         bool
	PlanName                     string // "individual" or "family", for "your 2026 family dues"
	PayURL                       string
	PayLabel                     string
	Saved                        bool
	Message                      string // a problem with what was typed
	EmailsBack                   bool   // club emails were just turned back on
	Confirmed                    bool   // they just confirmed their email
	Email                        string // typed into "send me my link"
	// This year's insurance waiver (waivers.go): the signed one, or the form
	// to sign it.
	Waiver        *Waiver
	WaiverSigned  bool   // just signed
	WaiverMessage string // a problem signing it
	Signature     string // what was typed, to show again after a mistake
	WaiverTicked  bool
	// Waivers they signed this year as parent or guardian of riders under
	// 18, and the form to sign another.
	Minors       []Waiver
	MinorForm    minorForm
	MinorSigned  bool   // just signed one
	MinorMessage string // a problem signing it
	// Other adults on their Family plan (household.go), with their waivers;
	// and for one of those adults, opening their own link, who they are.
	Household        []householdStatus
	HouseholdRoom    bool   // room to add another
	HouseholdDone    string // just added, sent or removed
	HouseholdMessage string // a problem with that
	HouseholdForm    adultRow
	Adult            *HouseholdAdult
}

// membershipURL is a member's private link to their page.
func (c Config) membershipURL(token string) string {
	return c.SiteURL + "/membership?t=" + url.QueryEscape(token)
}

func (a *App) membershipView(m Member, token string) membershipData {
	year := a.cfg.ThisYear()
	d := membershipData{State: "page", Token: token, Member: m, Year: year, Paid: m.PaidFor(year), PlanName: strings.ToLower(m.PlanName()),
		Form: memberDetails{Level: m.Level}, PhoneOnFile: phoneTail(m.Phone)}
	if m.EmergencyName != "" {
		d.EmergencyOnFile = shortName(m.EmergencyName)
		if tail := phoneTail(m.EmergencyPhone); tail != "" {
			d.EmergencyOnFile += ", phone ending in " + tail
		}
	}
	if !d.Paid {
		if link := a.cfg.PayLink(m.Plan); link != "" {
			d.PayURL, d.PayLabel = link, fmt.Sprintf("Pay $%d with PayPal", a.cfg.Dues(m.Plan))
		}
	}
	if w, ok := a.store.WaiverFor(m.ID, year); ok {
		d.Waiver = &w
	}
	d.Minors = a.store.MinorWaivers(m.ID, year)
	d.Household = a.householdStatus(m, year)
	d.HouseholdRoom = m.Plan == "family" && len(m.Household) < maxHousehold
	return d
}

// phoneTail is the last four digits of a phone number, for "ending in 0123".
func phoneTail(phone string) string {
	if d := phoneDigits(phone); len(d) >= 4 {
		return d[len(d)-4:]
	}
	return ""
}

// shortName is a first name and last initial, like "Sam R.".
func shortName(name string) string {
	f := strings.Fields(name)
	if len(f) < 2 {
		return name
	}
	last := []rune(f[len(f)-1])
	return f[0] + " " + string(last[0]) + "."
}

func (a *App) renderMembership(w http.ResponseWriter, status int, d membershipData) {
	a.site.render(w, status, "membership", "/membership", "My Membership — "+siteName, d)
}

func (a *App) membershipPage(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	if t == "" {
		a.renderMembership(w, http.StatusOK, membershipData{State: "find"})
		return
	}
	if t == testToken { // the My membership link in a test email
		a.renderMembership(w, http.StatusOK, membershipData{State: "test"})
		return
	}
	_, _, v, ok := a.waiverSigner(t) // a member, or an adult on their Family plan
	if !ok {
		a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
		return
	}
	v.WaiverSigned = v.Waiver != nil && r.URL.Query().Get("signed") != ""
	v.MinorSigned = len(v.Minors) > 0 && r.URL.Query().Get("minor") != ""
	v.HouseholdDone = r.URL.Query().Get("household")
	a.renderMembership(w, http.StatusOK, v)
}

// saveMembership saves the details a member changed on their page. The page
// doesn't show what's on file in full, so a box left blank keeps it.
func (a *App) saveMembership(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	r.ParseForm()
	t := r.PostFormValue("t")
	m, ok := a.store.MemberByToken(t)
	if !ok {
		a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
		return
	}
	typed := memberDetails{Phone: strings.TrimSpace(r.FormValue("phone")), EmergencyName: r.FormValue("emergency_name"),
		EmergencyPhone: strings.TrimSpace(r.FormValue("emergency_phone")), Level: r.FormValue("level")}
	d := memberDetails{
		Phone: cleanPhone(typed.Phone), EmergencyName: cleanName(typed.EmergencyName),
		EmergencyPhone: cleanPhone(typed.EmergencyPhone), Level: cleanLevel(typed.Level),
	}
	problem := ""
	switch {
	case (typed.Phone != "" || m.Phone == "") && !phoneOK(typed.Phone):
		problem = "phone"
	case d.Level == "":
		problem = "level"
	case d.EmergencyName == "" && (m.EmergencyName == "" || typed.EmergencyPhone != ""):
		problem = "emergency" // a new emergency phone goes with a name
	case (typed.EmergencyPhone != "" || m.EmergencyPhone == "" || d.EmergencyName != "") && !phoneOK(typed.EmergencyPhone):
		problem = "emergency-phone" // and a new name with a phone
	}
	if problem != "" {
		v := a.membershipView(m, t)
		v.Message = joinProblems[problem]
		v.Form = typed
		a.renderMembership(w, http.StatusBadRequest, v)
		return
	}
	m, ok, err := a.store.UpdateMemberDetails(t, d)
	if err != nil || !ok {
		log.Printf("membership save: %v", err)
		v := a.membershipView(m, t)
		v.Message, v.Form = "Something went wrong on our end. Try again in a minute.", d
		a.renderMembership(w, http.StatusInternalServerError, v)
		return
	}
	v := a.membershipView(m, t)
	v.Saved = true
	a.renderMembership(w, http.StatusOK, v)
}

// resubscribe turns club emails back on for someone who unsubscribed. Only
// the member's own link can do that.
func (a *App) resubscribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	t := r.PostFormValue("t")
	m, ok, err := a.store.Resubscribe(t)
	if err != nil {
		log.Printf("resubscribe: %v", err)
	}
	if !ok {
		a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
		return
	}
	v := a.membershipView(m, t)
	v.EmailsBack = true
	a.renderMembership(w, http.StatusOK, v)
}

// sendMembershipLink emails a member their link. It says the same thing
// whether or not the address is on the list, so it can't be used to find
// out who's a member.
func (a *App) sendMembershipLink(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	typed := strings.TrimSpace(r.PostFormValue("email"))
	d := membershipData{State: "find", Email: typed}
	email, ok := cleanEmail(typed)
	switch {
	case !ok:
		d.Message = joinProblems["email"]
	case !a.limit.Allow("mylink:"+clientIP(r), 5, time.Hour) || !a.limit.Allow("mylink:"+email, 3, time.Hour):
		d.Message = "Too many links sent. Try again in an hour."
	default:
		if m, found := a.store.MemberByEmail(email); found && m.Unconfirmed {
			// Their link is in the confirm email, which says what to press
			if err := a.sendConfirm(m); err != nil {
				d.Message = "The email didn’t send. Try again later."
				break
			}
		} else if found {
			body := fmt.Sprintf("Hi %s,\n\nHere's the link to your PaxVelo membership page. Check your %d dues, and update your phone and emergency contact:\n\n[[My membership](%s)]\n\nKeep this email: the link is just for you, and it doesn't expire.",
				or(m.FirstName(), "there"), a.cfg.ThisYear(), a.cfg.membershipURL(m.Token))
			if err := a.mailer.SendDirect(mailMyLink, m.Name, m.Email, "Your PaxVelo membership link", body); err != nil {
				d.Message = "The email didn’t send. Try again later."
				break
			}
		} else if m, h, found := a.store.HouseholdByEmail(email); found && h.LinkSent {
			if err := a.sendHouseholdLink(m, h); err != nil {
				d.Message = "The email didn’t send. Try again later."
				break
			}
		}
		d.State = "sent"
	}
	status := http.StatusOK
	if d.Message != "" {
		status = http.StatusBadRequest
	}
	a.renderMembership(w, status, d)
}

// ---- Confirming the email ----

// rideLevelsFor are the calendar levels that suit each answer on the join
// form, for the rides the confirm email suggests.
var rideLevelsFor = map[string][]string{
	"new": {"social"}, "some": {"social", "intermediate"},
	"experienced": {"intermediate", "elite"}, "racer": {"elite"},
}

// ridesFor is the next few rides on the road in the coming three weeks at a
// level that suits a member's answer on the join form (any, with no answer).
func (a *App) ridesFor(level string, n int) []RideOcc {
	want := rideLevelsFor[level]
	today := a.today()
	todayS, now := today.Format("2006-01-02"), time.Now().In(a.cfg.Location).Format("15:04")
	var out []RideOcc
	for _, o := range a.store.Rides(today, today.AddDate(0, 0, 21)) {
		if len(out) == n {
			break
		}
		if o.Cancelled || !o.IsRide() || o.IsVirtual() || o.Date == todayS && o.Time != "" && o.Time <= now { // a first ride is on the road
			continue
		}
		if len(want) == 0 || slices.ContainsFunc(o.Levels(), func(l rideLevel) bool { return slices.Contains(want, l.Key) }) {
			out = append(out, o)
		}
	}
	return out
}

// sendConfirm emails someone who joined on the website, or came back after
// unsubscribing, the link to confirm their address: their membership page,
// where they press Confirm my email. With it go their dues, if still owed,
// and the rides coming up that suit them. Paying doesn't wait for it.
func (a *App) sendConfirm(m Member) error {
	year := a.cfg.ThisYear()
	intro := "Thanks for joining PaxVelo! Press the button to confirm this is your email, so club news and ride updates reach you."
	if !m.Active() {
		intro = "Welcome back to PaxVelo! You unsubscribed from club emails earlier. Press the button to confirm this is your email, and they’ll come here again."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\n%s\n\n[[Confirm my email](%s#confirm)]", or(m.FirstName(), "there"), intro, a.cfg.membershipURL(m.Token))
	if pay := a.cfg.PayLink(m.Plan); pay != "" && !m.PaidFor(year) {
		fmt.Fprintf(&b, "\n\nHaven’t paid yet? [Pay your $%d %s dues for %d](%s).", a.cfg.Dues(m.Plan), strings.ToLower(m.PlanName()), year, pay)
	}
	if rides := a.ridesFor(m.Level, 3); len(rides) > 0 {
		b.WriteString("\n\nRides coming up that suit you:\n")
		for _, o := range rides {
			when := o.ShortDate()
			if o.Time != "" {
				when += ", " + o.TimeLabel()
			}
			fmt.Fprintf(&b, "\n- **%s:** [%s](%s/calendar/ride/%s)", when, o.Name, a.cfg.SiteURL, o.Key)
			if o.Place != "" {
				b.WriteString(", from " + o.Place)
			}
		}
	}
	fmt.Fprintf(&b, "\n\nEvery club ride is on the calendar: %s/calendar\n\nBefore your first ride, take a couple of minutes to read Bike Safety 101: %s/bike-safety\n\nThe button opens your membership page, where you can also check your dues and update your details. Keep this email: the link is just for you.", a.cfg.SiteURL, a.cfg.SiteURL)
	return a.mailer.SendDirect(mailConfirm, m.Name, m.Email, "Confirm your email for PaxVelo", b.String())
}

// confirmEmail is the Confirm my email button on a member's page.
func (a *App) confirmEmail(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	t := r.PostFormValue("t")
	m, ok, err := a.store.ConfirmEmail(t)
	if err != nil {
		log.Printf("confirm email: %v", err)
	}
	if !ok {
		a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
		return
	}
	v := a.membershipView(m, t)
	v.Confirmed = err == nil
	a.renderMembership(w, http.StatusOK, v)
}

// ---- Store ----

func (s *Store) MemberByEmail(email string) (Member, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.byEmail(email); m != nil {
		return *m, true
	}
	return Member{}, false
}

// UpdateMemberDetails saves what a member changed on their own page. Unlike
// the join form, it replaces what's on file, since only they have the link;
// anything left blank stays as it was.
func (s *Store) UpdateMemberDetails(token string, d memberDetails) (Member, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byToken(token)
	if m == nil {
		return Member{}, false, nil
	}
	for _, f := range []struct {
		to   *string
		from string
	}{{&m.Phone, d.Phone}, {&m.EmergencyName, d.EmergencyName}, {&m.EmergencyPhone, d.EmergencyPhone}, {&m.Level, d.Level}} {
		if f.from != "" {
			*f.to = f.from
		}
	}
	return *m, true, s.save()
}

func (s *Store) Resubscribe(token string) (Member, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byToken(token)
	if m == nil {
		return Member{}, false, nil
	}
	if m.Active() {
		return *m, true, nil
	}
	m.Unsubscribed = nil
	return *m, true, s.save()
}

func (s *Store) byToken(token string) *Member {
	if token == "" {
		return nil
	}
	for _, m := range s.d.Members {
		if m.Token == token {
			return m
		}
	}
	return nil
}

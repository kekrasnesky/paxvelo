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
	// The club kit order, while it's open (kit.go): their order, if any.
	KitOpen  bool
	KitName  string
	KitOrder *KitOrder
	Year                         int
	Paid                         bool
	PaidOn                       time.Time // when the club marked this year paid; zero if not known
	DuesExpire                   time.Time // when their dues run out (moved.go)
	LevelName                    string    // their riding answer, briefly
	DetailsDone                  bool      // phone, emergency contact and riding answer all on file
	PlanName                     string    // "individual" or "family", for "your 2026 family dues"
	PayURL                       string
	PayLabel                     string
	Saved                        bool
	Message                      string // a problem with what was typed
	EmailsBack                   bool   // club emails were just turned back on
	TopicsSaved                  bool   // they just changed which kinds of club email they get
	Confirmed                    bool   // they just confirmed their email
	Renewed                      bool   // they just confirmed the renewal they filled in on the join form
	AutoConfirm                  bool   // opened from the button in the confirm email: the page confirms itself
	RenewalDoes                  string // what confirming that renewal does, for the Confirm box
	Email                        string // typed into "send me my link"
	SignedOut                    bool   // they just signed out of this device
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
	d := membershipData{State: "page", Token: token, Member: m, Year: year, Paid: m.Paid(), PlanName: strings.ToLower(m.PlanName()),
		Form: memberDetails{Level: m.Level}, PhoneOnFile: phoneTail(m.Phone)}
	d.PaidOn = m.PaidOn[m.LastPaid()]
	d.DuesExpire = m.DuesExpire()
	if m.EmergencyName != "" {
		d.EmergencyOnFile = shortName(m.EmergencyName)
		if tail := phoneTail(m.EmergencyPhone); tail != "" {
			d.EmergencyOnFile += ", ···" + tail
		}
	}
	d.LevelName = m.LevelName()
	if shop := a.store.Kit(); shop.IsOpen(time.Now()) && d.Paid {
		d.KitOpen, d.KitName = true, or(shop.Name, "The club kit order")
		if shop.ClosesLabel() != "" {
			d.KitName += " is open until " + shop.ClosesLabel()
		} else {
			d.KitName += " is open"
		}
		if o, ok := a.store.KitOrderFor(m.ID); ok {
			d.KitOrder = &o
		}
	}
	d.DetailsDone = m.Phone != "" && m.EmergencyName != "" && m.EmergencyPhone != "" && d.LevelName != ""
	if !d.Paid {
		if link := a.cfg.PayLink(m.Plan); link != "" {
			d.PayURL, d.PayLabel = link, fmt.Sprintf("Pay $%d with PayPal", a.cfg.Dues(m.Plan))
		}
	}
	if w, ok := a.store.WaiverFor(m.ID, year); ok {
		d.Waiver = &w
	}
	d.Minors = a.store.MinorWaivers(m.ID, year)
	if m.RenewalWaiting() {
		d.RenewalDoes = a.renewalDoes(m)
	}
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

// memberCookie remembers a member's link on their own device, so My
// Membership in the menu opens their page without another email. It holds
// the same secret as their link, so it's HttpOnly, and "Sign out of this
// device" forgets it.
const memberCookie = "pv_member"

func (a *App) rememberMember(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: memberCookie, Value: token, Path: "/membership", MaxAge: maxAge,
		HttpOnly: true, Secure: strings.HasPrefix(a.cfg.SiteURL, "https://"), SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) membershipPage(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	remembered := false
	if t == "" {
		if c, err := r.Cookie(memberCookie); err == nil && c.Value != "" {
			t, remembered = c.Value, true
		}
	}
	if t == "" {
		a.renderMembership(w, http.StatusOK, membershipData{State: "find", SignedOut: r.URL.Query().Get("out") != ""})
		return
	}
	if t == testToken { // the My membership link in a test email
		a.renderMembership(w, http.StatusOK, membershipData{State: "test"})
		return
	}
	_, _, v, ok := a.waiverSigner(t) // a member, or an adult on their Family plan
	if !ok {
		if remembered { // a member since deleted: forget them here, and offer a fresh link
			a.rememberMember(w, "", -1)
			a.renderMembership(w, http.StatusOK, membershipData{State: "find"})
			return
		}
		a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
		return
	}
	if !remembered {
		a.rememberMember(w, t, 400*24*3600) // browsers cap it at 400 days
	}
	v.WaiverSigned = v.Waiver != nil && r.URL.Query().Get("signed") != ""
	v.MinorSigned = len(v.Minors) > 0 && r.URL.Query().Get("minor") != ""
	v.HouseholdDone = r.URL.Query().Get("household")
	// Back from Confirm, or Turn club emails back on: each redirects here, so
	// reloading or bookmarking the page works.
	q := r.URL.Query()
	v.Confirmed = q.Get("confirmed") != "" && !v.Member.Unconfirmed
	v.Renewed = q.Get("renewed") != "" && !v.Member.RenewalWaiting()
	v.EmailsBack = q.Get("emails") == "on" && v.Member.Emailable()
	v.TopicsSaved = q.Get("emails") == "saved"
	// The button in the confirm email: the page's script presses Confirm as
	// soon as a person's browser opens it, so one click does it. Loading the
	// link alone changes nothing, because email scanners open every link in
	// an email; a renewal must never be signed by one.
	v.AutoConfirm = q.Get("confirm") != "" && (v.Member.Unconfirmed || v.Member.RenewalWaiting())
	a.renderMembership(w, http.StatusOK, v)
}

// forgetMember is "Sign out of this device" on a member's page.
func (a *App) forgetMember(w http.ResponseWriter, r *http.Request) {
	a.rememberMember(w, "", -1)
	http.Redirect(w, r, "/membership?out=1", http.StatusSeeOther)
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

// resubscribe turns club emails back on for someone who unsubscribed, or
// (with do=topics) saves the kinds of club email they want. Only the
// member's own link can do either.
func (a *App) resubscribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	t := r.PostFormValue("t")
	if r.PostFormValue("do") == "topics" {
		_, ok, err := a.store.SetTopics(t, r.PostFormValue("newsletter") != "", r.PostFormValue("events") != "")
		if err != nil {
			log.Printf("email topics: %v", err)
		}
		if !ok {
			a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
			return
		}
		http.Redirect(w, r, "/membership?t="+url.QueryEscape(t)+"&emails=saved#emails", http.StatusSeeOther)
		return
	}
	m, ok, err := a.store.Resubscribe(t)
	if err != nil {
		log.Printf("resubscribe: %v", err)
	}
	if !ok {
		a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
		return
	}
	a.welcomeIfDue(m)
	http.Redirect(w, r, "/membership?t="+url.QueryEscape(t)+"&emails=on#emails", http.StatusSeeOther)
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
		if m, found := a.store.MemberByEmail(email); found && (m.Unconfirmed || m.RenewalWaiting()) {
			// Their link is in the confirm email, which says what to press
			if err := a.sendConfirm(m); err != nil {
				d.Message = "The email didn’t send. Try again later."
				break
			}
		} else if found {
			body := fmt.Sprintf("Hi %s,\n\nHere's the link to your PaxVelo membership page. Check your %d dues, and update your phone and emergency contact:\n\n[[My membership](%s)]\n\nKeep this email: the link is just for you, and it doesn't expire.",
				or(m.FirstName(), "there"), a.cfg.ThisYear(), a.cfg.membershipURL(m.Token))
			if err := a.mailer.SendMine(mailMyLink, m, "Your PaxVelo membership link", body); err != nil {
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
	return ridesFor(a.store, a.cfg.Location, level, n)
}

func ridesFor(store *Store, loc *time.Location, level string, n int) []RideOcc {
	want := rideLevelsFor[level]
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	todayS, clock := today.Format("2006-01-02"), now.Format("15:04")
	var out []RideOcc
	for _, o := range store.Rides(today, today.AddDate(0, 0, 21)) {
		if len(out) == n {
			break
		}
		if o.Cancelled || !o.IsRide() || o.IsVirtual() || o.Date == todayS && o.Time != "" && o.Time <= clock { // a first ride is on the road
			continue
		}
		if len(want) == 0 || slices.ContainsFunc(o.Levels(), func(l rideLevel) bool { return slices.Contains(want, l.Key) }) {
			out = append(out, o)
		}
	}
	return out
}

// sendConfirm emails the link to confirm their address to someone who joined
// on the website, came back after unsubscribing, or renewed on the join form:
// their membership page, where they press Confirm. With it go their dues, if
// still owed, and the rides coming up that suit them. Paying doesn't wait.
func (a *App) sendConfirm(m Member) error {
	year := a.cfg.ThisYear()
	renewing := m.RenewalWaiting()
	subject, button := "Confirm your email for PaxVelo", "Confirm my email"
	var intro, unexpected string
	switch {
	case !m.Active():
		unexpected = "If you weren’t expecting this email, no action is needed: you’ll stay unsubscribed unless you confirm."
		intro = "Welcome back to PaxVelo! You unsubscribed from club emails earlier. Press the button to confirm this is your email, and they’ll come here again."
		if renewing {
			intro += " It also " + a.renewalDoes(m) + " on the join form."
		}
	case renewing && !m.Unconfirmed:
		subject, button = "Finish renewing your PaxVelo membership", "Confirm it’s me"
		unexpected = "If you weren’t expecting this email, no action is needed: nothing on your membership changes unless you confirm."
		intro = "Someone, probably you, filled in PaxVelo’s join form with this email address. Press the button to confirm it was you: that " + a.renewalDoes(m) + "."
	case renewing:
		intro = "Thanks for joining PaxVelo! Press the button to confirm this is your email, so club news and ride updates reach you. It also " + a.renewalDoes(m) + " on the join form."
	}
	if intro == "" { // a new joiner: the email as admins wrote it (Email → Automations)
		e := a.store.ConfirmText()
		mine := a.mailer.personalize(a.fillConfirm(e.Body, m), m)
		return a.mailer.SendMine(mailConfirm, m, a.mailer.personalize(e.Subject, m), mine)
	}
	if unexpected == "" {
		unexpected = "If you didn’t sign up for PaxVelo, no action is needed: this address won’t get club emails unless it’s confirmed."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\n%s\n\n[[%s](%s&confirm=1#confirm)]", or(m.FirstName(), "there"), intro, button, a.cfg.membershipURL(m.Token))
	if pay := a.cfg.PayLink(m.Plan); pay != "" && !m.Paid() {
		fmt.Fprintf(&b, "\n\nHaven’t paid yet? [Pay your $%d %s dues for %d](%s).", a.cfg.Dues(m.Plan), strings.ToLower(m.PlanName()), year, pay)
	}
	if list := rideList(a.ridesFor(m.Level, 3), a.cfg.SiteURL); list != "" {
		b.WriteString("\n\n" + list)
	}
	fmt.Fprintf(&b, "\n\nEvery club ride is on the calendar: %s/calendar\n\nBefore your first ride, take a couple of minutes to read Bike Safety 101: %s/bike-safety\n\nThe button also opens your membership page, where you can check your dues and update your details any time. Keep this email: the link is just for you.\n\n%s", a.cfg.SiteURL, a.cfg.SiteURL, unexpected)
	return a.mailer.SendMine(mailConfirm, m, subject, b.String())
}

// renewalDoes says what confirming a renewal from the join form does: signs
// the waiver, unless it's signed already, and saves what was filled in.
func (a *App) renewalDoes(m Member) string {
	if m.PendingJoin == nil {
		return ""
	}
	if _, signed := a.store.WaiverFor(m.ID, m.PendingJoin.Year); signed {
		return "saves what you filled in"
	}
	return fmt.Sprintf("signs your %d insurance waiver and saves what you filled in", m.PendingJoin.Year)
}

// rideList is the rides coming up that suit someone, as a list for an email,
// or "" when there are none.
func rideList(rides []RideOcc, site string) string {
	if len(rides) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Rides coming up that suit you:\n")
	for _, o := range rides {
		when := o.ShortDate()
		if o.Time != "" {
			when += ", " + o.TimeLabel()
		}
		fmt.Fprintf(&b, "\n- **%s:** [%s](%s/calendar/ride/%s)", when, o.Name, site, o.Key)
		if o.Place != "" {
			b.WriteString(", from " + o.Place)
		}
	}
	return b.String()
}

// confirmEmail is the Confirm button on a member's page: Confirm my email,
// or Confirm it's me for a renewal from the join form, which this signs.
// It goes back to their page, so reloading it doesn't send the form again.
func (a *App) confirmEmail(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	t := r.PostFormValue("t")
	before, _ := a.store.MemberByToken(t)
	m, p, ok, err := a.store.ConfirmEmail(t)
	if err != nil {
		log.Printf("confirm email: %v", err)
	}
	if !ok {
		a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
		return
	}
	q := url.Values{"t": {t}}
	if before.Unconfirmed && err == nil {
		q.Set("confirmed", "1")
	}
	if p != nil && err == nil {
		riders := make([]joinRider, len(p.Riders))
		for i, k := range p.Riders {
			riders[i] = joinRider{k.Name, k.Age}
		}
		_, added := a.applyJoin(m, p.Year, p.Signature, p.Drawing, p.IP, riders, p.Adults)
		a.emailHousehold(m, added) // if their dues are paid; otherwise once they are
		q.Set("renewed", "1")
	}
	a.welcomeIfDue(m)
	http.Redirect(w, r, "/membership?"+q.Encode()+"#confirm", http.StatusSeeOther)
}

// welcomeIfDue sends a website joiner's welcome email once they're both
// marked paid and confirmed, whichever came last.
func (a *App) welcomeIfDue(m Member) {
	if !a.store.Welcome().Enabled {
		return
	}
	if m, due, err := a.store.TakeWelcome(m.ID, a.cfg.ThisYear()); err == nil && due {
		a.mailer.Go(func() { a.mailer.SendWelcome(m) })
	}
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
	if m.Active() && !m.Unconfirmed {
		return *m, true, nil
	}
	// Their own link proves the inbox is theirs, so this confirms the
	// address too; otherwise the page would say emails are on while they
	// still wait for the confirmation.
	m.Unsubscribed, m.Unconfirmed = nil, false
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

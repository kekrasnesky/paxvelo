package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A member's own page, opened from the "My membership" link in every club
// email. The link carries the same secret as their unsubscribe link, so there
// are no passwords: it shows whether this year's dues are paid and lets them
// update their phone, emergency contact and riding answers.

type membershipData struct {
	State      string // page, find (no link), invalid, sent
	Token      string
	Member     Member
	Form       memberDetails // what was typed, to show again after a mistake
	Year       int
	Paid       bool
	PlanName   string // "individual" or "family", for "your 2026 family dues"
	PayURL     string
	PayLabel   string
	Saved      bool
	Message    string // a problem with what was typed
	EmailsBack bool   // club emails were just turned back on
	Email      string // typed into "send me my link"
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
		Form: memberDetails{Phone: m.Phone, EmergencyName: m.EmergencyName, EmergencyPhone: m.EmergencyPhone, Level: m.Level}}
	if !d.Paid {
		if link := a.cfg.PayLink(m.Plan); link != "" {
			d.PayURL, d.PayLabel = link, fmt.Sprintf("Pay $%d with PayPal", a.cfg.Dues(m.Plan))
		}
	}
	if w, ok := a.store.WaiverFor(m.ID, year); ok {
		d.Waiver = &w
	}
	d.Minors = a.store.MinorWaivers(m.ID, year)
	d.MinorForm.Phone = m.Phone
	d.Household = a.householdStatus(m, year)
	d.HouseholdRoom = m.Plan == "family" && len(m.Household) < maxHousehold
	return d
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

// saveMembership saves the details a member changed on their page.
func (a *App) saveMembership(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	r.ParseForm()
	t := r.PostFormValue("t")
	m, ok := a.store.MemberByToken(t)
	if !ok {
		a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
		return
	}
	d := memberDetails{
		Phone: cleanPhone(r.FormValue("phone")), EmergencyName: cleanName(r.FormValue("emergency_name")),
		EmergencyPhone: cleanPhone(r.FormValue("emergency_phone")), Level: cleanLevel(r.FormValue("level")),
	}
	problem := ""
	switch {
	case len(phoneDigits(d.Phone)) != 10:
		problem = "phone"
	case d.Level == "":
		problem = "level"
	case d.EmergencyName == "":
		problem = "emergency"
	case len(phoneDigits(d.EmergencyPhone)) != 10:
		problem = "emergency-phone"
	}
	if problem != "" {
		v := a.membershipView(m, t)
		v.Message = joinProblems[problem]
		v.Form = memberDetails{Phone: r.FormValue("phone"), EmergencyName: r.FormValue("emergency_name"),
			EmergencyPhone: r.FormValue("emergency_phone"), Level: r.FormValue("level")}
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
		if m, found := a.store.MemberByEmail(email); found {
			body := fmt.Sprintf("Hi %s,\n\nHere's the link to your PaxVelo membership page. Check your %d dues, and update your phone and emergency contact:\n\n[[My membership](%s)]\n\nKeep this email: the link is just for you, and it doesn't expire.",
				or(m.FirstName(), "there"), a.cfg.ThisYear(), a.cfg.membershipURL(m.Token))
			if err := a.mailer.SendDirect(m.Name, m.Email, "Your PaxVelo membership link", body); err != nil {
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
// the join form, it replaces what's on file, since only they have the link.
func (s *Store) UpdateMemberDetails(token string, d memberDetails) (Member, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byToken(token)
	if m == nil {
		return Member{}, false, nil
	}
	m.Phone, m.EmergencyName, m.EmergencyPhone = d.Phone, d.EmergencyName, d.EmergencyPhone
	m.Level = d.Level
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

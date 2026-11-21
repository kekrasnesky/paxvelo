package main

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// A member's own admin page (/admin/members/{id}): their details to fix, the
// years they paid, the waivers they signed, and the adults on their Family
// plan. Delete lives here too, away from the list, so it's never one slip
// from Mark paid.

// memberView is the member page.
type memberView struct {
	Member
	Waivers     []Waiver     // every waiver they signed, for themselves or others, newest first
	RideWaivers []RideWaiver // ride waivers signed with their email
	Primary     *Member      // for a rider: the member whose Family plan they're on
	Card        []payRow     // dues paid by card through Stripe (stripe.go), newest first
	LeftPaying  time.Time    // when they last left the card payment page without paying, or zero
}

var errEmailTaken = errors.New("another member has that email")

func (a *App) memberPage(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	m, ok := a.store.MemberByID(id)
	if !ok {
		back(w, r, "/admin/members?err=no-member")
		return
	}
	v := adminView{Title: m.Name, Tab: "members", Year: a.cfg.ThisYear(), CanManage: a.isBoard(r)}
	v.Flash, v.Error = flashFrom(r)
	v.MemberPage = &memberView{Member: m, Waivers: a.store.WaiversOf(id), RideWaivers: a.store.RideWaiversOf(id)}
	v.MemberPage.Card, v.MemberPage.LeftPaying = a.memberPayments(m)
	if m.FamilyOf != 0 {
		if p, ok := a.store.MemberByID(m.FamilyOf); ok {
			v.MemberPage.Primary = &p
		}
	}
	a.renderAdmin(w, r, http.StatusOK, "member", v)
}

// saveFamily changes who's on a member's Family plan, from their page: add an
// adult (who is emailed their waiver link once the dues are paid), change
// their name or email, or take them off.
func (a *App) saveFamily(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	page := "/admin/members/" + strconv.Itoa(id)
	m, ok := a.store.MemberByID(id)
	if !ok || m.FamilyOf != 0 { // riders are changed on their plan's page
		back(w, r, page+"?err=save")
		return
	}
	name, email := r.FormValue("adult_name"), r.FormValue("adult_email")
	old := r.FormValue("old_email")
	switch r.FormValue("do") {
	case "add":
		if m.Plan != "family" {
			back(w, r, page+"?err=not-family")
			return
		}
		adults, problem := readHousehold([]adultRow{{Name: name, Email: email}}, m.Email)
		switch {
		case problem != "" || len(adults) == 0:
			back(w, r, page+"?err="+or(problem, "adult-name"))
			return
		case len(m.Household) >= maxHousehold:
			back(w, r, page+"?err=family-full")
			return
		}
		added, err := a.store.AddHousehold(id, adults, false)
		switch {
		case err != nil:
			back(w, r, page+"?err=save")
			return
		case len(added) == 0:
			back(w, r, page+"?err=adult-listed")
			return
		}
		a.emailHousehold(m, added) // sends the waiver link if their dues are paid
		back(w, r, page+"?ok=family-added")
	case "edit":
		adults, problem := readHousehold([]adultRow{{Name: name, Email: email}}, m.Email)
		if problem != "" || len(adults) == 0 {
			back(w, r, page+"?err="+or(problem, "adult-name"))
			return
		}
		switch err := a.store.UpdateHousehold(id, old, adults[0].Name, adults[0].Email); {
		case errors.Is(err, errEmailTaken):
			back(w, r, page+"?err=email-taken")
		case errors.Is(err, errAdultListed):
			back(w, r, page+"?err=adult-listed")
		case err != nil:
			back(w, r, page+"?err=save")
		default:
			back(w, r, page+"?ok=family-saved")
		}
	case "remove":
		if err := a.store.RemoveHousehold(id, old); err != nil {
			back(w, r, page+"?err=save")
			return
		}
		back(w, r, page+"?ok=family-removed")
	default:
		back(w, r, page+"?err=save")
	}
}

// saveMember changes a member's details, as typed on their page. Unlike the
// public forms it replaces what's on file, blanks included.
func (a *App) saveMember(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	page := "/admin/members/" + strconv.Itoa(id)
	d, problem := joinDetails(r, false)
	if problem != "" {
		back(w, r, page+"?err="+problem)
		return
	}
	switch err := a.store.UpdateMember(id, d); {
	case errors.Is(err, errEmailTaken):
		back(w, r, page+"?err=email-taken")
	case err != nil:
		back(w, r, page+"?err=save")
	default:
		back(w, r, page+"?ok=member-saved")
	}
}

// payMember marks a member paid for this year and sends what that sets off:
// their welcome email the first time, and waiver links for the other adults
// on their Family plan. It returns the flash code.
func (a *App) payMember(id int) (string, error) {
	if _, err := a.store.Pay(id, false); err != nil {
		return "", err
	}
	return a.afterPaid(id), nil
}

// afterPaid does what follows a payment, from Mark paid or Stripe: the
// welcome email when it's due, and waiver links for the other adults on a
// Family plan. It returns Mark paid's notice code.
func (a *App) afterPaid(id int) string {
	code := "paid"
	if a.store.Welcome().Enabled {
		switch m, due, err := a.store.TakeWelcome(id, a.cfg.ThisYear()); {
		case err == nil && due:
			a.mailer.Go(func() { a.mailer.SendWelcome(m) })
			code = "paid-welcome"
		case m.WelcomePending && m.Unconfirmed:
			code = "paid-wait" // it goes when they confirm their email
		}
	}
	if names := a.emailHousehold(a.store.UnsignedHousehold(id, a.cfg.ThisYear())); len(names) > 0 {
		code += "-links"
	}
	return code
}

// markPaidMany marks every ticked member paid at once, for the January rush.
func (a *App) markPaidMany(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	n, welcomes := 0, 0
	for _, s := range r.Form["id"] {
		id, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		code, err := a.payMember(id)
		if err != nil {
			back(w, r, "/admin/members?err=save")
			return
		}
		n++
		if strings.HasPrefix(code, "paid-welcome") {
			welcomes++
		}
	}
	q := url.Values{}
	if ret, err := url.Parse(r.FormValue("return")); err == nil && ret.Path == "/admin/members" {
		q = ret.Query()
	}
	q.Set("ok", "paid-many")
	q.Set("n", strconv.Itoa(n))
	q.Set("w", strconv.Itoa(welcomes))
	back(w, r, "/admin/members?"+q.Encode())
}

// ---- Store ----

func (s *Store) MemberByID(id int) (Member, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.byID(id); m != nil {
		return *m, true
	}
	return Member{}, false
}

// UpdateMember replaces a member's details with what an admin typed.
func (s *Store) UpdateMember(id int, d memberDetails) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(id)
	if m == nil {
		return nil
	}
	if o := s.byEmail(d.Email); o != nil && o.ID != id {
		return errEmailTaken
	}
	if d.Email != m.Email {
		for _, w := range s.d.Waivers {
			if w.MemberID == id {
				w.Account = d.Email
			}
		}
	}
	changed := d.Email != m.Email
	m.Name, m.Email, m.Plan = d.Name, d.Email, d.Plan
	m.Phone, m.EmergencyName, m.EmergencyPhone, m.Level = d.Phone, d.EmergencyName, d.EmergencyPhone, d.Level
	if changed {
		s.linkRideWaivers() // ride waivers signed with the new address are theirs
	}
	return s.save()
}

// WaiversOf lists every waiver a member signed, newest first.
func (s *Store) WaiversOf(memberID int) []Waiver {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Waiver
	for i := len(s.d.Waivers) - 1; i >= 0; i-- {
		if w := s.d.Waivers[i]; w.MemberID == memberID {
			c := *w
			c.Drawing = nil
			out = append(out, c)
		}
	}
	return out
}

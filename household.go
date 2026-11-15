package main

import (
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Other adults on a Family membership. The member who joined pays the dues
// and gets club emails, but the club's insurance needs every adult who rides
// to sign the waiver themselves, each year. So each adult listed gets their
// own private link, by email, to a page with just their waiver: the same
// /membership?t= address members use. Like the welcome email, it waits until
// an admin marks the member's dues paid, so nobody can use the join form to
// send emails to strangers; each year, marking them paid emails the adults
// who haven't signed that year's waiver yet.

const maxHousehold = 3 // adults on a Family membership besides the member

// HouseholdAdult is another adult who rides on a member's Family plan.
type HouseholdAdult struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Token string    `json:"token"` // the secret in their link to sign the waiver
	Added time.Time `json:"added"`
	// LinkSent: their link has been emailed to them, once the member's dues
	// were paid. Until then "Send me my link" doesn't send it either.
	LinkSent bool `json:"link_sent,omitempty"`
}

func (h HouseholdAdult) FirstName() string { return Member{Name: h.Name}.FirstName() }

// adultRow is an adult as typed on a form.
type adultRow struct{ Name, Email string }

// adultRows pairs typed names and emails, for the join form's rows.
func adultRows(names, emails []string) []adultRow {
	rows := make([]adultRow, maxHousehold)
	for i := range rows {
		if i < len(names) {
			rows[i].Name = names[i]
		}
		if i < len(emails) {
			rows[i].Email = emails[i]
		}
	}
	return rows
}

// readHousehold checks the adults typed on a form. Rows left empty are
// skipped, and so is anyone listed twice; each adult needs a first and last
// name and their own email address, not the member's (own).
func readHousehold(rows []adultRow, own string) ([]HouseholdAdult, string) {
	var out []HouseholdAdult
	for _, row := range rows {
		name, typed := cleanName(row.Name), strings.TrimSpace(row.Email)
		if name == "" && typed == "" {
			continue
		}
		email, ok := cleanEmail(typed)
		switch {
		case len(strings.Fields(name)) < 2:
			return nil, "adult-name"
		case !ok:
			return nil, "adult-email"
		case email == own:
			return nil, "adult-self"
		}
		if !slices.ContainsFunc(out, func(h HouseholdAdult) bool { return h.Email == email }) {
			out = append(out, HouseholdAdult{Name: name, Email: email})
		}
	}
	return out, ""
}

// emailHousehold emails their links to adults on a member's Family plan, in
// the background, if the member's dues are paid for this year, and returns
// their first names. Otherwise it sends nothing, and marking the dues paid
// sends them.
func (a *App) emailHousehold(m Member, adults []HouseholdAdult) []string {
	if !m.Paid() || len(adults) == 0 {
		return nil
	}
	var names, emails []string
	for _, h := range adults {
		names, emails = append(names, h.FirstName()), append(emails, h.Email)
		a.mailer.Go(func() { a.sendHouseholdLink(m, h) }) // a failure is logged by send; the member can send it again
	}
	if err := a.store.MarkLinksSent(m.ID, emails); err != nil {
		log.Printf("household links %s: %v", m.Email, err)
	}
	return names
}

// sendHouseholdLink emails an adult on a member's Family plan their link to
// sign this year's waiver.
func (a *App) sendHouseholdLink(m Member, h HouseholdAdult) error {
	body := fmt.Sprintf("Hi %s,\n\nYou’re on %s’s PaxVelo family membership. The club’s insurance, through the League of American Bicyclists, needs every adult who rides to sign its waiver once a year. Here’s your link to sign the %d waiver:\n\n[[Sign my waiver](%s)]\n\nIt takes a minute. Keep this email: the link is just for you, and it’s where you’ll sign each new year’s waiver.",
		or(h.FirstName(), "there"), m.Name, a.cfg.ThisYear(), a.cfg.membershipURL(h.Token))
	return a.mailer.SendDirect(mailWaiverLink, h.Name, h.Email, "Sign your PaxVelo insurance waiver", body)
}

// householdStatus is an adult on a member's Family plan, with this year's
// waiver if they've signed it.
type householdStatus struct {
	Adult  HouseholdAdult
	Waiver *Waiver
}

func (a *App) householdStatus(m Member, year int) []householdStatus {
	var out []householdStatus
	for _, h := range m.Household {
		s := householdStatus{Adult: h}
		if w, ok := a.store.HouseholdWaiver(m.ID, year, h.Email); ok {
			s.Waiver = &w
		}
		out = append(out, s)
	}
	return out
}

// adultView is an adult's own page, from their link: just their waiver.
func (a *App) adultView(m Member, h HouseholdAdult, token string) membershipData {
	year := a.cfg.ThisYear()
	d := membershipData{State: "adult", Token: token, Member: m, Adult: &h, Year: year}
	if w, ok := a.store.HouseholdWaiver(m.ID, year, h.Email); ok {
		d.Waiver = &w
	}
	return d
}

// waiverSigner finds whose link is signing a waiver: a member, or an adult
// on a member's Family plan. It returns the waiver they'd sign (who it
// covers, not yet signed), the name they sign with, and their page.
func (a *App) waiverSigner(t string) (Waiver, string, membershipData, bool) {
	// A rider's link is their adult link too (same token), so it's checked
	// first: riders sign their waiver on their plan, like any other adult.
	if m, h, ok := a.store.HouseholdByToken(t); ok {
		return Waiver{MemberID: m.ID, Household: true, Rider: h.Name, RiderEmail: h.Email}, h.Name, a.adultView(m, h, t), true
	}
	if m, ok := a.store.MemberByToken(t); ok {
		return Waiver{MemberID: m.ID}, m.Name, a.membershipView(m, t), true
	}
	return Waiver{}, "", membershipData{}, false
}

// saveHousehold adds an adult to a member's Family plan from the member's
// page, emailing them their link, or sends the link again, or removes them.
func (a *App) saveHousehold(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	r.ParseForm()
	t := r.PostFormValue("t")
	m, ok := a.store.MemberByToken(t)
	if !ok || m.FamilyOf != 0 { // riders are on someone else's plan: nothing to add to
		a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
		return
	}
	v := a.membershipView(m, t)
	listed := func(email string) (HouseholdAdult, bool) {
		i := slices.IndexFunc(m.Household, func(h HouseholdAdult) bool { return h.Email == email })
		if i < 0 {
			return HouseholdAdult{}, false
		}
		return m.Household[i], true
	}
	done := ""
	switch r.PostFormValue("do") {
	case "remove":
		if err := a.store.RemoveHousehold(m.ID, r.PostFormValue("email")); err != nil {
			log.Printf("household remove %s: %v", m.Email, err)
			v.HouseholdMessage = "Something went wrong on our end. Try again in a minute."
		}
		done = "removed"
	case "send":
		h, found := listed(r.PostFormValue("email"))
		switch {
		case !found:
		case !v.Paid:
			v.HouseholdMessage = fmt.Sprintf("We’ll email %s’s link once your %d dues are paid.", h.FirstName(), v.Year)
		case !a.limit.Allow("household:"+h.Email, 3, time.Hour):
			v.HouseholdMessage = fmt.Sprintf("We’ve emailed %s a few times already. Try again in an hour.", h.FirstName())
		case a.sendHouseholdLink(m, h) != nil:
			v.HouseholdMessage = "The email didn’t send. Try again later."
		default:
			a.store.MarkLinksSent(m.ID, []string{h.Email})
			done = "sent"
		}
	default:
		row := adultRow{Name: r.PostFormValue("adult_name"), Email: r.PostFormValue("adult_email")}
		adults, problem := readHousehold([]adultRow{row}, m.Email)
		switch {
		case m.Plan != "family":
			v.HouseholdMessage = "Other adults can ride on a Family membership."
		case problem != "" || len(adults) == 0:
			v.HouseholdMessage = joinProblems[or(problem, "adult-name")]
		case len(m.Household) >= maxHousehold:
			v.HouseholdMessage = fmt.Sprintf("A family membership has room for %d more adults.", maxHousehold)
		case !a.limit.Allow("household-add:"+t, 10, time.Hour):
			v.HouseholdMessage = "Too many adults added. Try again in an hour."
		default:
			if h, found := listed(adults[0].Email); found {
				v.HouseholdMessage = h.Name + " is already on your membership."
				break
			}
			added, err := a.store.AddHousehold(m.ID, adults, false)
			if err != nil {
				log.Printf("household add %s: %v", m.Email, err)
				v.HouseholdMessage = "Something went wrong on our end. Try again in a minute."
				break
			}
			done = "added-later" // their link waits for the dues
			if len(a.emailHousehold(m, added)) > 0 {
				done = "added"
			}
		}
		if v.HouseholdMessage != "" {
			v.HouseholdForm = row
		}
	}
	if v.HouseholdMessage != "" {
		a.renderMembership(w, http.StatusBadRequest, v)
		return
	}
	http.Redirect(w, r, "/membership?t="+url.QueryEscape(t)+"&household="+done+"#household", http.StatusSeeOther)
}

// ---- Store ----

// AddHousehold lists adults on a member's Family plan, up to maxHousehold,
// skipping anyone already listed, and returns the ones added, with their
// links. With onlyFirst (the public join form, where anyone can type anyone's
// email) it adds nobody to a member who already lists adults.
func (s *Store) AddHousehold(memberID int, adults []HouseholdAdult, onlyFirst bool) ([]HouseholdAdult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(memberID)
	if m == nil || (onlyFirst && len(m.Household) > 0) {
		return nil, nil
	}
	var added []HouseholdAdult
	for _, h := range adults {
		if len(m.Household) >= maxHousehold {
			break
		}
		if h.Email == m.Email || slices.ContainsFunc(m.Household, func(o HouseholdAdult) bool { return o.Email == h.Email }) {
			continue
		}
		h.Token, h.Added = randHex(16), time.Now().UTC()
		m.Household = append(m.Household, h)
		s.addRider(m, h)
		added = append(added, h)
	}
	if len(added) == 0 {
		return nil, nil
	}
	return added, s.save()
}

// addRider makes an adult on a Family plan a member record too, so club
// emails reach them. It shares the adult's link token, so the link in their
// waiver email is also their membership page. An email already on the list
// is left alone: that person already gets club emails. Callers hold s.mu.
func (s *Store) addRider(primary *Member, h HouseholdAdult) {
	if s.byEmail(h.Email) != nil {
		return
	}
	s.d.Members = append(s.d.Members, &Member{ID: s.d.NextMember, Name: h.Name, Email: h.Email, Token: h.Token,
		Joined: h.Added, Plan: primary.Plan, FamilyOf: primary.ID})
	s.d.NextMember++
}

// syncRiders copies each rider's dues and plan from the member they're on
// the plan of, so Paid and the emails about dues treat them the same. Their
// dues run from the plan's join day, not the day they were added. A rider
// with no plan left is kept as they are. Callers hold s.mu (save does).
func (s *Store) syncRiders() {
	for _, r := range s.d.Members {
		if r.FamilyOf == 0 {
			continue
		}
		p := s.byID(r.FamilyOf)
		if p == nil {
			continue
		}
		r.Plan, r.Joined = p.Plan, p.Joined
		r.PaidYears = slices.Clone(p.PaidYears)
		r.PaidOn = maps.Clone(p.PaidOn)
	}
}

// ensureRiders makes sure every adult on a Family plan has a rider record,
// for plans listed before riders were members. Run at startup. Callers hold s.mu.
func (s *Store) ensureRiders() {
	for _, m := range s.d.Members {
		for _, h := range m.Household {
			if s.byEmail(h.Email) == nil {
				s.addRider(m, h)
			}
		}
	}
}

// removeRider takes a rider's record off with their place on the plan.
// Callers hold s.mu.
func (s *Store) removeRider(primaryID int, email string) {
	s.d.Members = slices.DeleteFunc(s.d.Members, func(m *Member) bool {
		return m.FamilyOf == primaryID && m.Email == email
	})
}

// UpdateHousehold changes an adult's name and email on a member's Family
// plan, and their rider record with it.
func (s *Store) UpdateHousehold(memberID int, oldEmail, name, email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(memberID)
	if m == nil {
		return nil
	}
	i := slices.IndexFunc(m.Household, func(h HouseholdAdult) bool { return h.Email == oldEmail })
	if i < 0 {
		return nil
	}
	if o := s.byEmail(email); o != nil && o.ID != memberID && (o.FamilyOf != memberID || o.Email != oldEmail) {
		return errEmailTaken
	}
	if email == m.Email {
		return errEmailTaken
	}
	if slices.ContainsFunc(m.Household, func(h HouseholdAdult) bool { return h.Email == email && h.Email != oldEmail }) {
		return errAdultListed
	}
	m.Household[i].Name, m.Household[i].Email = name, email
	for _, r := range s.d.Members {
		if r.FamilyOf == memberID && r.Email == oldEmail {
			r.Name, r.Email = name, email
		}
	}
	return s.save()
}

var errAdultListed = errors.New("that adult is already on this family plan")

// MarkLinksSent notes that adults on a member's Family plan were emailed
// their links.
func (s *Store) MarkLinksSent(memberID int, emails []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(memberID)
	if m == nil {
		return nil
	}
	for i := range m.Household {
		if slices.Contains(emails, m.Household[i].Email) {
			m.Household[i].LinkSent = true
		}
	}
	return s.save()
}

// UnsignedHousehold lists the adults on a member's Family plan who haven't
// signed a year's waiver.
func (s *Store) UnsignedHousehold(memberID, year int) (Member, []HouseholdAdult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(memberID)
	if m == nil {
		return Member{}, nil
	}
	var out []HouseholdAdult
	for _, h := range m.Household {
		if !slices.ContainsFunc(s.d.Waivers, func(w *Waiver) bool {
			return w.MemberID == memberID && w.Year == year && w.Household && w.RiderEmail == h.Email
		}) {
			out = append(out, h)
		}
	}
	return *m, out
}

// RemoveHousehold takes an adult off a member's Family plan. Waivers they
// signed stay on record.
func (s *Store) RemoveHousehold(memberID int, email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(memberID)
	if m == nil {
		return nil
	}
	n := len(m.Household)
	m.Household = slices.DeleteFunc(m.Household, func(h HouseholdAdult) bool { return h.Email == email })
	if len(m.Household) == n {
		return nil
	}
	s.removeRider(memberID, email)
	return s.save()
}

// HouseholdByToken finds the adult a waiver link is for, and whose Family
// plan they're on.
func (s *Store) HouseholdByToken(token string) (Member, HouseholdAdult, bool) {
	return s.household(func(h HouseholdAdult) bool { return token != "" && h.Token == token })
}

// HouseholdByEmail finds an adult on someone's Family plan by email.
func (s *Store) HouseholdByEmail(email string) (Member, HouseholdAdult, bool) {
	return s.household(func(h HouseholdAdult) bool { return h.Email == email })
}

func (s *Store) household(match func(HouseholdAdult) bool) (Member, HouseholdAdult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.d.Members {
		for _, h := range m.Household {
			if match(h) {
				return *m, h, true
			}
		}
	}
	return Member{}, HouseholdAdult{}, false
}

// HouseholdWaiver returns the waiver an adult on a member's Family plan signed
// for a year, if they have.
func (s *Store) HouseholdWaiver(memberID, year int, email string) (Waiver, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.d.Waivers {
		if w.MemberID == memberID && w.Year == year && w.Household && w.RiderEmail == email {
			return *w, true
		}
	}
	return Waiver{}, false
}

// householdSigned is whether an adult on a member's Family plan has signed
// that year's waiver. Callers hold s.mu.
func (s *Store) householdSigned(memberID, year int, email string) bool {
	for _, w := range s.d.Waivers {
		if w.MemberID == memberID && w.Year == year && w.Household && w.RiderEmail == email {
			return true
		}
	}
	return false
}

package main

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// A member's own admin page (/admin/members/{id}): their details to fix, the
// years they paid, the waivers they signed, and the adults on their Family
// plan. Delete lives here too, away from the list, so it's never one slip
// from Mark paid.

// memberView is the member page.
type memberView struct {
	Member
	Waivers []Waiver // every waiver they signed, for themselves or others, newest first
}

var errEmailTaken = errors.New("another member has that email")

func (a *App) memberPage(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	m, ok := a.store.MemberByID(id)
	if !ok {
		back(w, r, "/admin/members?err=no-member")
		return
	}
	v := adminView{Title: m.Name, Tab: "members", Year: a.cfg.ThisYear(), CanManage: a.canManage(r)}
	v.Flash, v.Error = flashFrom(r)
	v.MemberPage = &memberView{Member: m, Waivers: a.store.WaiversOf(id)}
	a.renderAdmin(w, r, http.StatusOK, "member", v)
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
	if err := a.store.SetPaid(id, a.cfg.ThisYear(), true); err != nil {
		return "", err
	}
	code := "paid"
	if a.store.Welcome().Enabled {
		if m, due, err := a.store.TakeWelcome(id); err == nil && due {
			go a.mailer.SendWelcome(m)
			code = "paid-welcome"
		}
	}
	if names := a.emailHousehold(a.store.UnsignedHousehold(id, a.cfg.ThisYear())); len(names) > 0 {
		code += "-links"
	}
	return code, nil
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
	m.Name, m.Email, m.Plan = d.Name, d.Email, d.Plan
	m.Phone, m.EmergencyName, m.EmergencyPhone, m.Level = d.Phone, d.EmergencyName, d.EmergencyPhone, d.Level
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

package main

import (
	"net/url"
	"strings"
	"testing"
)

// An admin can fix a member's details on their page, but not take another
// member's email.
func TestEditMember(t *testing.T) {
	a, h, c := testApp(t)
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual", Phone: "301-555-0123"}, 2026)
	a.store.Join(memberDetails{Name: "Sam Rider", Email: "sam@example.com", Plan: "individual"}, 2026)

	ct, body := form(url.Values{"name": {"Pat Ryder"}, "email": {"pat.ryder@example.com"}, "phone": {""}, "plan": {"family"},
		"emergency_name": {"Kim Ryder"}, "emergency_phone": {"240-555-0100"}, "level": {"racer"}})
	w := do(t, h, c, "POST", "/admin/members/"+itoa(pat.ID), ct, body)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "ok=member-saved") {
		t.Fatalf("saving went to %q", loc)
	}
	m, _ := a.store.MemberByID(pat.ID)
	if m.Name != "Pat Ryder" || m.Email != "pat.ryder@example.com" || m.Phone != "" || m.Plan != "family" || m.EmergencyName != "Kim Ryder" || m.Level != "racer" {
		t.Errorf("member after editing: %+v", m)
	}

	ct, body = form(url.Values{"name": {"Pat Ryder"}, "email": {"sam@example.com"}, "plan": {"family"}})
	w = do(t, h, c, "POST", "/admin/members/"+itoa(pat.ID), ct, body)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "err=email-taken") {
		t.Errorf("taking another member's email went to %q", loc)
	}
	if m, _ := a.store.MemberByID(pat.ID); m.Email != "pat.ryder@example.com" {
		t.Errorf("email changed to %q", m.Email)
	}
	if w := do(t, h, c, "GET", "/admin/members/999", "", nil); !strings.Contains(w.Header().Get("Location"), "err=no-member") {
		t.Error("a missing member should go back to the list")
	}
}

// Ticking several members marks them all paid, keeping the list's filter.
func TestMarkPaidMany(t *testing.T) {
	a, h, c := testApp(t)
	var ids []string
	for _, e := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		m, _, _ := a.store.Join(memberDetails{Name: "Rider", Email: e, Plan: "individual"}, 2026)
		ids = append(ids, itoa(m.ID))
	}
	ct, body := form(url.Values{"id": ids[:2], "return": {"/admin/members?q=&dues=unpaid"}})
	w := do(t, h, c, "POST", "/admin/members/paid", ct, body)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "dues=unpaid") || !strings.Contains(loc, "ok=paid-many") || !strings.Contains(loc, "n=2") {
		t.Errorf("went to %q", loc)
	}
	year := a.cfg.ThisYear()
	paid := 0
	for _, m := range a.store.Members() {
		if m.PaidFor(year) {
			paid++
		}
	}
	if paid != 2 {
		t.Errorf("%d marked paid, want 2", paid)
	}
	if page := do(t, h, c, "GET", loc, "", nil).Body.String(); !strings.Contains(page, "Marked 2 members as paid.") {
		t.Error("the list should say how many were marked paid")
	}

	// Marking one paid from its own page goes back there.
	ct, body = form(url.Values{"paid": {"1"}, "return": {"/admin/members/" + ids[2]}})
	if loc := do(t, h, c, "POST", "/admin/members/"+ids[2]+"/paid", ct, body).Header().Get("Location"); loc != "/admin/members/"+ids[2]+"?ok=paid" {
		t.Errorf("marking paid from the member page went to %q", loc)
	}
}

// The email being written is kept as it's typed.
func TestEmailDraftSaves(t *testing.T) {
	a, h, c := testApp(t)
	ct, body := form(url.Values{"subject": {"Spring kickoff"}, "body": {"See you **Saturday**."}})
	if w := do(t, h, c, "POST", "/admin/email/draft", ct, body); w.Code != 204 {
		t.Fatalf("saving the draft = %d", w.Code)
	}
	if d := a.store.Draft(); d.Subject != "Spring kickoff" || d.Body != "See you **Saturday**." {
		t.Errorf("draft = %+v", d)
	}
	if w := do(t, h, nil, "POST", "/admin/email/draft", ct, body); w.Code == 204 {
		t.Error("saving a draft needs a login")
	}
}

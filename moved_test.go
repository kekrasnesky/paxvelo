package main

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// Dues run out on the day they joined, the year after they last paid.
func TestDuesExpire(t *testing.T) {
	m := Member{Joined: time.Date(2024, time.March, 14, 12, 0, 0, 0, time.UTC), PaidYears: []int{2024, 2026}}
	if got := m.DuesExpire().Format("2006-01-02"); got != "2027-03-14" {
		t.Errorf("expires %s", got)
	}
	if !(Member{Joined: m.Joined}).DuesExpire().IsZero() {
		t.Error("never paid: no expiry")
	}
}

// Marking an old-site member paid with the site-move email sends that email,
// not the welcome, and their page shows when their dues run out.
func TestSiteMoveEmail(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	joined := time.Date(year-2, time.June, 3, 12, 0, 0, 0, time.UTC)
	a.store.Import([]Member{
		{Name: "Old Ollie", Email: "ollie@example.com", PaidYears: []int{year}, Joined: joined},
		{Name: "Lapsed Lee", Email: "lee@example.com", PaidYears: []int{year - 1}, Joined: joined},
	})
	ollie, _ := a.store.MemberByEmail("ollie@example.com")
	lee, _ := a.store.MemberByEmail("lee@example.com")
	box := newMailbox(a)

	ct, body := form(url.Values{"id": {itoa(ollie.ID), itoa(lee.ID)}, "return": {"/admin/members?dues=paid"}})
	w := do(t, h, admin, "POST", "/admin/members/moved", ct, body)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "ok=moved") || !strings.Contains(loc, "dues=paid") {
		t.Fatalf("after sending: %s", loc)
	}
	expire := time.Date(year+1, time.June, 3, 0, 0, 0, 0, time.UTC).Format("January 2, 2006")
	for _, addr := range []string{"ollie@example.com", "lee@example.com"} {
		got := box.to(a, addr)
		if len(got) != 1 || got[0].subject != "PaxVelo has a new website" || !strings.Contains(got[0].body, "paid through "+expire) {
			t.Errorf("%s should get the site-move email with their expiry: %+v", addr, got)
		}
	}
	got, _ := a.store.MemberByID(lee.ID)
	if !got.PaidFor(year) || got.MovedSent == nil {
		t.Errorf("Lee should be marked paid and sent: %+v", got)
	}
	if page := do(t, h, admin, "GET", "/admin/members", "", nil).Body.String(); !strings.Contains(page, "Site-move email sent") || !strings.Contains(page, "Until ") {
		t.Error("the list should show the expiry and that the email went")
	}
	if page := do(t, h, nil, "GET", "/membership?t="+ollie.Token, "", nil).Body.String(); !strings.Contains(page, "Good through") {
		t.Error("their page should show when their dues run out")
	}
	if page := do(t, h, admin, "GET", "/admin/email", "", nil).Body.String(); !strings.Contains(page, `id="moved"`) {
		t.Error("the Email page should let admins edit the site-move email")
	}
}

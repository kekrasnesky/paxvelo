package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A copy of a member handed out by the store is read without the store's
// lock, so marking them paid (or undoing it) must not write to the map or
// list that copy holds: Go stops the whole program on a map read during a
// write.
func TestMemberCopiesKeepTheirOwnPaidDates(t *testing.T) {
	a, _, _ := testApp(t)
	m, _, err := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "family"}, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetPaid(m.ID, 2025, true); err != nil {
		t.Fatal(err)
	}
	before, _ := a.store.MemberByID(m.ID)
	if err := a.store.SetPaid(m.ID, 2026, true); err != nil {
		t.Fatal(err)
	}
	if len(before.PaidOn) != 1 {
		t.Fatalf("marking paid changed a copy made earlier: %v", before.PaidOn)
	}
	paid, _ := a.store.MemberByID(m.ID)
	if err := a.store.SetPaid(m.ID, 2026, false); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetRecord(m.ID, nil, time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	if len(paid.PaidOn) != 2 {
		t.Fatalf("undoing a payment changed a copy made earlier: %v", paid.PaidOn)
	}
	if now, _ := a.store.MemberByID(m.ID); len(now.PaidOn) != 0 || len(now.PaidYears) != 0 {
		t.Fatalf("the record still shows payments: %v %v", now.PaidYears, now.PaidOn)
	}

	// The same for the other adults on a Family plan.
	adults := []HouseholdAdult{{Name: "Sam Rider", Email: "sam@example.com"}, {Name: "Lee Rider", Email: "lee@example.com"}}
	if _, err := a.store.AddHousehold(m.ID, adults, false); err != nil {
		t.Fatal(err)
	}
	listed, _ := a.store.MemberByID(m.ID)
	if err := a.store.MarkLinksSent(m.ID, []string{"sam@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.RemoveHousehold(m.ID, "sam@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(listed.Household) != 2 || listed.Household[0].Email != "sam@example.com" || listed.Household[0].LinkSent || listed.Household[1].Email != "lee@example.com" {
		t.Fatalf("changing the household changed a copy made earlier: %+v", listed.Household)
	}
	if now, _ := a.store.MemberByID(m.ID); len(now.Household) != 1 || now.Household[0].Email != "lee@example.com" {
		t.Fatalf("household after removing Sam: %+v", now.Household)
	}
}

// A name typed on a public form goes into emails the site writes to admins.
// It must arrive as words, never as a link of the visitor's choosing.
func TestTypedNamesNeverBecomeLinks(t *testing.T) {
	for _, name := range []string{"[Open the Members page](https://evil.example/login)", "Pat https://evil.example/login", "[[Pay now](https://evil.example)]"} {
		html := string(emailHTML("**"+mailLiteral(name)+"** just joined.\n\n[Open the Members page](https://www.paxvelo.club/admin/members)", "https://www.paxvelo.club"))
		if strings.Contains(html, `href="https://evil.example`) {
			t.Errorf("%q became a link:\n%s", name, html)
		}
		if !strings.Contains(html, `href="https://www.paxvelo.club/admin/members"`) {
			t.Errorf("the site's own link is gone:\n%s", html)
		}
	}
	if got := mailLiteral("Robert (Bob) O’Neil-Smith"); got != "Robert (Bob) O’Neil-Smith" {
		t.Errorf("an ordinary name changed: %q", got)
	}

	a, _, _ := testApp(t)
	box := newMailbox(a)
	a.cfg.AdminEmail = "admin@example.com"
	a.mailer.cfg.AdminEmail = "admin@example.com"
	a.mailer.NotifySignup(Member{Name: "[Mark paid](https://evil.example)", Email: "new@example.com", Plan: "individual"})
	got := box.to(a, "admin@example.com")
	if len(got) != 1 {
		t.Fatalf("signup notices sent: %d", len(got))
	}
	if html := string(emailHTML(got[0].body, "")); strings.Contains(html, `href="https://evil.example`) {
		t.Errorf("the new member's name is a link in the admins' email:\n%s", html)
	}
}

func TestClipKeepsWholeCharacters(t *testing.T) {
	s := strings.Repeat("é", 400) // two bytes each: a cut at 300 bytes is fine, at 301 isn't
	for _, n := range []int{299, 300, 301} {
		got := clip(s, n)
		if !utf8.ValidString(got) || utf8.RuneCountInString(got) != n {
			t.Errorf("clip to %d: %d characters, valid %v", n, utf8.RuneCountInString(got), utf8.ValidString(got))
		}
	}
	if clip("short", 300) != "short" {
		t.Error("short text changed")
	}
}

// The API's "details" is the ride's description, as on the admin form: it
// keeps its lines and isn't cut at the 100 characters a name gets.
func TestAPIKeepsTheWholeDescription(t *testing.T) {
	a, h, _ := testApp(t)
	a.cfg.CalendarAPIKey = "secret-key"
	details := "Meet at the square at 8.\\nNo-drop, 30 miles, coffee stop in Leonardtown. " + strings.Repeat("Bring lights. ", 10)
	r := httptest.NewRequest("PUT", "/api/rides/long-1", strings.NewReader(`{"date":"2031-05-03","name":"Coffee Ride","details":"`+details+`"}`))
	r.Header.Set("Authorization", "Bearer secret-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	var got string
	for _, o := range a.store.Rides(time.Date(2031, 5, 3, 0, 0, 0, 0, time.UTC), time.Date(2031, 5, 3, 0, 0, 0, 0, time.UTC)) {
		if o.External == "api:long-1" {
			got = o.Details
		}
	}
	if !strings.Contains(got, "square at 8.\nNo-drop") || !strings.HasSuffix(got, "Bring lights.") {
		t.Errorf("details were cut or flattened: %q", got)
	}
}

func TestInstagramPostIDsAreSafeFileNames(t *testing.T) {
	for id, ok := range map[string]bool{"17912345678901234": true, "3a": true, "": false, "../paxvelo": false, `a\b`: false, "a/b": false, "a.b": false} {
		if instaIDRE.MatchString(id) != ok {
			t.Errorf("id %q allowed: %v, want %v", id, !ok, ok)
		}
	}
}

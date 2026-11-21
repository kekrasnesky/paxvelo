package main

import (
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The renewal and pay reminders (and the membership emails, stripe_test.go)
// are there from the start, switched on, and added once.
func TestRenewalReminderSeeded(t *testing.T) {
	a, h, admin := testApp(t)
	autos := a.store.Automations()
	if len(autos) != 5 || autos[0].Name != "Renewal reminder" || autos[0].Trigger != "expiring" || autos[0].Days != 30 || autos[0].Paused {
		t.Fatalf("seeded: %+v", autos)
	}
	if p := autos[1]; p.Name != "Pay reminder" || p.Paused || p.When() != "5 days after someone joins on the website, if they haven’t paid yet" {
		t.Fatalf("pay reminder: %+v", p)
	}
	if w := autos[0].When(); w != "30 days before a member’s dues run out" {
		t.Errorf("when: %q", w)
	}
	page := do(t, h, admin, "GET", "/admin/email/auto", "", nil).Body.String()
	if !strings.Contains(page, `id="a-`+strconv.Itoa(autos[0].ID)+`"`) || !strings.Contains(page, `aria-checked="true" aria-label="Renewal reminder"`) {
		t.Error("the Automations list should show the Renewal reminder, on")
	}
	if !strings.Contains(page, "Paid-up members (on a Family plan, only the one who pays)") || strings.Contains(page, "{dues_expire}") {
		t.Error("the row should say who gets it and show the subject filled in")
	}
	// Its editor has the switch too, and turning it off there pauses it.
	edit := do(t, h, admin, "GET", "/admin/email/auto/"+strconv.Itoa(autos[0].ID), "", nil).Body.String()
	if !strings.Contains(edit, `aria-checked="true" aria-label="Renewal reminder"`) || !strings.Contains(edit, `<span class="switch-state" aria-hidden="true">On</span>`) {
		t.Error("the editor should show the switch, on")
	}
	ct, body := form(url.Values{"paused": {"1"}})
	do(t, h, admin, "POST", "/admin/email/auto/"+strconv.Itoa(autos[0].ID)+"/pause", ct, body)
	if au, _ := a.store.Automation(autos[0].ID); !au.Paused {
		t.Error("the switch should turn it off")
	}
	again, err := openStore(a.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(again.Automations()); n != 5 {
		t.Fatalf("reopening the store should not add another: %d", n)
	}
}

// It goes 30 days before dues run out, once, to the member who pays: not a
// family rider, an unpaid member or an unsubscribed one.
func TestRenewalReminderSends(t *testing.T) {
	a, _, _ := testApp(t)
	var mu sync.Mutex
	var got []string
	a.mailer.onSend = func(to, subject, body string) { mu.Lock(); got = append(got, to+"|"+subject+"|"+body); mu.Unlock() }
	sent := func() []string { mu.Lock(); defer mu.Unlock(); out := got; got = nil; return out }

	loc := a.cfg.Location
	clubTZ = loc
	now := time.Now().In(loc)
	nine := time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, loc)
	expire := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 30)
	year := a.cfg.ThisYear()
	add := func(name, email, plan string, paid bool) Member {
		m, _, err := a.store.Join(memberDetails{Name: name, Email: email, Plan: plan}, year)
		if err != nil {
			t.Fatal(err)
		}
		a.store.mu.Lock()
		p := a.store.byID(m.ID)
		p.Joined, p.Unconfirmed, p.PaidYears = expire.AddDate(-1, 0, 0), false, nil
		if paid {
			p.PaidYears = []int{expire.Year() - 1}
		}
		a.store.save()
		m = *p
		a.store.mu.Unlock()
		return m
	}
	pat := add("Pat Holder", "pat@example.com", "family", true)
	if _, err := a.store.AddHousehold(pat.ID, []HouseholdAdult{{Name: "Jo Rider", Email: "jo@example.com"}}, false); err != nil {
		t.Fatal(err)
	}
	if jo, _ := a.store.MemberByEmail("jo@example.com"); jo.FamilyOf != pat.ID || !jo.Paid() || !jo.DuesExpire().Equal(expire) {
		t.Fatalf("rider: %+v", jo)
	}
	add("Una Unpaid", "una@example.com", "individual", false)
	gone := add("Gus Gone", "gus@example.com", "individual", true)
	if _, _, err := a.store.Unsubscribe(gone.Token); err != nil {
		t.Fatal(err)
	}
	add("Lou Later", "lou@example.com", "individual", true)
	a.store.mu.Lock()
	a.store.byEmail("lou@example.com").Joined = expire.AddDate(-1, 0, 1) // runs out the day after: due tomorrow
	a.store.mu.Unlock()

	a.runAutomations(nine)
	s := sent()
	if len(s) != 1 {
		t.Fatalf("sent: %q", s)
	}
	want := "pat@example.com|Your PaxVelo membership runs out " + expire.Format("January 2, 2006") + "|Hi Pat,"
	if !strings.HasPrefix(s[0], want) || !strings.Contains(s[0], "[[Renew now](") || !strings.Contains(s[0], "/membership?t=") {
		t.Fatalf("the email: %q", s[0])
	}

	a.runAutomations(nine.Add(time.Hour))
	if s := sent(); len(s) != 0 {
		t.Fatalf("sent twice: %q", s)
	}
}

// A deleted renewal reminder stays deleted when the site restarts.
func TestRenewalReminderStaysDeleted(t *testing.T) {
	a, h, admin := testApp(t)
	a.store.d.Automations = a.store.d.Automations[:1] // just the renewal reminder
	id := a.store.Automations()[0].ID
	do(t, h, admin, "POST", "/admin/email/auto/"+strconv.Itoa(id)+"/remove", "", nil)
	if n := len(a.store.Automations()); n != 0 {
		t.Fatalf("not removed: %d", n)
	}
	again, err := openStore(a.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(again.Automations()); n != 0 {
		t.Fatalf("a deleted renewal reminder came back: %d", n)
	}
}

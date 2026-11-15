package main

import (
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The day before an invitation, from 9 am, everyone who said yes or maybe
// gets a reminder with their answer and links, once; not those who can't
// come, not an invitation that skips it, not while it's switched off.
func TestRSVPReminder(t *testing.T) {
	a, h, admin := testApp(t)
	var mu sync.Mutex
	var got []string
	a.mailer.onSend = func(to, subject, body string) { mu.Lock(); got = append(got, to+"|"+subject+"|"+body); mu.Unlock() }
	sent := func() []string { mu.Lock(); defer mu.Unlock(); out := got; got = nil; return out }

	year := a.cfg.ThisYear()
	a.store.Import([]Member{
		{Name: "Pat Rider", Email: "pat@example.com", PaidYears: []int{year}, Joined: time.Now().AddDate(-1, 0, -1)},
		{Name: "Sam Spoke", Email: "sam@example.com", PaidYears: []int{year}, Joined: time.Now().AddDate(-1, 0, -1)},
		{Name: "Nora No", Email: "nora@example.com", PaidYears: []int{year}, Joined: time.Now().AddDate(-1, 0, -1)},
	})
	pat, _ := a.store.MemberByEmail("pat@example.com")
	sam, _ := a.store.MemberByEmail("sam@example.com")
	nora, _ := a.store.MemberByEmail("nora@example.com")

	loc := a.cfg.Location
	now := time.Now().In(loc)
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	ct, body := form(url.Values{"title": {"December Meeting"}, "date": {tomorrow}, "time": {"18:00"}, "place": {"12 Spoke Lane, Leonardtown"},
		"ask_bringing": {"1"}, "show_guests": {"1"}, "remind": {"1"}})
	do(t, h, admin, "POST", "/admin/rsvp", ct, body)
	e, _ := a.store.RSVPEvent(1)
	if e.NoReminder || e.ReminderDay().Format("2006-01-02") != now.Format("2006-01-02") {
		t.Fatalf("the reminder day: %+v", e)
	}
	a.store.AnswerRSVP(1, pat, RSVPReply{Name: "Pat Rider", Answer: rsvpYes, Guests: 2, Who: "Alex and Jo", Bringing: "Chili"}, now)
	a.store.AnswerRSVP(1, sam, RSVPReply{Name: "Sam", Answer: rsvpMaybe}, now)
	a.store.AnswerRSVP(1, nora, RSVPReply{Name: "Nora", Answer: rsvpNo}, now)

	nine := time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, loc)
	a.runRSVPReminders(nine.Add(-time.Hour)) // 8:30: not yet
	if s := sent(); len(s) != 0 {
		t.Fatalf("sent before 9: %q", s)
	}
	a.runRSVPReminders(nine)
	s := sent()
	if len(s) != 2 {
		t.Fatalf("sent: %q", s)
	}
	var patMail, samMail string
	for _, m := range s {
		switch {
		case strings.HasPrefix(m, "pat@example.com|"):
			patMail = m
		case strings.HasPrefix(m, "sam@example.com|"):
			samMail = m
		}
	}
	for _, want := range []string{"|Tomorrow: December Meeting|", "Hi Pat,", "You said you’ll be there, with 2 guests (Alex and Jo), bringing Chili.", "12 Spoke Lane",
		"/rsvp/1/directions?t=" + pat.Token, "/rsvp/1?t=" + pat.Token} {
		if !strings.Contains(patMail, want) {
			t.Errorf("Pat's reminder should have %q: %s", want, patMail)
		}
	}
	if !strings.Contains(samMail, "You said maybe.") {
		t.Errorf("Sam's reminder: %s", samMail)
	}
	a.runRSVPReminders(nine.Add(time.Hour)) // once only, even after changing the answer
	a.store.AnswerRSVP(1, sam, RSVPReply{Name: "Sam", Answer: rsvpYes}, now)
	a.runRSVPReminders(nine.Add(2 * time.Hour))
	if s := sent(); len(s) != 0 {
		t.Fatalf("sent twice: %q", s)
	}
	if page := do(t, h, admin, "GET", "/admin/rsvp/1", "", nil).Body.String(); !strings.Contains(page, "Reminder sent to 2") {
		t.Error("the RSVP page should say the reminder went")
	}

	// On Automations: a row with a switch; switched off, nothing goes.
	if page := do(t, h, admin, "GET", "/admin/email/auto", "", nil).Body.String(); !strings.Contains(page, `id="a-rsvp-reminder"`) {
		t.Error("the Automations list should have the RSVP reminder")
	}
	if page := do(t, h, admin, "GET", "/admin/email/auto/rsvp-reminder/preview", "", nil).Body.String(); !strings.Contains(page, "You said you’ll be there") {
		t.Errorf("the preview: %s", page)
	}
	ct, body = form(url.Values{"paused": {"1"}})
	do(t, h, admin, "POST", "/admin/email/auto/rsvp-reminder/pause", ct, body)
	if a.store.RSVPReminderOn() {
		t.Fatal("still on")
	}
	ct, body = form(url.Values{"title": {"Holiday Party"}, "date": {tomorrow}, "remind": {"1"}})
	do(t, h, admin, "POST", "/admin/rsvp", ct, body)
	a.store.AnswerRSVP(2, pat, RSVPReply{Name: "Pat", Answer: rsvpYes}, now)
	a.runRSVPReminders(nine.Add(3 * time.Hour))
	if s := sent(); len(s) != 0 {
		t.Fatalf("sent while off: %q", s)
	}

	// Back on, but this invitation skips it.
	ct, body = form(url.Values{"paused": {"0"}})
	do(t, h, admin, "POST", "/admin/email/auto/rsvp-reminder/pause", ct, body)
	ct, body = form(url.Values{"title": {"Holiday Party"}, "date": {tomorrow}})
	do(t, h, admin, "POST", "/admin/rsvp/2", ct, body)
	a.runRSVPReminders(nine.Add(4 * time.Hour))
	if s := sent(); len(s) != 0 {
		t.Fatalf("sent for an invitation without a reminder: %q", s)
	}

	// Edited words are used; the built-in ones aren't stored.
	ct, body = form(url.Values{"subject": {"See you tomorrow, {first_name}"}, "body": {"Hi {first_name}, {your_answer}"}})
	do(t, h, admin, "POST", "/admin/email/auto/rsvp-reminder/save", ct, body)
	if d := a.store.RSVPReminderText(); d.Subject != "See you tomorrow, {first_name}" {
		t.Errorf("saved: %+v", d)
	}
}

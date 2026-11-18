package main

import (
	"strings"
	"testing"
	"time"
)

// Notices that can wait go to the club inbox together from 9 am; a ride
// cancelled, or anything about today or tomorrow, goes at once; switched
// off, everything goes at once.
func TestMorningSummary(t *testing.T) {
	a, _, sent := rideApp(t)
	later, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 5).Format(time.DateOnly), Name: "Hilly Loop"})
	tomorrow, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 1).Format(time.DateOnly), Name: "Coffee Ride"})

	a.notifyRide("First ride: Sam on Hilly Loop", "**Sam** is coming.", later, "", false)
	a.notifyRide("First ride: Ann on Coffee Ride", "**Ann** is coming.", tomorrow, "", false)
	a.notifyRide("Hilly Loop is cancelled", "Rain.", later, "", true)
	a.mailer.bg.Wait()
	if len(*sent) != 2 || (*sent)[0].subject != "First ride: Ann on Coffee Ride" || (*sent)[1].subject != "Hilly Loop is cancelled" {
		t.Fatalf("sent at once: %+v", *sent)
	}
	if w := a.store.WaitingNotices(); len(w) != 1 || w[0].Subject != "First ride: Sam on Hilly Loop" {
		t.Fatalf("waiting: %+v", w)
	}

	d := a.today()
	at := func(days, h, m int) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day()+days, h, m, 0, 0, a.cfg.Location)
	}
	a.runMorningSummary(at(0, 8, 0)) // before 9: nothing
	if len(*sent) != 2 {
		t.Fatal("the summary went before 9 am")
	}
	a.runMorningSummary(at(0, 9, 15))
	a.runMorningSummary(at(0, 9, 30)) // once a day
	if len(*sent) != 3 {
		t.Fatalf("summary: %+v", *sent)
	}
	s := (*sent)[2]
	if s.subject != "Since yesterday: 1 notice" || !strings.Contains(s.body, "## First ride: Sam on Hilly Loop") || !strings.Contains(s.body, "/calendar/ride/"+later.Key) {
		t.Errorf("summary = %+v", s)
	}
	if len(a.store.WaitingNotices()) != 0 {
		t.Error("sent notices should be emptied")
	}
	// A notice after today's summary waits for tomorrow's.
	a.notifyRide("New on the calendar: Hilly Loop", "Added.", later, "", false)
	a.runMorningSummary(at(0, 10, 0))
	if len(*sent) != 3 {
		t.Error("a second summary went the same day")
	}
	a.runMorningSummary(at(1, 9, 0))
	if len(*sent) != 4 {
		t.Error("tomorrow's summary didn't go")
	}

	// Switched off: at once, like before.
	a.store.SetSummaryOn(false)
	a.notifyRide("First ride: Pat on Hilly Loop", "**Pat** is coming.", later, "", false)
	a.mailer.bg.Wait()
	if len(*sent) != 5 || len(a.store.WaitingNotices()) != 0 {
		t.Errorf("summary off: sent %d, waiting %d", len(*sent), len(a.store.WaitingNotices()))
	}
}

// The summary is a row on Email → Automations with a switch, and its
// preview draws what's waiting.
func TestMorningSummaryRow(t *testing.T) {
	a, h, admin := testApp(t)
	page := do(t, h, admin, "GET", "/admin/email/auto", "", nil).Body.String()
	if !strings.Contains(page, `id="a-morning-summary"`) || !strings.Contains(page, `action="/admin/email/auto/morning-summary/pause"`) {
		t.Fatal("Automations should list the morning summary with a switch")
	}
	ct, b := form(map[string][]string{"paused": {"1"}})
	do(t, h, admin, "POST", "/admin/email/auto/morning-summary/pause", ct, b)
	if a.store.SummaryOn() {
		t.Error("the switch should turn it off")
	}
	a.store.SetSummaryOn(true)
	a.store.KeepNotice(ClubNotice{Subject: "Loaner bike request: Coffee Ride", Body: "Sam asked."})
	if p := do(t, h, admin, "GET", "/admin/email/auto/morning-summary/preview", "", nil).Body.String(); !strings.Contains(p, "Loaner bike request: Coffee Ride") {
		t.Error("the preview should show what's waiting")
	}
}

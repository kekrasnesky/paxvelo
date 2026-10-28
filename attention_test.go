package main

import (
	"strings"
	"testing"
	"time"
)

// Admins land on Needs attention: rides waiting for approval, rides this week
// without a leader, and unpublished newsletter drafts, each with a link, and a
// count on Home in the bar.
func TestNeedsAttention(t *testing.T) {
	a, h, admin := testApp(t)
	page := do(t, h, admin, "GET", "/admin", "", nil).Body.String()
	if !strings.Contains(page, "Needs attention") || !strings.Contains(page, `href="/admin/members?dues=unpaid"`) {
		t.Fatal("/admin should be the Needs attention page")
	}

	day := a.today().AddDate(0, 0, 2).Format("2006-01-02")
	a.store.AddRide(CalRide{Name: "Sunday Spin", Date: day, Time: "08:00", Held: true})
	a.store.AddRide(CalRide{Name: "Coffee Ride", Date: day, Time: "09:00", Groups: []string{"A group", "B group"},
		Leaders: []Leader{{Group: "A group", Name: "Ann"}}})
	now := time.Now().In(a.cfg.Location)
	a.store.SaveIssue(Issue{Month: int(now.Month()), Year: now.Year()})

	page = do(t, h, admin, "GET", "/admin", "", nil).Body.String()
	for _, want := range []string{
		"1 ride added on the website is waiting for approval",
		"Coffee Ride", "needs a leader for the B group",
		"newsletter is a draft, not published yet",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Needs attention should say %q", want)
		}
	}
	if strings.Contains(page, "Sunday Spin,") {
		t.Error("a ride waiting for approval isn't on the calendar yet, so it shouldn't ask for a leader")
	}
	if !strings.Contains(page, `<span class="badge">`) {
		t.Error("Home should show how many things need attention")
	}

	// The members list moved to /admin/members, and the bar groups the screens.
	members := do(t, h, admin, "GET", "/admin/members", "", nil).Body.String()
	if !strings.Contains(members, `action="/admin/members"`) {
		t.Error("the members list should live at /admin/members")
	}
	if !strings.Contains(members, `class="corner" href="/admin/admins"`) {
		t.Error("Admins should sit in the corner of the bar")
	}
	cal := do(t, h, admin, "GET", "/admin/calendar", "", nil).Body.String()
	if !strings.Contains(cal, `<nav class="sub"`) || !strings.Contains(cal, `href="/admin/rides">Weekly rides</a>`) {
		t.Error("the Rides group should list its screens under the bar")
	}
}

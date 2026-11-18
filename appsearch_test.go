package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The admin app's search finds members by name or email, rides in the
// window (and held ones), and RSVP invitations, each linking to its page;
// a website admin gets the same, since they can open each page.
func TestAppSearch(t *testing.T) {
	a, h, _ := testApp(t)
	web, _, _ := adminLevels(t, a)
	a.store.mu.Lock()
	a.store.d.Members = append(a.store.d.Members, &Member{ID: 900, Name: "Gwen Gravel", Email: "gwen@example.com"})
	a.store.mu.Unlock()
	day := func(n int) string { return a.today().AddDate(0, 0, n).Format(time.DateOnly) }
	soon, _ := a.store.AddRide(CalRide{Date: day(10), Name: "Gravel Grinder", Place: "Solomons"})
	a.store.AddRide(CalRide{Date: day(-60), Name: "Gravel Long Ago"})
	a.store.AddRide(CalRide{Date: day(70), Name: "Gravel Far Off"})
	held, _ := a.store.AddRide(CalRide{Date: day(5), Name: "Gravel Pending", Held: true})
	id, _ := a.store.SaveRSVPEvent(RSVPEvent{Title: "Gravel Picnic", Date: day(20)})

	part := do(t, h, web, "GET", "/admin/app/search?q=gravel", "", nil).Body.String()
	for _, want := range []string{"/admin/members/900", "Gwen Gravel", "/admin/rides/" + soon.Key, "/admin/rides/" + held.Key, "waiting for approval", "/admin/rsvp/" + itoa(id), "Gravel Picnic"} {
		if !strings.Contains(part, want) {
			t.Errorf("results should have %q:\n%s", want, part)
		}
	}
	if strings.Contains(part, "Long Ago") || strings.Contains(part, "Far Off") {
		t.Error("rides outside 4 weeks back to 8 weeks ahead are left out")
	}
	if p := do(t, h, web, "GET", "/admin/app/search?q=gwen%40example", "", nil).Body.String(); !strings.Contains(p, "Gwen Gravel") || strings.Contains(p, "Gravel Grinder") {
		t.Error("an email finds the member only")
	}
	if p := do(t, h, web, "GET", "/admin/app/search?q=zzzz", "", nil).Body.String(); !strings.Contains(p, "No matches") {
		t.Error("nothing found says so")
	}
	if p := do(t, h, web, "GET", "/admin/app/search?q=g", "", nil).Body.String(); p != "" {
		t.Error("one letter isn't searched")
	}
	if w := do(t, h, nil, "GET", "/admin/app/search?q=gravel", "", nil); w.Code != 303 {
		t.Errorf("logged out = %d", w.Code)
	}
	// Without scripts the box submits to the app page.
	if p := do(t, h, web, "GET", "/admin/app?q=gwen", "", nil).Body.String(); !strings.Contains(p, "/admin/members/900") || !strings.Contains(p, `value="gwen"`) {
		t.Error("/admin/app?q= should show the results")
	}
}

// Picking a date on Add a ride starts from the weekly ride on that weekday,
// or the latest one-off ride on that weekday.
func TestRideStarts(t *testing.T) {
	a, h, _ := rideApp(t)
	a.store.AddSeries(RideSeries{Name: "Thursday Hills", Weekday: 4, Time: "17:30", Place: "Hollywood", Groups: []string{"A group", "B group", "C group"}, Level: "intermediate"})
	d := a.today()
	for d.Weekday() != time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	a.store.AddRide(CalRide{Date: d.AddDate(0, 0, -7).Format(time.DateOnly), Name: "Old Sunday", Time: "07:00", Place: "Old Place"})
	a.store.AddRide(CalRide{Date: d.Format(time.DateOnly), Name: "Sunday Spin", Time: "09:15", Place: "Leonardtown", Groups: []string{"A group", "B group"}})
	a.store.AddRide(CalRide{Date: d.AddDate(0, 0, -14).Format(time.DateOnly), Name: "Sunday Party", Kind: "social", Time: "18:00"})

	var starts map[string]rideStart
	if err := json.Unmarshal([]byte(a.rideStarts()), &starts); err != nil {
		t.Fatal(err)
	}
	if s := starts["4"]; s.Time != "17:30" || s.Place != "Hollywood" || s.Pace != "3" || s.Level != "intermediate" {
		t.Errorf("Thursday = %+v", s)
	}
	if s := starts["0"]; s.Time != "09:15" || s.Place != "Leonardtown" || s.Pace != "2" {
		t.Errorf("Sunday = %+v (the latest one-off ride, not an event)", s)
	}
	for _, p := range []string{"/calendar/new"} {
		if page := do(t, h, nil, "GET", p, "", nil).Body.String(); !strings.Contains(page, "data-starts=") || !strings.Contains(page, "data-fresh") || !strings.Contains(page, "17:30") {
			t.Errorf("%s should carry the starting values", p)
		}
	}
}

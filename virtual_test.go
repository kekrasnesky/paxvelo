package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGuessVirtual(t *testing.T) {
	for text, want := range map[string]bool{
		"Zwift Tuesday group ride":         true,
		"Indoor trainer ride, Watopia":     true,
		"Winter trainer rides":             true,
		"Virtual Saturday Ride":            true,
		"Meet on ZWIFT at 6":               true,
		"Saturday Morning Ride":            false,
		"Virtually no traffic on this one": false,
		"Bring your trainer wheel":         false,
	} {
		if got := guessVirtual(text); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
	// Ticking or unticking the box wins over the words.
	if isVirtual("no", "Zwift ride") || !isVirtual("yes", "Coffee ride") {
		t.Error("the box should win")
	}
}

// A ride that mentions Zwift is virtual by itself, with the Zwift badge in
// its day on the calendar; admins can untick it, and anyone adding a ride can
// tick it.
func TestVirtualRides(t *testing.T) {
	a, h, _ := rideApp(t)
	rec := httptest.NewRecorder()
	a.setSession(rec, "Board Member", 3600)
	admin := rec.Result().Cookies()[0]
	wed := nextWeekday(a, time.Wednesday)
	z, _ := a.store.AddRide(CalRide{Date: wed, Time: "18:00", Name: "Wednesday Night Zwift", Details: "Watopia flat route"})
	road, _ := a.store.AddRide(CalRide{Date: wed, Time: "08:00", Name: "Coffee Ride"})

	cal := do(t, h, nil, "GET", "/calendar?m="+wed[:7], "", nil).Body.String()
	if n := strings.Count(cal, `class="zwift"`); n < 2 { // in its day, and in the list
		t.Errorf("the calendar should show the Zwift badge: %d", n)
	}
	if page := do(t, h, nil, "GET", "/calendar/ride/"+z.Key, "", nil).Body.String(); !strings.Contains(page, "Virtual: ride it on Zwift") {
		t.Error("the ride page should say it's virtual")
	}
	if page := do(t, h, nil, "GET", "/calendar/ride/"+road.Key, "", nil).Body.String(); strings.Contains(page, `class="zwift"`) {
		t.Error("a road ride isn't virtual")
	}

	// The admin's form shows it ticked; saving untouched keeps it automatic.
	edit := do(t, h, admin, "GET", "/admin/rides/"+z.Key, "", nil).Body.String()
	if !strings.Contains(edit, `name="virtual" checked`) || !strings.Contains(edit, `name="virtual_was" value="1"`) {
		t.Fatal("the admin form should show the box ticked")
	}
	save := func(key string, v url.Values) CalRide {
		t.Helper()
		ct, body := form(v)
		do(t, h, admin, "POST", "/admin/rides/"+key, ct, body)
		o, _ := a.store.Ride(key)
		return o.CalRide
	}
	base := func(name string) url.Values {
		return url.Values{"name": {name}, "date": {wed}, "time": {"18:00"}, "pace": {""}}
	}
	v := base("Wednesday Night Zwift")
	v.Set("virtual", "on")
	v.Set("virtual_was", "1")
	if c := save(z.Key, v); c.Virtual != "" || !c.IsVirtual() {
		t.Errorf("untouched should stay automatic: %q", c.Virtual)
	}
	// Unticked: it stays off even though the name says Zwift.
	v.Del("virtual")
	if c := save(z.Key, v); c.Virtual != "no" || c.IsVirtual() {
		t.Errorf("unticked: %q", c.Virtual)
	}
	// Ticked on a ride whose words don't say so.
	v = base("Coffee Ride")
	v.Set("virtual", "on")
	v.Set("virtual_was", "0")
	if c := save(road.Key, v); c.Virtual != "yes" || !c.IsVirtual() {
		t.Errorf("ticked: %q", c.Virtual)
	}

	// Adding a ride: the box, or the words.
	page := do(t, h, nil, "GET", "/calendar/new", "", nil).Body.String()
	if !strings.Contains(page, `name="virtual"`) {
		t.Fatal("Add to the calendar should have the Virtual box")
	}
	add := func(name string, tick bool) CalRide {
		t.Helper()
		v := url.Values{"date": {wed}, "time": {"19:00"}, "name": {name}, "kind": {""}, "pace": {"1"},
			"leader_name": {"Pat Rider"}, "lead": {"on"}, "ft": {oldToken(a)}, "virtual_was": {"0"}}
		if tick {
			v.Set("virtual", "on")
		}
		ct, body := form(v)
		do(t, h, nil, "POST", "/calendar/new", ct, body)
		for _, c := range a.store.HeldRides() {
			if c.Name == name {
				return c.CalRide
			}
		}
		t.Fatalf("%s wasn't added", name)
		return CalRide{}
	}
	if c := add("Trainer ride with the club", false); !c.IsVirtual() || c.Virtual != "" {
		t.Errorf("the words should make it virtual: %+v", c.Virtual)
	}
	if c := add("Thursday spin", true); !c.IsVirtual() || c.Virtual != "yes" {
		t.Errorf("the box should make it virtual: %+v", c.Virtual)
	}

	// Weekly rides: the box is there too, and its dates follow.
	ws, err := a.store.AddSeries(RideSeries{Name: "Winter Zwift", Weekday: int(time.Monday), Time: "19:00"})
	if err != nil {
		t.Fatal(err)
	}
	if page := do(t, h, admin, "GET", "/admin/weekly/"+itoa(ws), "", nil).Body.String(); !strings.Contains(page, `name="virtual" checked`) {
		t.Error("the weekly ride form should show the box ticked")
	}
	mon := nextWeekday(a, time.Monday)
	if o, ok := a.store.Ride(seriesKey(ws, mon)); !ok || !o.IsVirtual() {
		t.Error("a weekly Zwift ride's dates are virtual")
	}
}

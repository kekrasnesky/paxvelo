package main

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRideLevels(t *testing.T) {
	keys := func(o RideOcc) string {
		var out []string
		for _, l := range o.Levels() {
			out = append(out, l.Key)
		}
		return strings.Join(out, " ")
	}
	ride := func(name, level string, pace int) RideOcc {
		o := RideOcc{CalRide: CalRide{Name: name, Level: level}}
		if pace != 0 {
			o.Strava = &StravaInfo{Pace: pace}
		}
		return o
	}
	for _, c := range []struct {
		ride RideOcc
		want string
	}{
		{ride("Tuesday Night Ride", "", 0), "intermediate elite"},
		{ride("TNR", "", 4), "intermediate elite"},
		{ride("ThNR to Point Lookout", "", 4), "intermediate"},
		{ride("SuMR", "", 4), "social"},
		{ride("Saturday Morning Ride", "", 0), "social"},
		{ride("Old Line Cycles Shop Ride", "", 0), "social"},
		{ride("Wildewood Ride", "", 4), "elite"},
		{ride("Wildewood Ride", "", 1), "social"},
		{ride("Cafe Ride", "", 0), "intermediate"},
		{ride("SuMR", "elite", 1), "elite"}, // an admin's pick wins
	} {
		if got := keys(c.ride); got != c.want {
			t.Errorf("%q: %q, want %q", c.ride.Name, got, c.want)
		}
	}
	if levelFor("intermediate elite") != "intermediate elite" || levelFor("fast") != "" {
		t.Error("the Level menu should take only its own choices")
	}
}

// Club admins signed in with Strava can change a ride's level on its page.
func TestSetRideLevel(t *testing.T) {
	a, h, _ := rideApp(t)
	ann, _ := signIn(t, h, "ann", "/calendar")
	bob, _ := signIn(t, h, "bob", "/calendar")
	key := seriesKey(1, nextWeekday(a, time.Tuesday))
	if page := do(t, h, ann, "GET", "/calendar/ride/"+key, "", nil).Body.String(); !strings.Contains(page, `action="/calendar/ride/`+key+`/level"`) {
		t.Fatal("an admin should see the Level menu")
	}
	if page := do(t, h, bob, "GET", "/calendar/ride/"+key, "", nil).Body.String(); strings.Contains(page, "/level\"") {
		t.Error("a rider who isn't an admin shouldn't see the Level menu")
	}
	ct, body := form(url.Values{"level": {"social"}})
	do(t, h, bob, "POST", "/calendar/ride/"+key+"/level", ct, body)
	if o, _ := a.store.Ride(key); o.Level != "" {
		t.Errorf("a rider who isn't an admin changed the level: %q", o.Level)
	}
	ct, body = form(url.Values{"level": {"social"}})
	if w := do(t, h, ann, "POST", "/calendar/ride/"+key+"/level", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=level") {
		t.Errorf("save level: %s", w.Header().Get("Location"))
	}
	if o, _ := a.store.Ride(key); len(o.Levels()) != 1 || o.Levels()[0].Key != "social" {
		t.Errorf("Tuesday's level after the admin set it: %+v", o.Levels())
	}
}

package main

import (
	"strings"
	"testing"
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
		{ride("ThNR to Point Lookout", "", 4), "intermediate elite"},
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
	if levelFor([]string{"elite", "intermediate"}) != "intermediate elite" || levelFor([]string{"fast"}) != "" || levelFor(nil) != "" {
		t.Error("the Level boxes should take only their own levels, in order")
	}
}

// Our Rides shows each ride's level tags, the way the calendar does.
func TestLevelTagsOnPages(t *testing.T) {
	_, h, _ := testApp(t)
	page := do(t, h, nil, "GET", "/rides", "", nil).Body.String()
	both := `<span class="lvl lvl-intermediate">Intermediate</span> 18–20 mph or <span class="lvl lvl-elite">Elite</span> 20+ mph, depending on who turns out`
	if strings.Count(page, both) != 2 || strings.Count(page, `<span class="lvl lvl-social">Social</span> 16–18 mph</p>`) != 2 {
		t.Error("Our Rides should tag the Tuesday and Thursday rides Intermediate or Elite, and the shop and weekend rides Social")
	}
	if strings.Contains(page, "{level-") {
		t.Error("a level tag wasn't filled in")
	}
}

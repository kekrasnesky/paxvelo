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
	if levelFor("intermediate elite") != "intermediate elite" || levelFor("fast") != "" {
		t.Error("the Level menu should take only its own choices")
	}
}

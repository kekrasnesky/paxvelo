package main

import (
	"testing"
	"time"
)

// A ride's calendar length grows with its distance: 18 mph plus half an hour
// per 20 miles, rounded up to a quarter hour.
func TestRideLength(t *testing.T) {
	for _, c := range []struct {
		o    RideOcc
		want time.Duration
	}{
		{RideOcc{CalRide: CalRide{Name: "Coffee Ride"}}, 2 * time.Hour},  // distance unknown
		{RideOcc{Route: &Route{Distance: 60 * 1609.344}}, 5 * time.Hour}, // 3h20 riding + 1h30 stops
		{RideOcc{CalRide: CalRide{Name: "Thursday Night Ride", Details: "Tempo to Point Lookout · about 22 mph, 40 miles"}}, 3*time.Hour + 15*time.Minute},
		{RideOcc{CalRide: CalRide{Name: "Saturday 30mi Social"}}, 2*time.Hour + 30*time.Minute},
		{RideOcc{CalRide: CalRide{Name: "Metric century", Details: "62-mile loop, 1 mile neutral start"}}, 5 * time.Hour},
		{RideOcc{CalRide: CalRide{Name: "Club meeting", Kind: "meeting", Details: "Planning the 50 mile ride"}}, 2 * time.Hour},
	} {
		if got := c.o.Length(); got != c.want {
			t.Errorf("%q %q: %v, want %v", c.o.Name, c.o.Details, got, c.want)
		}
	}
}

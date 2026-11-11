package main

import (
	"math"
	"regexp"
	"strconv"
	"time"
)

// How long a ride is down for in someone's calendar ("Add to my calendar"):
// riding time at rideAvgMPH plus rideStopMinutes for every 20 miles (regroups,
// a store stop), rounded up to the next quarter hour. So a 30-mile ride is
// about 2½ hours and a 60-mile ride about 5. A ride whose distance isn't known
// (no Strava route, no "40 miles" in its name or details) and an event
// (meeting, social, clinic) stay at rideDefaultLength.
const (
	rideAvgMPH        = 18.0
	rideStopMinutes   = 30.0 // per 20 miles
	rideDefaultLength = 2 * time.Hour
)

// milesRE finds a distance written in a ride's name or details: "40 miles",
// "62mi", "35-mile loop", "about 45 mi".
var milesRE = regexp.MustCompile(`(?i)\b(\d{1,3}(?:\.\d+)?)\s*-?\s*(?:miles?|mi)\b`)

// Miles is how far the ride goes: its Strava route's distance, or else the
// largest distance named in its name, details or Strava notes. 0 = unknown.
func (o RideOcc) Miles() float64 {
	if o.Route != nil && o.Route.Distance > 0 {
		return o.Route.Distance / 1609.344
	}
	best := 0.0
	for _, s := range []string{o.Name, o.Details, o.Notes()} {
		for _, m := range milesRE.FindAllStringSubmatch(s, -1) {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= 5 && v <= 300 && v > best {
				best = v
			}
		}
	}
	return best
}

// Length is how long the ride is down for in a calendar (see above).
func (o RideOcc) Length() time.Duration {
	return rideLength(o.Miles(), o.Kind != "")
}

func rideLength(miles float64, event bool) time.Duration {
	if event || miles <= 0 {
		return rideDefaultLength
	}
	minutes := miles/rideAvgMPH*60 + miles/20*rideStopMinutes
	return time.Duration(math.Ceil(minutes/15)) * 15 * time.Minute
}

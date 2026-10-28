package main

import (
	"slices"
	"strings"
)

// What new members tell us about their riding on the join form, and the
// welcome email advice that follows from it. The paces match content/rides.md;
// change both together.

type choice struct{ Value, Label, Short string }

var riderLevels = []choice{
	{"new", "New to group rides", "New"},
	{"some", "I’ve done a few group rides", "Some group rides"},
	{"experienced", "Experienced on group rides", "Experienced"},
	{"racer", "I race", "Racer"},
}

var clubRides = []choice{
	{"tnr-a", "Tuesday Night Ride, A group (race pace)", "Tue A"},
	{"tnr-b", "Tuesday Night Ride, B group", "Tue B"},
	{"thursday", "Thursday Night Ride", "Thu"},
	{"weekend", "Weekend rides", "Weekend"},
}

// levelAdvice opens {ride_tips}: where to start, given their experience.
var levelAdvice = map[string]string{
	"new":         "Since you’re new to group rides, start with the Old Line Cycles shop ride. It’s made for people getting into the sport and has several regroup stops (ask the shop when the next one is). When you’re ready for more, the Thursday Night Ride is fast but doesn’t drop anyone.",
	"some":        "A good first club ride is the Thursday Night Ride: about 22 mph, and nobody gets left behind. The Tuesday B group, about 19–21 mph, is a good next step.",
	"experienced": "You’ll fit right in on the Thursday Night Ride (about 22 mph) and the Tuesday B group (about 19–21 mph). When you want a harder night, try the Tuesday A group.",
	"racer":       "Come race-pace training with the Tuesday Night Ride A group, about 23–25 mph. The Thursday ride to Point Lookout makes a good tempo day.",
}

// rideNotes lists the rides they picked.
var rideNotes = map[string]string{
	"tnr-a":    "**Tuesday Night Ride, A group:** race pace, about 23–25 mph, mid-March through October.",
	"tnr-b":    "**Tuesday Night Ride, B group:** about 19–21 mph. Hard, but not a race.",
	"thursday": "**Thursday Night Ride:** about 22 mph to Point Lookout and back, and nobody gets left behind.",
	"weekend":  "**Weekend rides:** whenever a member hosts one, usually a café ride on Sundays.",
}

func cleanLevel(s string) string {
	for _, c := range riderLevels {
		if c.Value == s {
			return s
		}
	}
	return ""
}

// cleanRides keeps the known rides picked, once each, in the form's order.
func cleanRides(picked []string) []string {
	var out []string
	for _, c := range clubRides {
		if slices.Contains(picked, c.Value) {
			out = append(out, c.Value)
		}
	}
	return out
}

// Picked reports whether a ride was ticked, to show the form again after a mistake.
func (d memberDetails) Picked(ride string) bool { return slices.Contains(d.Rides, ride) }

// LevelName is how the member describes their riding, for the admin.
func (m Member) LevelName() string {
	for _, c := range riderLevels {
		if c.Value == m.Level {
			return c.Short
		}
	}
	return ""
}

// RideNames lists the rides they're interested in, briefly, for the admin.
func (m Member) RideNames() string {
	var names []string
	for _, c := range clubRides {
		if slices.Contains(m.Rides, c.Value) {
			names = append(names, c.Short)
		}
	}
	return strings.Join(names, ", ")
}

// rideTips is what {ride_tips} becomes in the welcome email: advice for their
// experience, then the rides they picked. It's empty for members added by
// an admin, who didn't answer.
func rideTips(m Member, site string) string {
	var parts []string
	if a := levelAdvice[m.Level]; a != "" {
		parts = append(parts, a)
	}
	if len(m.Rides) > 0 {
		var b strings.Builder
		b.WriteString("The rides you picked:")
		for _, r := range m.Rides {
			b.WriteString("\n- " + rideNotes[r])
		}
		parts = append(parts, b.String())
	}
	if len(parts) > 0 {
		parts = append(parts, "Times, start spots, and this week’s routes are on Strava and at "+site+"/rides")
	}
	return strings.Join(parts, "\n\n")
}

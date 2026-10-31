package main

import (
	"slices"
	"strings"
)

// What new members tell us about their riding on the join form: one answer,
// which picks the welcome email's ride advice. The paces match
// content/rides.md; change both together.

type choice struct{ Value, Label, Short string }

var riderLevels = []choice{
	{"new", "New to group rides", "New"},
	{"some", "I’ve done some group rides (around 19–21 mph)", "Some group rides"},
	{"experienced", "Experienced on fast group rides (22+ mph)", "Experienced"},
	{"racer", "I race", "Racer"},
}

// clubRides were checkboxes on the old join form. They're no longer asked,
// but members who joined then still have their picks, shown in the admin.
var clubRides = []choice{
	{"tnr-a", "Tuesday Night Ride, A group (race pace)", "Tue A"},
	{"tnr-b", "Tuesday Night Ride, B group", "Tue B"},
	{"thursday", "Thursday Night Ride", "Thu"},
	{"weekend", "Weekend rides", "Weekend"},
}

// levelAdvice is the built-in {ride_tips} for each answer, used until an
// admin writes their own on the Email tab.
var levelAdvice = map[string]string{
	"new":         "Since you’re new to group rides, start with the Old Line Cycles shop ride, Wednesdays at 6:00 PM from the Wildewood Shopping Center. It’s about 20 miles at a social pace, with plenty of regroups. Weekend café rides, when a member hosts one, are another easy way to meet people.\n\nWhen you’re ready for more, try the Thursday Night Ride: a tempo ride to Point Lookout and back that keeps things tame when newer riders come, and nobody gets left behind.",
	"some":        "Start with the Thursday Night Ride: a tempo ride to Point Lookout and back, roughly 40 miles. The pace suits who turns out, and nobody gets left behind.\n\nWhen that feels good, try the Tuesday Night Ride’s B group, about 19–21 mph. It’s hard, but not a race.",
	"experienced": "You’ll fit right in on the Thursday Night Ride (tempo, and nobody gets left behind) and the Tuesday Night Ride’s B group (about 19–21 mph).\n\nWhen you want a harder night, try the Tuesday A group. It’s race pace, typically 22–28 mph for about 30 miles, and if you come off the back you’re on your own.",
	"racer":       "Come race-pace training with the Tuesday Night Ride A group, typically 22–28 mph for about 30 miles, mid-March through October.\n\nThe Thursday ride to Point Lookout makes a good tempo day, and it doesn’t drop anyone.",
}

func cleanLevel(s string) string {
	for _, c := range riderLevels {
		if c.Value == s {
			return s
		}
	}
	return ""
}

// LevelName is how the member describes their riding, for the admin.
func (m Member) LevelName() string {
	for _, c := range riderLevels {
		if c.Value == m.Level {
			return c.Short
		}
	}
	return ""
}

// RideNames lists the rides they picked on the old join form, briefly, for the admin.
func (m Member) RideNames() string {
	var names []string
	for _, c := range clubRides {
		if slices.Contains(m.Rides, c.Value) {
			names = append(names, c.Short)
		}
	}
	return strings.Join(names, ", ")
}

// Tip is the ride advice the welcome email gives for one answer: the admin's
// own, or the built-in advice where they haven't written any.
func (w WelcomeEmail) Tip(level string) string {
	if t := strings.TrimSpace(w.Tips[level]); t != "" {
		return t
	}
	return levelAdvice[level]
}

// rideTips is what {ride_tips} becomes in the welcome email: the advice for
// their answer, then where to find the details. It's empty for members added
// by an admin, who didn't answer.
func rideTips(w WelcomeEmail, m Member, site string) string {
	if cleanLevel(m.Level) == "" {
		return ""
	}
	return w.Tip(m.Level) + "\n\nTimes, start spots, and this week’s routes are on Strava and at " + site + "/rides"
}

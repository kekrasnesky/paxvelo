package main

import (
	"regexp"
	"strings"
)

// Every ride on the calendar is tagged with its level, so new riders can tell
// how fast it goes: Social, Intermediate, or Elite. An admin can set a ride's
// level (on a weekly ride, or one date). Otherwise the club's regular rides
// have theirs by name, a Strava event's pace sets the rest, and anything else
// counts as Intermediate.

type rideLevel struct{ Key, Name, MPH string }

var rideLevels = []rideLevel{
	{"social", "Social", "16–18 mph"},
	{"intermediate", "Intermediate", "18–20 mph"},
	{"elite", "Elite", "20+ mph"},
}

// levelChoices is the admin's Level menu. The Tuesday Night Ride is either,
// depending on who turns out.
var levelChoices = []paceChoice{{"", "Automatic"}, {"social", "Social"}, {"intermediate", "Intermediate"},
	{"elite", "Elite"}, {"intermediate elite", "Intermediate or Elite"}}

// levelFor reads the admin's Level menu.
func levelFor(choice string) string {
	for _, c := range levelChoices {
		if c.Value == choice {
			return choice
		}
	}
	return ""
}

// The club's regular rides, by the names they go by on Strava and the calendar.
var levelNames = []struct {
	re     *regexp.Regexp
	levels string
}{
	{regexp.MustCompile(`(?i)\btuesday night\b|\btnr\b`), "intermediate elite"},
	{regexp.MustCompile(`(?i)\bthursday night\b|\bthnr\b`), "intermediate"},
	{regexp.MustCompile(`(?i)\bsamr\b|\bsumr\b|\bsaturday morning\b|\bsunday morning\b|\bold ?line\b|\bshop ride\b`), "social"},
}

// stravaLevels turns a Strava event's pace (1 casual, 2 tempo, 4 hammerfest)
// into a level.
var stravaLevels = map[int]string{1: "social", 2: "intermediate", 4: "elite"}

// Levels is the ride's level, or two when it depends on turnout.
func (o RideOcc) Levels() []rideLevel {
	keys := o.Level
	if keys == "" {
		for _, n := range levelNames {
			if n.re.MatchString(o.Name) {
				keys = n.levels
				break
			}
		}
	}
	if keys == "" && o.Strava != nil {
		keys = stravaLevels[o.Strava.Pace]
	}
	if keys == "" {
		keys = "intermediate"
	}
	var out []rideLevel
	for _, l := range rideLevels {
		if strings.Contains(" "+keys+" ", " "+l.Key+" ") {
			out = append(out, l)
		}
	}
	return out
}

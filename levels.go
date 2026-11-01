package main

import (
	"fmt"
	"html/template"
	"regexp"
	"slices"
	"strings"
)

// Every ride on the calendar is tagged with its level, so new riders can tell
// how fast it goes: Social, Intermediate, or Elite. An admin can set a ride's
// level (on a weekly ride, or one date). Otherwise the club's regular rides
// have theirs by name, a Strava event's pace sets the rest, and anything else
// counts as Intermediate. A virtual ride is open to all three.

type rideLevel struct{ Key, Name, MPH string }

var rideLevels = []rideLevel{
	{"social", "Social", "16–18 mph"},
	{"intermediate", "Intermediate", "18–20 mph"},
	{"elite", "Elite", "20+ mph"},
}

// levelChoices are the {level-…} widgets content pages can use. The Tuesday
// Night Ride is either, depending on who turns out.
var levelChoices = []paceChoice{{"", "Automatic"}, {"social", "Social"}, {"intermediate", "Intermediate"},
	{"elite", "Elite"}, {"intermediate elite", "Intermediate or Elite"}}

// levelBox is one of the admin's Level checkboxes.
type levelBox struct {
	rideLevel
	Checked bool
}

// levelBoxes is the Level checkboxes for a ride's level. None ticked means
// automatic.
func levelBoxes(level string) []levelBox {
	var out []levelBox
	for _, l := range rideLevels {
		out = append(out, levelBox{l, strings.Contains(" "+level+" ", " "+l.Key+" ")})
	}
	return out
}

// levelWidget is a level's tags for a content page, the way a ride's page
// shows them, like {level-intermediate-elite} on a line by itself.
func levelWidget(keys string) template.HTML {
	var parts []string
	for _, k := range strings.Fields(keys) {
		for _, l := range rideLevels {
			if l.Key == k {
				parts = append(parts, fmt.Sprintf(`<span class="lvl lvl-%s">%s</span> %s`, l.Key, l.Name, l.MPH))
			}
		}
	}
	s := strings.Join(parts, " or ")
	if len(parts) > 1 {
		s += ", depending on who turns out"
	}
	return template.HTML(`<p class="lvls page-lvls">` + s + "</p>")
}

// levelFor reads the admin's Level checkboxes, in the order rideLevels has
// them, so "social elite" is stored the same however it was ticked.
func levelFor(ticked []string) string {
	var keys []string
	for _, l := range rideLevels {
		if slices.Contains(ticked, l.Key) {
			keys = append(keys, l.Key)
		}
	}
	return strings.Join(keys, " ")
}

// The club's regular rides, by the names they go by on Strava and the calendar.
var levelNames = []struct {
	re     *regexp.Regexp
	levels string
}{
	{regexp.MustCompile(`(?i)\btuesday night\b|\btnr\b`), "intermediate elite"},
	{regexp.MustCompile(`(?i)\bthursday night\b|\bthnr\b`), "intermediate elite"}, // tempo, set by who turns out
	{regexp.MustCompile(`(?i)\bsamr\b|\bsumr\b|\bsaturday morning\b|\bsunday morning\b|\bold ?line\b|\bshop ride\b`), "social"},
}

// stravaLevels turns a Strava event's pace (1 casual, 2 tempo, 4 hammerfest)
// into a level.
var stravaLevels = map[int]string{1: "social", 2: "intermediate", 4: "elite"}

// Levels is the ride's level, or two when it depends on turnout.
func (o RideOcc) Levels() []rideLevel {
	if !o.IsRide() {
		return nil
	}
	keys := o.Level
	if keys == "" && o.IsVirtual() { // anyone can join online
		keys = "social intermediate elite"
	}
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

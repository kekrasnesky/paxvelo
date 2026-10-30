package main

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// The Routes page shows each ride once, though leaders post the same loop as
// many Strava routes ("Steer Horn", "Steerhorn Neck"). Every variation of the
// club's two big weekly rides makes one card, at the top. Other routes that
// share a place in their names and are about as long and hilly make one card
// too. Each card counts the rides of all its routes.

// routeFamilies are the club's main rides, found by route or event name.
var routeFamilies = []struct {
	name string
	re   *regexp.Regexp
}{
	{"Tuesday Night Ride", regexp.MustCompile(`(?i)\btnr\b|tuesday night|base laps`)},
	{"Thursday Night Ride", regexp.MustCompile(`(?i)\bthnr\b|thursday night`)},
}

// How close two routes must be to count as the same ride.
const (
	sameRouteMeters = 3 * 1609.344
	sameRouteClimb  = 0.15 // of the hillier one
)

// Words that don't tell routes apart: ride names, starts, and words like "point".
var routeStopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`sumr samr thnr tnr route ride rides the and from back via mile miles
		start social loop point neck road lap laps full short long way reverse south north east west down
		leonardtown lms wildewood wildwood old line shop bike csm mcdonalds pax cafe coffee center shopping`) {
		routeStopWords[w] = true
	}
}

var nonLetters = regexp.MustCompile(`[^a-z]+`)

// placeWords are the words in a route's name that say where it goes, plus each
// pair run together, so "Steer Horn" matches "Steerhorn".
func placeWords(name string) map[string]bool {
	name = strings.NewReplacer("'", "", "’", "").Replace(strings.ToLower(name))
	words := strings.Fields(nonLetters.ReplaceAllString(name, " "))
	out := map[string]bool{}
	for i, w := range words {
		if len(w) > 2 && !routeStopWords[w] {
			out[w] = true
		}
		if i > 0 && !routeStopWords[words[i-1]+w] {
			out[words[i-1]+w] = true
		}
	}
	return out
}

func sameRoute(a, b Route, aw, bw map[string]bool) bool {
	if math.Abs(a.Distance-b.Distance) > sameRouteMeters ||
		math.Abs(a.Elevation-b.Elevation) > sameRouteClimb*math.Max(a.Elevation, b.Elevation) {
		return false
	}
	for w := range aw {
		if bw[w] {
			return true
		}
	}
	return false
}

func routeFamily(r Route) int {
	for i, f := range routeFamilies {
		if f.re.MatchString(r.Name) || f.re.MatchString(r.Usual) {
			return i
		}
	}
	return -1
}

// groupRoutes makes the Routes page's cards from the club's routes: the main
// rides first, then the rest by how often the club rode them. A card the club
// rode only once is left off.
func groupRoutes(routes []Route) []routeCard {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Rides != routes[j].Rides {
			return routes[i].Rides > routes[j].Rides
		}
		return routes[i].Name < routes[j].Name
	})
	// group[i] is the route whose card route i joins (union-find).
	group := make([]int, len(routes))
	find := func(i int) int {
		for group[i] != i {
			i = group[i]
		}
		return i
	}
	family := make([]int, len(routes))
	words := make([]map[string]bool, len(routes))
	firstOf := map[int]int{}
	for i, r := range routes {
		group[i], family[i], words[i] = i, routeFamily(r), placeWords(r.Name)
		if f := family[i]; f >= 0 {
			if first, ok := firstOf[f]; ok {
				group[i] = first
			} else {
				firstOf[f] = i
			}
		}
	}
	for i := range routes {
		for j := 0; j < i; j++ {
			if family[i] < 0 && family[j] < 0 && sameRoute(routes[i], routes[j], words[i], words[j]) {
				if a, b := find(i), find(j); a != b {
					group[max(a, b)] = min(a, b) // the most ridden leads
				}
			}
		}
	}

	var cards []routeCard
	at := map[int]int{}
	for i, r := range routes {
		g := find(i)
		if k, ok := at[g]; ok {
			cards[k].Rides += r.Rides
			cards[k].Variants = append(cards[k].Variants, r)
			continue
		}
		at[g] = len(cards)
		c := routeCard{Route: r, Family: family[i]}
		if c.Family >= 0 {
			c.Name, c.Usual = routeFamilies[c.Family].name, ""
		}
		cards = append(cards, c)
	}
	var out []routeCard
	for _, c := range cards {
		if c.Rides > 1 {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, fj := out[i].Family, out[j].Family
		if (fi >= 0) != (fj >= 0) {
			return fi >= 0
		}
		if fi >= 0 {
			return fi < fj
		}
		return out[i].Rides > out[j].Rides
	})
	return out
}

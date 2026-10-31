package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// What the calendar learns from the club's Strava events beyond the date and
// title: the route (with its map, distance and climbing), the pace, who
// posted it, how many riders have joined, the start's map pin, and the
// ride notes. The club's past events also build the Routes page.
//
// Most PaxVelo events are private to the Strava club, and some start at a
// member's house. For those the public site leaves out the ride notes, a
// start that's a street address, and its map pin. Riders signed in with
// Strava (who are club members) and admins see everything.

// StravaInfo is what Strava says about an imported ride's event.
type StravaInfo struct {
	Description string  `json:"description,omitempty"`
	Organizer   string  `json:"organizer,omitempty"` // "Patrick O."
	OrganizerID int64   `json:"organizer_id,omitempty"`
	Pace        int     `json:"pace,omitempty"`  // Strava's skill level: 1 casual, 2 tempo, 4 hammerfest
	Going       int     `json:"going,omitempty"` // riders who joined the event on Strava
	Lat         float64 `json:"lat,omitempty"`
	Lng         float64 `json:"lng,omitempty"`
	Private     bool    `json:"private,omitempty"` // only club members can see the event on Strava
	RouteID     string  `json:"route_id,omitempty"`
}

// Route is a Strava route the club rides.
type Route struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Distance   float64   `json:"distance"`      // meters
	Elevation  float64   `json:"elevation"`     // meters climbed
	MovingTime int       `json:"moving_time"`   // Strava's estimate, in seconds
	Map        string    `json:"map,omitempty"` // a picture of the route on a map
	Private    bool      `json:"private,omitempty"`
	Missing    bool      `json:"missing,omitempty"` // Strava wouldn't share it (deleted, or someone's private route)
	Rides      int       `json:"rides,omitempty"`   // club events that used it
	Usual      string    `json:"usual,omitempty"`   // the event name that uses it most
	Checked    time.Time `json:"checked"`
}

// StravaClub is the club's own Strava page.
type StravaClub struct {
	Members int       `json:"members"`
	Checked time.Time `json:"checked"`
}

var paceNames = map[int]string{1: "Casual", 2: "Tempo", 4: "Hammerfest"}

// PaceLabel is the event's pace, as Strava names it.
func (s *StravaInfo) PaceLabel() string {
	if s == nil {
		return ""
	}
	return paceNames[s.Pace]
}

// Shown reports whether the route can be shown on the public site: Strava has
// sent its details, and it isn't private.
func (r *Route) Shown() bool {
	return r != nil && !r.Checked.IsZero() && !r.Private && !r.Missing && r.Name != ""
}

func (r *Route) URL() string { return "https://www.strava.com/routes/" + r.ID }

func (r *Route) Miles() string { return fmt.Sprintf("%.0f mi", r.Distance/1609.344) }

func (r *Route) Feet() string {
	return fmt.Sprintf("%s ft", thousands(int(math.Round(r.Elevation*3.28084/10)*10)))
}

// TimeLabel is Strava's estimate of the riding time, like "1 hr 5 min".
func (r *Route) TimeLabel() string {
	if r.MovingTime <= 0 {
		return ""
	}
	m := (r.MovingTime + 299) / 300 * 5 // to the next 5 minutes
	switch {
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m%60 == 0:
		return fmt.Sprintf("%d hr", m/60)
	}
	return fmt.Sprintf("%d hr %d min", m/60, m%60)
}

// Stats is the route in one line: "21 mi · 400 ft of climbing".
func (r *Route) Stats() string { return r.Miles() + " · " + r.Feet() + " of climbing" }

func thousands(n int) string {
	s := fmt.Sprint(n)
	if n >= 1000 {
		s = s[:len(s)-3] + "," + s[len(s)-3:]
	}
	return s
}

var streetRE = regexp.MustCompile(`^\d+\s`)

// homeStart reports whether the ride starts at a street address on an event
// only the club can see: likely someone's house.
func (o RideOcc) homeStart() bool {
	return o.Strava != nil && o.Strava.Private && streetRE.MatchString(o.Place)
}

// PublicPlace is where the ride starts, for pages and feeds anyone can see.
func (o RideOcc) PublicPlace() string {
	if o.homeStart() {
		return ""
	}
	return o.Place
}

// PlaceFor is where the ride starts, for this visitor.
func (o RideOcc) PlaceFor(member bool) string {
	if member {
		return o.Place
	}
	return o.PublicPlace()
}

// NotesFor is the ride notes from Strava, for this visitor.
func (o RideOcc) NotesFor(member bool) string {
	if o.Strava == nil || (o.Strava.Private && !member) {
		return ""
	}
	return o.Strava.Description
}

// HiddenNotes reports whether the event has notes only club members can see.
func (o RideOcc) HiddenNotes(member bool) bool {
	return o.Strava != nil && o.Strava.Description != "" && o.NotesFor(member) == ""
}

// DirectionsFor links to directions to the start, for this visitor.
func (o RideOcc) DirectionsFor(member bool) string {
	if o.Strava == nil || o.Strava.Lat == 0 || (o.homeStart() && !member) {
		return ""
	}
	return fmt.Sprintf("https://www.google.com/maps/dir/?api=1&destination=%.6f,%.6f", o.Strava.Lat, o.Strava.Lng)
}

// Meta is the short line of facts under a ride in calendar lists.
func (o RideOcc) Meta() string {
	var parts []string
	if o.Route.Shown() {
		parts = append(parts, o.Route.Miles())
	}
	if o.Strava != nil && o.Strava.Going > 0 && !o.Cancelled {
		parts = append(parts, fmt.Sprintf("%d going", o.Strava.Going))
	}
	return strings.Join(parts, " · ")
}

// ---- Store ----

// withRoute adds the ride's route to it. Callers must hold s.mu.
func (s *Store) withRoute(o *RideOcc) {
	if o.Strava == nil || o.Strava.RouteID == "" {
		return
	}
	if r := s.d.Routes[o.Strava.RouteID]; r != nil {
		c := *r
		o.Route = &c
	}
}

// staleRoutes lists routes that haven't been read from Strava in a week, or
// ever, the ones the calendar uses first.
func (s *Store) staleRoutes(first []string, limit int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		r := s.d.Routes[id]
		if id == "" || seen[id] || len(out) >= limit || (r != nil && time.Since(r.Checked) < 7*24*time.Hour) {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range first {
		add(id)
	}
	ids := make([]string, 0, len(s.d.Routes))
	for id := range s.d.Routes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return s.d.Routes[ids[i]].Rides > s.d.Routes[ids[j]].Rides })
	for _, id := range ids {
		add(id)
	}
	return out
}

// saveRoute keeps what Strava says about a route, and keeps its club history.
func (s *Store) saveRoute(r Route) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Routes == nil {
		s.d.Routes = map[string]*Route{}
	}
	if old := s.d.Routes[r.ID]; old != nil {
		r.Rides, r.Usual = old.Rides, old.Usual
		if r.Missing && old.Name != "" { // keep what we knew, but stop showing it
			old.Missing, old.Checked = true, r.Checked
			s.saveOrLog()
			return
		}
	}
	s.d.Routes[r.ID] = &r
	s.saveOrLog()
}

// routeHistory is how often the club's events used each route, and the event
// name that used it most.
type routeHistory struct {
	Rides int
	Usual string
	Name  string
}

// SetRouteHistory records how the club's past events used each route.
func (s *Store) SetRouteHistory(h map[string]routeHistory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Routes == nil {
		s.d.Routes = map[string]*Route{}
	}
	for id, r := range s.d.Routes {
		r.Rides, r.Usual = h[id].Rides, h[id].Usual
	}
	for id, x := range h {
		if s.d.Routes[id] == nil {
			s.d.Routes[id] = &Route{ID: id, Name: x.Name, Rides: x.Rides, Usual: x.Usual}
		}
	}
	now := time.Now().UTC()
	s.d.RoutesChecked = &now
	s.saveOrLog()
}

func (s *Store) routeHistoryDue() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.RoutesChecked == nil || time.Since(*s.d.RoutesChecked) > 24*time.Hour || s.d.History == nil
}

// ClubRoutes returns the routes the club rides that anyone can see.
// groupRoutes makes them into the Routes page's cards.
func (s *Store) ClubRoutes() []Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Route
	for _, r := range s.d.Routes {
		if r.Shown() && r.Rides > 0 {
			out = append(out, *r)
		}
	}
	return out
}

func (s *Store) StravaClub() StravaClub {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.StravaClub == nil {
		return StravaClub{}
	}
	return *s.d.StravaClub
}

func (s *Store) SetStravaClub(c StravaClub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.StravaClub = &c
	s.saveOrLog()
}

// ---- Reading from Strava ----

type stravaMapURLs struct {
	URL   string `json:"url"`
	Light string `json:"light_url"` // sharper, for high-resolution screens
}

type stravaRoute struct {
	ID            json.Number   `json:"id"`
	Name          string        `json:"name"`
	Distance      float64       `json:"distance"`
	ElevationGain float64       `json:"elevation_gain"`
	MovingTime    int           `json:"estimated_moving_time"`
	Private       bool          `json:"private"`
	MapURLs       stravaMapURLs `json:"map_urls"`
}

// stravaGetter returns a function that reads from Strava's API with the
// connected account, renewing its key first if it's about to run out.
func (a *App) stravaGetter(ctx context.Context) (func(path string, out any) error, error) {
	link, ok := a.store.StravaLink()
	if !ok || !a.stravaReady() {
		return nil, errNotConnected
	}
	if link.AccessToken == "" || time.Now().Unix() > link.Expires-300 {
		tok, err := a.stravaToken(ctx, map[string][]string{"refresh_token": {link.RefreshToken}, "grant_type": {"refresh_token"}})
		if err != nil {
			return nil, fmt.Errorf("Strava didn’t accept the connection (%v). Connect again", err)
		}
		link.AccessToken, link.Expires = tok.AccessToken, tok.ExpiresAt
		if tok.RefreshToken != "" {
			link.RefreshToken = tok.RefreshToken
		}
		a.store.updateStravaLink(func(l *StravaLink) {
			l.AccessToken, l.Expires, l.RefreshToken = link.AccessToken, link.Expires, link.RefreshToken
		})
	}
	return func(path string, out any) error {
		req, _ := http.NewRequestWithContext(ctx, "GET", a.cfg.StravaAPI+"/api/v3"+path, nil)
		req.Header.Set("Authorization", "Bearer "+link.AccessToken)
		return stravaDo(req, out)
	}, nil
}

// Strava allows 100 requests every 15 minutes. An hourly import reads the
// events, who's going to each (up to maxRSVPReads), and a few routes.
const (
	maxRSVPReads  = 30
	maxRouteReads = 25
)

// readGoing counts the riders who joined each event on Strava.
func readGoing(get func(string, any) error, ids []string) map[string]int {
	going := map[string]int{}
	for i, id := range ids {
		if i >= maxRSVPReads {
			break
		}
		var athletes []json.RawMessage
		if err := get("/group_events/"+id+"/athletes?per_page=200", &athletes); err == nil {
			going[id] = len(athletes)
		}
	}
	return going
}

// readRoutes updates the routes the calendar uses, then the club's other
// routes a few at a time.
func (a *App) readRoutes(get func(string, any) error, first []string) {
	for _, id := range a.store.staleRoutes(first, maxRouteReads) {
		var sr stravaRoute
		r := Route{ID: id, Checked: time.Now().UTC()}
		if err := get("/routes/"+id, &sr); err != nil {
			r.Missing = true
		} else {
			r.Name, r.Distance, r.Elevation, r.MovingTime, r.Private = cleanName(sr.Name), sr.Distance, sr.ElevationGain, sr.MovingTime, sr.Private
			r.Map = or(sr.MapURLs.Light, sr.MapURLs.URL)
		}
		a.store.saveRoute(r)
	}
}

// readHistory reads every club event Strava still has, to count how often
// each route is ridden, along with the club's member count. Once a day.
func (a *App) readHistory(get func(string, any) error) error {
	if !a.store.routeHistoryDue() {
		return nil
	}
	var club struct {
		MemberCount int `json:"member_count"`
	}
	if err := get("/clubs/"+a.cfg.StravaClubID, &club); err == nil && club.MemberCount > 0 {
		a.store.SetStravaClub(StravaClub{Members: club.MemberCount, Checked: time.Now().UTC()})
	}
	type pastEvent struct {
		ID      json.Number `json:"id"`
		Title   string      `json:"title"`
		RouteID json.Number `json:"route_id"`
		Route   *struct {
			Name string `json:"name"`
		} `json:"route"`
		Occurrences []string       `json:"upcoming_occurrences"` // for a past event, the day it was
		Address     string         `json:"address"`
		Private     bool           `json:"private"`
		Organizer   *stravaAthlete `json:"organizing_athlete"`
	}
	var past []PastRide
	seen := map[string]bool{}
	names := map[string]map[string]int{}
	h := map[string]routeHistory{}
	for page := 1; page <= 10; page++ {
		var events []pastEvent
		if err := get(fmt.Sprintf("/clubs/%s/group_events?per_page=200&page=%d", a.cfg.StravaClubID, page), &events); err != nil {
			return fmt.Errorf("reading the club’s past events: %w", err)
		}
		fresh := 0
		for _, ev := range events {
			if seen[ev.ID.String()] {
				continue
			}
			seen[ev.ID.String()], fresh = true, fresh+1
			if pr, ok := a.pastRide(ev.ID.String(), ev.Title, ev.RouteID.String(), ev.Address, ev.Private, ev.Occurrences, ev.Organizer); ok {
				past = append(past, pr)
			}
			id := ev.RouteID.String()
			if id == "" || id == "0" {
				continue
			}
			x := h[id]
			x.Rides++
			if ev.Route != nil && x.Name == "" {
				x.Name = cleanName(ev.Route.Name)
			}
			h[id] = x
			if names[id] == nil {
				names[id] = map[string]int{}
			}
			names[id][cleanName(ev.Title)]++
		}
		if fresh == 0 || len(events) < 200 {
			break
		}
	}
	// A route's usual ride is the event name used most for it, if that's a
	// regular ride ("SuMR") and not one dated event's name.
	for id, x := range h {
		best := 0
		for n, c := range names[id] {
			if c > best || (c == best && n < x.Usual) {
				x.Usual, best = n, c
			}
		}
		if best < 2 || best*4 < x.Rides {
			x.Usual = ""
		}
		h[id] = x
	}
	a.store.SetRouteHistory(h)
	a.store.SaveHistory(past)
	a.importPastRides()
	return nil
}

// ---- The Routes page ----

type routesView struct {
	Routes    []routeCard
	Members   int
	ClubURL   string
	Connected bool
}

// routeCard is one ride on the Routes page: its most ridden route, with the
// rides of all its routes added up.
type routeCard struct {
	Route
	Family   int      // which of routeFamilies it is, or -1
	Variants []Route  // its other routes, most ridden first
	Next     *RideOcc // the next ride on the calendar that uses it
}

func (a *App) routesPage(w http.ResponseWriter, r *http.Request) {
	_, connected := a.store.StravaLink()
	v := routesView{Members: a.store.StravaClub().Members, ClubURL: a.cfg.StravaURL, Connected: connected}
	today := a.today()
	rides := a.store.Rides(today, today.AddDate(0, 0, 60))
	for _, c := range groupRoutes(a.store.ClubRoutes()) {
		ids := map[string]bool{c.ID: true}
		for _, r := range c.Variants {
			ids[r.ID] = true
		}
		for _, o := range rides {
			if o.Cancelled {
				continue
			}
			if (o.Strava != nil && ids[o.Strava.RouteID]) || (c.Family >= 0 && routeFamilies[c.Family].re.MatchString(o.Name)) {
				c.Next = &o
				break
			}
		}
		v.Routes = append(v.Routes, c)
	}
	a.site.render(w, http.StatusOK, "routes", "/routes", "Club Routes — "+siteName, v)
}

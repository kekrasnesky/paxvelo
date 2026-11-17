package main

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// A ride's route without a Strava event: whoever adds the ride, its leader
// (with the club PIN) or an admin pastes a Strava route link, or picks one of
// the club's routes from the list the Strava import keeps. The site reads its
// name, distance, climbing and map from Strava (with the connected account)
// and shows them at the top of the ride page; riders open it on Strava, where
// "Save" sends it to a Garmin or Wahoo. A route from the ride's Strava event
// still counts when none is set.

var routeLinkRE = regexp.MustCompile(`^(?:(?:https?://)?(?:www\.)?strava\.com/routes/)?(\d{1,20})(?:[/?#].*)?$`)

// parseRoute reads the Route box: a Strava route link or its number, or
// nothing. ok is false for anything else.
func parseRoute(s string) (id string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	m := routeLinkRE.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func routeURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://www.strava.com/routes/" + id
}

// RouteURL is the Route box's value.
func (c CalRide) RouteURL() string    { return routeURL(c.RouteID) }
func (s RideSeries) RouteURL() string { return routeURL(s.RouteID) }

// routeIDOf is the route a ride uses: its own, else its Strava event's.
func (o RideOcc) routeIDOf() string {
	if o.RouteID != "" {
		return o.RouteID
	}
	if o.Strava != nil {
		return o.Strava.RouteID
	}
	return ""
}

// RouteOnStrava is a link for a route the site hasn't read yet (Strava isn't
// connected, or the import hasn't got to it), or "" when there's nothing to
// show: no route, or one Strava keeps private.
func (o RideOcc) RouteOnStrava() string {
	id := o.routeIDOf()
	switch {
	case id == "", o.Route != nil && (o.Route.Private || o.Route.Missing || o.Route.Shown()):
		return ""
	}
	return routeURL(id)
}

// RouteStats is the route's distance and climbing for the top of the ride
// page: "21 mi · 400 ft".
func (o RideOcc) RouteStats() string {
	if !o.Route.Shown() {
		return ""
	}
	return o.Route.Miles() + " · " + o.Route.Feet()
}

// routeProblem is the message for a Route box that isn't a route.
const routeProblem = "Paste the route’s Strava link, like https://www.strava.com/routes/123, or pick a club route."

// rideRouteIDs are the routes rides on the calendar set themselves, for the
// hourly Strava import to read first.
func (s *Store) rideRouteIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen, out := map[string]bool{}, []string{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, se := range s.d.Series {
		add(se.RouteID)
	}
	for _, c := range s.d.CalRides {
		add(c.RouteID)
	}
	return out
}

// routeKnown says the site has read a route from Strava in the last week.
func (s *Store) routeKnown(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.d.Routes[id]
	return r != nil && time.Since(r.Checked) < 7*24*time.Hour
}

// readRouteSoon reads a newly set route from Strava in the background, so
// its stats show on the ride page in a moment, when Strava is connected.
func (a *App) readRouteSoon(id string) {
	if id == "" || a.store.routeKnown(id) {
		return
	}
	if _, ok := a.store.StravaLink(); !ok || !a.stravaReady() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if get, err := a.stravaGetter(ctx); err == nil {
			a.readRoute(get, id)
		}
	}()
}

// setRideRoute saves the route from a leader's form (name + club PIN).
func (a *App) setRideRoute(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	name, code, ok := a.leaderPost(w, r)
	if !ok {
		return
	}
	id, good := parseRoute(r.FormValue("route"))
	if code == "" && !good {
		code = "route"
	}
	if code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	_, err := a.store.UpdateRide(key, func(c *CalRide) error {
		if _, leading := c.groupOf(name); !leading {
			return errNotYours
		}
		c.RouteID = id
		return nil
	})
	if code := rideErrCode(err); code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	a.readRouteSoon(id)
	rideBack(w, r, key, "ok=route")
}

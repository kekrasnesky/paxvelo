package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseRoute(t *testing.T) {
	for in, want := range map[string]string{
		"":                                      "",
		"https://www.strava.com/routes/3245678": "3245678",
		"strava.com/routes/99?share=1":          "99",
		"http://strava.com/routes/12/":          "12",
		"  31415 ":                              "31415",
	} {
		if got, ok := parseRoute(in); !ok || got != want {
			t.Errorf("parseRoute(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"https://example.com/routes/1", "https://www.strava.com/clubs/10197/group_events/9", "abc"} {
		if _, ok := parseRoute(bad); ok {
			t.Errorf("parseRoute(%q) should fail", bad)
		}
	}
}

// A leader sets a route with the club PIN; the ride page shows its distance
// and climbing at the top once the site knows the route, a link to it on
// Strava before then, and nothing for a private one. A weekly ride's route
// carries to its dates.
func TestRideRoute(t *testing.T) {
	a, h, _ := rideApp(t)
	a.store.SetClubPIN("8356")
	o, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 2).Format("2006-01-02"), Time: "08:00", Name: "Hills", Leaders: []Leader{{Name: "Jane Doe"}}})
	set := func(route string) string {
		ct, body := form(url.Values{"leader_name": {"Jane Doe"}, "pin": {"8356"}, "route": {route}})
		return do(t, h, nil, "POST", "/calendar/ride/"+o.Key+"/route", ct, body).Header().Get("Location")
	}
	page := func() string { return do(t, h, nil, "GET", "/calendar/ride/"+o.Key, "", nil).Body.String() }

	if loc := set("https://example.com/routes/5"); !strings.Contains(loc, "err=route") {
		t.Errorf("a bad link: %q", loc)
	}
	if loc := set("https://www.strava.com/routes/777"); !strings.Contains(loc, "ok=route") {
		t.Fatalf("set route: %q", loc)
	}
	if p := page(); !strings.Contains(p, `href="https://www.strava.com/routes/777"`) || !strings.Contains(p, "On Strava") {
		t.Error("before the site reads the route, the page should link to it on Strava")
	}
	a.store.saveRoute(Route{ID: "777", Name: "Hilly Loop", Distance: 40 * 1609.344, Elevation: 610, Checked: time.Now()})
	p := page()
	if !strings.Contains(p, `<span class="ride-stats"> · 40 mi · 2,000 ft</span>`) || !strings.Contains(p, "Hilly Loop</a>, 40 mi") || strings.Contains(p, "On Strava</a>") {
		t.Error("the page should show the route's distance and climbing up top")
	}
	if got, _ := a.store.Ride(o.Key); got.Meta() != "40 mi" {
		t.Errorf("calendar line = %q", got.Meta())
	}
	a.store.saveRoute(Route{ID: "777", Name: "Hilly Loop", Private: true, Checked: time.Now()})
	if p := page(); strings.Contains(p, "Hilly Loop") || strings.Contains(p, "On Strava</a>") || strings.Contains(p, "ride-stats") {
		t.Error("a private route shouldn't show")
	}
	set("")
	if got, _ := a.store.Ride(o.Key); got.RouteID != "" {
		t.Error("an empty box clears the route")
	}

	// Admins: on a ride's form, and on a weekly ride, whose dates follow it.
	rec := httptest.NewRecorder()
	a.setSession(rec, "Board Member", 3600)
	admin := rec.Result().Cookies()[0]
	ct, body := form(url.Values{"name": {"Hills"}, "date": {o.Date}, "time": {"08:00"}, "pace": {""}, "route": {"not a route"}})
	if w := do(t, h, admin, "POST", "/admin/rides/"+o.Key, ct, body); w.Code != 400 || !strings.Contains(w.Body.String(), "routes/123") {
		t.Errorf("admin bad route: %d", w.Code)
	}
	if p := do(t, h, admin, "GET", "/admin/rides/"+o.Key, "", nil).Body.String(); !strings.Contains(p, `name="route"`) || !strings.Contains(p, `<datalist id="club-routes">`) {
		t.Error("the admin ride form should have the route box and the club's routes")
	}
	se, _ := a.store.Series(1)
	vals := url.Values{"name": {se.Name}, "weekday": {"2"}, "time": {se.Time}, "pace": {""}, "route": {"https://www.strava.com/routes/4242"}}
	ct, body = form(vals)
	do(t, h, admin, "POST", "/admin/weekly/1", ct, body)
	if got, _ := a.store.Ride(seriesKey(1, nextWeekday(a, time.Tuesday))); got.RouteID != "4242" {
		t.Errorf("weekly ride's date route = %q", got.RouteID)
	}
	if ids := a.store.rideRouteIDs(); len(ids) == 0 || ids[0] != "4242" {
		t.Errorf("routes for the import to read first: %v", ids)
	}
}

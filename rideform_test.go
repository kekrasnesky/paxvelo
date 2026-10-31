package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The admin's Add a ride page: what it is (a ride, a clinic, a meeting…),
// level tickboxes, a description, and repeating every week.

func adminCookie(a *App) *http.Cookie {
	w := httptest.NewRecorder()
	a.setSession(w, "Board Member", 3600)
	return w.Result().Cookies()[0]
}

func TestAddEventThatIsntARide(t *testing.T) {
	a, h, sent := rideApp(t)
	admin := adminCookie(a)
	sat := nextWeekday(a, time.Saturday)
	// Pace groups and a level don't apply to a meeting, even if they're sent.
	ct, body := form(url.Values{"kind": {"meeting"}, "name": {"Club Meeting"}, "date": {sat}, "time": {"19:00"}, "pace": {"3"},
		"level": {"elite"}, "leader:": {"Sam P."}, "details": {"Agenda:\r\n\r\n\r\n- next year's kit\n- the century"}})
	w := do(t, h, admin, "POST", "/admin/rides/new", ct, body)
	key := strings.TrimPrefix(strings.Split(w.Header().Get("Location"), "?")[0], "/admin/rides/")
	o, ok := a.store.Ride(key)
	if !ok || o.Kind != "meeting" || o.Groups != nil || o.Level != "" || o.Levels() != nil || o.Status() != "event" {
		t.Fatalf("meeting: %+v", o)
	}
	if o.Details != "Agenda:\n\n- next year's kit\n- the century" {
		t.Errorf("description: %q", o.Details)
	}
	if o.LeaderSummary() != "Club meeting · Sam P." {
		t.Errorf("summary: %q", o.LeaderSummary())
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0].body, "added club meeting to the calendar") || strings.Contains((*sent)[0].body, "needs leaders") {
		t.Errorf("notice: %+v", *sent)
	}
	// Nobody signs up to lead it, and it never needs a leader.
	page := do(t, h, nil, "GET", "/calendar/ride/"+key, "", nil).Body.String()
	if strings.Contains(page, "I’ll lead") || !strings.Contains(page, "Host") || !strings.Contains(page, "Sam P.") || strings.Contains(page, "<dt>Level</dt>") {
		t.Error("an event's page shouldn't offer leading it, or a level")
	}
	ct, body = form(url.Values{"group": {""}, "leader_name": {"Pat Rider"}, "ft": {oldToken(a)}})
	if loc := do(t, h, nil, "POST", "/calendar/ride/"+key+"/lead", ct, body).Header().Get("Location"); strings.Contains(loc, "ok=led") {
		t.Errorf("signed up to lead a meeting: %q", loc)
	}
	for _, it := range a.attention() {
		if strings.Contains(it.Text, "Club Meeting") {
			t.Errorf("a meeting shouldn't need a leader: %q", it.Text)
		}
	}
}

func TestLevelTickboxes(t *testing.T) {
	a, h, _ := rideApp(t)
	admin := adminCookie(a)
	ct, body := form(url.Values{"name": {"Hill Repeats"}, "date": {nextWeekday(a, time.Friday)}, "level": {"elite", "social"}})
	w := do(t, h, admin, "POST", "/admin/rides/new", ct, body)
	key := strings.TrimPrefix(strings.Split(w.Header().Get("Location"), "?")[0], "/admin/rides/")
	if o, _ := a.store.Ride(key); o.Level != "social elite" {
		t.Errorf("level: %q", o.Level)
	}
	page := do(t, h, admin, "GET", "/admin/rides/"+key, "", nil).Body.String()
	if !strings.Contains(page, `value="social" checked`) || !strings.Contains(page, `value="elite" checked`) || strings.Contains(page, `value="intermediate" checked`) {
		t.Error("the ride page should tick the ride's levels")
	}
}

func TestAddRideRepeatingWeekly(t *testing.T) {
	a, h, sent := rideApp(t)
	admin := adminCookie(a)
	wed := nextWeekday(a, time.Wednesday)
	d, _ := time.Parse("2006-01-02", wed)
	until := d.AddDate(0, 0, 14).Format("2006-01-02")
	add := func(v url.Values) *httptest.ResponseRecorder {
		ct, body := form(v)
		return do(t, h, admin, "POST", "/admin/rides/new", ct, body)
	}
	// Repeating needs a start time, and a last date after the first.
	if w := add(url.Values{"name": {"Skills Clinic"}, "date": {wed}, "repeat": {"weekly"}}); w.Code != http.StatusBadRequest {
		t.Errorf("weekly without a time: %d", w.Code)
	}
	if w := add(url.Values{"name": {"Skills Clinic"}, "date": {wed}, "time": {"18:00"}, "repeat": {"weekly"}, "repeat_until": {d.AddDate(0, 0, -7).Format("2006-01-02")}}); w.Code != http.StatusBadRequest {
		t.Errorf("weekly ending before it starts: %d", w.Code)
	}
	w := add(url.Values{"kind": {"clinic"}, "name": {"Skills Clinic"}, "date": {wed}, "time": {"18:00"}, "repeat": {"weekly"}, "repeat_until": {until}, "leader:": {"Coach K."}})
	if !strings.Contains(w.Header().Get("Location"), "ok=weekly-added") {
		t.Fatalf("add weekly: %d %s", w.Code, w.Header().Get("Location"))
	}
	var dates []string
	for _, o := range a.store.Rides(a.today(), a.today().AddDate(0, 0, 42)) {
		if o.Name == "Skills Clinic" {
			dates = append(dates, o.Date)
			if o.Kind != "clinic" || o.SeriesID == 0 {
				t.Errorf("clinic date: %+v", o.CalRide)
			}
			if o.Date == wed && (o.leaderFor("") == nil || o.leaderFor("").Name != "Coach K.") {
				t.Errorf("the first date should have its instructor: %+v", o.Leaders)
			}
		}
	}
	if len(dates) != 3 || dates[0] != wed || dates[2] != until {
		t.Errorf("weekly dates: %v", dates)
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0].subject, "every Wednesday") {
		t.Errorf("notice: %+v", *sent)
	}
}

func TestStravaRoutePreview(t *testing.T) {
	a, h, _ := rideApp(t)
	tue := nextWeekday(a, time.Tuesday)
	fakeEventsMu.Lock()
	fakeEvents = []map[string]any{
		{"id": 801, "title": "Tuesday Night Ride", "upcoming_occurrences": []string{tue + "T18:00:00Z"}, "route_id": json.Number("3550943418851772710")},
		{"id": 802, "title": "Secret Ride", "upcoming_occurrences": []string{tue + "T07:00:00Z"}, "route_id": 77},
	}
	fakeEventsMu.Unlock()
	admin := adminCookie(a)
	preview := func(link string) stravaPreview {
		var p stravaPreview
		w := do(t, h, admin, "GET", "/admin/strava/preview?url="+url.QueryEscape(link), "", nil)
		json.NewDecoder(w.Body).Decode(&p)
		return p
	}
	if p := preview("https://www.strava.com/clubs/10197/group_events/801"); !strings.Contains(p.Note, "Connect Strava") {
		t.Errorf("before connecting: %+v", p)
	}
	w := do(t, h, admin, "POST", "/admin/strava/connect", "", nil)
	u, _ := url.Parse(w.Header().Get("Location"))
	do(t, h, admin, "GET", "/admin/strava/callback?code=ann&state="+url.QueryEscape(u.Query().Get("state")), "", nil)

	// An imported event, and one Strava sends when asked.
	for _, id := range []string{"801", "901"} {
		p := preview("https://www.strava.com/clubs/10197/group_events/" + id)
		if p.Map != "https://maps.example/a@2x" || p.Route != "St Mary's Cafe Loop" || !strings.Contains(p.Stats, "21 mi") || p.Title == "" {
			t.Errorf("event %s: %+v", id, p)
		}
	}
	if p := preview("https://www.strava.com/clubs/10197/group_events/802"); p.Map != "" || p.Note == "" {
		t.Errorf("a private route: %+v", p)
	}
	if p := preview("https://www.strava.com/clubs/10197/group_events/999"); p.Map != "" || !strings.Contains(p.Note, "wouldn’t show") {
		t.Errorf("an unknown event: %+v", p)
	}
	if p := preview("https://example.com/group_events/801"); !strings.Contains(p.Note, "isn’t a Strava event link") {
		t.Errorf("not Strava: %+v", p)
	}
	// Only admins can ask.
	if w := do(t, h, nil, "GET", "/admin/strava/preview?url=x", "", nil); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "note") {
		t.Error("the preview answered someone who isn't logged in")
	}
}

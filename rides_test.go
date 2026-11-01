package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStrava stands in for Strava: the one-time code says who is signing in.
func fakeStrava(t *testing.T) *httptest.Server {
	type who struct {
		id                int64
		first, last       string
		member, clubAdmin bool
	}
	people := map[string]who{
		"jane":     {1, "Jane", "Doe", true, false},
		"bob":      {2, "Bob", "Smith", true, false},
		"ann":      {3, "Ann", "Admin", true, true},
		"outsider": {4, "Otto", "Side", false, false},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("grant_type") == "refresh_token" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-ann", "refresh_token": "refresh-2", "expires_at": time.Now().Add(6 * time.Hour).Unix()})
			return
		}
		p, ok := people[r.FormValue("code")]
		if !ok || r.FormValue("client_secret") != "secret" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + r.FormValue("code"), "refresh_token": "refresh-1", "expires_at": 1,
			"athlete": map[string]any{"id": p.id, "firstname": p.first, "lastname": p.last}})
	})
	mux.HandleFunc("GET /api/v3/athlete/clubs", func(w http.ResponseWriter, r *http.Request) {
		p := people[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer tok-")]
		// Like Strava since 2026, the list doesn't say who's an admin.
		clubs := []map[string]any{{"id": 555}}
		if p.member {
			clubs = append(clubs, map[string]any{"id": 10197})
		}
		json.NewEncoder(w).Encode(clubs)
	})
	mux.HandleFunc("GET /api/v3/clubs/10197", func(w http.ResponseWriter, r *http.Request) {
		p := people[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer tok-")]
		json.NewEncoder(w).Encode(map[string]any{"id": 10197, "admin": p.clubAdmin, "owner": false, "member_count": 148})
	})
	mux.HandleFunc("GET /api/v3/group_events/{id}/athletes", func(w http.ResponseWriter, r *http.Request) {
		going := []map[string]any{}
		switch r.PathValue("id") {
		case "601":
			going = append(going, map[string]any{"id": 1}, map[string]any{"id": 2}, map[string]any{"id": 3})
		case "701":
			going = append(going, map[string]any{"id": 31, "firstname": "Laura", "lastname": "Smith"}, map[string]any{"id": 32, "firstname": "Tim", "lastname": "Hall"})
		}
		json.NewEncoder(w).Encode(going)
	})
	mux.HandleFunc("GET /api/v3/group_events/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "901": // an event the calendar hasn't imported
			json.NewEncoder(w).Encode(map[string]any{"id": 901, "title": "Cafe Loop Clinic", "route_id": json.Number("3550943418851772710")})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	mux.HandleFunc("GET /api/v3/routes/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "3550943418851772710": // bigger than a float64 holds exactly, like Strava's real route IDs
			json.NewEncoder(w).Encode(map[string]any{"id": json.Number("3550943418851772710"), "name": "St Mary's Cafe Loop", "distance": 34488.3,
				"elevation_gain": 122.4, "estimated_moving_time": 3882, "map_urls": map[string]any{"url": "https://maps.example/a", "light_url": "https://maps.example/a@2x"}})
		case "77":
			json.NewEncoder(w).Encode(map[string]any{"id": 77, "name": "Someone's Secret Loop", "distance": 1000, "private": true})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	mux.HandleFunc("GET /api/v3/clubs/10197/admins", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"firstname": "Ann", "lastname": "A."}})
	})
	mux.HandleFunc("GET /api/v3/clubs/10197/group_events", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-ann" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fakeEventsMu.Lock()
		defer fakeEventsMu.Unlock()
		json.NewEncoder(w).Encode(fakeEvents)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type notice struct{ to, subject, body string }

func rideApp(t *testing.T) (*App, http.Handler, *[]notice) {
	t.Helper()
	cfg := testConfig()
	cfg.DataDir, cfg.AdminPassword = t.TempDir(), "test-password-123"
	cfg.StravaClientID, cfg.StravaClientSecret, cfg.StravaClubID = "client", "secret", "10197"
	cfg.StravaAPI, cfg.RidesEmail = fakeStrava(t).URL, "PaxVeloClub@gmail.com"
	store, err := openStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	store.d.Series, store.d.NextSeries = testSeries(), 3
	site, err := newSite(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := openStats(cfg.DataDir, store.Secret(), cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, store: store, site: site, limit: newLimiter(), stats: stats}
	a.mailer = newMailer(cfg, store, site)
	a.mailer.stats = stats
	var mu sync.Mutex
	var sent []notice
	a.mailer.onNotify = func(to, subject, body string) {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, notice{to, subject, body})
	}
	return a, a.routes(), &sent
}

// nextWeekday is the next date (after today) falling on a weekday.
func nextWeekday(a *App, wd time.Weekday) string {
	d := a.today().AddDate(0, 0, 1)
	for d.Weekday() != wd {
		d = d.AddDate(0, 0, 1)
	}
	return d.Format("2006-01-02")
}

// Anyone takes an empty spot by typing their name. With their name and the
// club PIN, a leader steps down, adds the Strava link, or cancels the ride;
// nobody can change someone else's spot from the calendar.
func TestRideLeaders(t *testing.T) {
	a, h, sent := rideApp(t)
	a.store.SetClubPIN("8356")
	key := seriesKey(1, nextWeekday(a, time.Tuesday))
	groupA, groupB := "A group", "B group"
	post := func(path string, vals url.Values) string {
		v := url.Values{"ft": {oldToken(a)}}
		for k, x := range vals {
			v[k] = x
		}
		ct, body := form(v)
		return do(t, h, nil, "POST", "/calendar/ride/"+key+path, ct, body).Header().Get("Location")
	}
	lead := func(name, group string) string {
		return post("/lead", url.Values{"leader_name": {name}, "group": {group}})
	}
	page := do(t, h, nil, "GET", "/calendar/ride/"+key, "", nil).Body.String()
	if strings.Contains(page, "Sign in with Strava") || strings.Contains(page, "Leading this ride?") {
		t.Error("no Strava sign-in, and nothing to manage before anyone leads")
	}

	if loc := lead("Jane Doe", groupA); !strings.Contains(loc, "ok=led") {
		t.Fatalf("jane lead: %q", loc)
	}
	if len(*sent) != 0 {
		t.Errorf("leader changes aren't emailed: %+v", *sent)
	}
	// Each pace group has its own leader: Bob can lead B, but Jane can't lead both.
	if loc := lead("jane doe", groupB); !strings.Contains(loc, "err=already") {
		t.Errorf("jane led two groups: %q", loc)
	}
	if loc := lead("Bob Smith", groupB); !strings.Contains(loc, "ok=led") {
		t.Errorf("bob lead B: %q", loc)
	}
	if o, _ := a.store.Ride(key); o.Status() != "led" || o.LeaderSummary() != "Jane Doe, Bob Smith" {
		t.Errorf("both groups led: %q %q", o.Status(), o.LeaderSummary())
	}
	// A filled spot can't be taken from the calendar.
	if loc := lead("Ann Admin", groupA); !strings.Contains(loc, "err=taken") {
		t.Errorf("took a led spot: %q", loc)
	}
	if page := do(t, h, nil, "GET", "/calendar/ride/"+key, "", nil).Body.String(); !strings.Contains(page, "Leading this ride?") || !strings.Contains(page, `name="pin"`) {
		t.Error("the ride page should let leaders manage it with the PIN")
	}

	// Stepping down needs the club PIN and a leader's name.
	manage := func(path, name, pin string, extra url.Values) string {
		v := url.Values{"leader_name": {name}, "pin": {pin}}
		for k, x := range extra {
			v[k] = x
		}
		return post(path, v)
	}
	if loc := manage("/stepdown", "Bob Smith", "", nil); !strings.Contains(loc, "err=pinneeded") {
		t.Errorf("no PIN: %q", loc)
	}
	if loc := manage("/stepdown", "Bob Smith", "1234", nil); !strings.Contains(loc, "err=pin") {
		t.Errorf("wrong PIN: %q", loc)
	}
	if loc := manage("/stepdown", "Someone Else", "8356", nil); !strings.Contains(loc, "err=notyours") {
		t.Errorf("not a leader: %q", loc)
	}
	*sent = nil
	if loc := manage("/stepdown", "bob smith", "8356", nil); !strings.Contains(loc, "ok=stepped") {
		t.Errorf("bob steps down: %q", loc)
	}
	if o, _ := a.store.Ride(key); o.Status() != "partial" || o.leaderFor(groupB) != nil {
		t.Errorf("after stepping down: %+v", o.Leaders)
	}
	if len(*sent) != 0 {
		t.Errorf("stepping down isn't emailed: %+v", *sent)
	}

	// A leader adds the Strava link; a bad link is refused.
	if loc := manage("/strava", "Jane Doe", "8356", url.Values{"strava_url": {"https://example.com/x"}}); !strings.Contains(loc, "err=link") {
		t.Errorf("bad link: %q", loc)
	}
	manage("/strava", "Jane Doe", "8356", url.Values{"strava_url": {"https://www.strava.com/clubs/10197/group_events/9"}})
	if o, _ := a.store.Ride(key); o.StravaURL != "https://www.strava.com/clubs/10197/group_events/9" {
		t.Errorf("strava link: %q", o.StravaURL)
	}

	// A leader cancels it, and can put it back.
	*sent = nil
	manage("/cancel", "Jane Doe", "8356", url.Values{"cancel": {"1"}})
	if o, _ := a.store.Ride(key); !o.Cancelled || len(*sent) != 1 || !strings.Contains((*sent)[0].subject, "is cancelled") {
		t.Errorf("cancel: %v %+v", o.Cancelled, *sent)
	}
	manage("/cancel", "Jane Doe", "8356", url.Values{"cancel": {"0"}})
	if o, _ := a.store.Ride(key); o.Cancelled {
		t.Error("putting it back")
	}

	// With no PIN set, leaders ask an admin.
	a.store.SetClubPIN("")
	if page := do(t, h, nil, "GET", "/calendar/ride/"+key, "", nil).Body.String(); !strings.Contains(page, "ask a club admin") || strings.Contains(page, `name="pin"`) {
		t.Error("with no PIN, the ride page should send leaders to an admin")
	}

	// Past rides can't be taken.
	past := seriesKey(1, func() string {
		d := a.today().AddDate(0, 0, -1)
		for d.Weekday() != time.Tuesday {
			d = d.AddDate(0, 0, -1)
		}
		return d.Format("2006-01-02")
	}())
	ct, body := form(url.Values{"leader_name": {"Jane Doe"}, "ft": {oldToken(a)}})
	if loc := do(t, h, nil, "POST", "/calendar/ride/"+past+"/lead", ct, body).Header().Get("Location"); !strings.Contains(loc, "err=past") {
		t.Errorf("past ride: %q", loc)
	}

	// Signing up needs a name, and a form from this site.
	if loc := lead("", groupB); !strings.Contains(loc, "err=name") {
		t.Errorf("lead without a name: %q", loc)
	}
	r := httptest.NewRequest("POST", "/calendar/ride/"+key+"/lead", nil)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-site post: %d", w.Code)
	}
	// Strava sign-in is gone.
	if w := do(t, h, nil, "GET", "/strava/login", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("/strava/login: %d", w.Code)
	}
}

func TestAddRideAndCalendar(t *testing.T) {
	a, h, sent := rideApp(t)
	a.store.SetClubPIN("8356")
	date := nextWeekday(a, time.Saturday)
	ct, body := form(url.Values{"date": {date}, "time": {"08:30"}, "name": {"Coffee Ride"}, "place": {"Leonardtown Square"}, "lead": {"on"},
		"leader_name": {"Jane D."}, "pin": {"8356"}, "ft": {oldToken(a)}})
	w := do(t, h, nil, "POST", "/calendar/new", ct, body)
	if !strings.Contains(w.Header().Get("Location"), "ok=added") {
		t.Fatalf("add ride: %d %s", w.Code, w.Body.String())
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0].subject, "New ride: Coffee Ride") {
		t.Errorf("notice: %+v", *sent)
	}
	page := do(t, h, nil, "GET", "/calendar?m="+date[:7], "", nil).Body.String()
	for _, want := range []string{"Coffee Ride", "8:30 AM", "Jane D.", "Tuesday Night Ride", "Needs a leader"} {
		if !strings.Contains(page, want) {
			t.Errorf("calendar is missing %q", want)
		}
	}
	// Only a name and date are needed.
	ct, body = form(url.Values{"date": {date}, "name": {""}, "leader_name": {"Jane D."}, "ft": {oldToken(a)}})
	if w := do(t, h, nil, "POST", "/calendar/new", ct, body); w.Code != http.StatusBadRequest {
		t.Errorf("ride without a name: %d", w.Code)
	}
	// The Thursday ride only runs in season.
	if s := (RideSeries{Weekday: 4, SeasonStart: "03-01", SeasonEnd: "10-10"}); s.runsOn(time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC)) || !s.runsOn(time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)) {
		t.Error("season check is wrong")
	}
}

func TestRidesAdmin(t *testing.T) {
	a, h, sent := rideApp(t)
	cookie := httptest.NewRecorder()
	a.setSession(cookie, "Board Member", 3600)
	admin := cookie.Result().Cookies()[0]
	key := seriesKey(1, nextWeekday(a, time.Tuesday))
	if w := do(t, h, admin, "GET", "/admin/rides/"+key, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `name="leader:B group"`) {
		t.Fatalf("ride page: %d", w.Code)
	}
	ride := func(vals url.Values) *httptest.ResponseRecorder {
		v := url.Values{"name": {"Tuesday Night Ride"}, "time": {"18:00"}, "place": {"NAS Pax"}, "pace": {"2"}}
		for k, x := range vals {
			v[k] = x
		}
		ct, body := form(v)
		return do(t, h, admin, "POST", "/admin/rides/"+key, ct, body)
	}
	ride(url.Values{"leader:A group": {"Chris T."}, "leader:B group": {""}})
	if o, _ := a.store.Ride(key); len(o.Leaders) != 1 || o.Leaders[0].Name != "Chris T." || o.Leaders[0].SetBy != "Board Member" || o.Leaders[0].Group != "A group" {
		t.Errorf("admin-assigned leader: %+v", o.Leaders)
	}
	if len(*sent) != 0 {
		t.Errorf("a new leader shouldn't email the club: %+v", *sent)
	}
	// An admin can change a filled spot, add a C group, and move the start time.
	*sent = nil
	ride(url.Values{"pace": {"3"}, "time": {"18:30"}, "leader:A group": {"Dana K."}, "leader:C group": {"Eli P."}})
	o, _ := a.store.Ride(key)
	if o.Time != "18:30" || len(o.Groups) != 3 || o.leaderFor("A group").Name != "Dana K." || o.leaderFor("C group").Name != "Eli P." {
		t.Errorf("edited ride: %+v", o.CalRide)
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0].subject, "has changed") {
		t.Errorf("notices after editing: %+v", *sent)
	}
	// A ride needs a name; nothing is saved without one.
	if w := ride(url.Values{"name": {""}}); w.Code != http.StatusBadRequest {
		t.Errorf("ride without a name: %d", w.Code)
	}
	// Cancelling emails the club.
	*sent = nil
	ct, body := form(url.Values{"cancel": {"1"}})
	do(t, h, admin, "POST", "/admin/rides/"+key+"/cancel", ct, body)
	if o, _ := a.store.Ride(key); !o.Cancelled || len(*sent) != 1 {
		t.Errorf("cancel: %v %+v", o.Cancelled, *sent)
	}
	ct, body = form(url.Values{"cancel": {"0"}})
	do(t, h, admin, "POST", "/admin/rides/"+key+"/cancel", ct, body)

	// Admins add one-off rides, and can delete them.
	sat := nextWeekday(a, time.Saturday)
	ct, body = form(url.Values{"name": {"Fall Colors Ride"}, "date": {sat}, "time": {"09:00"}, "pace": {""}, "leader:": {"Fran G."}})
	w := do(t, h, admin, "POST", "/admin/rides/new", ct, body)
	newKey := strings.TrimPrefix(strings.Split(w.Header().Get("Location"), "?")[0], "/admin/rides/")
	if o, ok := a.store.Ride(newKey); !ok || o.Name != "Fall Colors Ride" || o.LeaderSummary() != "Fran G." {
		t.Fatalf("added ride %q: %+v", newKey, o)
	}
	if w := do(t, h, admin, "GET", "/admin/calendar?m="+sat[:7], "", nil); !strings.Contains(w.Body.String(), "/admin/rides/"+newKey) {
		t.Error("the admin calendar doesn't link to the new ride")
	}
	do(t, h, admin, "POST", "/admin/rides/"+newKey+"/delete", "", nil)
	if _, ok := a.store.Ride(newKey); ok {
		t.Error("deleted ride is still there")
	}

	ct, body = form(url.Values{"name": {"Saturday Coffee Ride"}, "weekday": {"6"}, "time": {"08:00"}, "pace": {"3"}})
	do(t, h, admin, "POST", "/admin/weekly", ct, body)
	if s := a.store.AllSeries(); len(s) != 3 || len(s[2].Groups) != 3 {
		t.Errorf("weekly rides: %+v", s)
	}
	if w := do(t, h, admin, "GET", "/admin/rides", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Dana K.") {
		t.Errorf("rides tab: %d", w.Code)
	}
	// Editing a weekly ride updates its dates, but not what was changed on one date.
	satKey := seriesKey(3, sat)
	ct, body = form(url.Values{"name": {"Saturday Coffee Ride"}, "time": {"08:00"}, "place": {"Gate 2 lot"}, "pace": {"3"}, "leader:A group": {"Gus R."}})
	do(t, h, admin, "POST", "/admin/rides/"+satKey, ct, body)
	ct, body = form(url.Values{"name": {"Saturday Social"}, "weekday": {"6"}, "time": {"08:30"}, "pace": {"2"}, "level": {"elite"}})
	if w := do(t, h, admin, "POST", "/admin/weekly/3", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=series-saved") {
		t.Fatalf("save weekly ride: %s", w.Header().Get("Location"))
	}
	if w := do(t, h, admin, "GET", "/admin/weekly/3", "", nil); !strings.Contains(w.Body.String(), `value="elite" checked`) {
		t.Error("the weekly ride's page should show its level")
	}
	// The date follows the new name, time, level, and groups (Gus keeps the A group), but keeps its own start.
	if o, _ := a.store.Ride(satKey); o.Name != "Saturday Social" || o.Time != "08:30" || len(o.Groups) != 2 || o.Place != "Gate 2 lot" || o.leaderFor("A group") == nil ||
		len(o.Levels()) != 1 || o.Levels()[0].Key != "elite" {
		t.Errorf("weekly ride date after the edit: %+v", o.CalRide)
	}
	// Removing a weekly ride keeps dates that have a leader.
	do(t, h, admin, "POST", "/admin/weekly/1/delete", "", nil)
	found := false
	for _, o := range a.store.Rides(a.today(), a.today().AddDate(0, 0, 14)) {
		if o.Name == "Tuesday Night Ride" {
			found = found || (len(o.Leaders) > 0 && o.Custom)
		}
	}
	if !found {
		t.Error("the led Tuesday ride disappeared with its weekly ride")
	}
}

// oldToken is a join-form token from a few seconds ago, like a real visitor's.
func oldToken(a *App) string {
	ts := strconv.FormatInt(time.Now().Add(-10*time.Second).Unix(), 10)
	return ts + "." + sign(a.formKey(), ts)[:24]
}

func TestLeadWithJustAName(t *testing.T) {
	a, h, sent := rideApp(t)
	key := seriesKey(2, nextWeekday(a, time.Thursday))
	if _, ok := a.store.Ride(key); !ok {
		key = seriesKey(1, nextWeekday(a, time.Tuesday))
	}
	o, _ := a.store.Ride(key)
	group := o.groupNames()[0]
	post := func(vals url.Values) string {
		ct, body := form(vals)
		return do(t, h, nil, "POST", "/calendar/ride/"+key+"/lead", ct, body).Header().Get("Location")
	}
	if loc := post(url.Values{"group": {group}, "leader_name": {"Pat Rider"}, "ft": {a.formToken()}}); !strings.Contains(loc, "err=expired") {
		t.Errorf("brand-new form accepted (bot check): %q", loc)
	}
	if loc := post(url.Values{"group": {group}, "ft": {oldToken(a)}}); !strings.Contains(loc, "err=name") {
		t.Errorf("no name: %q", loc)
	}
	if loc := post(url.Values{"group": {group}, "leader_name": {"Pat Rider"}, "ft": {oldToken(a)}}); !strings.Contains(loc, "ok=led") {
		t.Fatalf("lead by name: %q", loc)
	}
	if o, _ := a.store.Ride(key); o.leaderFor(group) == nil || o.leaderFor(group).Name != "Pat Rider" {
		t.Errorf("leader: %+v", o.Leaders)
	}
	if len(*sent) != 0 {
		t.Errorf("signing up to lead shouldn't email the club: %+v", *sent)
	}
	// Someone else can't replace them by typing a different name.
	if loc := post(url.Values{"group": {group}, "leader_name": {"Someone Else"}, "ft": {oldToken(a)}}); !strings.Contains(loc, "err=taken") {
		t.Errorf("name-only takeover: %q", loc)
	}
	// Adding a ride works without Strava too, but waits for an admin to approve it.
	sunday := nextWeekday(a, time.Sunday)
	ct, body := form(url.Values{"date": {sunday}, "name": {"Sunday Spin"}, "leader_name": {"Pat Rider"}, "lead": {"on"}, "ft": {oldToken(a)}})
	if w := do(t, h, nil, "POST", "/calendar/new", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=held") {
		t.Errorf("add ride by name: %d %s", w.Code, w.Header().Get("Location"))
	}
}

// A ride added without the club PIN stays off the calendar, the newsletter's
// data and the API until an admin approves it.
func TestRidesAddedByNameAreHeld(t *testing.T) {
	a, h, sent := rideApp(t)
	sunday := nextWeekday(a, time.Sunday)
	onCalendar := func(name string) bool {
		for _, o := range a.store.Rides(a.today(), a.today().AddDate(0, 0, 14)) {
			if o.Name == name {
				return true
			}
		}
		return false
	}
	ct, body := form(url.Values{"date": {sunday}, "name": {"Sunday Spin"}, "leader_name": {"Pat Rider"}, "ft": {oldToken(a)}})
	if w := do(t, h, nil, "POST", "/calendar/new", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=held") {
		t.Fatalf("add ride by name: %s", w.Header().Get("Location"))
	}
	if onCalendar("Sunday Spin") {
		t.Fatal("a held ride is on the calendar")
	}
	held := a.store.HeldRides()
	if len(held) != 1 || !held[0].Held {
		t.Fatalf("held rides: %+v", held)
	}
	key := held[0].Key
	if w := do(t, h, nil, "GET", "/calendar/ride/"+key, "", nil); w.Code != http.StatusNotFound {
		t.Errorf("the public can open a held ride: %d", w.Code)
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0].subject, "waiting for approval") || !strings.Contains((*sent)[0].body, "/admin/calendar") {
		t.Errorf("notice: %+v", *sent)
	}
	ct, body = form(url.Values{"group": {""}, "leader_name": {"Sam"}, "ft": {oldToken(a)}})
	do(t, h, nil, "POST", "/calendar/ride/"+key+"/lead", ct, body)
	if o, _ := a.store.Ride(key); len(o.Leaders) != 0 {
		t.Errorf("someone signed up to lead a held ride: %+v", o.Leaders)
	}

	// The admin sees it, approves it, and then it's public.
	rec := httptest.NewRecorder()
	a.setSession(rec, "Board Member", 3600)
	admin := rec.Result().Cookies()[0]
	if page := do(t, h, admin, "GET", "/admin/calendar", "", nil).Body.String(); !strings.Contains(page, "Waiting for approval") || !strings.Contains(page, "Sunday Spin") {
		t.Error("the Calendar tab doesn't list the held ride")
	}
	if w := do(t, h, admin, "GET", "/calendar/ride/"+key, "", nil); w.Code != http.StatusOK {
		t.Errorf("an admin can't open the held ride: %d", w.Code)
	}
	do(t, h, admin, "POST", "/admin/rides/"+key+"/approve", "", nil)
	if !onCalendar("Sunday Spin") || len(a.store.HeldRides()) != 0 {
		t.Error("approving didn't put the ride on the calendar")
	}
	if w := do(t, h, nil, "GET", "/calendar/ride/"+key, "", nil); w.Code != http.StatusOK {
		t.Errorf("the approved ride isn't public: %d", w.Code)
	}
}

var (
	fakeEventsMu sync.Mutex
	fakeEvents   []map[string]any
)

func TestStravaImportAndAPI(t *testing.T) {
	a, h, _ := rideApp(t)
	a.cfg.CalendarAPIKey = "secret-key"
	h = a.routes()
	tue, sat := nextWeekday(a, time.Tuesday), nextWeekday(a, time.Saturday)
	fakeEventsMu.Lock()
	fakeEvents = []map[string]any{
		{"id": 501, "title": "Tuesday Night Ride", "address": "NAS Pax", "upcoming_occurrences": []string{tue + "T18:00:00Z"}},
		{"id": 502, "title": "Gravel Saturday", "address": "St. Mary’s River State Park", "upcoming_occurrences": []string{sat + "T08:00:00Z"}},
	}
	fakeEventsMu.Unlock()

	// Connecting: the admin approves on Strava, which sends them back here.
	cookie := httptest.NewRecorder()
	a.setSession(cookie, "Board Member", 3600)
	admin := cookie.Result().Cookies()[0]
	w := do(t, h, admin, "POST", "/admin/strava/connect", "", nil)
	u, _ := url.Parse(w.Header().Get("Location"))
	w = do(t, h, admin, "GET", "/admin/strava/callback?code=ann&state="+url.QueryEscape(u.Query().Get("state")), "", nil)
	if !strings.Contains(w.Header().Get("Location"), "ok=connected") {
		t.Fatalf("connect: %s", w.Header().Get("Location"))
	}
	if l, ok := a.store.StravaLink(); !ok || l.Athlete != "Ann A." || l.LastError != "" {
		t.Errorf("link: %+v", l)
	}
	rides := a.store.Rides(a.today(), a.today().AddDate(0, 0, 14))
	var tuesdays, gravel int
	for _, o := range rides {
		if o.Date == tue && o.Name == "Tuesday Night Ride" {
			tuesdays++
			if !strings.HasSuffix(o.StravaURL, "/group_events/501") || o.SeriesID == 0 {
				t.Errorf("weekly ride not linked to its Strava event: %+v", o)
			}
		}
		if o.Name == "Gravel Saturday" {
			gravel++
			if o.Date != sat || o.Time != "08:00" || o.Place != "St. Mary’s River State Park" {
				t.Errorf("imported ride: %+v", o)
			}
		}
	}
	if tuesdays != 1 || gravel != 1 {
		t.Errorf("tuesdays=%d gravel=%d", tuesdays, gravel)
	}

	// An event deleted on Strava leaves the calendar on the next import.
	fakeEventsMu.Lock()
	fakeEvents = fakeEvents[:1]
	fakeEventsMu.Unlock()
	if res, err := a.syncStrava(context.Background()); err != nil || res.Removed != 1 {
		t.Errorf("second sync: %+v %v", res, err)
	}

	// The API: anyone can read; changes need the key.
	ride := `{"date":"` + sat + `","time":"09:00","name":"Coffee Ride","groups":["Fast","Social"]}`
	put := func(key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/rides/coffee-1", strings.NewReader(body))
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := put("", ride); w.Code != http.StatusUnauthorized {
		t.Errorf("no key: %d", w.Code)
	}
	if w := put("secret-key", ride); w.Code != http.StatusCreated {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	if w := put("secret-key", `{"date":"`+sat+`","time":"09:30","name":"Coffee Ride"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"09:30"`) {
		t.Errorf("update: %d %s", w.Code, w.Body.String())
	}
	if w := put("secret-key", `{"date":"soon","name":"x"}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad date: %d", w.Code)
	}
	list := do(t, h, nil, "GET", "/api/rides?from="+sat+"&to="+sat, "", nil)
	var got []apiRide
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].ID != "coffee-1" || len(got[0].Leaders) != 2 {
		t.Errorf("list: %v %s", err, list.Body.String())
	}
	r := httptest.NewRequest("DELETE", "/api/rides/coffee-1", nil)
	r.Header.Set("Authorization", "Bearer secret-key")
	dw := httptest.NewRecorder()
	h.ServeHTTP(dw, r)
	if dw.Code != http.StatusOK || len(a.store.Rides(a.today(), a.today().AddDate(0, 0, 14))) == 0 {
		t.Errorf("delete: %d", dw.Code)
	}
}

// The calendar page lists the next few rides above the month, whatever month
// is shown, and says plainly when there are none (a weekly ride can be out of
// season for months).
func TestCalendarUpcomingRides(t *testing.T) {
	a, h, _ := rideApp(t)
	page := do(t, h, nil, "GET", "/calendar?m=2031-01", "", nil).Body.String()
	if !strings.Contains(page, "Coming up") || !strings.Contains(page, `class="cal-up-list"`) || !strings.Contains(page, "Night Ride") {
		t.Error("the calendar should list upcoming rides, even when another month is shown")
	}
	up, note := a.upcomingRides()
	if len(up) != 4 || note != "" {
		t.Errorf("upcoming: %d rides, note %q", len(up), note)
	}

	for _, s := range a.store.AllSeries() {
		a.store.DeleteSeries(s.ID)
	}
	page = do(t, h, nil, "GET", "/calendar", "", nil).Body.String()
	if !strings.Contains(page, "No rides are scheduled yet.") || strings.Contains(page, `class="cal-up-list"`) {
		t.Error("with no rides, the page should say so")
	}
	// One far-off ride: the note says when it is.
	far := a.today().AddDate(0, 0, 200)
	a.store.AddRide(CalRide{Date: far.Format("2006-01-02"), Name: "Winter Ride"})
	if _, note := a.upcomingRides(); !strings.Contains(note, "No rides until "+far.Format("Monday, January 2, 2006")) {
		t.Errorf("note: %q", note)
	}
}

// A ride from earlier today has rolled out, so it leaves the upcoming list,
// while one later today stays.
func TestUpcomingSkipsRidesAlreadyGone(t *testing.T) {
	a, _, _ := rideApp(t)
	for _, s := range a.store.AllSeries() {
		a.store.DeleteSeries(s.ID)
	}
	today := a.today().Format("2006-01-02")
	a.store.AddRide(CalRide{Date: today, Time: "00:00", Name: "Dawn Patrol"})
	a.store.AddRide(CalRide{Date: today, Name: "Sometime Today"})
	up, _ := a.upcomingRides()
	if len(up) != 1 || up[0].Name != "Sometime Today" {
		t.Errorf("upcoming: %+v", up)
	}
}

// On phones, an empty month says what fits: gone, nothing left, or nothing
// yet, with a link on to the next month.
func TestCalendarEmptyMonthNote(t *testing.T) {
	a, _, _ := rideApp(t)
	for _, s := range a.store.AllSeries() {
		a.store.DeleteSeries(s.ID)
	}
	now := a.today()
	for m, want := range map[string]string{
		now.AddDate(0, -2, 0).Format("2006-01"): "No rides this month.",
		now.Format("2006-01"):                   "Nothing else on the calendar this month.",
		now.AddDate(0, 2, 0).Format("2006-01"):  "No rides on the calendar this month yet.",
	} {
		if got := a.month(m).NoDays; got != want {
			t.Errorf("%s: %q, want %q", m, got, want)
		}
	}
}

func TestSitemap(t *testing.T) {
	_, h, _ := rideApp(t)
	w := do(t, h, nil, "GET", "/sitemap.xml", "", nil)
	body := w.Body.String()
	for _, want := range []string{"<urlset", "/rides</loc>", "/calendar</loc>", "/join</loc>"} {
		if !strings.Contains(body, want) {
			t.Errorf("sitemap is missing %q", want)
		}
	}
	if strings.Contains(body, "/admin") {
		t.Error("the sitemap lists an admin page")
	}
	robots := do(t, h, nil, "GET", "/robots.txt", "", nil).Body.String()
	if !strings.Contains(robots, "Sitemap: ") {
		t.Error("robots.txt should point to the sitemap")
	}
}

func TestStravaDetails(t *testing.T) {
	a, h, _ := rideApp(t)
	sun := nextWeekday(a, time.Sunday)
	fakeEventsMu.Lock()
	fakeEvents = []map[string]any{
		{"id": 601, "title": "St Mary's City Cafe Ride", "address": "St Mary's City", "upcoming_occurrences": []string{sun + "T14:00:00Z"},
			"description": "Coffee at the end.", "skill_levels": 2, "start_latlng": []float64{38.19, -76.43}, "private": false,
			"route_id": json.Number("3550943418851772710"), "route": map[string]any{"name": "St Mary's Cafe Loop"},
			"organizing_athlete": map[string]any{"id": 18, "firstname": "Gabriel", "lastname": "Murray"}},
		{"id": 602, "title": "Wildewood Ride", "address": "21725 Tulagi Pl, Lexington Park", "upcoming_occurrences": []string{sun + "T13:00:00Z"},
			"description": "Meet in the driveway.", "private": true, "start_latlng": []float64{38.3, -76.5}, "route_id": 77,
			"organizing_athlete": map[string]any{"id": 19, "firstname": "Pat", "lastname": "O"}},
		{"id": 603, "title": "SuMR", "route_id": json.Number("3550943418851772710")}, // a past event: only counts toward the route
	}
	fakeEventsMu.Unlock()

	cookie := httptest.NewRecorder()
	a.setSession(cookie, "Board Member", 3600)
	admin := cookie.Result().Cookies()[0]
	w := do(t, h, admin, "POST", "/admin/strava/connect", "", nil)
	u, _ := url.Parse(w.Header().Get("Location"))
	if w = do(t, h, admin, "GET", "/admin/strava/callback?code=ann&state="+url.QueryEscape(u.Query().Get("state")), "", nil); !strings.Contains(w.Header().Get("Location"), "ok=connected") {
		t.Fatalf("connect: %s", w.Header().Get("Location"))
	}

	var cafe, home RideOcc
	for _, o := range a.store.Rides(a.today(), a.today().AddDate(0, 0, 8)) {
		switch o.Name {
		case "St Mary's City Cafe Ride":
			cafe = o
		case "Wildewood Ride":
			home = o
		}
	}
	if cafe.Strava == nil || cafe.Strava.Going != 3 || cafe.Strava.PaceLabel() != "Tempo" || cafe.Strava.Organizer != "Gabriel M." {
		t.Fatalf("cafe ride's Strava details: %+v", cafe.Strava)
	}
	if !cafe.Route.Shown() || cafe.Route.Stats() != "21 mi · 400 ft of climbing" || cafe.Route.TimeLabel() != "1 hr 5 min" || cafe.Route.Rides != 2 {
		t.Errorf("cafe route: %+v", cafe.Route)
	}
	if cafe.LeaderSummary() != "Gabriel M." {
		t.Errorf("the organizer should lead: %q", cafe.LeaderSummary())
	}
	if cafe.Meta() != "21 mi · 3 going" {
		t.Errorf("meta: %q", cafe.Meta())
	}
	if home.Route.Shown() || home.PublicPlace() != "" || home.DirectionsFor(false) != "" || home.NotesFor(false) != "" {
		t.Errorf("a private event at a house shouldn't be public: %+v", home)
	}

	// The public ride page shows the route but not a private event's notes or address.
	page := do(t, h, nil, "GET", "/calendar/ride/"+cafe.Key, "", nil).Body.String()
	for _, want := range []string{"Coffee at the end.", "St Mary&#39;s Cafe Loop", "21 mi · 400 ft of climbing", `class="lvl lvl-intermediate">Intermediate</span> 18–20 mph`, "3 going", "posted by Gabriel M.", "google.com/maps/dir", "maps.example/a@2x"} {
		if !strings.Contains(page, want) {
			t.Errorf("cafe ride page is missing %q", want)
		}
	}
	page = do(t, h, nil, "GET", "/calendar/ride/"+home.Key, "", nil).Body.String()
	for _, hidden := range []string{"Tulagi", "driveway", "Secret Loop", "google.com/maps"} {
		if strings.Contains(page, hidden) {
			t.Errorf("public page for a private ride shows %q", hidden)
		}
	}
	if !strings.Contains(page, "ride notes are for club members") {
		t.Error("the public page should say the notes are for club members")
	}
	if page = do(t, h, admin, "GET", "/calendar/ride/"+home.Key, "", nil).Body.String(); !strings.Contains(page, "Tulagi") || !strings.Contains(page, "driveway") {
		t.Error("an admin should see a private ride's address and notes")
	}

	// The Routes page lists the club's public routes, with the next ride on each.
	page = do(t, h, nil, "GET", "/routes", "", nil).Body.String()
	if !strings.Contains(page, "St Mary&#39;s Cafe Loop") || !strings.Contains(page, "Ridden 2 times") || !strings.Contains(page, "148 riders") ||
		!strings.Contains(page, "/calendar/ride/"+cafe.Key) || strings.Contains(page, "Secret Loop") {
		t.Errorf("routes page:\n%s", page)
	}
	// The calendar's upcoming rides show the route's map, but not a private one.
	page = do(t, h, nil, "GET", "/calendar", "", nil).Body.String()
	if !strings.Contains(page, `class="cal-up-map" src="https://maps.example/a@2x"`) || strings.Count(page, `class="cal-up-map"`) != 1 {
		t.Errorf("calendar route maps:\n%s", page)
	}
	// A route ridden only once stays off it.
	a.store.saveRoute(Route{ID: "99", Name: "One-Off Loop", Distance: 1000, Checked: time.Now()})
	a.store.SetRouteHistory(map[string]routeHistory{"3550943418851772710": {Rides: 2}, "99": {Rides: 1}})
	if page = do(t, h, nil, "GET", "/routes", "", nil).Body.String(); strings.Contains(page, "One-Off Loop") || !strings.Contains(page, "St Mary&#39;s Cafe Loop") {
		t.Errorf("a one-ride route is on the routes page:\n%s", page)
	}
	// The same ride posted as another route shares its card, and its rides count.
	a.store.saveRoute(Route{ID: "98", Name: "St Mary's Cafe Ride v2", Distance: 34000, Elevation: 120, Checked: time.Now()})
	a.store.SetRouteHistory(map[string]routeHistory{"3550943418851772710": {Rides: 2}, "98": {Rides: 1}})
	page = do(t, h, nil, "GET", "/routes", "", nil).Body.String()
	for _, want := range []string{"Ridden 3 times", "Also posted as 1 other route", "St Mary&#39;s Cafe Ride v2</a> · 21 mi, ridden once", "strava.com/routes/98"} {
		if !strings.Contains(page, want) {
			t.Errorf("routes page is missing %q:\n%s", want, page)
		}
	}
	// So does the API, without the house.
	if body := do(t, h, nil, "GET", "/api/rides?from="+sun+"&to="+sun, "", nil).Body.String(); strings.Contains(body, "Tulagi") {
		t.Errorf("the API shows a private start address: %s", body)
	}
}

func TestRideHistory(t *testing.T) {
	a, h, _ := rideApp(t)
	recent := a.today().AddDate(0, 0, -10).Format("2006-01-02")
	old := a.today().AddDate(-3, 0, 0).Format("2006-01-02")
	fakeEventsMu.Lock()
	fakeEvents = []map[string]any{
		{"id": 701, "title": "ThNR", "upcoming_occurrences": []string{recent + "T21:15:00Z"}, "route_id": 88,
			"organizing_athlete": map[string]any{"id": 5, "firstname": "Patrick", "lastname": "O"}},
		{"id": 702, "title": "SuMR", "upcoming_occurrences": []string{old + "T12:00:00Z"},
			"organizing_athlete": map[string]any{"id": 6, "firstname": "Michael", "lastname": "D"}},
	}
	fakeEventsMu.Unlock()
	cookie := httptest.NewRecorder()
	a.setSession(cookie, "Board Member", 3600)
	admin := cookie.Result().Cookies()[0]
	w := do(t, h, admin, "POST", "/admin/strava/connect", "", nil)
	u, _ := url.Parse(w.Header().Get("Location"))
	do(t, h, admin, "GET", "/admin/strava/callback?code=ann&state="+url.QueryEscape(u.Query().Get("state")), "", nil)

	byID := map[string]PastRide{}
	for _, p := range a.store.PastRides() {
		byID[p.ID] = p
	}
	thnr, sumr := byID["701"], byID["702"]
	if thnr.Date != recent || thnr.Organizer != "Patrick O." || thnr.RouteID != "88" || !thnr.final() || len(thnr.Going) != 2 || thnr.Going[0].Name != "Laura S." {
		t.Errorf("recent ride: %+v", thnr)
	}
	if sumr.Date != old || sumr.Checked != nil {
		t.Errorf("a ride older than Strava keeps sign-ups for shouldn't be asked about: %+v", sumr)
	}
	// Saved sign-ups stay when Strava's list changes or empties.
	a.store.SaveHistory([]PastRide{{ID: "701", Date: recent, Title: "ThNR (renamed)", Organizer: "Patrick O."}})
	if p := a.store.PastRides()[0]; p.Title != "ThNR (renamed)" || len(p.Going) != 2 {
		t.Errorf("after the next read: %+v", p)
	}

	page := do(t, h, admin, "GET", "/admin/history?y="+recent[:4], "", nil).Body.String()
	for _, want := range []string{"ThNR (renamed)", "Laura S., Tim H.", "Patrick O.: 1", "Laura S.: 1"} {
		if !strings.Contains(page, want) {
			t.Errorf("history page is missing %q", want)
		}
	}
	if page = do(t, h, admin, "GET", "/admin/history?y=all", "", nil).Body.String(); !strings.Contains(page, "Michael D.") {
		t.Error("all years should include the old ride")
	}
	if w := do(t, h, nil, "GET", "/admin/history", "", nil); w.Code == http.StatusOK {
		t.Error("the history should be for admins only")
	}
}

// This year's past club events from Strava go on the calendar, merged into a
// weekly ride's date when they match it, and a weekly ride's past date with
// no event drops off, so past months show the rides that happened.
func TestPastRidesOnCalendar(t *testing.T) {
	a, h, _ := rideApp(t)
	today := a.today()
	if today.YearDay() < 30 {
		t.Skip("needs a few past weeks in this year")
	}
	back := func(days int, wd time.Weekday) time.Time {
		d := today.AddDate(0, 0, -days)
		for d.Weekday() != wd {
			d = d.AddDate(0, 0, -1)
		}
		return d
	}
	at := func(d time.Time, hour int) string {
		return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, a.cfg.Location).UTC().Format(time.RFC3339)
	}
	tnr, skipped, coffee := back(14, time.Tuesday), back(7, time.Tuesday), back(10, time.Saturday)
	lastYear := time.Date(today.Year()-1, 6, 2, 0, 0, 0, 0, time.UTC)
	fakeEventsMu.Lock()
	fakeEvents = []map[string]any{
		{"id": 801, "title": "TNR", "upcoming_occurrences": []string{at(tnr, 18)}, "organizing_athlete": map[string]any{"id": 5, "firstname": "Patrick", "lastname": "O"}},
		{"id": 802, "title": "Coffee Ride", "upcoming_occurrences": []string{at(coffee, 8)}, "address": "12 Tulagi Pl", "private": true,
			"organizing_athlete": map[string]any{"id": 6, "firstname": "Michael", "lastname": "D"}},
		{"id": 803, "title": "Old Ride", "upcoming_occurrences": []string{at(lastYear, 8)}},
	}
	fakeEventsMu.Unlock()
	cookie := httptest.NewRecorder()
	a.setSession(cookie, "Board Member", 3600)
	admin := cookie.Result().Cookies()[0]
	w := do(t, h, admin, "POST", "/admin/strava/connect", "", nil)
	u, _ := url.Parse(w.Header().Get("Location"))
	do(t, h, admin, "GET", "/admin/strava/callback?code=ann&state="+url.QueryEscape(u.Query().Get("state")), "", nil)

	day := func(d time.Time) []RideOcc { return a.store.Rides(d, d) }
	if got := day(tnr); len(got) != 1 || got[0].SeriesID != 1 || got[0].Name != "Tuesday Night Ride" || !strings.HasSuffix(got[0].StravaURL, "/group_events/801") ||
		got[0].External != "strava:801:"+tnr.Format("2006-01-02") {
		t.Errorf("the TNR event should be merged into the weekly ride's date: %+v", got)
	}
	got := day(coffee)
	if len(got) != 1 || got[0].Name != "Coffee Ride" || got[0].Place != "12 Tulagi Pl" || got[0].Strava == nil || !got[0].Strava.Private ||
		got[0].leaderFor("") == nil || got[0].leaderFor("").Name != "Michael D." {
		t.Fatalf("the coffee ride should be on the calendar: %+v", got)
	}
	if page := do(t, h, nil, "GET", "/calendar/ride/"+got[0].Key, "", nil).Body.String(); !strings.Contains(page, "Coffee Ride") || strings.Contains(page, "Tulagi") {
		t.Error("a past private ride's page shouldn't show the house")
	}
	if len(day(lastYear)) != 0 {
		t.Error("last year's events stay off the calendar")
	}

	month := func(d time.Time) string {
		return do(t, h, nil, "GET", "/calendar?m="+d.Format("2006-01"), "", nil).Body.String()
	}
	if page := month(tnr); !strings.Contains(page, "/calendar/ride/"+seriesKey(1, tnr.Format("2006-01-02"))) {
		t.Error("the month should show the TNR that happened")
	}
	if page := month(skipped); strings.Contains(page, "/calendar/ride/"+seriesKey(1, skipped.Format("2006-01-02"))) {
		t.Error("a past weekly date with no Strava event shouldn't show")
	}
	next := nextWeekday(a, time.Tuesday)
	nextDay, _ := time.Parse("2006-01-02", next)
	if page := month(nextDay); !strings.Contains(page, "/calendar/ride/"+seriesKey(1, next)) {
		t.Error("upcoming weekly rides still show")
	}

	// Each day is imported once, so a past ride an admin deletes stays gone.
	a.store.DeleteRide(got[0].Key)
	a.importPastRides()
	if len(day(coffee)) != 0 {
		t.Error("a deleted past ride came back")
	}
}

// testSeries are weekly rides for the tests. A new site starts with none:
// admins add the club's on the Rides tab.
func testSeries() []*RideSeries {
	return []*RideSeries{
		{ID: 1, Name: "Tuesday Night Ride", Weekday: 2, Time: "18:00", Place: "NAS Pax", Details: "Race pace, on base or off · about 23–25 mph",
			Groups: []string{"A group", "B group"}},
		{ID: 2, Name: "Thursday Night Ride", Weekday: 4, Time: "18:00", Place: "Park & Ride by NAS Pax Gate 2", Details: "Tempo to Point Lookout and back · about 22 mph, 40 miles",
			SeasonStart: "03-01", SeasonEnd: "10-10"},
	}
}

// A new site starts with no weekly rides: admins add the club's own.
func TestNewSiteHasNoWeeklyRides(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.AllSeries(); len(got) != 0 {
		t.Errorf("weekly rides on a new site: %+v", got)
	}
}

// With the club PIN, a ride added without signing in with Strava goes
// straight onto the calendar. A wrong PIN is refused, and with no PIN set
// the box isn't there.
func TestRideWithClubPIN(t *testing.T) {
	a, h, sent := rideApp(t)
	sunday := nextWeekday(a, time.Sunday)
	if page := do(t, h, nil, "GET", "/calendar/new", "", nil).Body.String(); strings.Contains(page, `name="pin"`) {
		t.Error("with no PIN set, Add a ride shouldn't ask for one")
	}
	a.store.SetClubPIN("Velo2026")
	if page := do(t, h, nil, "GET", "/calendar/new", "", nil).Body.String(); !strings.Contains(page, `name="pin"`) {
		t.Error("Add a ride should ask for the club PIN")
	}
	add := func(name, pin string) *httptest.ResponseRecorder {
		ct, body := form(url.Values{"date": {sunday}, "name": {name}, "leader_name": {"Pat Rider"}, "pin": {pin}, "ft": {oldToken(a)}})
		return do(t, h, nil, "POST", "/calendar/new", ct, body)
	}
	if w := add("Wrong Spin", "nope"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "isn’t right") {
		t.Errorf("a wrong PIN: %d", w.Code)
	}
	if len(a.store.HeldRides()) != 0 || len(*sent) != 0 {
		t.Error("a ride with a wrong PIN shouldn't be saved")
	}
	if w := add("Sunday Spin", "velo2026"); !strings.Contains(w.Header().Get("Location"), "ok=added") {
		t.Fatalf("the right PIN, any case: %d %s", w.Code, w.Header().Get("Location"))
	}
	on := a.store.Rides(a.today(), a.today().AddDate(0, 0, 14))
	if !slices.ContainsFunc(on, func(o RideOcc) bool { return o.Name == "Sunday Spin" && !o.Held }) || len(a.store.HeldRides()) != 0 {
		t.Errorf("the ride should be on the calendar: %+v", on)
	}
	if w := add("Later Spin", ""); !strings.Contains(w.Header().Get("Location"), "ok=held") {
		t.Errorf("no PIN typed: held as before: %s", w.Header().Get("Location"))
	}
}

// Strava stops listing an event as upcoming once it starts. That isn't a
// cancellation: today's rides stay as they are, and a weekly ride whose
// Strava event goes away keeps running, just without the link.
func TestStravaImportDoesNotCancelStartedRides(t *testing.T) {
	a, _, _ := rideApp(t)
	today := a.today().Format("2006-01-02")
	tue := nextWeekday(a, time.Tuesday)
	if tue == today {
		tue = a.today().AddDate(0, 0, 7).Format("2006-01-02")
	}
	to := a.today().Add(importWindow).Format("2006-01-02")
	lead := func(key string) {
		a.store.UpdateRide(key, func(c *CalRide) error { c.setLeader("", &Leader{Name: "Pat"}); return nil })
	}
	morning, _, _ := a.store.UpsertExternal(ExternalRide{External: "strava:1:" + today, Date: today, Time: "08:00", Name: "Cafe Ride"}, false)
	lead(morning.Key)
	weekly, _, _ := a.store.UpsertExternal(ExternalRide{External: "strava:2:" + tue, Date: tue, Time: "18:00", Name: "Tuesday Night Ride"}, true)
	if weekly.SeriesID == 0 {
		t.Fatalf("the Tuesday ride should link to the weekly ride: %+v", weekly)
	}
	lead(weekly.Key)
	if _, err := a.store.ImportStrava(nil, today, to); err != nil {
		t.Fatal(err)
	}
	if o, _ := a.store.Ride(morning.Key); o.Cancelled {
		t.Error("a ride that has started today shouldn't be cancelled")
	}
	if o, _ := a.store.Ride(weekly.Key); o.Cancelled || o.External != "" || len(o.Leaders) == 0 {
		t.Errorf("the weekly ride should keep running, unlinked, with its leader: %+v", o)
	}
}

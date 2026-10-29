package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	site, err := newSite(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, store: store, site: site, limit: newLimiter()}
	a.mailer = newMailer(cfg, store, site)
	var mu sync.Mutex
	var sent []notice
	a.mailer.onNotify = func(to, subject, body string) {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, notice{to, subject, body})
	}
	return a, a.routes(), &sent
}

// signIn goes through "Sign in with Strava" and returns the rider cookie.
func signIn(t *testing.T, h http.Handler, code, next string) (*http.Cookie, string) {
	t.Helper()
	w := do(t, h, nil, "GET", "/strava/login?next="+url.QueryEscape(next), "", nil)
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Query().Get("state") == "" {
		t.Fatalf("login redirect: %q", w.Header().Get("Location"))
	}
	w = do(t, h, nil, "GET", "/strava/callback?code="+code+"&state="+url.QueryEscape(u.Query().Get("state")), "", nil)
	for _, c := range w.Result().Cookies() {
		if c.Name == riderCookie && c.Value != "" {
			return c, w.Header().Get("Location")
		}
	}
	return nil, w.Header().Get("Location")
}

// nextWeekday is the next date (after today) falling on a weekday.
func nextWeekday(a *App, wd time.Weekday) string {
	d := a.today().AddDate(0, 0, 1)
	for d.Weekday() != wd {
		d = d.AddDate(0, 0, 1)
	}
	return d.Format("2006-01-02")
}

func TestRideLeaders(t *testing.T) {
	a, h, sent := rideApp(t)
	jane, next := signIn(t, h, "jane", "/calendar")
	bob, _ := signIn(t, h, "bob", "/calendar")
	ann, _ := signIn(t, h, "ann", "/calendar")
	if jane == nil || bob == nil || ann == nil || next != "/calendar" {
		t.Fatalf("sign in failed (next=%q)", next)
	}
	if rd, _ := a.rider(httptestReq(ann)); !rd.Admin || rd.Name != "Ann A." {
		t.Errorf("Ann should be a club admin: %+v", rd)
	}
	if names, _ := a.store.StravaAdmins(); len(names) != 1 || names[0] != "Ann A." {
		t.Errorf("club admins: %v", names)
	}

	// Not in the club on Strava: can't sign in.
	if c, loc := signIn(t, h, "outsider", "/calendar"); c != nil || !strings.Contains(loc, "err=notmember") {
		t.Errorf("outsider signed in: %v %q", c, loc)
	}

	// The weekly Tuesday ride has no leader, so anyone can take it.
	key := seriesKey(1, nextWeekday(a, time.Tuesday))
	groupA, groupB := "A group", "B group"
	gA, gB := url.Values{"group": {groupA}}, url.Values{"group": {groupB}}
	if w := do(t, h, nil, "GET", "/calendar/ride/"+key, "", nil); !strings.Contains(w.Body.String(), "Sign in with Strava") {
		t.Errorf("signed-out ride page: %d", w.Code)
	}
	post := func(c *http.Cookie, path string, vals url.Values) string {
		v := url.Values{"ft": {oldToken(a)}}
		for k, x := range vals {
			v[k] = x
		}
		ct, body := form(v)
		return do(t, h, c, "POST", path, ct, body).Header().Get("Location")
	}
	if loc := post(jane, "/calendar/ride/"+key+"/lead", gA); !strings.Contains(loc, "ok=led") {
		t.Fatalf("jane lead: %q", loc)
	}
	if o, _ := a.store.Ride(key); func() bool { l := o.leaderFor(groupA); return l == nil || l.Name != "Jane D." }() {
		t.Fatalf("leader: %+v", o.Leaders)
	}
	if len(*sent) != 1 || (*sent)[0].to != "PaxVeloClub@gmail.com" || !strings.Contains((*sent)[0].subject, "Jane D. will lead Tuesday Night Ride (A group") ||
		!strings.Contains((*sent)[0].body, "isn’t an admin of the PaxVelo club on Strava") {
		t.Errorf("notice: %+v", *sent)
	}

	// Each pace group has its own leader: Bob can lead B, but Jane can't lead both.
	if loc := post(jane, "/calendar/ride/"+key+"/lead", gB); !strings.Contains(loc, "err=already") {
		t.Errorf("jane led two groups: %q", loc)
	}
	if loc := post(bob, "/calendar/ride/"+key+"/lead", gB); !strings.Contains(loc, "ok=led") {
		t.Errorf("bob lead B: %q", loc)
	}
	if o, _ := a.store.Ride(key); o.Status() != "led" || o.LeaderSummary() != "Jane D., Bob S." {
		t.Errorf("both groups led: %q %q", o.Status(), o.LeaderSummary())
	}
	post(bob, "/calendar/ride/"+key+"/stepdown", gB)
	if o, _ := a.store.Ride(key); o.Status() != "partial" || o.LeaderSummary() != "Needs 1 more leader" {
		t.Errorf("one group led: %q %q", o.Status(), o.LeaderSummary())
	}

	// Once a group has a leader, other riders can't take it or remove her...
	if loc := post(bob, "/calendar/ride/"+key+"/lead", gA); !strings.Contains(loc, "err=taken") {
		t.Errorf("bob took a led ride: %q", loc)
	}
	if loc := post(bob, "/calendar/ride/"+key+"/stepdown", gA); !strings.Contains(loc, "err=notyours") {
		t.Errorf("bob removed the leader: %q", loc)
	}
	// ...but a club admin can.
	if loc := post(ann, "/calendar/ride/"+key+"/lead", gA); !strings.Contains(loc, "ok=led") {
		t.Errorf("admin take over: %q", loc)
	}
	if o, _ := a.store.Ride(key); func() bool { l := o.leaderFor(groupA); return l == nil || l.Name != "Ann A." || !l.StravaAdmin }() {
		t.Errorf("after take over: %+v", o.Leaders)
	}
	if last := (*sent)[len(*sent)-1]; !strings.Contains(last.subject, "took over") || strings.Contains(last.body, "isn’t an admin") {
		t.Errorf("take over notice: %+v", last)
	}
	// The leader can step down, and then anyone can take it again.
	post(ann, "/calendar/ride/"+key+"/stepdown", gA)
	if o, _ := a.store.Ride(key); o.leaderFor(groupA) != nil {
		t.Errorf("leader after stepping down: %+v", o.Leaders)
	}
	if loc := post(bob, "/calendar/ride/"+key+"/lead", gA); !strings.Contains(loc, "ok=led") {
		t.Errorf("bob lead after step down: %q", loc)
	}

	// Leaders can add the Strava event link; others can't.
	if loc := post(jane, "/calendar/ride/"+key+"/strava", url.Values{"strava_url": {"https://www.strava.com/clubs/10197/group_events/9"}}); !strings.Contains(loc, "err=notyours") {
		t.Errorf("jane set bob's link: %q", loc)
	}
	post(bob, "/calendar/ride/"+key+"/strava", url.Values{"strava_url": {"https://www.strava.com/clubs/10197/group_events/9"}})
	if o, _ := a.store.Ride(key); o.StravaURL != "https://www.strava.com/clubs/10197/group_events/9" {
		t.Errorf("strava link: %q", o.StravaURL)
	}

	// Past rides can't be taken.
	past := seriesKey(1, func() string {
		d := a.today().AddDate(0, 0, -1)
		for d.Weekday() != time.Tuesday {
			d = d.AddDate(0, 0, -1)
		}
		return d.Format("2006-01-02")
	}())
	if loc := post(jane, "/calendar/ride/"+past+"/lead", nil); !strings.Contains(loc, "err=past") {
		t.Errorf("past ride: %q", loc)
	}

	// Signing up without Strava needs a name, and a form from this site.
	if loc := post(nil, "/calendar/ride/"+key+"/lead", gA); !strings.Contains(loc, "err=name") {
		t.Errorf("signed out lead without a name: %q", loc)
	}
	r := httptest.NewRequest("POST", "/calendar/ride/"+key+"/lead", nil)
	r.Header.Set("Origin", "https://evil.example")
	r.AddCookie(jane)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-site post: %d", w.Code)
	}
}

func httptestReq(c *http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	return r
}

func TestAddRideAndCalendar(t *testing.T) {
	a, h, sent := rideApp(t)
	jane, _ := signIn(t, h, "jane", "/calendar/new")
	date := nextWeekday(a, time.Saturday)
	ct, body := form(url.Values{"date": {date}, "time": {"08:30"}, "name": {"Coffee Ride"}, "place": {"Leonardtown Square"}, "lead": {"on"}, "ft": {oldToken(a)}})
	w := do(t, h, jane, "POST", "/calendar/new", ct, body)
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
	ct, body = form(url.Values{"date": {date}, "name": {""}, "ft": {oldToken(a)}})
	if w := do(t, h, jane, "POST", "/calendar/new", ct, body); w.Code != http.StatusBadRequest {
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
	if len(*sent) != 1 || !strings.Contains((*sent)[0].body, "Board Member made Chris T. the leader") {
		t.Errorf("notice: %+v", *sent)
	}
	// An admin can change a filled spot, add a C group, and move the start time.
	*sent = nil
	ride(url.Values{"pace": {"3"}, "time": {"18:30"}, "leader:A group": {"Dana K."}, "leader:C group": {"Eli P."}})
	o, _ := a.store.Ride(key)
	if o.Time != "18:30" || len(o.Groups) != 3 || o.leaderFor("A group").Name != "Dana K." || o.leaderFor("C group").Name != "Eli P." {
		t.Errorf("edited ride: %+v", o.CalRide)
	}
	if len(*sent) != 3 || !strings.Contains((*sent)[0].body, "instead of Chris T.") || !strings.Contains((*sent)[2].subject, "has changed") {
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
	ct, body = form(url.Values{"name": {"Saturday Social"}, "weekday": {"6"}, "time": {"08:30"}, "pace": {"2"}})
	if w := do(t, h, admin, "POST", "/admin/weekly/3", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=series-saved") {
		t.Fatalf("save weekly ride: %s", w.Header().Get("Location"))
	}
	// The date follows the new name, time, and groups (Gus keeps the A group), but keeps its own start.
	if o, _ := a.store.Ride(satKey); o.Name != "Saturday Social" || o.Time != "08:30" || len(o.Groups) != 2 || o.Place != "Gate 2 lot" || o.leaderFor("A group") == nil {
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

func TestStravaStateCantBeForged(t *testing.T) {
	_, h, _ := rideApp(t)
	w := do(t, h, nil, "GET", "/strava/callback?code=jane&state=bogus.sig", "", nil)
	if !strings.Contains(w.Header().Get("Location"), "err=signin") || len(w.Result().Cookies()) > 0 {
		t.Errorf("forged state: %s", w.Header().Get("Location"))
	}
	if got := safeNext("//evil.example/x"); got != "/calendar" {
		t.Errorf("safeNext let an outside address through: %q", got)
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
	if len(*sent) != 1 || !strings.Contains((*sent)[0].body, "signed up with just their name") {
		t.Errorf("notice: %+v", *sent)
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

// A ride added by someone who isn't signed in with Strava stays off the
// calendar, the newsletter's data and the API until an admin approves it. A
// ride from a rider signed in with Strava (so in the club) goes straight on.
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

	// Signed in with Strava: straight onto the calendar.
	rec = httptest.NewRecorder()
	a.setRider(rec, &Rider{ID: 7, Name: "Ann A."})
	rider := rec.Result().Cookies()[0]
	ct, body = form(url.Values{"date": {sunday}, "name": {"Coffee Ride"}, "ft": {oldToken(a)}})
	if w := do(t, h, rider, "POST", "/calendar/new", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=added") || !onCalendar("Coffee Ride") {
		t.Errorf("a ride from a Strava rider should go straight on: %s", w.Header().Get("Location"))
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

// A Strava sign-in lasts a month, but its club-admin powers only a day, so
// someone removed as a club admin on Strava loses them the next day.
func TestStravaAdminPowersExpire(t *testing.T) {
	a, _, _ := rideApp(t)
	cookie := func(checked int64) *http.Request {
		b, _ := json.Marshal(Rider{ID: 7, Name: "Ann A.", Admin: true, Checked: checked})
		exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
		enc := base64.RawURLEncoding.EncodeToString(b)
		r := httptest.NewRequest("GET", "/calendar", nil)
		r.AddCookie(&http.Cookie{Name: riderCookie, Value: exp + "." + enc + "." + sign(a.riderKey(), exp+"."+enc)})
		return r
	}
	if rd, ok := a.rider(cookie(time.Now().Unix())); !ok || !rd.Admin {
		t.Errorf("a fresh sign-in should be an admin: %+v", rd)
	}
	if rd, ok := a.rider(cookie(time.Now().Add(-25 * time.Hour).Unix())); !ok || rd.Admin {
		t.Errorf("a day-old sign-in should stay signed in but lose admin powers: %+v", rd)
	}
	if rd, ok := a.rider(cookie(0)); !ok || rd.Admin {
		t.Errorf("a sign-in from before this check should lose admin powers: %+v", rd)
	}
}

// The calendar page lists the next few rides above the month, whatever month
// is shown, and says plainly when there are none (a weekly ride can be out of
// season for months).
func TestCalendarUpcomingRides(t *testing.T) {
	a, h, _ := rideApp(t)
	page := do(t, h, nil, "GET", "/calendar?m=2031-01", "", nil).Body.String()
	if !strings.Contains(page, "Upcoming rides") || !strings.Contains(page, `class="cal-up-list"`) || !strings.Contains(page, "Night Ride") {
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

// On phones, an empty month says what fits: gone, all done, or nothing yet.
func TestCalendarEmptyMonthNote(t *testing.T) {
	a, _, _ := rideApp(t)
	for _, s := range a.store.AllSeries() {
		a.store.DeleteSeries(s.ID)
	}
	now := a.today()
	for m, want := range map[string]string{
		now.AddDate(0, -2, 0).Format("2006-01"): "No rides this month.",
		now.Format("2006-01"):                   "No more rides this month.",
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
	if cafe.Meta() != "21 mi · Tempo · 3 going" {
		t.Errorf("meta: %q", cafe.Meta())
	}
	if home.Route.Shown() || home.PublicPlace() != "" || home.DirectionsFor(false) != "" || home.NotesFor(false) != "" {
		t.Errorf("a private event at a house shouldn't be public: %+v", home)
	}

	// The public ride page shows the route but not a private event's notes or address.
	page := do(t, h, nil, "GET", "/calendar/ride/"+cafe.Key, "", nil).Body.String()
	for _, want := range []string{"Coffee at the end.", "St Mary&#39;s Cafe Loop", "21 mi · 400 ft of climbing", "Tempo", "3 going", "posted by Gabriel M.", "google.com/maps/dir", "maps.example/a@2x"} {
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

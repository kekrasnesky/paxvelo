package main

import (
	"context"
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
		clubs := []map[string]any{{"id": 555, "admin": true}}
		if p.member {
			clubs = append(clubs, map[string]any{"id": 10197, "admin": p.clubAdmin, "owner": false})
		}
		json.NewEncoder(w).Encode(clubs)
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
	groupA, groupB := "A group · 24–28 mph", "B group · 20–22 mph"
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
	ct, body := form(url.Values{"leader0": {"Chris T."}, "leader1": {""}})
	do(t, h, admin, "POST", "/admin/rides/"+key+"/leader", ct, body)
	if o, _ := a.store.Ride(key); len(o.Leaders) != 1 || o.Leaders[0].Name != "Chris T." || o.Leaders[0].SetBy != "Board Member" || o.Leaders[0].Group != "A group · 24–28 mph" {
		t.Errorf("admin-assigned leader: %+v", o.Leaders)
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0].body, "Board Member made Chris T. the leader") {
		t.Errorf("notice: %+v", *sent)
	}
	ct, body = form(url.Values{"name": {"Saturday Coffee Ride"}, "weekday": {"6"}, "time": {"08:00"}})
	do(t, h, admin, "POST", "/admin/rides/series", ct, body)
	if len(a.store.AllSeries()) != 3 {
		t.Errorf("weekly rides: %+v", a.store.AllSeries())
	}
	if w := do(t, h, admin, "GET", "/admin/rides", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Chris T.") {
		t.Errorf("rides tab: %d", w.Code)
	}
	// Removing a weekly ride keeps dates that have a leader.
	do(t, h, admin, "POST", "/admin/rides/series/1/delete", "", nil)
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
	// Adding a ride works without Strava too.
	ct, body := form(url.Values{"date": {nextWeekday(a, time.Sunday)}, "name": {"Sunday Spin"}, "leader_name": {"Pat Rider"}, "lead": {"on"}, "ft": {oldToken(a)}})
	if w := do(t, h, nil, "POST", "/calendar/new", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=added") {
		t.Errorf("add ride by name: %d %s", w.Code, w.Header().Get("Location"))
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

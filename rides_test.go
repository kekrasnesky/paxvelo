package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
		p, ok := people[r.FormValue("code")]
		if !ok || r.FormValue("client_secret") != "secret" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + r.FormValue("code"),
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
	if w := do(t, h, nil, "GET", "/calendar/ride/"+key, "", nil); !strings.Contains(w.Body.String(), "Sign in with Strava") {
		t.Errorf("signed-out ride page: %d", w.Code)
	}
	post := func(c *http.Cookie, path string, vals url.Values) string {
		ct, body := form(vals)
		return do(t, h, c, "POST", path, ct, body).Header().Get("Location")
	}
	if loc := post(jane, "/calendar/ride/"+key+"/lead", nil); !strings.Contains(loc, "ok=led") {
		t.Fatalf("jane lead: %q", loc)
	}
	if o, _ := a.store.Ride(key); o.Leader == nil || o.Leader.Name != "Jane D." {
		t.Fatalf("leader: %+v", o.Leader)
	}
	if len(*sent) != 1 || (*sent)[0].to != "PaxVeloClub@gmail.com" || !strings.Contains((*sent)[0].subject, "Jane D. will lead Tuesday Night Ride") ||
		!strings.Contains((*sent)[0].body, "isn’t an admin of the PaxVelo club on Strava") {
		t.Errorf("notice: %+v", *sent)
	}

	// Once it has a leader, other riders can't take it or remove her...
	if loc := post(bob, "/calendar/ride/"+key+"/lead", nil); !strings.Contains(loc, "err=taken") {
		t.Errorf("bob took a led ride: %q", loc)
	}
	if loc := post(bob, "/calendar/ride/"+key+"/stepdown", nil); !strings.Contains(loc, "err=notyours") {
		t.Errorf("bob removed the leader: %q", loc)
	}
	// ...but a club admin can.
	if loc := post(ann, "/calendar/ride/"+key+"/lead", nil); !strings.Contains(loc, "ok=led") {
		t.Errorf("admin take over: %q", loc)
	}
	if o, _ := a.store.Ride(key); o.Leader == nil || o.Leader.Name != "Ann A." || !o.Leader.StravaAdmin {
		t.Errorf("after take over: %+v", o.Leader)
	}
	if last := (*sent)[len(*sent)-1]; !strings.Contains(last.subject, "took over") || strings.Contains(last.body, "isn’t an admin") {
		t.Errorf("take over notice: %+v", last)
	}
	// The leader can step down, and then anyone can take it again.
	post(ann, "/calendar/ride/"+key+"/stepdown", nil)
	if o, _ := a.store.Ride(key); o.Leader != nil {
		t.Errorf("leader after stepping down: %+v", o.Leader)
	}
	if loc := post(bob, "/calendar/ride/"+key+"/lead", nil); !strings.Contains(loc, "ok=led") {
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

	// Signing up needs a sign-in, and a form from this site.
	if loc := post(nil, "/calendar/ride/"+key+"/lead", nil); !strings.HasPrefix(loc, "/strava/login") {
		t.Errorf("signed out lead: %q", loc)
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
	ct, body := form(url.Values{"date": {date}, "time": {"08:30"}, "name": {"Coffee Ride"}, "place": {"Leonardtown Square"}, "lead": {"on"}})
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
	ct, body = form(url.Values{"date": {date}, "name": {""}})
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
	ct, body := form(url.Values{"leader": {"Chris T."}})
	do(t, h, admin, "POST", "/admin/rides/"+key+"/leader", ct, body)
	if o, _ := a.store.Ride(key); o.Leader == nil || o.Leader.Name != "Chris T." || o.Leader.SetBy != "Board Member" {
		t.Errorf("admin-assigned leader: %+v", o.Leader)
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
			found = found || (o.Leader != nil && o.Custom)
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

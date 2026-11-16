package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A leader, with the club PIN, marks their ride on or cancels it with a note.
// The ride page shows it in a banner, the next rides, calendar and feed show
// it, a cancellation emails the club with the note, and a ride cancelled for
// tomorrow stays in the next rides, marked.
func TestRideStatus(t *testing.T) {
	a, h, sent := rideApp(t)
	a.store.SetClubPIN("8356")
	day := func(n int) string { return a.today().AddDate(0, 0, n).Format("2006-01-02") }
	o, err := a.store.AddRide(CalRide{Date: day(1), Time: "08:00", Name: "Coffee Spin", Place: "Leonardtown Square",
		Leaders: []Leader{{Name: "Jane Doe"}}})
	if err != nil {
		t.Fatal(err)
	}
	key := o.Key
	status := func(name, status, note string) string {
		ct, body := form(url.Values{"leader_name": {name}, "pin": {"8356"}, "status": {status}, "note": {note}})
		return do(t, h, nil, "POST", "/calendar/ride/"+key+"/status", ct, body).Header().Get("Location")
	}
	page := func() string { return do(t, h, nil, "GET", "/calendar/ride/"+key, "", nil).Body.String() }

	if p := page(); !strings.Contains(p, `name="status" value="on"`) || !strings.Contains(p, `name="note"`) {
		t.Error("the leader's tools should have Ride is on and a note")
	}
	if loc := status("Someone Else", statusOn, ""); !strings.Contains(loc, "err=notyours") {
		t.Errorf("someone else: %q", loc)
	}

	// On, with a note.
	if loc := status("jane doe", statusOn, "  Meet at the\nback lot  "); !strings.Contains(loc, "ok=on") {
		t.Fatalf("on: %q", loc)
	}
	got, _ := a.store.Ride(key)
	if !got.IsOn() || got.StatusNote != "Meet at the back lot" || got.StatusBy != "jane doe" || got.StatusAt.IsZero() {
		t.Errorf("after on: %+v", got.CalRide)
	}
	for _, want := range []string{`class="ride-status on"`, "Ride is on", "Meet at the back lot", "jane doe"} {
		if !strings.Contains(page(), want) {
			t.Errorf("ride page is missing %q", want)
		}
	}
	if cal := do(t, h, nil, "GET", "/calendar", "", nil).Body.String(); !strings.Contains(cal, `<span class="cal-on">On</span>Meet at the back lot`) {
		t.Error("the calendar should show the ride is on")
	}
	if err := a.rebuildContent(); err != nil {
		t.Fatal(err)
	}
	if home := do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(home, `<span class="cal-on">On</span>`) {
		t.Error("the home page's next rides should show the ride is on")
	}
	if feed := do(t, h, nil, "GET", "/calendar/feed.ics", "", nil).Body.String(); !strings.Contains(strings.ReplaceAll(feed, "\r\n ", ""), "It's on: Meet at the back lot") {
		t.Error("the calendar feed should say it's on")
	}

	// Cancelled, with a note: the club is emailed with it.
	*sent = nil
	if loc := status("Jane Doe", statusCancel, "Rain"); !strings.Contains(loc, "ok=cancelled") {
		t.Fatalf("cancel: %q", loc)
	}
	got, _ = a.store.Ride(key)
	if !got.Cancelled || got.On || got.LeaderSummary() != "Cancelled · Rain" {
		t.Errorf("after cancel: %+v", got.CalRide)
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0].body, "Note: Rain") {
		t.Errorf("cancel email: %+v", *sent)
	}
	if p := page(); !strings.Contains(p, `class="ride-status off"`) || !strings.Contains(p, "Rain") || !strings.Contains(p, `value="open"`) {
		t.Error("the ride page should show it's cancelled, and offer to put it back")
	}
	// Cancelled for tomorrow: still in the next rides, marked.
	up, _ := a.upcomingRides()
	found := false
	for _, u := range up {
		found = found || (u.Key == key && u.Cancelled)
	}
	if !found {
		t.Error("a ride cancelled for tomorrow should stay in the next rides")
	}
	// Cancelling again doesn't email again.
	*sent = nil
	status("Jane Doe", statusCancel, "Rain and wind")
	if len(*sent) != 0 {
		t.Errorf("a second cancel emailed: %+v", *sent)
	}

	// Put back: neither on nor cancelled, note gone.
	status("Jane Doe", statusOpen, "ignored")
	if got, _ := a.store.Ride(key); got.Cancelled || got.On || got.StatusNote != "" || got.StatusBy != "" {
		t.Errorf("after putting back: %+v", got.CalRide)
	}

	// The older Cancel form still works.
	ct, body := form(url.Values{"leader_name": {"Jane Doe"}, "pin": {"8356"}, "cancel": {"1"}})
	do(t, h, nil, "POST", "/calendar/ride/"+key+"/cancel", ct, body)
	if got, _ := a.store.Ride(key); !got.Cancelled {
		t.Error("cancel=1 should still cancel")
	}

	// A ride cancelled further ahead isn't in the next rides.
	later, _ := a.store.AddRide(CalRide{Date: day(3), Time: "07:00", Name: "Early Bird", Cancelled: true})
	up, _ = a.upcomingRides()
	for _, u := range up {
		if u.Key == later.Key {
			t.Error("a ride cancelled days ahead shouldn't be in the next rides")
		}
	}

	// A past ride can't be changed.
	past, _ := a.store.AddRide(CalRide{Date: day(-1), Name: "Yesterday", Leaders: []Leader{{Name: "Jane Doe"}}})
	key = past.Key
	if loc := status("Jane Doe", statusCancel, ""); !strings.Contains(loc, "err=past") {
		t.Errorf("past ride: %q", loc)
	}
}

// An admin marks a ride on or cancels it, with a note, from its admin page.
func TestRideStatusAdmin(t *testing.T) {
	a, h, sent := rideApp(t)
	rec := httptest.NewRecorder()
	a.setSession(rec, "Board Member", 3600)
	admin := rec.Result().Cookies()[0]
	key := seriesKey(1, nextWeekday(a, time.Tuesday))
	post := func(v url.Values) {
		ct, body := form(v)
		do(t, h, admin, "POST", "/admin/rides/"+key+"/cancel", ct, body)
	}
	post(url.Values{"status": {statusOn}, "note": {"Lights on"}})
	if o, _ := a.store.Ride(key); !o.IsOn() || o.StatusNote != "Lights on" || o.StatusBy == "" {
		t.Errorf("admin on: %+v", o.CalRide)
	}
	if p := do(t, h, admin, "GET", "/admin/rides/"+key, "", nil).Body.String(); !strings.Contains(p, `class="tag tag-on"`) || !strings.Contains(p, `value="Lights on"`) {
		t.Error("the admin ride page should show it's on, with its note")
	}
	*sent = nil
	post(url.Values{"status": {statusCancel}, "note": {"Storms"}})
	if o, _ := a.store.Ride(key); !o.Cancelled || o.StatusNote != "Storms" || len(*sent) != 1 {
		t.Errorf("admin cancel: %+v %+v", o.CalRide, *sent)
	}
}

// The hourly Strava import used to put a ride cancelled on the website back
// on; Strava events are never marked cancelled, so it leaves it alone. The
// calendar API still says either way.
func TestStravaKeepsWebsiteCancel(t *testing.T) {
	a, _, _ := rideApp(t)
	date := a.today().AddDate(0, 0, 2).Format("2006-01-02")
	x := ExternalRide{External: "strava:7:" + date, Date: date, Time: "08:00", Name: "Cafe Ride"}
	o, _, err := a.store.UpsertExternal(x, false)
	if err != nil {
		t.Fatal(err)
	}
	a.store.UpdateRide(o.Key, func(c *CalRide) error { c.setStatus(statusCancel, "Rain", "Pat", time.Now()); return nil })
	if _, _, err := a.store.UpsertExternal(x, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.store.Ride(o.Key); !got.Cancelled || got.StatusNote != "Rain" {
		t.Errorf("the Strava import undid a cancellation: %+v", got.CalRide)
	}

	api := ExternalRide{External: "coffee-1", Date: date, Name: "API Ride", Cancelled: true}
	ao, _, _ := a.store.UpsertExternal(api, false)
	api.Cancelled = false
	a.store.UpsertExternal(api, false)
	if got, _ := a.store.Ride(ao.Key); got.Cancelled {
		t.Error("the calendar API should still put a ride back on")
	}
}

func TestCleanStatusNote(t *testing.T) {
	if got := cleanStatusNote("  a\n b\t c "); got != "a b c" {
		t.Errorf("clean = %q", got)
	}
	if got := cleanStatusNote(strings.Repeat("é", 200)); len([]rune(got)) != maxStatusNote {
		t.Errorf("long note kept %d characters", len([]rune(got)))
	}
}

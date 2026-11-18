package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Anyone says "I'm in" with a name; the page shows who's going (short
// names, never emails), knows it's them by a cookie, lets them say "Can't
// make it", and tells the club inbox about a first club ride. Cancelling
// the ride or putting it back on emails the riders who left an email.
func TestImIn(t *testing.T) {
	a, h, notices := rideApp(t)
	var mu sync.Mutex
	var mails []string
	a.mailer.onSend = func(to, subject, body string) {
		if to == a.cfg.RidesEmail {
			return // the club inbox's notices are in notices
		}
		mu.Lock()
		mails = append(mails, to+"|"+subject+"|"+body)
		mu.Unlock()
	}
	day := func(n int) string { return a.today().AddDate(0, 0, n).Format("2006-01-02") }
	o, err := a.store.AddRide(CalRide{Date: day(2), Time: "08:00", Name: "Coffee Spin", Groups: []string{"A group", "B group"}})
	if err != nil {
		t.Fatal(err)
	}
	key := o.Key
	in := func(c *http.Cookie, v url.Values) *httptest.ResponseRecorder {
		v.Set("ft", oldToken(a))
		ct, body := form(v)
		return do(t, h, c, "POST", "/calendar/ride/"+key+"/in", ct, body)
	}
	page := func(c *http.Cookie) string { return do(t, h, c, "GET", "/calendar/ride/"+key, "", nil).Body.String() }

	if p := page(nil); !strings.Contains(p, `action="/calendar/ride/`+key+`/in"`) || !strings.Contains(p, `name="group"`) {
		t.Fatal("the ride page should have the I'm in form, with the pace groups")
	}
	// Sam: first ride, with an email and a group.
	w := in(nil, url.Values{"rider_name": {"Sam Rider"}, "email": {"Sam@Example.com"}, "group": {"B group"}, "first": {"1"}})
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "ok=infirst") {
		t.Fatalf("I'm in: %q", loc)
	}
	var sam *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == riderCookie {
			sam = c
		}
	}
	if sam == nil || !sam.HttpOnly || sam.Path != "/calendar" {
		t.Fatalf("rider cookie: %+v", sam)
	}
	got, _ := a.store.Ride(key)
	if len(got.Riders) != 1 || got.Riders[0].Email != "sam@example.com" || got.Riders[0].Group != "B group" || !got.Riders[0].First {
		t.Fatalf("riders: %+v", got.Riders)
	}
	if d := got.Riders[0].Device; d == "" || d == sam.Value {
		t.Error("the data file should keep a hash of the cookie, not the cookie")
	}
	if len(*notices) != 1 || !strings.Contains((*notices)[0].subject, "First ride: Sam Rider") {
		t.Errorf("first-ride notice: %+v", *notices)
	}
	mine := page(sam)
	for _, want := range []string{"You’re in", "Can’t make it", `href="/ride-waiver"`, "Sam R."} {
		if !strings.Contains(mine, want) {
			t.Errorf("Sam's page is missing %q", want)
		}
	}
	other := page(nil)
	if !strings.Contains(other, "Sam R.") || strings.Contains(other, "You’re in") || strings.Contains(other, "sam@example.com") || strings.Contains(other, "Sam Rider") {
		t.Error("others see Sam's short name only")
	}

	// The same phone again updates its entry; no second notice.
	in(sam, url.Values{"rider_name": {"Samantha Rider"}})
	if got, _ := a.store.Ride(key); len(got.Riders) != 1 || got.Riders[0].Name != "Samantha Rider" || got.Riders[0].Email != "" {
		t.Errorf("update: %+v", got.Riders)
	}
	in(sam, url.Values{"rider_name": {"Sam Rider"}, "email": {"sam@example.com"}})
	if len(*notices) != 1 {
		t.Errorf("notices after updating: %d", len(*notices))
	}
	// The form remembers the name on this phone for the next ride.
	o2, _ := a.store.AddRide(CalRide{Date: day(4), Name: "Hills"})
	if p := do(t, h, sam, "GET", "/calendar/ride/"+o2.Key, "", nil).Body.String(); !strings.Contains(p, `value="Sam Rider"`) {
		t.Error("the form should fill in the name used last time")
	}

	// Ann, on another phone, no email.
	in(nil, url.Values{"rider_name": {"Ann Lee"}})
	got, _ = a.store.Ride(key)
	if len(got.Riders) != 2 || got.Meta() != "2 going" {
		t.Errorf("two riders: %d, meta %q", len(got.Riders), got.Meta())
	}

	// Bad input.
	for v, code := range map[string]string{"": "err=ridername", "bad-email": "err=email"} {
		vals := url.Values{"rider_name": {"Bo"}}
		if v == "" {
			vals.Set("rider_name", "")
		} else {
			vals.Set("email", "not an email")
		}
		if loc := in(nil, vals).Header().Get("Location"); !strings.Contains(loc, code) {
			t.Errorf("%q: %q", v, loc)
		}
	}

	// Cancelling emails Sam (who left an email), not Ann; putting it back too.
	a.applyStatus(key, statusCancel, "Rain", "Jane", nil)
	a.mailer.bg.Wait()
	mu.Lock()
	if len(mails) != 1 || !strings.HasPrefix(mails[0], "sam@example.com|Cancelled: Coffee Spin") || !strings.Contains(mails[0], "Rain") {
		t.Errorf("cancel emails: %q", mails)
	}
	mails = nil
	mu.Unlock()
	if p := page(nil); strings.Contains(p, `/in"`) {
		t.Error("a cancelled ride shouldn't take I'm in")
	}
	a.applyStatus(key, statusOpen, "", "Jane", nil)
	a.mailer.bg.Wait()
	mu.Lock()
	if len(mails) != 1 || !strings.Contains(mails[0], "|Back on: Coffee Spin") {
		t.Errorf("put-back emails: %q", mails)
	}
	mu.Unlock()

	// Can't make it.
	ct, body := form(url.Values{})
	if loc := do(t, h, sam, "POST", "/calendar/ride/"+key+"/out", ct, body).Header().Get("Location"); !strings.Contains(loc, "ok=out") {
		t.Errorf("out: %q", loc)
	}
	if got, _ := a.store.Ride(key); len(got.Riders) != 1 || got.Riders[0].Name != "Ann Lee" {
		t.Errorf("after Sam's out: %+v", got.Riders)
	}

	// An admin sees names and emails, and removes someone.
	rec := httptest.NewRecorder()
	a.setSession(rec, "Board Member", 3600)
	admin := rec.Result().Cookies()[0]
	if p := do(t, h, admin, "GET", "/admin/rides/"+key, "", nil).Body.String(); !strings.Contains(p, "Ann Lee") || !strings.Contains(p, `id="riders"`) {
		t.Error("the admin ride page should list who's going")
	}
	got, _ = a.store.Ride(key)
	do(t, h, admin, "POST", "/admin/rides/"+key+"/riders/"+got.Riders[0].ID+"/remove", ct, body)
	if got, _ := a.store.Ride(key); len(got.Riders) != 0 {
		t.Errorf("after the admin removed Ann: %+v", got.Riders)
	}

	// A past ride doesn't take riders.
	past, _ := a.store.AddRide(CalRide{Date: day(-1), Name: "Yesterday"})
	key = past.Key
	if loc := in(nil, url.Values{"rider_name": {"Late Larry"}}).Header().Get("Location"); !strings.Contains(loc, "err=noride") {
		t.Errorf("past ride: %q", loc)
	}
}

// Riders' emails and device codes go 30 days after the ride; names stay.
func TestTidyRiders(t *testing.T) {
	a, _, _ := rideApp(t)
	now := time.Now()
	oldR, _ := a.store.AddRide(CalRide{Date: now.AddDate(0, 0, -40).Format("2006-01-02"), Name: "Old",
		Riders: []RideRider{{ID: "a", Name: "Sam Rider", Email: "sam@example.com", Device: "d1"}}})
	newR, _ := a.store.AddRide(CalRide{Date: now.AddDate(0, 0, -5).Format("2006-01-02"), Name: "Recent",
		Riders: []RideRider{{ID: "b", Name: "Ann Lee", Email: "ann@example.com", Device: "d2"}}})
	a.store.mu.Lock()
	a.store.tidyRiders(now)
	a.store.mu.Unlock()
	if o, _ := a.store.Ride(oldR.Key); o.Riders[0].Email != "" || o.Riders[0].Device != "" || o.Riders[0].Name != "Sam Rider" {
		t.Errorf("old ride: %+v", o.Riders)
	}
	if o, _ := a.store.Ride(newR.Key); o.Riders[0].Email == "" {
		t.Error("a recent ride keeps its riders' emails")
	}
}

// A virtual ride asks for no waiver: no First club ride tick, no Waiver
// code button, and a "first" sent anyway is dropped (no club notice).
func TestVirtualRideNoWaiver(t *testing.T) {
	a, h, notices := rideApp(t)
	day := a.today().AddDate(0, 0, 2).Format("2006-01-02")
	o, err := a.store.AddRide(CalRide{Date: day, Time: "18:00", Name: "Zwift Group Ride"})
	if err != nil {
		t.Fatal(err)
	}
	p := do(t, h, nil, "GET", "/calendar/ride/"+o.Key, "", nil).Body.String()
	if !strings.Contains(p, `/in"`) {
		t.Fatal("a virtual ride still takes I'm in")
	}
	if strings.Contains(p, `name="first"`) || strings.Contains(p, "/qr/ride-waiver") {
		t.Error("a virtual ride shouldn't ask about the waiver")
	}
	v := url.Values{"rider_name": {"Sam Rider"}, "first": {"1"}, "ft": {oldToken(a)}}
	ct, body := form(v)
	w := do(t, h, nil, "POST", "/calendar/ride/"+o.Key+"/in", ct, body)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "ok=in#") {
		t.Errorf("I'm in: %q", loc)
	}
	if got, _ := a.store.Ride(o.Key); len(got.Riders) != 1 || got.Riders[0].First {
		t.Errorf("riders: %+v", got.Riders)
	}
	if len(*notices) != 0 {
		t.Errorf("no first-ride notice for a virtual ride: %+v", *notices)
	}
}

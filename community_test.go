package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Someone adding to the calendar says whether it's a PaxVelo event. A "No"
// shows it as a community ride: labelled, with no loaner bikes, waiver code
// or first-ride waiver.
func TestCommunityRide(t *testing.T) {
	a, h, _ := testApp(t)
	a.store.SetClubPIN("8356")
	date := a.today().AddDate(0, 0, 3).Format(time.DateOnly)
	post := func(name, official string) *http.Response {
		v := url.Values{"date": {date}, "time": {"09:00"}, "name": {name}, "pace": {"1"}, "leader_name": {"Pat Rider"},
			"pin": {"8356"}, "ft": {oldToken(a)}}
		if official != "" {
			v.Set("official", official)
		}
		ct, body := form(v)
		return do(t, h, nil, "POST", "/calendar/new", ct, body).Result()
	}
	if w := post("Unanswered", ""); w.StatusCode != http.StatusBadRequest {
		t.Errorf("no answer to the PaxVelo question: %d", w.StatusCode)
	}
	post("Club Ride", "yes")
	post("Trail Ride", "no")
	var club, mtb RideOcc
	for _, o := range a.store.Rides(a.today(), a.today().AddDate(0, 0, 7)) {
		switch o.Name {
		case "Club Ride":
			club = o
		case "Trail Ride":
			mtb = o
		}
	}
	if club.Key == "" || mtb.Key == "" || club.Unofficial || !mtb.Unofficial {
		t.Fatalf("club %+v\nmtb %+v", club, mtb)
	}
	if !a.loanerOpen(club) || a.loanerOpen(mtb) {
		t.Error("loaner bikes are for club rides only")
	}
	page := do(t, h, nil, "GET", "/calendar/ride/"+mtb.Key, "", nil).Body.String()
	for _, bad := range []string{"Borrow a club bike", "/qr/ride-waiver", "First club ride"} {
		if strings.Contains(page, bad) {
			t.Errorf("community ride page has %q", bad)
		}
	}
	if !strings.Contains(page, "Community ride") || !strings.Contains(page, "Not a PaxVelo event") {
		t.Error("community ride page should say it isn't a PaxVelo event")
	}
	if page := do(t, h, nil, "GET", "/calendar/ride/"+club.Key, "", nil).Body.String(); strings.Contains(page, "Community ride") || !strings.Contains(page, "Borrow a club bike") {
		t.Error("club ride page")
	}
	if cal := do(t, h, nil, "GET", "/calendar?m="+date[:7], "", nil).Body.String(); !strings.Contains(cal, "community-tag") {
		t.Error("the calendar labels the community ride")
	}
	ct, body := form(loanerForm(a, a.store.LoanerBikes()[3].ID, nil))
	if w := do(t, h, nil, "POST", loanerPath(mtb.Key), ct, body); w.Code != http.StatusBadRequest {
		t.Errorf("asking for a loaner on a community ride: %d", w.Code)
	}
}

// A bike marked Not available leaves the public pages; with none available,
// the ride waiver and home page stop offering loaner bikes.
func TestLoanerNotAvailable(t *testing.T) {
	a, h, admin := testApp(t)
	ride, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 4).Format(time.DateOnly), Name: "Shop Ride"})
	bikes := a.store.LoanerBikes()
	xs, xl := bikes[0], bikes[3]
	off := func(b LoanerBike) string {
		ct, body := form(url.Values{"id": {strconv.Itoa(b.ID)}, "size": {b.Size}, "fits": {b.Fits}, "bike": {b.Bike}, "ready": {""}})
		return do(t, h, admin, "POST", "/admin/loaners/bikes", ct, body).Header().Get("Location")
	}
	off(xs)
	page := do(t, h, nil, "GET", "/calendar/ride/"+ride.Key, "", nil).Body.String()
	if strings.Contains(page, "Trek Domane") || !strings.Contains(page, "Synapse") {
		t.Error("a bike that's not available leaves the ride page")
	}
	// A rider holding the XL: marking it off says so.
	if _, err := a.store.RequestLoaner(LoanerRequest{Bike: xl.ID, RideKey: ride.Key, RideDate: ride.Date, Name: "Sam", Email: "sam@example.com"}); err != nil {
		t.Fatal(err)
	}
	if loc := off(xl); !strings.Contains(loc, "err=bike-held-off") {
		t.Errorf("marking a reserved bike off: %s", loc)
	}
	if page := do(t, h, nil, "GET", "/calendar/ride/"+ride.Key, "", nil).Body.String(); strings.Contains(page, "Borrow a club bike") {
		t.Error("no bikes available: no loaner section")
	}
	waiver := do(t, h, nil, "GET", "/ride-waiver?bike=loaner", "", nil).Body.String()
	if strings.Contains(waiver, `value="loaner"`) || !strings.Contains(waiver, `name="bike" value="own"`) {
		t.Error("the ride waiver stops offering a loaner bike")
	}
	if a.store.AnyLoanerReady() {
		t.Error("none ready")
	}
}

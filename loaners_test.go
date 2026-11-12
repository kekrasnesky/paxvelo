package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func loanerForm(a *App, bike int, v url.Values) url.Values {
	f := rideWaiverForm(a, url.Values{"bike_id": {strconv.Itoa(bike)}})
	for k, vs := range v {
		f[k] = vs
	}
	return f
}

// The fleet starts with four sizes: the two bikes the club has, ready, and
// medium and large waiting for theirs.
func TestLoanerFleetSeeded(t *testing.T) {
	a, _, _ := testApp(t)
	bikes := a.store.LoanerBikes()
	if len(bikes) != 4 {
		t.Fatalf("bikes %+v", bikes)
	}
	var ready []string
	for _, b := range bikes {
		if b.Ready {
			ready = append(ready, b.Size)
		}
	}
	if strings.Join(ready, ",") != "XS,XL" || !strings.Contains(bikes[0].Bike, "Trek Domane") || !strings.Contains(bikes[3].Bike, "Synapse") {
		t.Errorf("ready bikes %v: %+v", ready, bikes)
	}
}

// A rider picks a ready bike on a ride's page and signs the loaner waiver
// with the request. The bike is held for that ride while it waits; the club
// is emailed; an admin confirms it and the rider is emailed.
func TestLoanerRequest(t *testing.T) {
	a, h, admin := testApp(t)
	var mu sync.Mutex
	var notices []string
	a.mailer.onNotify = func(to, subject, body string) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, subject+"\n"+body)
	}
	date := a.today().AddDate(0, 0, 3).Format(time.DateOnly)
	ride, _ := a.store.AddRide(CalRide{Date: date, Time: "08:00", Name: "Coffee Ride", Place: "Leonardtown Square"})
	other, _ := a.store.AddRide(CalRide{Date: date, Time: "17:00", Name: "Evening Spin"})
	xs := a.store.LoanerBikes()[0]

	page := do(t, h, nil, "GET", "/calendar/ride/"+ride.Key, "", nil).Body.String()
	if !strings.Contains(page, "Borrow a club bike") || !strings.Contains(page, loanerPath(ride.Key)+"?bike="+strconv.Itoa(xs.ID)) || strings.Contains(page, "5'4\"–5'8\"") {
		t.Fatalf("the ride page should offer the ready bikes only:\n%s", page)
	}
	reqPage := do(t, h, nil, "GET", loanerPath(ride.Key)+"?bike="+strconv.Itoa(xs.ID), "", nil).Body.String()
	for _, want := range []string{"Borrow a Club Bike", "Coffee Ride", `name="bike_id" value="` + strconv.Itoa(xs.ID) + `" required checked`, "PAXVELO LOANER BICYCLE AGREEMENT", "sig-canvas"} {
		if !strings.Contains(reqPage, want) {
			t.Errorf("the request form should have %q", want)
		}
	}

	ct, b := form(loanerForm(a, xs.ID, nil))
	w := do(t, h, nil, "POST", loanerPath(ride.Key), ct, b)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != loanerPath(ride.Key)+"?asked=1" {
		t.Fatalf("asking: %d %s %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	qs := a.store.LoanerRequests()
	if len(qs) != 1 || qs[0].Status != loanWaiting || qs[0].Email != "sam@example.com" || qs[0].Waiver == 0 || !strings.Contains(qs[0].BikeLabel, "Trek") {
		t.Fatalf("requests %+v", qs)
	}
	if ws := a.store.RideWaivers(a.cfg.ThisYear(), a.cfg.Location); len(ws) != 1 || !ws[0].Loaner() {
		t.Errorf("the loaner waiver is signed with the request: %+v", ws)
	}
	mu.Lock()
	if len(notices) != 1 || !strings.Contains(notices[0], "Loaner bike request: Coffee Ride") || !strings.Contains(notices[0], "/admin/loaners") {
		t.Errorf("the club is emailed: %v", notices)
	}
	mu.Unlock()
	if !strings.Contains(do(t, h, nil, "GET", "/calendar/ride/"+ride.Key, "", nil).Body.String(), "Reserved") {
		t.Error("a held bike shows as Reserved")
	}
	if !needsAttention(a, "loaner bike request") {
		t.Error("admin Home shows the waiting request")
	}

	// Held for this ride: nobody else gets it, but another ride can.
	ct, b = form(loanerForm(a, xs.ID, url.Values{"email": {"jo@example.com"}, "name": {"Jo Rider"}, "signature": {"Jo Rider"}}))
	if w := do(t, h, nil, "POST", loanerPath(ride.Key), ct, b); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "reserved for this ride") {
		t.Errorf("a held bike can't be asked for again: %d", w.Code)
	}
	ct, b = form(loanerForm(a, xs.ID, url.Values{"email": {"jo@example.com"}, "name": {"Jo Rider"}, "signature": {"Jo Rider"}}))
	if w := do(t, h, nil, "POST", loanerPath(other.Key), ct, b); w.Code != http.StatusSeeOther {
		t.Errorf("the same bike on another ride: %d", w.Code)
	}

	// The admin answers.
	list := do(t, h, admin, "GET", "/admin/loaners", "", nil).Body.String()
	if !strings.Contains(list, "Coffee Ride") || !strings.Contains(list, "/admin/loaners/"+strconv.Itoa(qs[0].ID)+"/confirm") {
		t.Fatal("the Loaner bikes page lists the waiting request")
	}
	ct, b = form(url.Values{})
	if w := do(t, h, admin, "POST", "/admin/loaners/"+strconv.Itoa(qs[0].ID)+"/confirm", ct, b); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "ok=loaner-confirmed") {
		t.Fatalf("confirm: %d %s", w.Code, w.Header().Get("Location"))
	}
	a.mailer.bg.Wait()
	var told bool
	for _, e := range a.mailer.log.All() {
		told = told || e.Kind == mailLoaner && e.To == "sam@example.com" && strings.Contains(e.Subject, "confirmed")
	}
	if !told {
		t.Error("the rider is emailed when it's confirmed")
	}
	ct, b = form(url.Values{})
	if w := do(t, h, admin, "POST", "/admin/loaners/"+strconv.Itoa(qs[0].ID)+"/decline", ct, b); !strings.Contains(w.Header().Get("Location"), "err=loaner-answered") {
		t.Error("a confirmed request can't be declined")
	}

	// Cancelling frees the bike for that ride.
	ct, b = form(url.Values{"reason": {"Flat we can't fix in time."}})
	do(t, h, admin, "POST", "/admin/loaners/"+strconv.Itoa(qs[0].ID)+"/cancel", ct, b)
	for _, c := range a.store.LoanerChoices(ride.Key) {
		if c.ID == xs.ID && c.Taken {
			t.Error("a cancelled request frees the bike")
		}
	}
	// A bike held for a ride coming up can't be removed.
	ct, b = form(url.Values{})
	if w := do(t, h, admin, "POST", "/admin/loaners/bikes/"+strconv.Itoa(xs.ID)+"/remove", ct, b); !strings.Contains(w.Header().Get("Location"), "err=bike-booked") {
		t.Errorf("removing a held bike: %s", w.Header().Get("Location"))
	}
}

// Requests close at 8 pm the day before, and only group rides on the road
// take them. Bikes that aren't ready can't be asked for.
func TestLoanerOpen(t *testing.T) {
	a, h, _ := testApp(t)
	tomorrow := a.today().AddDate(0, 0, 1).Format(time.DateOnly)
	later := a.today().AddDate(0, 0, 5).Format(time.DateOnly)
	for name, c := range map[string]struct {
		ride CalRide
		want bool
	}{
		"in five days":  {CalRide{Date: later, Name: "Shop Ride"}, true},
		"a meeting":     {CalRide{Date: later, Name: "Club Meeting", Kind: "meeting"}, false},
		"cancelled":     {CalRide{Date: later, Name: "Shop Ride", Cancelled: true}, false},
		"on Zwift":      {CalRide{Date: later, Name: "Zwift Social"}, false},
		"waiting":       {CalRide{Date: later, Name: "Shop Ride", Held: true}, false},
		"already today": {CalRide{Date: a.today().Format(time.DateOnly), Name: "Shop Ride"}, false},
	} {
		o, _ := a.store.AddRide(c.ride)
		if got := a.loanerOpen(o); got != c.want {
			t.Errorf("%s: open %v, want %v", name, got, c.want)
		}
	}
	o, _ := a.store.AddRide(CalRide{Date: tomorrow, Name: "Tomorrow Ride"})
	cutoff, _ := time.ParseInLocation(time.DateOnly, a.today().Format(time.DateOnly), a.cfg.Location)
	if want := time.Now().Before(cutoff.Add(loanerCutoffHour * time.Hour)); a.loanerOpen(o) != want {
		t.Errorf("tomorrow's ride is open until 8 pm today: %v", a.loanerOpen(o))
	}

	ride, _ := a.store.AddRide(CalRide{Date: later, Name: "Shop Ride"})
	medium := a.store.LoanerBikes()[1]
	ct, b := form(loanerForm(a, medium.ID, nil))
	if w := do(t, h, nil, "POST", loanerPath(ride.Key), ct, b); w.Code != http.StatusBadRequest || len(a.store.LoanerRequests()) != 0 {
		t.Errorf("a bike that isn't ready can't be asked for: %d", w.Code)
	}
}

// Admins add bikes, fill them in and mark them ready; ready bikes show on rides.
func TestLoanerBikesAdmin(t *testing.T) {
	a, h, admin := testApp(t)
	medium := a.store.LoanerBikes()[1]
	ct, b := form(url.Values{"id": {strconv.Itoa(medium.ID)}, "size": {"M"}, "fits": {"5'4\"–5'8\""}, "bike": {"2019 Giant Defy, 52 cm"}, "ready": {"yes"}})
	if w := do(t, h, admin, "POST", "/admin/loaners/bikes", ct, b); !strings.Contains(w.Header().Get("Location"), "ok=saved") {
		t.Fatalf("save: %s", w.Header().Get("Location"))
	}
	ct, b = form(url.Values{"size": {"XXL"}, "fits": {"6'4\"+"}})
	do(t, h, admin, "POST", "/admin/loaners/bikes", ct, b)
	bikes := a.store.LoanerBikes()
	if len(bikes) != 5 || !bikes[1].Ready || bikes[1].Bike != "2019 Giant Defy, 52 cm" || bikes[4].Size != "XXL" || bikes[4].Ready {
		t.Fatalf("bikes %+v", bikes)
	}
	ride, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 4).Format(time.DateOnly), Name: "Shop Ride"})
	if page := do(t, h, nil, "GET", "/calendar/ride/"+ride.Key, "", nil).Body.String(); !strings.Contains(page, "Giant Defy") || strings.Contains(page, "XXL") {
		t.Error("ready bikes show on the ride, others don't")
	}
	ct, b = form(url.Values{})
	do(t, h, admin, "POST", "/admin/loaners/bikes/"+strconv.Itoa(bikes[4].ID)+"/remove", ct, b)
	if len(a.store.LoanerBikes()) != 4 {
		t.Error("remove a bike")
	}
}

func needsAttention(a *App, text string) bool {
	for _, it := range a.attention() {
		if strings.Contains(it.Text, text) {
			return true
		}
	}
	return false
}

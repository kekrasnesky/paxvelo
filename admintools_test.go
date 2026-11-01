package main

import (
	"net/url"
	"strings"
	"testing"
)

// An email can go to the members who haven't signed this year's waiver, or
// to one riding level, and the Email page counts each group.
func TestEmailAudiences(t *testing.T) {
	a, h, c := testApp(t)
	year := a.cfg.ThisYear()
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat", Email: "pat@example.com", Plan: "individual", Level: "racer"}, year)
	a.store.Join(memberDetails{Name: "Sam", Email: "sam@example.com", Plan: "individual", Level: "new"}, year)
	a.store.mu.Lock()
	a.store.d.Waivers = append(a.store.d.Waivers, &Waiver{ID: 1, MemberID: pat.ID, Year: year})
	a.store.mu.Unlock()

	page := do(t, h, c, "GET", "/admin/email", "", nil).Body.String()
	for _, want := range []string{`value="unsigned">Haven’t signed the ` + itoa(year) + ` waiver (1)`, `value="level-racer">Riding: Racer (1)`, `value="all">Everyone (2)`, `id="mail-link-box"`, `data-do="italic"`} {
		if !strings.Contains(page, want) {
			t.Errorf("email page is missing %q", want)
		}
	}
	camp, err := a.store.StartCampaign("Sign your waiver", "Hi", "unsigned", year)
	if err != nil || camp.Total != 1 || camp.Audience != "Haven’t signed the "+itoa(year)+" waiver" {
		t.Errorf("unsigned campaign: %+v, %v", camp, err)
	}
	if camp, err := a.store.StartCampaign("Racers", "Hi", "level-racer", year); err != nil || camp.Total != 1 || camp.Audience != "Riding: Racer" {
		t.Errorf("racer campaign: %+v, %v", camp, err)
	}
}

// Rides waiting for approval show on the Rides tab, and a ride can be
// approved or cancelled from there without leaving it.
func TestRidesListActions(t *testing.T) {
	a, h, c := testApp(t)
	date := a.today().AddDate(0, 0, 2).Format("2006-01-02")
	held, _ := a.store.AddRide(CalRide{Date: date, Name: "Gravel Spin", Held: true})
	ride, _ := a.store.AddRide(CalRide{Date: date, Name: "Coffee Ride"})

	page := do(t, h, c, "GET", "/admin/rides", "", nil).Body.String()
	if !strings.Contains(page, "Waiting for approval") || !strings.Contains(page, "/admin/rides/"+held.Key+"/approve") {
		t.Error("the Rides tab should list rides waiting for approval")
	}
	ct, body := form(url.Values{"return": {"rides"}})
	if loc := do(t, h, c, "POST", "/admin/rides/"+held.Key+"/approve", ct, body).Header().Get("Location"); loc != "/admin/rides?ok=ride-approved" {
		t.Errorf("approving from the Rides tab went to %q", loc)
	}
	ct, body = form(url.Values{"return": {"rides"}, "cancel": {"1"}})
	if loc := do(t, h, c, "POST", "/admin/rides/"+ride.Key+"/cancel", ct, body).Header().Get("Location"); loc != "/admin/rides?ok=cancelled" {
		t.Errorf("cancelling from the Rides tab went to %q", loc)
	}
	if o, _ := a.store.Ride(ride.Key); !o.Cancelled {
		t.Error("the ride should be cancelled")
	}
	if o, _ := a.store.Ride(held.Key); o.Held {
		t.Error("the ride should be approved")
	}
}

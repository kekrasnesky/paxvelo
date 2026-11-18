package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Admin alerts for held rides, loaner requests and spending requests carry
// two buttons: one posting to the same route as the Needs you button, and
// Look first. A plain tap (iPhones show no buttons) opens the right screen.
func TestAdminAlertActions(t *testing.T) {
	a, _, _ := testApp(t)
	adminLevels(t, a)
	bea, _ := a.store.AdminByEmail("bea@example.com")
	phone := newFakePhone(t, http.StatusCreated)
	a.store.AddAdminPush(bea.ID, phone.sub())

	o, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 5).Format(time.DateOnly), Name: "Hilly Loop", Held: true})
	a.alertHeldRide(o, "Sam")
	a.push.wg.Wait()
	a.alertLoaner(LoanerRequest{ID: 7, Name: "Sam Rider", RideName: "Coffee Ride", RideDate: o.Date, BikeLabel: "M · Trek"})
	a.push.wg.Wait()
	a.store.SetBoard([]BoardMember{{Name: "Bea", Role: "Treasurer", Member: 801}})
	a.alertRequestSent(SpendRequest{ID: 5, Status: reqWaiting, Cents: 4500, What: "Water bottles", By: "Wes Site"})
	a.push.wg.Wait()

	ms := phone.messages()
	if len(ms) != 3 {
		t.Fatalf("got %d alerts", len(ms))
	}
	want := []struct{ url, label, post, done string }{
		{"/admin/rides/" + o.Key, "Approve", "/admin/rides/" + o.Key + "/approve", "Approved: Hilly Loop"},
		{"/admin/loaners#q7", "Confirm", "/admin/loaners/7/confirm", "Confirmed: M · Trek for Sam Rider"},
		{requestsPath + "#r5", "Approve", requestsPath + "/5/approve", "Approved: Water bottles"},
	}
	for i, w := range want {
		m := ms[i]
		if m.URL != w.url || len(m.Actions) != 2 {
			t.Fatalf("alert %d = %+v", i, m)
		}
		if x := m.Actions[0]; x.Action != "do" || x.Title != w.label || x.Post != w.post || x.Done != w.done {
			t.Errorf("alert %d button = %+v", i, x)
		}
		if x := m.Actions[1]; x.Action != "open" || x.Title != "Look first" || x.Post != "" {
			t.Errorf("alert %d Look first = %+v", i, x)
		}
	}
	if b, _ := json.Marshal(pushMsg{Title: "x"}); strings.Contains(string(b), "actions") {
		t.Error("alerts without buttons leave actions out")
	}
}

// The button posts with the admin's login cookie and X-Alert-Action; the
// site answers where it would have gone, so the phone can say Done or open
// the page. The usual level checks apply.
func TestAdminAlertActionPost(t *testing.T) {
	a, h, _ := testApp(t)
	web, _, _ := adminLevels(t, a)
	o, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 5).Format(time.DateOnly), Name: "Hilly Loop", Held: true})
	post := func(c *http.Cookie, path string) (int, string) {
		r := httptest.NewRequest("POST", path, strings.NewReader("return=%2Fadmin%2Fapp"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "http://example.com")
		r.Host = "example.com"
		r.Header.Set(alertActionHeader, "1")
		if c != nil {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var j struct{ To string }
		json.Unmarshal(w.Body.Bytes(), &j)
		return w.Code, j.To
	}

	if code, _ := post(nil, "/admin/rides/"+o.Key+"/approve"); code != http.StatusUnauthorized {
		t.Errorf("logged out = %d", code)
	}
	if got, _ := a.store.Ride(o.Key); !got.Held {
		t.Fatal("approved without a login")
	}
	if code, to := post(web, "/admin/rides/"+o.Key+"/approve"); code != 200 || to != "/admin/app?ok=ride-approved" {
		t.Errorf("approve = %d %q", code, to)
	}
	if got, _ := a.store.Ride(o.Key); got.Held {
		t.Error("the ride is still held")
	}
	// A website admin can't answer a spending request: the answer says so,
	// and the phone opens the page.
	if code, to := post(web, requestsPath+"/1/approve"); code != 200 || !strings.Contains(to, "err=") {
		t.Errorf("website admin approving a request = %d %q", code, to)
	}
	// Without the header the button on Needs you still redirects.
	o2, _ := a.store.AddRide(CalRide{Date: o.Date, Name: "Flat Loop", Held: true})
	ct, b := form(map[string][]string{"return": {"/admin/app"}})
	if w := do(t, h, web, "POST", "/admin/rides/"+o2.Key+"/approve", ct, b); w.Code != http.StatusSeeOther {
		t.Errorf("Needs you button = %d", w.Code)
	}
}

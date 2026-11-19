package main

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// The app's phone jobs list the right things, and their buttons come back
// to the screen they were pressed on.
func TestAppJobs(t *testing.T) {
	a, h, admin, store := demoApp(t)
	home := do(t, h, admin, "GET", "/admin/app", "", nil).Body.String()
	for _, p := range []string{appTodayPath, appApprovalsPath, appPaidPath} {
		if !strings.Contains(home, `href="`+p+`"`) {
			t.Errorf("app Home should link %s", p)
		}
	}

	var key string
	for _, o := range store.Rides(a.today().AddDate(0, 0, 1), a.today().AddDate(0, 0, 1)) {
		if o.Name == "Early Coffee Spin" {
			key = o.Key
		}
	}
	today := do(t, h, admin, "GET", appTodayPath, "", nil).Body.String()
	if key == "" || !strings.Contains(today, "Early Coffee Spin") || !strings.Contains(today, `action="/admin/rides/`+key+`/cancel"`) {
		t.Fatalf("Rides today should list tomorrow's Early Coffee Spin (%q)", key)
	}
	ct, body := form(url.Values{"status": {"on"}, "return": {appTodayPath}})
	if w := do(t, h, admin, "POST", "/admin/rides/"+key+"/cancel", ct, body); !strings.HasPrefix(w.Header().Get("Location"), appTodayPath+"?ok=") {
		t.Errorf("Ride is on should come back to Rides today, went to %q", w.Header().Get("Location"))
	}
	if o, _ := store.Ride(key); !o.IsOn() {
		t.Error("the ride should be marked on")
	}

	appr := do(t, h, admin, "GET", appApprovalsPath, "", nil).Body.String()
	if !strings.Contains(appr, "Gravel Explorer") || !strings.Contains(appr, `name="return" value="`+appApprovalsPath+`"`) {
		t.Error("Approvals should list the held ride with buttons that come back to it")
	}
	if strings.Contains(appr, "newsletter is a draft") {
		t.Error("Approvals lists only lines with a button")
	}

	paid := do(t, h, admin, "GET", appPaidPath, "", nil).Body.String()
	var waiting *Member
	for _, m := range store.Members() {
		if m.WelcomePending && !m.Paid() {
			waiting = &m
			break
		}
	}
	if waiting == nil || !strings.Contains(paid, waiting.Name) {
		t.Fatal("Mark paid should list the website joiner")
	}
	if strings.Index(paid, waiting.Name) > strings.Index(paid, "Last paid") && strings.Contains(paid, "Last paid") {
		t.Error("website joiners come before lapsed members")
	}
	id := strconv.Itoa(waiting.ID)
	ct, body = form(url.Values{"paid": {"1"}, "return": {appPaidPath}})
	if w := do(t, h, admin, "POST", "/admin/members/"+id+"/paid", ct, body); !strings.HasPrefix(w.Header().Get("Location"), appPaidPath+"?ok=") {
		t.Errorf("Mark paid should come back to its screen, went to %q", w.Header().Get("Location"))
	}
	if strings.Contains(do(t, h, admin, "GET", appPaidPath, "", nil).Body.String(), `action="/admin/members/`+id+`/paid"`) {
		t.Error("a paid member should leave the list")
	}

	web, _, _ := adminLevels(t, a)
	if strings.Contains(do(t, h, web, "GET", "/admin/app", "", nil).Body.String(), appPaidPath) {
		t.Error("website admins shouldn't get Mark paid")
	}
	if w := do(t, h, web, "GET", appPaidPath, "", nil); w.Code != 303 {
		t.Errorf("website admin on Mark paid: %d", w.Code)
	}
}

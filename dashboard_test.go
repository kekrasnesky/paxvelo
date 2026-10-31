package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Home shows the membership at a glance, website signups to mark paid, the
// week's rides, and shortcuts to the common jobs.
func TestHomeDashboard(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	a.store.Join(memberDetails{Name: "Paid Pat", Email: "pat@example.com", Plan: "family"}, year)
	pat, _ := a.store.MemberByEmail("pat@example.com")
	a.store.SetPaid(pat.ID, year, true)
	sam, _, _ := a.store.JoinPublic(memberDetails{Name: "Signup Sam", Email: "sam@example.com", Plan: "individual"}, year)

	day := a.today().AddDate(0, 0, 1).Format("2006-01-02")
	a.store.AddRide(CalRide{Name: "Coffee Ride", Date: day, Time: "09:00", Strava: &StravaInfo{Going: 4}})
	a.store.AddRide(CalRide{Name: "Rainy Ride", Date: day, Time: "10:00", Cancelled: true})

	page := do(t, h, admin, "GET", "/admin", "", nil).Body.String()
	for _, want := range []string{
		"<h1>Home</h1>", "Needs attention",
		`href="/admin/members#add"`, `href="/admin/members?dues=unpaid"`, `href="/admin/email"`, `href="/admin/rides/new"`,
		"<dt>Active</dt><dd>2</dd>", "Individual plans</span><strong>1</strong>", "Family plans</span><strong>1</strong>",
		"Signup Sam", `action="/admin/members/` + itoa(sam.ID) + `/paid"`,
		"Coffee Ride", "4 joined on Strava", "<strong>Cancelled:</strong>", "Rainy Ride",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Home should show %q", want)
		}
	}
	if strings.Contains(page, "Paid Pat") {
		t.Error("only signups who haven't been marked paid are listed")
	}

	// Marking a signup paid from Home comes back to Home.
	ct, body := form(url.Values{"paid": {"1"}, "return": {"/admin"}})
	if to := do(t, h, admin, "POST", "/admin/members/"+itoa(sam.ID)+"/paid", ct, body).Header().Get("Location"); !strings.HasPrefix(to, "/admin?ok=paid") {
		t.Errorf("mark paid from Home went to %s", to)
	}
	if page := do(t, h, admin, "GET", "/admin", "", nil).Body.String(); strings.Contains(page, "Signup Sam") {
		t.Error("a signup marked paid should leave the list")
	}
}

// Security warnings show only when they apply, and the setup password's
// warning comes first.
func TestHomeSecurityNudges(t *testing.T) {
	a, h, admin := testApp(t)
	page := do(t, h, admin, "GET", "/admin", "", nil).Body.String()
	if i := strings.Index(page, "still using the setup password"); i < 0 || i > strings.Index(page, "<h1>Home</h1>") {
		t.Error("the setup password warning should sit at the very top")
	}
	if !strings.Contains(page, "The club PIN is off") {
		t.Error("Home should say the club PIN is off")
	}
	if strings.Contains(page, "nightly backup") {
		t.Error("with no backup folder, Home can't tell, so it says nothing")
	}

	a.store.SetClubPIN("8356")
	a.cfg.BackupDir = t.TempDir()
	if page := do(t, h, admin, "GET", "/admin", "", nil).Body.String(); strings.Contains(page, "club PIN is off") || !strings.Contains(page, "no nightly backup") {
		t.Error("with a PIN set and an empty backup folder")
	}
	old := filepath.Join(a.cfg.BackupDir, "paxvelo-2026-01-01.json")
	os.WriteFile(old, []byte("{}"), 0o644)
	os.Chtimes(old, time.Now(), time.Now().Add(-5*24*time.Hour))
	if page := do(t, h, admin, "GET", "/admin", "", nil).Body.String(); !strings.Contains(page, "last nightly backup is 5 days old") {
		t.Error("Home should say the backup is old")
	}
	os.Chtimes(old, time.Now(), time.Now())
	if page := do(t, h, admin, "GET", "/admin", "", nil).Body.String(); strings.Contains(page, "nightly backup") || strings.Contains(page, ">Security</h2>") {
		t.Error("nothing to warn about, so no Security list")
	}
}

// An admin's first visits to Home show a welcome card pointing to How the
// site works, until they open it or press Got it.
func TestHomeWelcomeCard(t *testing.T) {
	a, h, _ := testApp(t)
	login := func(name, email string) *http.Cookie {
		u, _ := a.store.AddAdmin(name, email, "test")
		rec := httptest.NewRecorder()
		a.setSession(rec, accountSession(u), 3600)
		return rec.Result().Cookies()[0]
	}
	pat := login("Pat", "pat@example.com")
	page := do(t, h, pat, "GET", "/admin", "", nil).Body.String()
	if !strings.Contains(page, "Welcome to the admin portal") || strings.Contains(page, "setup password") {
		t.Fatal("a new admin should get the welcome card, not the setup warning")
	}
	do(t, h, pat, "GET", "/admin/guide", "", nil)
	if strings.Contains(do(t, h, pat, "GET", "/admin", "", nil).Body.String(), "Welcome to the admin portal") {
		t.Error("opening How the site works should put the card away")
	}

	lee := login("Lee", "lee@example.com")
	do(t, h, lee, "POST", "/admin/intro", "", nil)
	if strings.Contains(do(t, h, lee, "GET", "/admin", "", nil).Body.String(), "Welcome to the admin portal") {
		t.Error("Got it should put the card away")
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -demo refuses a folder with real club data, an email server and a real
// site address; it fills an empty folder once and leaves a demo club as it
// is on the next start.
func TestDemoCheck(t *testing.T) {
	cfg := testConfig()
	cfg.SiteURL, cfg.SMTPHost, cfg.DataDir = "http://localhost:8080", "", t.TempDir()
	if err := demoCheck(cfg); err != nil {
		t.Fatalf("empty folder: %v", err)
	}
	bad := cfg
	bad.SMTPHost = "smtp-relay.example.com"
	if demoCheck(bad) == nil {
		t.Error("should refuse with an email server set")
	}
	bad = cfg
	bad.SiteURL = "https://www.paxvelo.club"
	if demoCheck(bad) == nil {
		t.Error("should refuse on the real site address")
	}

	real := cfg
	real.DataDir = t.TempDir()
	s, err := openStore(real.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Import([]Member{{Name: "Real Person", Email: "real@example.org"}}); err != nil {
		t.Fatal(err)
	}
	if err := demoCheck(real); err == nil || !strings.Contains(err.Error(), "real club data") {
		t.Errorf("should refuse a folder with real members: %v", err)
	}
	if err := seedDemo(s, cfg.Location); err != nil {
		t.Fatal(err)
	}
	if s.d.Demo || len(s.d.Members) != 1 {
		t.Error("seedDemo must never add to a store that already has members")
	}

	d, err := openStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedDemo(d, cfg.Location); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "paxvelo.json")); err != nil {
		t.Fatal(err)
	}
	if err := demoCheck(cfg); err != nil {
		t.Errorf("a demo club folder should be accepted again: %v", err)
	}
	n := len(d.d.Members)
	if err := seedDemo(d, cfg.Location); err != nil || len(d.d.Members) != n {
		t.Errorf("a second start should leave the demo club alone: %d → %d", n, len(d.d.Members))
	}
}

// The demo club gives every admin screen something to show, and every one
// of them opens with it.
func TestDemoClubAdminPages(t *testing.T) {
	cfg := testConfig()
	cfg.DataDir, cfg.AdminPassword = t.TempDir(), "test-password-123"
	store, err := openStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedDemo(store, cfg.Location); err != nil {
		t.Fatal(err)
	}
	for _, m := range store.d.Members {
		if !strings.HasSuffix(m.Email, "@example.com") {
			t.Errorf("demo address outside example.com: %s", m.Email)
		}
	}
	site, err := newSite(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := openStats(cfg.DataDir, store.Secret(), cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, store: store, site: site, limit: newLimiter(), stats: stats}
	a.mailer = newMailer(cfg, store, site)
	t.Cleanup(a.mailer.bg.Wait)
	a.mailer.stats = stats
	a.refreshNewsletter()
	h := a.routes()
	w := httptest.NewRecorder()
	a.setSession(w, "Test Admin", 3600)
	admin := w.Result().Cookies()[0]

	home := do(t, h, admin, "GET", "/admin", "", nil).Body.String()
	for _, want := range []string{"Gravel Explorer, ", "is waiting for approval.", "Loaner bike for Early Coffee Spin", "/confirm\"><input type=\"hidden\" name=\"return\" value=\"/admin\">"} {
		if !strings.Contains(home, want) {
			t.Errorf("Needs you should list %q", want)
		}
	}
	var rideKey string
	for _, c := range store.d.CalRides {
		if c.Name == "Point Lookout Century" {
			rideKey = occOf(c).Key
		}
	}
	pages := []string{"/admin", "/admin/app", "/admin/members", "/admin/members/1", "/admin/members/2", "/admin/waivers",
		"/admin/rides", "/admin/calendar", "/admin/rides/new", "/admin/rides/" + rideKey, "/admin/weekly/1", "/admin/loaners",
		"/admin/rsvp", "/admin/rsvp/1", "/admin/email", "/admin/email/auto", "/admin/email/log", "/admin/email/alerts",
		"/admin/newsletter", "/admin/gallery", "/admin/submitted", "/admin/pages", "/admin/home-photos", "/admin/board",
		"/admin/kit", "/admin/minutes", "/admin/stats", "/admin/treasurer", "/admin/treasurer/income",
		"/admin/treasurer/requests", "/admin/treasurer/budget", "/admin/treasurer/reports", "/admin/admins", "/admin/security",
		"/admin/signs", "/admin/guide", "/calendar", "/calendar/ride/" + rideKey}
	for _, p := range pages {
		if w := do(t, h, admin, "GET", p, "", nil); w.Code != http.StatusOK {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

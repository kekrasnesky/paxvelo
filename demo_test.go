package main

import (
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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
		"/admin/signs", "/admin/guide", "/calendar", "/calendar/ride/" + rideKey,
		"/admin/app?q=ride", "/admin/app/search?q=example", "/admin/email/auto/morning-summary/preview", "/calendar/new"}
	if p := do(t, h, admin, "GET", "/admin/rides/new", "", nil).Body.String(); !strings.Contains(p, "data-starts=") || !strings.Contains(p, "18:00") {
		t.Error("Add a ride should start from the weekly rides")
	}
	if p := do(t, h, admin, "GET", "/admin/app/search?q=tuesday", "", nil).Body.String(); !strings.Contains(p, "Tuesday Night Ride") {
		t.Error("the admin app's search should find the demo's rides")
	}
	for _, p := range pages {
		if w := do(t, h, admin, "GET", p, "", nil); w.Code != http.StatusOK {
			t.Errorf("%s: %d", p, w.Code)
		}
	}

	// Every screen again as a website admin, a board member and the
	// president: each opens or refuses as main.go wraps its route
	// (permissions.go), and no screen offers a link or form the admin's
	// level would refuse.
	levelOf := routeLevels(t)
	web, board, pres := adminLevels(t, a)
	logins := []struct {
		name   string
		c      *http.Cookie
		refuse map[string]bool // wrappers this login is refused by
	}{
		{"website admin", web, map[string]bool{"boardOnly": true, "president": true, "treasurerOnly": true}},
		{"board member", board, map[string]bool{"president": true, "treasurerOnly": true}}, // Bea is Secretary
		{"president", pres, map[string]bool{}},
	}
	for _, l := range logins {
		for _, p := range pages {
			lvl := levelOf("GET", p)
			w := do(t, h, l.c, "GET", p, "", nil)
			if lvl == "boardOnly" && l.refuse[lvl] {
				if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || loc != "/admin?err=board-only" {
					t.Errorf("%s opening %s: %d %q, want Home with err=board-only", l.name, p, w.Code, loc)
				}
				continue
			}
			if w.Code != http.StatusOK {
				t.Errorf("%s opening %s: %d %s", l.name, p, w.Code, w.Header().Get("Location"))
				continue
			}
			for _, act := range pageActions(w.Body.String()) {
				if got := levelOf(act[0], act[1]); l.refuse[got] {
					t.Errorf("%s sees %s %s on %s, which is %s", l.name, act[0], act[1], p, got)
				}
				// Home and the app hide board-only links by boardHref: it
				// must agree with main.go.
				if lvl := levelOf("GET", act[1]); act[0] == "GET" && strings.HasPrefix(act[1], "/admin") && boardHref(act[1]) != (lvl == "boardOnly" || lvl == "president") {
					t.Errorf("boardHref(%q) = %v, but main.go wraps it in %q", act[1], boardHref(act[1]), lvl)
				}
			}
		}
	}
}

// routeLevels reads main.go and returns, for a method and path, the wrapper
// its route is registered with (auth, boardOnly, president, treasurerOnly),
// "" for a public route or "none" when nothing matches.
func routeLevels(t *testing.T) func(method, path string) string {
	t.Helper()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	wrap := map[string]string{}
	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("((?:GET|POST|PUT|DELETE) /[^"]*)",\s*(?:a\.(auth|boardOnly|president|treasurerOnly)\()?`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		wrap[m[1]] = m[2]
		mux.Handle(m[1], http.NotFoundHandler())
	}
	return func(method, path string) string {
		_, pattern := mux.Handler(httptest.NewRequest(method, path, nil))
		if pattern == "" {
			return "none"
		}
		if pattern == "GET /admin/backup" {
			return "president" // plain auth in main.go, checked inside
		}
		return wrap[pattern]
	}
}

var (
	formTag   = regexp.MustCompile(`(?is)<form\b[^>]*>`)
	attrRe    = regexp.MustCompile(`(?is)\s(action|method|formaction|href)="([^"]*)"`)
	buttonTag = regexp.MustCompile(`(?is)<button\b[^>]*\sformaction="[^"]*"[^>]*>`)
	linkTag   = regexp.MustCompile(`(?is)<a\b[^>]*\shref="([^"]*)"`)
)

// pageActions lists the site's own links and form targets on a page as
// {method, path} pairs (query and fragment dropped).
func pageActions(page string) [][2]string {
	var out [][2]string
	add := func(method, target string) {
		target = html.UnescapeString(target)
		if i := strings.IndexAny(target, "?#"); i >= 0 {
			target = target[:i]
		}
		if strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//") {
			out = append(out, [2]string{method, target})
		}
	}
	for _, tag := range formTag.FindAllString(page, -1) {
		method, action := "GET", ""
		for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
			switch strings.ToLower(m[1]) {
			case "method":
				method = strings.ToUpper(m[2])
			case "action":
				action = m[2]
			}
		}
		add(method, action)
	}
	for _, tag := range buttonTag.FindAllString(page, -1) {
		for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
			if strings.EqualFold(m[1], "formaction") {
				add("POST", m[2])
			}
		}
	}
	for _, m := range linkTag.FindAllStringSubmatch(page, -1) {
		add("GET", m[1])
	}
	return out
}

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The admin phone app: its own manifest (black icon, opens on /admin/app),
// linked from every admin page, and a home of buttons that leaves out the
// board's screens for website admins.
func TestAdminApp(t *testing.T) {
	a, h, _ := testApp(t)
	web, board, _ := adminLevels(t, a)

	w := do(t, h, nil, "GET", "/admin-app.webmanifest", "", nil)
	var m struct {
		Start string `json:"start_url"`
		Sc    string `json:"scope"`
		Icons []struct{ Src string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.Start != adminAppPath+"?app=1" || m.Sc != "/admin" || len(m.Icons) == 0 {
		t.Fatalf("manifest = %s", w.Body)
	}
	for _, ic := range m.Icons {
		if w := do(t, h, nil, "GET", ic.Src, "", nil); w.Code != http.StatusOK {
			t.Errorf("%s: %d", ic.Src, w.Code)
		}
	}

	page := do(t, h, board, "GET", adminAppPath, "", nil).Body.String()
	for _, want := range []string{`rel="manifest" href="/admin-app.webmanifest"`, `class="app-tile" href="/admin/calendar"`, `href="/admin/treasurer"`, `href="/admin/newsletter"`} {
		if !strings.Contains(page, want) {
			t.Errorf("board app home is missing %s", want)
		}
	}
	if strings.Contains(page, `class="app-top"`) {
		t.Error("the app home shouldn't have the Home bar")
	}
	page = do(t, h, web, "GET", adminAppPath, "", nil).Body.String()
	if strings.Contains(page, `href="/admin/treasurer"`) || strings.Contains(page, `href="/admin/newsletter"`) || !strings.Contains(page, `href="/admin/calendar"`) {
		t.Error("website admins should see only their own screens")
	}
	cal := do(t, h, web, "GET", "/admin/calendar", "", nil).Body.String()
	if strings.Contains(cal, `class="app-top"`) || !strings.Contains(cal, `<header class="bar"`) {
		t.Error("in a browser admin screens keep the menu bar")
	}
}

// The app's start URL sets a plain switch cookie (appframe.go): the server
// then draws the Home bar on every admin screen, until ?app=0 clears it.
func TestAdminAppFrame(t *testing.T) {
	a, h, admin := testApp(t)
	a.cfg.SiteURL = "https://www.paxvelo.club"

	w := do(t, h, nil, "GET", adminAppPath+"?app=1", "", nil) // logged out: still remembered for after the login
	var set *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == appCookie {
			set = c
		}
	}
	if set == nil || set.Value != "1" || set.Path != "/admin" || !set.HttpOnly || !set.Secure || set.SameSite != http.SameSiteLaxMode || set.MaxAge <= 0 {
		t.Fatalf("app cookie = %+v", set)
	}
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != adminAppPath {
		t.Errorf("?app=1 should open the same page without it: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(t, h, admin, "GET", "/admin/calendar?app=1&month=2026-12", "", nil); w.Header().Get("Location") != "/admin/calendar?month=2026-12" {
		t.Errorf("?app=1 keeps the rest of the address: %q", w.Header().Get("Location"))
	}
	for _, c := range do(t, h, admin, "POST", "/admin/calendar?app=1", "", nil).Result().Cookies() {
		if c.Name == appCookie {
			t.Error("only opening a page sets the app cookie")
		}
	}

	page := doCookies(t, h, "GET", "/admin/members", admin, set).Body.String()
	if !strings.Contains(page, `class="app-top"`) || strings.Contains(page, `<header class="bar"`) {
		t.Error("the cookie should draw the app frame")
	}
	if w := doCookies(t, h, "GET", "/admin?ok=saved", admin, set); w.Header().Get("Location") != adminAppPath+"?ok=saved" {
		t.Errorf("Home in the app: %q", w.Header().Get("Location"))
	}
	if !strings.Contains(doCookies(t, h, "GET", adminAppPath, admin, set).Body.String(), `href="/admin?app=0"`) {
		t.Error("the app home should offer the full menu")
	}

	w = doCookies(t, h, "GET", "/admin?app=0", admin, set)
	var cleared bool
	for _, c := range w.Result().Cookies() {
		cleared = cleared || (c.Name == appCookie && c.MaxAge < 0)
	}
	if !cleared || w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin" {
		t.Errorf("?app=0 should clear the cookie and open Home: %d %q cleared=%v", w.Code, w.Header().Get("Location"), cleared)
	}
	if page := do(t, h, admin, "GET", "/admin", "", nil).Body.String(); !strings.Contains(page, `<header class="bar"`) {
		t.Error("without the cookie Home has the menu bar")
	}
	if w := do(t, h, nil, "GET", "/?app=1", "", nil); len(w.Result().Cookies()) != 0 {
		t.Error("the public site never sets the app cookie")
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

// adminLevels sets up one login at each level: a website admin, a board
// member (tied to their member record) and the president.
func adminLevels(t *testing.T, a *App) (web, board, pres *http.Cookie) {
	t.Helper()
	w, _ := a.store.AddAdmin("Wes Site", "wes@example.com", "")
	b, _ := a.store.AddAdmin("Bea Board", "bea@example.com", "")
	p, _ := a.store.AddAdmin("Pia Pres", "pia@example.com", "")
	a.store.SetPresident(p.ID)
	a.store.mu.Lock()
	a.store.d.Members = append(a.store.d.Members, &Member{ID: 801, Name: "Beatrice Board", Email: "bea@example.com"})
	a.store.mu.Unlock()
	a.store.SetBoard([]BoardMember{{Name: "Bea", Role: "Secretary", Member: 801}})
	as := func(u AdminUser) *http.Cookie {
		u, _ = a.store.Admin(u.ID)
		rec := httptest.NewRecorder()
		a.setSession(rec, accountSession(u), 3600)
		return rec.Result().Cookies()[0]
	}
	return as(w), as(b), as(p)
}

// Every admin route says which level may use it (permissions.go). Routes
// main.go registers with plain auth are open to website admins, so this list
// is checked against main.go: a new admin route has to be placed here.
var websiteAdminRoutes = map[string]bool{
	"GET /admin": true, "GET /admin/guide": true, "POST /admin/intro": true, "GET /admin/stats": true,
	"GET /admin/admins": true, "POST /admin/admins/password": true, "POST /admin/admins/paypal": true, "POST /admin/admins/{id}/president": true, // checked inside: canNamePresident
	"GET /admin/backup":  true, // checked inside: president
	"GET /admin/members": true, "GET /admin/members/{id}": true,
	"GET /admin/email": true, "GET /admin/email/status": true, "POST /admin/email/test": true, "POST /admin/email/preview": true,
	"POST /admin/email/send": true, "POST /admin/email/{id}/stop": true, "POST /admin/email/{id}/resume": true,
	"POST /admin/email/discard": true, "POST /admin/email/draft": true,
	"GET /admin/submitted": true, "POST /admin/submitted": true, "GET /admin/submitted/{id}/photo": true,
	"GET /admin/gallery": true, "POST /admin/gallery": true, "POST /admin/gallery/upload": true,
	"GET /admin/gallery/{id}": true, "POST /admin/gallery/{id}": true,
	"GET /admin/rides": true, "POST /admin/strava/sync": true, "GET /admin/strava/preview": true,
	"GET /admin/calendar": true, "GET /admin/history": true, "GET /admin/rides/new": true, "POST /admin/rides/new": true,
	"GET /admin/rides/{key}": true, "POST /admin/rides/{key}": true, "POST /admin/rides/{key}/cancel": true,
	"POST /admin/rides/{key}/approve": true, "POST /admin/rides/{key}/delete": true,
	"POST /admin/weekly": true, "GET /admin/weekly/{id}": true, "POST /admin/weekly/{id}": true, "POST /admin/weekly/{id}/delete": true,
	"GET /admin/minutes":            true,
	"GET /admin/treasurer/requests": true, "POST /admin/treasurer/requests": true, "GET /admin/treasurer/receipts/{name}": true,
	"POST /admin/treasurer/requests/{id}/withdraw": true, "POST /admin/treasurer/requests/{id}/restore": true,
	"POST /admin/treasurer/requests/{id}/bought": true,
}

func TestAdminRoutesHaveALevel(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.Handle\("((?:GET|POST) /admin[^"]*)", a\.auth\(`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		if !websiteAdminRoutes[m[1]] {
			t.Errorf("%s is open to every admin: wrap it in boardOnly or president, or list it in websiteAdminRoutes", m[1])
		}
	}
}

func TestPermissionLevels(t *testing.T) {
	a, h, _ := testApp(t)
	web, board, pres := adminLevels(t, a)

	// Board-only screens: website admins go Home with an error; the board gets in.
	for _, p := range []string{"/admin/waivers", "/admin/members.csv", "/admin/newsletter", "/admin/email/auto", "/admin/email/log",
		"/admin/pages", "/admin/home-photos", "/admin/board", "/admin/instagram", "/admin/security", "/admin/treasurer",
		"/admin/treasurer/budget", "/admin/treasurer/reports"} {
		if loc := do(t, h, web, "GET", p, "", nil).Header().Get("Location"); loc != "/admin?err=board-only" {
			t.Errorf("website admin opening %s went to %q", p, loc)
		}
		if w := do(t, h, board, "GET", p, "", nil); w.Code != http.StatusOK {
			t.Errorf("board member opening %s: %d %s", p, w.Code, w.Header().Get("Location"))
		}
	}

	// Website admins' own screens.
	for _, p := range []string{"/admin", "/admin/calendar", "/admin/rides", "/admin/gallery", "/admin/submitted", "/admin/minutes",
		"/admin/email", "/admin/members", "/admin/treasurer/requests", "/admin/admins"} {
		if w := do(t, h, web, "GET", p, "", nil); w.Code != http.StatusOK {
			t.Errorf("website admin opening %s: %d %s", p, w.Code, w.Header().Get("Location"))
		}
	}

	// The menu shows a website admin only what they can open.
	home := do(t, h, web, "GET", "/admin", "", nil).Body.String()
	for _, p := range []string{`href="/admin/pages"`, `href="/admin/newsletter"`, `href="/admin/treasurer"`, `href="/admin/security"`} {
		if strings.Contains(home, p) {
			t.Errorf("website admin's menu links %s", p)
		}
	}
	if !strings.Contains(home, `href="/admin/treasurer/requests"`) {
		t.Error("website admin's menu should link Requests")
	}

	// Members: website admins look people up (emergency contact) but can't change them.
	m, _, _ := a.store.Join(memberDetails{Name: "Ria Rider", Email: "ria@example.com", EmergencyName: "Sam Rider", EmergencyPhone: "301-555-0199"}, a.cfg.ThisYear())
	page := do(t, h, web, "GET", "/admin/members/"+itoa(m.ID), "", nil).Body.String()
	if !strings.Contains(page, "301-555-0199") || !strings.Contains(page, "Sam Rider") {
		t.Error("website admins should see the emergency contact")
	}
	if strings.Contains(page, `action="/admin/members/`+itoa(m.ID)+`"`) || strings.Contains(page, "Mark paid") {
		t.Error("website admins shouldn't get the edit forms")
	}
	if list := do(t, h, web, "GET", "/admin/members", "", nil).Body.String(); strings.Contains(list, "Export CSV") || strings.Contains(list, "Add a member") {
		t.Error("website admins shouldn't get export or add on the list")
	}
	ct, body := form(url.Values{"name": {"Hacked"}, "email": {"ria@example.com"}})
	do(t, h, web, "POST", "/admin/members/"+itoa(m.ID), ct, body)
	if got, _ := a.store.MemberByID(m.ID); got.Name != "Ria Rider" {
		t.Error("a website admin changed a member")
	}

	// Permissions are the president's: the board can't add admins or change the board.
	for _, c := range []*http.Cookie{web, board} {
		ct, body := form(url.Values{"name": {"Eve"}, "email": {"eve@example.com"}})
		if loc := do(t, h, c, "POST", "/admin/admins", ct, body).Header().Get("Location"); loc != "/admin/admins?err=president-only" {
			t.Errorf("adding an admin went to %q", loc)
		}
		ct, body = form(url.Values{"count": {"1"}, "name0": {"Eve"}, "role0": {"Treasurer"}})
		do(t, h, c, "POST", "/admin/board", ct, body)
		if b := a.store.Board(); len(b) != 1 || b[0].Role != "Secretary" {
			t.Fatalf("the board was changed: %+v", b)
		}
	}
	if _, ok := a.store.AdminByEmail("eve@example.com"); ok {
		t.Error("Eve was added")
	}
	if page := do(t, h, board, "GET", "/admin/admins", "", nil).Body.String(); strings.Contains(page, "Add an admin") || strings.Contains(page, "/link\"") {
		t.Error("the board shouldn't see admin management")
	}
	ct, body = form(url.Values{"name": {"Eve"}, "email": {"eve@example.com"}})
	do(t, h, pres, "POST", "/admin/admins", ct, body)
	if _, ok := a.store.AdminByEmail("eve@example.com"); !ok {
		t.Error("the president should be able to add an admin")
	}

	// Club PIN: the board's, not website admins'.
	ct, body = form(url.Values{"pin": {"spokes42"}})
	do(t, h, web, "POST", "/admin/security", ct, body)
	if a.store.ClubPIN() != "" {
		t.Error("a website admin set the club PIN")
	}
	ct, body = form(url.Values{"pin": {"spokes42"}})
	do(t, h, board, "POST", "/admin/security", ct, body)
	if a.store.ClubPIN() != "spokes42" {
		t.Error("a board member should be able to set the club PIN")
	}

	// Website admins email about rides and events only, whatever the form says.
	ct, body = form(url.Values{"subject": {"Ride Saturday"}, "body": {"See you there"}, "topic": {""}, "audience": {""}})
	do(t, h, web, "POST", "/admin/email/send", ct, body)
	cs := a.store.Campaigns()
	if len(cs) == 0 || cs[0].Topic != "events" {
		t.Errorf("a website admin's email should be about events: %+v", cs)
	}
}

// Website admins send their own spending requests and see only those.
func TestWebsiteAdminRequests(t *testing.T) {
	a, h, _ := testApp(t)
	web, board, _ := adminLevels(t, a)
	send := func(c *http.Cookie, what string) {
		ct, body := receiptForm(t, map[string]string{"kind": "report", "date": "2026-11-01", "amount": "10", "category": "Bank Fees", "what": what}, "", nil)
		do(t, h, c, "POST", "/admin/treasurer/requests", ct, body)
	}
	send(web, "Cones for the clinic")
	send(board, "Board snacks")
	if n := len(a.store.SpendRequests()); n != 2 {
		t.Fatalf("requests: %d", n)
	}
	page := do(t, h, web, "GET", "/admin/treasurer/requests", "", nil).Body.String()
	if !strings.Contains(page, "Cones for the clinic") || strings.Contains(page, "Board snacks") {
		t.Error("a website admin should see only their own requests")
	}
	if page := do(t, h, board, "GET", "/admin/treasurer/requests", "", nil).Body.String(); !strings.Contains(page, "Cones for the clinic") {
		t.Error("the board should see every request")
	}
}

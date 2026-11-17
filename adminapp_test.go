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
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.Start != adminAppPath || m.Sc != "/admin" || len(m.Icons) == 0 {
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
	if !strings.Contains(do(t, h, web, "GET", "/admin/calendar", "", nil).Body.String(), `class="app-top" href="/admin/app"`) {
		t.Error("admin screens should carry the app's Home bar")
	}
}

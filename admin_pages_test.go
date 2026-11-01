package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestPagesAndBoardAdmin(t *testing.T) {
	_, h, admin := testApp(t)
	for _, path := range []string{"/admin/pages", "/admin/pages/rides.md", "/admin/board"} {
		if w := do(t, h, admin, "GET", path, "", nil); w.Code != 200 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	ct, body := form(url.Values{"text": {"---\nmenu: Our Rides\n---\n# Our Rides\n\nEdited in the admin."}})
	do(t, h, admin, "POST", "/admin/pages/rides.md", ct, body)
	if !strings.Contains(do(t, h, nil, "GET", "/rides", "", nil).Body.String(), "Edited in the admin.") {
		t.Error("page edit didn't show")
	}
	ct, body = form(url.Values{"text": {"no title"}})
	if w := do(t, h, admin, "POST", "/admin/pages/rides.md", ct, body); !strings.Contains(w.Body.String(), "Not saved yet") {
		t.Error("broken page edit wasn't refused")
	}
	// The live preview draws unsaved text in the site's layout, and says what's
	// wrong with text that can't be saved, without changing the page.
	ct, body = form(url.Values{"text": {"# Our Rides\n\nJust **typed**, not saved."}})
	if w := do(t, h, admin, "POST", "/admin/pages/rides.md/preview", ct, body); w.Code != 200 || !strings.Contains(w.Body.String(), "<strong>typed</strong>") || !strings.Contains(w.Body.String(), `id="info-menu"`) || strings.Contains(w.Body.String(), "<script") {
		t.Errorf("preview: %d", w.Code)
	}
	ct, body = form(url.Values{"text": {"no title"}})
	if w := do(t, h, admin, "POST", "/admin/pages/rides.md/preview", ct, body); w.Code != 422 || w.Body.Len() == 0 {
		t.Errorf("preview of a broken page: %d %q", w.Code, w.Body.String())
	}
	if strings.Contains(do(t, h, nil, "GET", "/rides", "", nil).Body.String(), "not saved") {
		t.Error("the preview changed the page")
	}
	if w := do(t, h, nil, "POST", "/admin/pages/rides.md/preview", ct, body); w.Code != 401 {
		t.Errorf("preview without logging in: %d", w.Code)
	}
	// Adding a page: it's live at its address straight away (no restart), last
	// in the Info menu, and can be deleted again.
	ct, body = form(url.Values{"title": {"Group Ride Etiquette"}, "menu": {"on"}})
	if w := do(t, h, admin, "POST", "/admin/pages", ct, body); !strings.Contains(w.Header().Get("Location"), "/admin/pages/group-ride-etiquette.md?ok=page-created") {
		t.Fatalf("add page: %d %s", w.Code, w.Header().Get("Location"))
	}
	page := do(t, h, nil, "GET", "/group-ride-etiquette", "", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Group Ride Etiquette") {
		t.Fatalf("new page: %d", page.Code)
	}
	home := do(t, h, nil, "GET", "/", "", nil).Body.String()
	menu := home[strings.Index(home, `id="info-menu"`):]
	menu = menu[:strings.Index(menu, "</div>")]
	if i, j := strings.Index(menu, "/group-ride-etiquette"), strings.Index(menu, "/routes"); i < 0 || j < i || strings.Count(menu[:i], "<a ") < 3 {
		t.Errorf("the new page should be at the end of the Info menu (before Club Routes): %s", menu)
	}
	for _, taken := range []string{"Group Ride Etiquette", "Calendar", "Our Rides"} {
		addr := map[string]string{"Our Rides": "rides"}[taken]
		ct, body = form(url.Values{"title": {taken}, "address": {addr}})
		if w := do(t, h, admin, "POST", "/admin/pages", ct, body); !strings.Contains(w.Header().Get("Location"), "err=page-taken") {
			t.Errorf("%q got an address that's already used: %s", taken, w.Header().Get("Location"))
		}
	}
	if w := do(t, h, admin, "GET", "/admin/pages/group-ride-etiquette.md", "", nil); !strings.Contains(w.Body.String(), "Delete this page") {
		t.Error("a page added here should offer Delete")
	}
	do(t, h, admin, "POST", "/admin/pages/group-ride-etiquette.md/delete", "", nil)
	if w := do(t, h, nil, "GET", "/group-ride-etiquette", "", nil); w.Code != 404 {
		t.Errorf("deleted page: %d", w.Code)
	}
	// Pages from GitHub can't be deleted, only reset.
	do(t, h, admin, "POST", "/admin/pages/rides.md/delete", "", nil)
	if w := do(t, h, nil, "GET", "/rides", "", nil); w.Code != 200 {
		t.Errorf("a GitHub page was deleted: %d", w.Code)
	}

	ct, body = form(url.Values{"count": {"1"}, "name0": {"Jane Smith"}, "role0": {"President"}, "order0": {"1"}})
	do(t, h, admin, "POST", "/admin/board", ct, body)
	if page := do(t, h, nil, "GET", "/committee", "", nil).Body.String(); !strings.Contains(page, "Jane Smith") || strings.Contains(page, "Vice President") {
		t.Error("board edit didn't show")
	}
}

// paxvelo.club/kit, on flyers and in the welcome email, shows the kit page,
// whose button goes on to the team store. The menu's Shop Kit link goes there too.
func TestKitPage(t *testing.T) {
	_, h, _ := testApp(t)
	w := do(t, h, nil, "GET", "/kit", "", nil)
	if w.Code != 200 {
		t.Fatalf("/kit: %d", w.Code)
	}
	got := w.Body.String()
	for _, want := range []string{"<h1>Club Kit</h1>", `href="https://example.com/kit"`, "Open the team store", `<a href="/kit" aria-current="page">Shop Kit</a>`} {
		if !strings.Contains(got, want) {
			t.Errorf("the kit page is missing %q", want)
		}
	}
}

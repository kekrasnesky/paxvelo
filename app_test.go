package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The site can be added to a phone's home screen: pages link the manifest
// and icon, the manifest names the app and its icons (which exist), the
// service worker is served from the top of the site with this version, and
// the offline page is there for it.
func TestHomeScreenApp(t *testing.T) {
	a, h, _ := testApp(t)
	home := do(t, h, nil, "GET", "/", "", nil).Body.String()
	for _, want := range []string{`<link rel="manifest" href="/manifest.webmanifest">`, `rel="apple-touch-icon" href="/static/img/apple-touch-icon.png"`, `apple-mobile-web-app-title`} {
		if !strings.Contains(home, want) {
			t.Errorf("home page is missing %q", want)
		}
	}

	w := do(t, h, nil, "GET", "/manifest.webmanifest", "", nil)
	if ct := w.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("manifest type = %q", ct)
	}
	var m struct {
		Name, StartURL, Display string
		Icons                   []struct{ Src, Sizes, Purpose string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "PaxVelo" || m.Display != "standalone" || len(m.Icons) < 2 {
		t.Errorf("manifest = %+v", m)
	}
	icons := []string{"/static/img/apple-touch-icon.png", "/apple-touch-icon.png", "/apple-touch-icon-precomposed.png"}
	for _, i := range m.Icons {
		icons = append(icons, i.Src)
	}
	for _, src := range icons {
		if w := do(t, h, nil, "GET", src, "", nil); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
			t.Errorf("icon %s = %d %q", src, w.Code, w.Header().Get("Content-Type"))
		}
	}

	w = do(t, h, nil, "GET", "/sw.js", "", nil)
	sw := w.Body.String()
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("sw.js headers = %v", w.Header())
	}
	if !strings.Contains(sw, "var V='"+a.site.version+"'") || strings.Contains(sw, "__V__") {
		t.Error("sw.js should carry the site's version")
	}
	for _, private := range []string{"admin", "membership", "rsvp", "kit", "unsubscribe", "request-answer", "join"} {
		if !strings.Contains(sw, "|"+private) && !strings.Contains(sw, "("+private) {
			t.Errorf("sw.js should never save /%s pages", private)
		}
	}

	off := do(t, h, nil, "GET", "/offline", "", nil)
	if off.Code != 200 || !strings.Contains(off.Body.String(), "No connection") || !strings.Contains(off.Body.String(), `content="noindex"`) {
		t.Errorf("offline page = %d", off.Code)
	}
	if !strings.Contains(string(a.site.js), "serviceWorker.register('/sw.js')") {
		t.Error("site.js should register the service worker")
	}
}

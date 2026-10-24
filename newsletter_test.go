package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// testApp runs the whole site against a temporary data folder, logged in as admin.
func testApp(t *testing.T) (*App, http.Handler, *http.Cookie) {
	t.Helper()
	cfg := testConfig()
	cfg.DataDir, cfg.AdminPassword = t.TempDir(), "test-password-123"
	store, err := openStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	site, err := newSite(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, store: store, site: site, limit: newLimiter()}
	a.mailer = newMailer(cfg, store, site)
	a.refreshNewsletter()
	rec := httptest.NewRecorder()
	a.setSession(rec, 3600)
	return a, a.routes(), rec.Result().Cookies()[0]
}

func do(t *testing.T, h http.Handler, c *http.Cookie, method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func form(vals url.Values) (string, io.Reader) {
	return "application/x-www-form-urlencoded", strings.NewReader(vals.Encode())
}

func TestNewsletterFlow(t *testing.T) {
	a, h, admin := testApp(t)

	// Before the first issue: a friendly placeholder, not a 404.
	if w := do(t, h, nil, "GET", "/newsletter", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "on its way") {
		t.Fatalf("empty newsletter: %d %s", w.Code, w.Body.String())
	}

	// Start an issue: it comes with sample content.
	ct, body := form(url.Values{"season": {"fall"}, "year": {"2026"}})
	if w := do(t, h, admin, "POST", "/admin/newsletter", ct, body); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "/admin/newsletter/1") {
		t.Fatalf("create: %d %s", w.Code, w.Header().Get("Location"))
	}
	is, ok := a.store.Issue(1)
	if !ok || is.Featured.Heading == "" || len(is.Rides) != 3 || is.Rides[0].Date == "" {
		t.Fatalf("sample issue: %+v", is)
	}

	// A second issue for the same season is refused.
	ct, body = form(url.Values{"season": {"fall"}, "year": {"2026"}})
	if w := do(t, h, admin, "POST", "/admin/newsletter", ct, body); !strings.Contains(w.Header().Get("Location"), "err=exists") {
		t.Errorf("duplicate season: %s", w.Header().Get("Location"))
	}

	// Drafts aren't public.
	if w := do(t, h, nil, "GET", "/newsletter/2026-fall", "", nil); w.Code != 404 {
		t.Errorf("draft is public: %d", w.Code)
	}

	// Edit it, uploading a cover photo and a ride with a Strava event.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range map[string]string{
		"season": "fall", "year": "2026", "tagline": "Stories from the road.",
		"featured.heading": "Meet Pat", "featured.body": "Pat rides **every** Thursday.",
		"featured.video_url": "https://youtube.com/watch?v=x", "featured.quote": "Best club around.",
		"kit.heading": "Design our next kit", "kit.link_label": "Visit the team store", "kit.link_url": "{kit}",
		"ride0.date": "2026-11-03", "ride0.name": "Turkey Trot Ride", "ride0.url": "https://www.strava.com/clubs/10197/group_events/1",
		"ride1.name": "", "featured.photo_remove": "on",
	} {
		mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("featured.photo_file", "pat.png")
	fw.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 64)))
	mw.Close()
	if w := do(t, h, admin, "POST", "/admin/newsletter/1", mw.FormDataContentType(), &buf); w.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	is, _ = a.store.Issue(1)
	if !strings.HasPrefix(is.Featured.Photo, "/media/") || len(is.Rides) != 1 || is.Kit.LinkURL != "{kit}" {
		t.Fatalf("after save: photo=%q rides=%+v", is.Featured.Photo, is.Rides)
	}
	if w := do(t, h, nil, "GET", is.Featured.Photo, "", nil); w.Code != 200 {
		t.Errorf("uploaded photo: %d", w.Code)
	}

	// Publishing a version that can't be shown is refused, and nothing is saved.
	broken := url.Values{"season": {"fall"}, "year": {"2026"}, "featured.heading": {"Meet Pat"}, "featured.body": {"See {stravva}"}}
	ct, body = form(broken)
	if w := do(t, h, admin, "POST", "/admin/newsletter/1?then=publish", ct, body); !strings.Contains(w.Body.String(), "unknown placeholder {stravva}") {
		t.Errorf("broken publish wasn't reported: %d", w.Code)
	}
	if is, _ := a.store.Issue(1); is.IsPublished() || strings.Contains(is.Featured.Body, "stravva") {
		t.Fatalf("broken edit was published or saved: %+v", is.Featured)
	}

	// Save and publish, then the issue is live at both addresses.
	cur, _ := a.store.Issue(1)
	vals := url.Values{"season": {"fall"}, "year": {"2026"}, "featured.heading": {cur.Featured.Heading}, "featured.body": {cur.Featured.Body},
		"featured.photo_current": {cur.Featured.Photo}, "featured.video_url": {cur.Featured.VideoURL}, "featured.quote": {cur.Featured.Quote},
		"kit.heading": {cur.Kit.Heading}, "kit.link_label": {cur.Kit.LinkLabel}, "kit.link_url": {cur.Kit.LinkURL},
		"ride0.date": {"2026-11-03"}, "ride0.name": {"Turkey Trot Ride"}, "ride0.url": {"https://www.strava.com/clubs/10197/group_events/1"},
		"ride1.name": {"Holiday Lights Ride"}}
	ct, body = form(vals)
	if w := do(t, h, admin, "POST", "/admin/newsletter/1?then=publish", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=published") {
		t.Fatalf("save and publish: %d %s", w.Code, w.Header().Get("Location"))
	}
	if is, _ := a.store.Issue(1); !is.IsPublished() || is.Featured.Photo != cur.Featured.Photo || is.Rides[1].Name != "Holiday Lights Ride" {
		t.Errorf("after publish: published=%v photo=%q rides=%+v (undated rides go last)", is.IsPublished(), is.Featured.Photo, is.Rides)
	}
	for _, path := range []string{"/newsletter", "/newsletter/2026-fall"} {
		w := do(t, h, nil, "GET", path, "", nil)
		page := w.Body.String()
		for _, want := range []string{
			"Fall 2026", "MEET PAT", "<strong>every</strong>", "Best club around.", "nl-play",
			`href="https://www.strava.com/clubs/10197/group_events/1"`, "<span>NOV</span>3",
			`href="https://example.com/kit"`, "Visit the team store",
		} {
			if !strings.Contains(strings.ToUpper(page), strings.ToUpper(want)) {
				t.Errorf("%s is missing %q", path, want)
			}
		}
		if w.Header().Get("ETag") == "" {
			t.Errorf("%s isn't served from the cache", path)
		}
	}

	// A broken edit to the live issue isn't saved, so the issue still shows
	// after a restart, and the edit comes back in the form to fix.
	ct, body = form(broken)
	w := do(t, h, admin, "POST", "/admin/newsletter/1", ct, body)
	if !strings.Contains(w.Body.String(), "unknown placeholder {stravva}") || !strings.Contains(w.Body.String(), "See {stravva}") {
		t.Errorf("broken edit wasn't reported with the text kept: %d", w.Code)
	}
	a.site.news = nil // as after a restart
	a.refreshNewsletter()
	if w := do(t, h, nil, "GET", "/newsletter", "", nil); !strings.Contains(w.Body.String(), "<strong>every</strong>") {
		t.Error("a broken edit replaced the live issue")
	}

	// Announcing it doesn't write over an email in progress.
	a.store.SetDraft(EmailDraft{Subject: "Dues reminder", Body: "Half written"})
	if w := do(t, h, admin, "POST", "/admin/newsletter/1/email", "", nil); !strings.Contains(w.Header().Get("Location"), "err=draft") {
		t.Errorf("email over a draft: %s", w.Header().Get("Location"))
	}
	if d := a.store.Draft(); d.Subject != "Dues reminder" {
		t.Errorf("draft was replaced: %+v", d)
	}
	a.store.SetDraft(EmailDraft{})
	do(t, h, admin, "POST", "/admin/newsletter/1/email", "", nil)
	if d := a.store.Draft(); !strings.Contains(d.Body, "/newsletter/2026-fall") {
		t.Errorf("announcement draft: %+v", d)
	}

	// Unpublish hides it again.
	do(t, h, admin, "POST", "/admin/newsletter/1/unpublish", "", nil)
	if w := do(t, h, nil, "GET", "/newsletter/2026-fall", "", nil); w.Code != 404 {
		t.Errorf("unpublished issue still public: %d", w.Code)
	}
}

func TestNewsletterNeedsLogin(t *testing.T) {
	_, h, _ := testApp(t)
	if w := do(t, h, nil, "GET", "/admin/newsletter", "", nil); w.Code != http.StatusSeeOther {
		t.Errorf("admin page without login: %d", w.Code)
	}
	ct, body := form(url.Values{"season": {"fall"}, "year": {"2026"}})
	if w := do(t, h, nil, "POST", "/admin/newsletter", ct, body); w.Code != http.StatusUnauthorized {
		t.Errorf("create without login: %d", w.Code)
	}
}

func TestSeasons(t *testing.T) {
	if s, y := nextSeason("fall", 2026); s != "winter" || y != 2027 {
		t.Errorf("after fall 2026: %s %d", s, y)
	}
	newer := Issue{Season: "winter", Year: 2027}
	if older := (Issue{Season: "fall", Year: 2026}); newer.order() <= older.order() {
		t.Error("winter 2027 should sort after fall 2026")
	}
}

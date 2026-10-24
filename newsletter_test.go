package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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
	a.setSession(rec, "Test Admin", 3600)
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
	ct, body := form(url.Values{"month": {"10"}, "year": {"2026"}})
	if w := do(t, h, admin, "POST", "/admin/newsletter", ct, body); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "/admin/newsletter/1") {
		t.Fatalf("create: %d %s", w.Code, w.Header().Get("Location"))
	}
	is, ok := a.store.Issue(1)
	if !ok || is.Featured.Heading == "" || len(is.Rides) != 3 || is.Rides[0].Date == "" {
		t.Fatalf("sample issue: %+v", is)
	}

	if is.CreatedBy != "Test Admin" || is.EditedBy != "Test Admin" {
		t.Errorf("author: created by %q, edited by %q", is.CreatedBy, is.EditedBy)
	}

	// Drafts aren't public.
	if w := do(t, h, nil, "GET", "/newsletter/2026-10", "", nil); w.Code != 404 {
		t.Errorf("draft is public: %d", w.Code)
	}

	// Edit it, uploading a cover photo and a ride with a Strava event.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range map[string]string{
		"month": "10", "year": "2026", "tagline": "Stories from the road.",
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
	broken := url.Values{"month": {"10"}, "year": {"2026"}, "featured.heading": {"Meet Pat"}, "featured.body": {"See {stravva}"}}
	ct, body = form(broken)
	if w := do(t, h, admin, "POST", "/admin/newsletter/1?then=publish", ct, body); !strings.Contains(w.Body.String(), "unknown placeholder {stravva}") {
		t.Errorf("broken publish wasn't reported: %d", w.Code)
	}
	if is, _ := a.store.Issue(1); is.IsPublished() || strings.Contains(is.Featured.Body, "stravva") {
		t.Fatalf("broken edit was published or saved: %+v", is.Featured)
	}

	// Save and publish, then the issue is live at both addresses.
	cur, _ := a.store.Issue(1)
	vals := url.Values{"month": {"10"}, "year": {"2026"}, "featured.heading": {cur.Featured.Heading}, "featured.body": {cur.Featured.Body},
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
	for _, path := range []string{"/newsletter", "/newsletter/2026-10"} {
		w := do(t, h, nil, "GET", path, "", nil)
		page := w.Body.String()
		for _, want := range []string{
			"October 2026", "MEET PAT", "<strong>every</strong>", "Best club around.", "nl-play",
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
	if d := a.store.Draft(); !strings.Contains(d.Body, "/newsletter/2026-10") {
		t.Errorf("announcement draft: %+v", d)
	}

	// Unpublish hides it again.
	do(t, h, admin, "POST", "/admin/newsletter/1/unpublish", "", nil)
	if w := do(t, h, nil, "GET", "/newsletter/2026-10", "", nil); w.Code != 404 {
		t.Errorf("unpublished issue still public: %d", w.Code)
	}
}

func TestSeveralDraftsOneLiveIssuePerMonth(t *testing.T) {
	a, h, admin := testApp(t)
	ct, body := form(url.Values{"month": {"11"}, "year": {"2026"}})
	do(t, h, admin, "POST", "/admin/newsletter", ct, body)
	// A second admin starts another draft for the same month by copying the first.
	other := httptest.NewRecorder()
	a.setSession(other, "Second Admin", 3600)
	second := other.Result().Cookies()[0]
	if w := do(t, h, second, "POST", "/admin/newsletter/1/copy", "", nil); !strings.Contains(w.Header().Get("Location"), "/admin/newsletter/2") {
		t.Fatalf("copy: %s", w.Header().Get("Location"))
	}
	if is, _ := a.store.Issue(2); is.CreatedBy != "Second Admin" || is.Month != 11 || is.IsPublished() {
		t.Fatalf("copied draft: %+v", is)
	}

	publish := func(id int, c *http.Cookie) *httptest.ResponseRecorder {
		is, _ := a.store.Issue(id)
		ct, body := form(url.Values{"month": {"11"}, "year": {"2026"}, "featured.heading": {is.Featured.Heading}, "featured.body": {is.Featured.Body}})
		return do(t, h, c, "POST", fmt.Sprintf("/admin/newsletter/%d?then=publish", id), ct, body)
	}
	if w := publish(1, admin); !strings.Contains(w.Header().Get("Location"), "ok=published") {
		t.Fatalf("publish first draft: %d %s", w.Code, w.Header().Get("Location"))
	}
	// The other draft can't be published while the first one is live, but its edits are kept.
	w := publish(2, second)
	if !strings.Contains(w.Body.String(), "already has a published issue") {
		t.Errorf("second publish wasn't refused: %d %s", w.Code, w.Header().Get("Location"))
	}
	if is, _ := a.store.Issue(2); is.IsPublished() || is.EditedBy != "Second Admin" {
		t.Errorf("second draft: published=%v edited by %q", is.IsPublished(), is.EditedBy)
	}
	if _, err := a.store.SaveIssue(func() Issue { is, _ := a.store.Issue(2); now := time.Now(); is.Published = &now; return is }()); !errors.Is(err, errMonthTaken) {
		t.Errorf("store allowed two live issues for one month: %v", err)
	}
	// After unpublishing the first, the second can go live.
	do(t, h, admin, "POST", "/admin/newsletter/1/unpublish", "", nil)
	if w := publish(2, second); !strings.Contains(w.Header().Get("Location"), "ok=published") {
		t.Errorf("publish after unpublishing the other: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := do(t, h, nil, "GET", "/newsletter/2026-11", "", nil); w.Code != 200 {
		t.Errorf("published issue: %d", w.Code)
	}
}

func TestLoginNeedsName(t *testing.T) {
	_, h, _ := testApp(t)
	ct, body := form(url.Values{"password": {"test-password-123"}})
	if w := do(t, h, nil, "POST", "/admin/login", ct, body); w.Code != http.StatusBadRequest {
		t.Errorf("login without a name: %d", w.Code)
	}
	ct, body = form(url.Values{"password": {"test-password-123"}, "name": {"Pat Rider"}})
	w := do(t, h, nil, "POST", "/admin/login", ct, body)
	if w.Code != http.StatusSeeOther || len(w.Result().Cookies()) == 0 {
		t.Fatalf("login: %d", w.Code)
	}
	if w := do(t, h, w.Result().Cookies()[0], "GET", "/admin/newsletter", "", nil); !strings.Contains(w.Body.String(), "Pat Rider") {
		t.Error("the admin bar doesn't show who is logged in")
	}
}

func TestMinutes(t *testing.T) {
	a, h, admin := testApp(t)
	a.refreshMinutes()
	if w := do(t, h, nil, "GET", "/minutes", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "No minutes have been posted yet") {
		t.Fatalf("empty minutes page: %d", w.Code)
	}
	upload := func(name, content string, publish bool) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("date", "2026-10-07")
		mw.WriteField("title", "October club meeting")
		mw.WriteField("summary", "Approved the 2027 ride calendar.")
		if publish {
			mw.WriteField("publish", "on")
		}
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write([]byte(content))
		mw.Close()
		return do(t, h, admin, "POST", "/admin/minutes", mw.FormDataContentType(), &buf)
	}
	if w := upload("notes.exe", "MZ not a document", true); !strings.Contains(w.Body.String(), "PDF or a Word") {
		t.Errorf("non-document accepted: %d", w.Code)
	}
	if w := upload("oct.pdf", "%PDF-1.7 fake", false); !strings.Contains(w.Header().Get("Location"), "ok=minutes-added") {
		t.Fatalf("upload: %d %s", w.Code, w.Header().Get("Location"))
	}
	m := a.store.AllMinutes()[0]
	if m.UploadedBy != "Test Admin" || m.IsPublished() || m.Kind() != "PDF" {
		t.Errorf("stored minutes: %+v", m)
	}
	if w := do(t, h, nil, "GET", "/minutes", "", nil); strings.Contains(w.Body.String(), "October club meeting") {
		t.Error("unpublished minutes are public")
	}
	ct, body := form(url.Values{"publish": {"1"}})
	do(t, h, admin, "POST", fmt.Sprintf("/admin/minutes/%d/publish", m.ID), ct, body)
	page := do(t, h, nil, "GET", "/minutes", "", nil).Body.String()
	for _, want := range []string{"October club meeting", "October 7, 2026", "Approved the 2027 ride calendar.", m.File} {
		if !strings.Contains(page, want) {
			t.Errorf("minutes page is missing %q", want)
		}
	}
	if w := do(t, h, nil, "GET", m.File, "", nil); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "%PDF") {
		t.Errorf("minutes file: %d", w.Code)
	}
	do(t, h, admin, "POST", fmt.Sprintf("/admin/minutes/%d/delete", m.ID), "", nil)
	if w := do(t, h, nil, "GET", m.File, "", nil); w.Code != 404 {
		t.Errorf("deleted file still served: %d", w.Code)
	}
}

func TestNewsletterNeedsLogin(t *testing.T) {
	_, h, _ := testApp(t)
	if w := do(t, h, nil, "GET", "/admin/newsletter", "", nil); w.Code != http.StatusSeeOther {
		t.Errorf("admin page without login: %d", w.Code)
	}
	ct, body := form(url.Values{"month": {"10"}, "year": {"2026"}})
	if w := do(t, h, nil, "POST", "/admin/newsletter", ct, body); w.Code != http.StatusUnauthorized {
		t.Errorf("create without login: %d", w.Code)
	}
}

func TestMonths(t *testing.T) {
	if m, y := nextMonth(12, 2026); m != 1 || y != 2027 {
		t.Errorf("after December 2026: %d %d", m, y)
	}
	newer := Issue{Month: 1, Year: 2027}
	if older := (Issue{Month: 12, Year: 2026}); newer.order() <= older.order() {
		t.Error("January 2027 should sort after December 2026")
	}
	if l := (Issue{Month: 10, Year: 2026}).Label(); l != "October 2026" {
		t.Errorf("label: %q", l)
	}
}

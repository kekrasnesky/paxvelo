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
		ct, body := form(url.Values{"month": {"11"}, "year": {"2026"}})
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

func TestInstagramLinks(t *testing.T) {
	posts, err := parseInstagramPosts("https://www.instagram.com/p/ABC123/?igsh=xyz\n\ninstagram.com/reel/XyZ-9_/\nhttps://instagram.com/paxvelo/p/ABC123/", 6)
	if err == nil {
		t.Fatalf("a link without https:// should be refused, got %v", posts)
	}
	posts, err = parseInstagramPosts("https://www.instagram.com/p/ABC123/?igsh=xyz\nhttps://instagram.com/paxvelo/p/ABC123/\nhttps://www.instagram.com/reel/XyZ-9_/", 6)
	if err != nil || len(posts) != 2 || posts[0] != "https://www.instagram.com/p/ABC123/" || posts[1] != "https://www.instagram.com/reel/XyZ-9_/" {
		t.Errorf("posts = %v, %v", posts, err)
	}
	if _, err := parseInstagramPosts("https://www.instagram.com/paxvelo/", 6); err == nil {
		t.Error("a profile link was accepted as a post")
	}
	if _, err := parseInstagramPosts("https://www.instagram.com/p/a/\nhttps://www.instagram.com/p/b/", 1); err == nil {
		t.Error("too many posts were accepted")
	}
}

func TestInstagramOnHomePage(t *testing.T) {
	_, h, admin := testApp(t)
	ct, body := form(url.Values{"posts": {"https://www.instagram.com/p/ABC123/"}})
	if w := do(t, h, admin, "POST", "/admin/instagram", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=instagram") {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	home := do(t, h, nil, "GET", "/", "", nil).Body.String()
	for _, want := range []string{`data-instgrm-permalink="https://www.instagram.com/p/ABC123/"`, "Follow @paxvelo"} {
		if !strings.Contains(home, want) {
			t.Errorf("home page is missing %s", want)
		}
	}
	// Instagram's script loads from site.js when the posts scroll into view, not with the page.
	if strings.Contains(home, `<script async src="https://www.instagram.com/embed.js">`) {
		t.Error("the page should not load Instagram's script up front")
	}
	if !strings.Contains(home, "instagram.com/embed.js") || !strings.Contains(home, "IntersectionObserver") {
		t.Error("site.js should load Instagram's script when the posts scroll into view")
	}
	ct, body = form(url.Values{"posts": {"not a link"}})
	if w := do(t, h, admin, "POST", "/admin/instagram", ct, body); !strings.Contains(w.Body.String(), "Not saved") {
		t.Errorf("bad link wasn't reported: %d", w.Code)
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

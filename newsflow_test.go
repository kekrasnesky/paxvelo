package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNewsletterFlow(t *testing.T) {
	a, h, admin := testApp(t)

	// Before the first issue: a friendly placeholder, not a 404.
	if w := do(t, h, nil, "GET", "/newsletter", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "on its way") {
		t.Fatalf("empty newsletter: %d %s", w.Code, w.Body.String())
	}

	// Start an issue: it comes with sample blocks.
	ct, body := form(url.Values{"month": {"10"}, "year": {"2026"}})
	if w := do(t, h, admin, "POST", "/admin/newsletter", ct, body); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "/admin/newsletter/1") {
		t.Fatalf("create: %d %s", w.Code, w.Header().Get("Location"))
	}
	is, ok := a.store.Issue(1)
	if !ok || len(is.Blocks) != 7 || is.Blocks[0].Kind != "big" || is.Blocks[4].Kind != "rides" || is.Blocks[6].Kind != "share" {
		t.Fatalf("sample issue: %+v", is.Blocks)
	}
	if is.CreatedBy != "Test Admin" || is.EditedBy != "Test Admin" {
		t.Errorf("author: created by %q, edited by %q", is.CreatedBy, is.EditedBy)
	}
	big, rides := is.Blocks[0].ID, is.Blocks[4].ID

	// Drafts aren't public.
	if w := do(t, h, nil, "GET", "/newsletter/2026-10", "", nil); w.Code != 404 {
		t.Errorf("draft is public: %d", w.Code)
	}

	// Edit it: keep the big story (with a new photo) and the rides, drop the
	// rest, and add a banner. The form lists the blocks in their new order.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fields := [][2]string{
		{"blocks_form", "1"}, {"month", "10"}, {"year", "2026"}, {"tagline", "Stories from the road."},
		{"block", big + ":big"}, {"b." + big + ".heading", "Meet Pat"}, {"b." + big + ".body", "Pat rides **every** Thursday."},
		{"b." + big + ".video_url", "https://youtube.com/watch?v=x"},
		{"block", "beef0001:banner"}, {"b.beef0001.heading", "Design our next kit"}, {"b.beef0001.link_label", "Visit the team store"}, {"b.beef0001.link_url", "{kit}"},
		{"block", rides + ":rides"},
	}
	for _, f := range fields {
		mw.WriteField(f[0], f[1])
	}
	fw, _ := mw.CreateFormFile("b."+big+".photo_file", "pat.png")
	fw.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 64)))
	mw.Close()
	if w := do(t, h, admin, "POST", "/admin/newsletter/1", mw.FormDataContentType(), &buf); w.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	is, _ = a.store.Issue(1)
	if len(is.Blocks) != 3 || is.Blocks[1].Kind != "banner" || !strings.HasPrefix(is.Blocks[0].Photo, "/media/") || is.Blocks[1].LinkURL != "{kit}" {
		t.Fatalf("after save: %+v", is.Blocks)
	}
	if w := do(t, h, nil, "GET", is.Blocks[0].Photo, "", nil); w.Code != 200 {
		t.Errorf("uploaded photo: %d", w.Code)
	}
	photo := is.Blocks[0].Photo

	// What the editor sends from now on.
	edit := func(bigBody string) url.Values {
		return url.Values{"blocks_form": {"1"}, "month": {"10"}, "year": {"2026"},
			"block":                 {big + ":big", "beef0001:banner", rides + ":rides"},
			"b." + big + ".heading": {"Meet Pat"}, "b." + big + ".body": {bigBody}, "b." + big + ".photo_current": {photo},
			"b." + big + ".video_url": {"https://youtube.com/watch?v=x"}, "b." + big + ".caption": {"Pat after the Thursday ride"},
			"cover.photo_current": {"/photos/hero.webp"}, "cover.caption": {"The Tuesday ride at NAS Pax"},
			"b.beef0001.heading": {"Design our next kit"}, "b.beef0001.link_label": {"Visit the team store"}, "b.beef0001.link_url": {"{kit}"}}
	}

	// Publishing a version that can't be shown is refused, and nothing is saved.
	ct, body = form(edit("See {stravva}"))
	if w := do(t, h, admin, "POST", "/admin/newsletter/1?then=publish", ct, body); !strings.Contains(w.Body.String(), "unknown placeholder {stravva}") {
		t.Errorf("broken publish wasn't reported: %d", w.Code)
	}
	if is, _ := a.store.Issue(1); is.IsPublished() || strings.Contains(is.Blocks[0].Body, "stravva") {
		t.Fatalf("broken edit was published or saved: %+v", is.Blocks[0])
	}

	// Save and publish, then the issue is live at both addresses, with the
	// rides from the calendar: the Tuesday ride needs leaders.
	ct, body = form(edit("Pat rides **every** Thursday."))
	if w := do(t, h, admin, "POST", "/admin/newsletter/1?then=publish", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=published") {
		t.Fatalf("save and publish: %d %s", w.Code, w.Header().Get("Location"))
	}
	d := a.today()
	for d.Weekday() != time.Tuesday {
		d = d.AddDate(0, 0, 1)
	}
	tue := seriesKey(1, d.Format("2006-01-02"))
	for _, path := range []string{"/newsletter", "/newsletter/2026-10"} {
		w := do(t, h, nil, "GET", path, "", nil)
		page := w.Body.String()
		for _, want := range []string{
			"October 2026", "MEET PAT", "<strong>every</strong>", "nl-play", photo,
			`href="https://example.com/kit"`, "Visit the team store",
			"Tuesday Night Ride", `href="/calendar/ride/` + tue + `"`, "Lead this ride", `<strong class="nl-needs">needs a leader</strong>`,
			// Laid out like a printed newsletter: the nameplate, captions, and
			// "In this issue" linking down to each story.
			"The</span> PaxVelo Newsletter", "<figcaption>Pat after the Thursday ride</figcaption>",
			"<figcaption>The Tuesday ride at NAS Pax</figcaption>", `href="#s-` + big + `">Meet Pat</a>`, `id="s-` + big + `"`,
			`href="#s-beef0001">Design our next kit</a>`, `href="#s-` + rides + `">Upcoming Rides</a>`,
		} {
			if !strings.Contains(strings.ToUpper(page), strings.ToUpper(want)) {
				t.Errorf("%s is missing %q", path, want)
			}
		}
		if w.Header().Get("ETag") == "" {
			t.Errorf("%s isn't served from the cache", path)
		}
		// Browsers must check back every time, or an issue published after
		// someone looked at the page wouldn't show up for them.
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("%s lets browsers reuse an old copy: %q", path, cc)
		}
	}

	// The rides block follows the calendar: once the ride has its leaders, the page says so.
	a.store.UpdateRide(tue, func(c *CalRide) error {
		c.setLeader("A group", &Leader{Name: "Jo Lead"})
		c.setLeader("B group", &Leader{Name: "Sam Lead"})
		return nil
	})
	if page := do(t, h, nil, "GET", "/newsletter", "", nil).Body.String(); !strings.Contains(page, "Jo Lead") {
		t.Error("the newsletter didn't pick up the calendar change")
	}

	// A broken edit to the live issue isn't saved, so the issue still shows
	// after a restart, and the edit comes back in the form to fix.
	ct, body = form(edit("See {stravva}"))
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
	// The announcement lists the rides still needing leaders (not the one now led).
	a.store.SetDraft(EmailDraft{})
	do(t, h, admin, "POST", "/admin/newsletter/1/email", "", nil)
	if d := a.store.Draft(); !strings.Contains(d.Body, "/newsletter/2026-10") || !strings.Contains(d.Body, "still need a leader") || strings.Contains(d.Body, tue) {
		t.Errorf("announcement draft: %+v", d)
	}

	// Unpublish hides it again.
	do(t, h, admin, "POST", "/admin/newsletter/1/unpublish", "", nil)
	if w := do(t, h, nil, "GET", "/newsletter/2026-10", "", nil); w.Code != 404 {
		t.Errorf("unpublished issue still public: %d", w.Code)
	}

	// The next issue starts with the same blocks and headings, minus the
	// one-off banner and last month's text and photos.
	ct, body = form(url.Values{"month": {"11"}, "year": {"2026"}})
	do(t, h, admin, "POST", "/admin/newsletter", ct, body)
	next, _ := a.store.Issue(2)
	if len(next.Blocks) != 2 || next.Blocks[0].Heading != "Meet Pat" || next.Blocks[0].Body != "" || next.Blocks[0].Photo != "" || next.Blocks[1].Kind != "rides" {
		t.Errorf("next issue's blocks: %+v", next.Blocks)
	}
}

func TestNewsletterPhotoStrip(t *testing.T) {
	a, h, admin := testApp(t)
	ct, body := form(url.Values{"month": {"10"}, "year": {"2026"}})
	do(t, h, admin, "POST", "/admin/newsletter", ct, body)

	// Photos upload on their own, before the issue is saved.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, name := range []string{"a.png", "b.png"} {
		fw, _ := mw.CreateFormFile("photos", name)
		fw.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 64)))
	}
	fw, _ := mw.CreateFormFile("photos", "notes.txt")
	fw.Write([]byte("just text"))
	mw.Close()
	w := do(t, h, admin, "POST", "/admin/newsletter/photos", mw.FormDataContentType(), &buf)
	var up struct {
		URLs  []string `json:"urls"`
		Error string   `json:"error"`
	}
	json.NewDecoder(w.Body).Decode(&up)
	if len(up.URLs) != 2 || !strings.Contains(up.Error, "notes.txt") {
		t.Fatalf("upload: %+v", up)
	}

	// A strip with the two new photos in a new order, then two stories that
	// share a row. Photos that aren't the site's are dropped.
	vals := url.Values{"blocks_form": {"1"}, "month": {"10"}, "year": {"2026"},
		"block":             {"aaaa0001:gallery", "aaaa0002:story", "aaaa0003:story"},
		"b.aaaa0001.photos": {up.URLs[1], "https://evil.example/x.png", up.URLs[0]}, "b.aaaa0001.focus": {"20% 30%", "", ""},
		"b.aaaa0002.heading": {"Swap meet"}, "b.aaaa0003.heading": {"New Rider Corner"}}
	ct, body = form(vals)
	if w := do(t, h, admin, "POST", "/admin/newsletter/1?then=publish", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=published") {
		t.Fatalf("publish: %d %s %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	is, _ := a.store.Issue(1)
	if g := is.Blocks[0]; len(g.Photos) != 2 || g.Photos[0] != up.URLs[1] || g.Focus[up.URLs[1]] != "20% 30%" {
		t.Errorf("photo strip: %v %v", g.Photos, g.Focus)
	}
	page := do(t, h, nil, "GET", "/newsletter", "", nil).Body.String()
	if !strings.Contains(page, `class="nl-toc"`) {
		t.Error("two stories with headings should get an In this issue list")
	}
	if strings.Count(page, `<div class="nl-row">`) != 1 || !strings.Contains(page, "New Rider Corner") || strings.Contains(page, "evil.example") {
		t.Error("the two stories should share one row, and only the site's photos show")
	}
	if strings.Contains(page, "Paceline") {
		t.Error("the page still says Paceline")
	}
}

func TestInThisIssueNeedsTwoStories(t *testing.T) {
	cfg := testConfig()
	site, err := newSite(cfg)
	if err != nil {
		t.Fatal(err)
	}
	one := Issue{Month: 10, Year: 2026, Blocks: []Block{{ID: "aaaa0001", Kind: "big", Story: Story{Heading: "Only story"}}}}
	if v, _ := site.newsView(one, nil, ridesView{}); v.Contents != nil {
		t.Errorf("one story shouldn't get a list: %+v", v.Contents)
	}
	one.Blocks = append(one.Blocks, Block{ID: "aaaa0002", Kind: "story", Story: Story{Heading: "Second"}})
	if v, _ := site.newsView(one, nil, ridesView{}); len(v.Contents) != 2 || v.Contents[1].ID != "s-aaaa0002" {
		t.Errorf("two stories: %+v", v.Contents)
	}
}

func TestOldIssuesBecomeBlocks(t *testing.T) {
	var is Issue
	json.Unmarshal([]byte(`{"id":1,"month":10,"year":2026,"featured":{"heading":"Meet Pat"},"recap":{"heading":"Last month"},
		"rides":[{"date":"2026-10-27","name":"Tuesday Night Ride"}],"spotlight":{"heading":"Chris"},
		"corner":[{"heading":"Swap meet"},{"heading":"Hidden","off":true}],"kit":{"heading":"Kit"},
		"gallery":["/photos/hero.webp"],"hidden":["spotlight"]}`), &is)
	is.upgrade()
	var kinds []string
	for _, b := range is.Blocks {
		kinds = append(kinds, b.Kind+" "+b.Heading)
	}
	if got := strings.Join(kinds, ", "); got != "big Meet Pat, story Last month, rides Upcoming Rides, story Swap meet, banner Kit, gallery " {
		t.Errorf("blocks: %s", got)
	}
	if b, _ := json.Marshal(is); strings.Contains(string(b), `"featured"`) || strings.Contains(string(b), `"corner"`) {
		t.Errorf("old sections are still saved: %s", b)
	}
}

package main

import (
	"html/template"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// The automatic check on GitHub runs these before anything is published, so a
// page edit that breaks the site never goes live.

func testConfig() Config {
	return Config{
		SiteURL: "https://www.paxvelo.club", Location: time.UTC,
		DuesIndividual: 35, DuesFamily: 45,
		StravaURL: "https://www.strava.com/clubs/10197", KitURL: "https://example.com/kit",
	}
}

func TestAllPagesBuild(t *testing.T) {
	s, err := newSite(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for path := range *s.pages.Load() {
		paths[path] = true
	}
	for _, want := range []string{"/", "/rides", "/bike-safety", "/committee", "/kit"} {
		if !paths[want] {
			t.Errorf("page %s is missing", want)
		}
	}
	if len(*s.infoLinks.Load()) == 0 {
		t.Error("the Info menu is empty")
	}
}

func testRenderer() *mdRenderer {
	return &mdRenderer{
		vars:    map[string]string{"strava": "https://strava.example", "donate": ""},
		widgets: map[string]template.HTML{"membership-plans": "<div class=\"plans\"></div>"},
		photos:  fstest.MapFS{"ride.webp": {}},
		version: "v1",
	}
}

func TestRender(t *testing.T) {
	src := `# Our Rides

Intro text with **bold** and *italics*.

[See rides]({strava})

## Tuesday Night Ride

- **Pace:** 20 mph
- **Start:** [The lot](/join)

## Board

- **Jane Smith** President
- ![](photos/ride.webp) **Sam Lee** — Treasurer

1. First
2. Second
   continues here

[Donate]({donate})

![A ride](photos/ride.webp)

{membership-plans}
<!-- a note for editors -->
`
	heading, intro, body, err := testRenderer().render(src, 1, pageStyle{People: true})
	if err != nil {
		t.Fatal(err)
	}
	if heading != "Our Rides" {
		t.Errorf("heading = %q", heading)
	}
	for _, want := range []string{
		`<p class="lead">Intro text with <strong>bold</strong> and <em>italics</em>.</p>`,
		`<a href="https://strava.example" class="btn" target="_blank" rel="noopener">See rides</a>`,
	} {
		if !strings.Contains(string(intro), want) {
			t.Errorf("intro is missing %s\n%s", want, intro)
		}
	}
	for _, want := range []string{
		`<section class="section" id="tuesday-night-ride">`,
		`<div><dt>Pace</dt><dd>20 mph</dd></div>`,
		`<dd><a href="/join">The lot</a></dd>`,
		`<ul class="board">`,
		`<strong>Jane Smith</strong><span>President</span>`,
		`<img class="avatar" src="/photos/ride.webp?v=v1"`,
		`<strong>Sam Lee</strong><span>Treasurer</span>`,
		`<li>Second continues here</li>`,
		`<img src="/photos/ride.webp?v=v1" alt="A ride"`,
		`<div class="plans"></div>`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body is missing %s\n%s", want, body)
		}
	}
	for _, bad := range []string{"Donate", "a note for editors"} {
		if strings.Contains(string(body), bad) {
			t.Errorf("body should not contain %q", bad)
		}
	}
}

func TestRenderEscapesHTML(t *testing.T) {
	_, intro, _, err := testRenderer().render("# Hi\n\n<script>alert(1)</script>", 1, pageStyle{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(intro), "<script>") {
		t.Errorf("HTML was not escaped: %s", intro)
	}
}

func TestRenderErrors(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"# A\n\n![x](photos/missing.jpg)", "line 3: can't find the photo photos/missing.jpg"},
		{"# A\n\nSee {stravva}", "line 3: unknown placeholder {stravva}"},
		{"# A\n\n[x](javascript:alert(1))", "links must start with"},
		{"# A\n# B", "line 2: a page can only have one title line"},
		{"# A\n\n[Strava](www.strava.com/clubs/1)", "web addresses need to start with https://"},
	} {
		_, _, _, err := testRenderer().render(tc.src, 1, pageStyle{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("render(%q) error = %v, want %q", tc.src, err, tc.want)
		}
	}
}

func TestRenderPhotoWithLink(t *testing.T) {
	_, intro, _, err := testRenderer().render("# A\n\n![A ride](photos/ride.webp) [Read more](rides.md)", 1, pageStyle{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<img src="/photos/ride.webp?v=v1" alt="A ride"`, `<a href="/rides">Read more</a>`} {
		if !strings.Contains(string(intro), want) {
			t.Errorf("missing %s in %s", want, intro)
		}
	}
}

func TestRenderPhotoRow(t *testing.T) {
	_, intro, _, err := testRenderer().render("# A\n\n![One](photos/ride.webp) ![Two](photos/ride.webp)", 1, pageStyle{})
	if err != nil {
		t.Fatal(err)
	}
	want := `<div class="photos"><img src="/photos/ride.webp?v=v1" alt="One"`
	if !strings.Contains(string(intro), want) || !strings.Contains(string(intro), `alt="Two"`) {
		t.Errorf("want a row of two photos, got %s", intro)
	}
}

func TestPeopleCardsNeedTheSetting(t *testing.T) {
	src := "# A\n\n- **Water** two full bottles\n- **Spare tube** and a pump"
	_, intro, _, err := testRenderer().render(src, 1, pageStyle{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(intro), "board") || !strings.Contains(string(intro), "<li><strong>Water</strong> two full bottles</li>") {
		t.Errorf("an ordinary list became people cards: %s", intro)
	}
}

func TestPageSettings(t *testing.T) {
	r := testRenderer()
	p, err := parsePage("rides.md", "---\ntitle: Rides\nmenu: Our Rides\norder: 2\n---\n# Our Rides\n", r)
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != "/rides" || p.Title != "Rides — PaxVelo" || p.Menu != "Our Rides" || p.Order != 2 {
		t.Errorf("got %+v", p)
	}
	for _, tc := range []struct{ file, src, want string }{
		{"Rides.md", "# A", "file names can only use lowercase"},
		{"a.md", "---\ntitel: A\n---\n# A", `unknown setting "titel"`},
		{"a.md", "---\ntitle: A\n# A", "closing --- line"},
		{"a.md", "Just text", "needs a title line"},
	} {
		if _, err := parsePage(tc.file, tc.src, r); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parsePage(%q) error = %v, want %q", tc.file, err, tc.want)
		}
	}
}

// A page edited in the admin that stops building later (a change on GitHub
// removed a photo it uses) must not stop the site from starting: the page falls
// back to its GitHub version, and one added in the admin is left out.
func TestBrokenAdminEditDoesNotStopTheSite(t *testing.T) {
	a, h, _ := testApp(t)
	broken := "# Edited Rides\n\n![gone](photos/not-there.jpg)\n"
	now := time.Now()
	a.store.SetPageEdit("rides.md", &PageEdit{Text: broken, EditedBy: "Pat", Edited: now})
	a.store.SetPageEdit("extra.md", &PageEdit{Text: broken, EditedBy: "Pat", Edited: now})
	if err := a.rebuildContent(); err != nil {
		t.Fatalf("a broken admin edit stopped the rebuild: %v", err)
	}
	if body := do(t, h, nil, "GET", "/rides", "", nil).Body.String(); strings.Contains(body, "Edited Rides") || !strings.Contains(body, "<h1>") {
		t.Error("/rides should show the GitHub version")
	}
	if w := do(t, h, nil, "GET", "/extra", "", nil); w.Code != 404 {
		t.Errorf("a broken page added in the admin should be left out: %d", w.Code)
	}
}

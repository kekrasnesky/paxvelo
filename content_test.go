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
	for _, p := range s.pages {
		paths[p.Path] = true
	}
	for _, want := range []string{"/", "/rides", "/bike-safety", "/committee"} {
		if !paths[want] {
			t.Errorf("page %s is missing", want)
		}
	}
	if len(s.infoLinks) == 0 {
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
	heading, intro, body, err := testRenderer().render(src, 1, false)
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
	_, intro, _, err := testRenderer().render("# Hi\n\n<script>alert(1)</script>", 1, false)
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
	} {
		_, _, _, err := testRenderer().render(tc.src, 1, false)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("render(%q) error = %v, want %q", tc.src, err, tc.want)
		}
	}
}

func TestPageSettings(t *testing.T) {
	r := testRenderer()
	p, err := parsePage("rides.md", "---\ntitle: Rides\nmenu: Our Rides\norder: 2\n---\n# Our Rides\n", r)
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != "/rides" || p.Title != "Rides — PAXVELO" || p.Menu != "Our Rides" || p.Order != 2 {
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

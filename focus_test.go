package main

import (
	"net/url"
	"strings"
	"testing"
)

// A framing is a focus point and an optional zoom; anything else, and the
// default (the middle at full size), is stored as empty.
func TestCleanFocus(t *testing.T) {
	for in, want := range map[string]string{
		"40% 30%":        "40% 30%",
		"40% 30% 1.5":    "40% 30% 1.5",
		"40% 30% 1":      "40% 30%",
		"50% 50%":        "",
		"50% 50% 1.0":    "",
		"50% 50% 2":      "50% 50% 2",
		"40% 30% 5":      "",
		"40% 30% 0.5":    "",
		"101% 30%":       "",
		"40%; color:red": "",
		"":               "",
	} {
		if got := cleanFocus(in); got != want {
			t.Errorf("cleanFocus(%q) = %q, want %q", in, got, want)
		}
	}
}

// Zoomed in, a photo scales around its focus point and is clipped back to
// its box, keeping its corners.
func TestFocusStyle(t *testing.T) {
	if got := string(focusStyle("40% 30%")); got != "object-position:40% 30%" {
		t.Errorf("unzoomed style = %q", got)
	}
	got := string(focusStyle("40% 30% 2"))
	for _, want := range []string{"object-position:40% 30%", "transform:scale(2)", "transform-origin:40% 30%",
		"clip-path:inset(15.000% 30.000% 35.000% 20.000% round calc(var(--r,0px) / 2))"} {
		if !strings.Contains(got, want) {
			t.Errorf("zoomed style %q is missing %q", got, want)
		}
	}
	if focusStyle("nonsense") != "" {
		t.Error("a bad framing should give no style")
	}
}

// A page photo's framing rides on its address, and the page draws it.
func TestPagePhotoFraming(t *testing.T) {
	if src, f := splitPhotoFocus("photos/ride.webp#focus=40,30,1.5"); src != "photos/ride.webp" || f != "40% 30% 1.5" {
		t.Errorf("split = %q, %q", src, f)
	}
	if src, f := splitPhotoFocus("photos/ride.webp"); src != "photos/ride.webp" || f != "" {
		t.Errorf("split without framing = %q, %q", src, f)
	}
	a, _, _ := testApp(t)
	html, err := a.site.previewPage("framing-test.md", "# Test\n\n![Riders](photos/ride.webp#focus=40,30,2)\n", a.contentData())
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	if !strings.Contains(page, `src="/photos/ride.webp`) || !strings.Contains(page, "transform:scale(2)") || strings.Contains(page, "#focus") {
		t.Errorf("the page photo should be drawn with its framing:\n%s", page[strings.Index(page, "<main"):])
	}
}

// Gallery photos keep a framing, set on the album page, and the public album
// draws it.
func TestGalleryPhotoFraming(t *testing.T) {
	a, h, c := testApp(t)
	al, err := a.store.AddAlbum("Spring Century", "2026-04-12")
	id := al.ID
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.AddGallery(GalleryPhoto{Album: id, Src: "/media/a.jpg", By: "Pat"}); err != nil {
		t.Fatal(err)
	}
	p := a.store.AlbumPhotos(id)[0]
	ct, body := form(url.Values{"do": {"save"}, "focus-" + itoa(p.ID): {"20% 70% 1.25"}})
	do(t, h, c, "POST", "/admin/gallery/"+itoa(id), ct, body)
	if got, _ := a.store.GalleryPhoto(p.ID); got.Focus != "20% 70% 1.25" {
		t.Fatalf("focus = %q", got.Focus)
	}
	al, _ = a.store.Album(id)
	page := do(t, h, nil, "GET", al.Href(), "", nil).Body.String()
	if !strings.Contains(page, "object-position:20% 70%;transform:scale(1.25)") {
		t.Error("the album page should draw the photo's framing")
	}
	admin := do(t, h, c, "GET", "/admin/gallery/"+itoa(id), "", nil).Body.String()
	if !strings.Contains(admin, `name="focus-`+itoa(p.ID)+`" value="20% 70% 1.25"`) || !strings.Contains(admin, "Adjust framing") {
		t.Error("the admin album page should offer the framing tool")
	}
}

// A zoomed photo in a swiping row of photos goes in a clipping wrapper, so
// its scaled box doesn't make the row (or the page) scroll past its end.
func TestZoomedPhotosInRows(t *testing.T) {
	row := framedInRow(`<img src="/a.jpg" style="object-position:40% 30%"><img src="/b.jpg" style="object-position:40% 30%;transform:scale(1.5);transform-origin:40% 30%">`)
	if strings.Count(row, `<span class="framed">`) != 1 || !strings.Contains(row, `<span class="framed"><img src="/b.jpg"`) {
		t.Errorf("only the zoomed photo should be wrapped: %s", row)
	}
	home := string(homePhotosWidget([]HomePhoto{{Photo: "/a.jpg", Focus: "40% 30%"}, {Photo: "/b.jpg", Focus: "40% 30% 2"}}))
	if strings.Count(home, `<span class="framed">`) != 1 || !strings.Contains(home, `<span class="framed"><img src="/b.jpg"`) {
		t.Errorf("home photo row: %s", home)
	}
}

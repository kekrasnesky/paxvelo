package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// One album can be featured: it shows first, and big, on the gallery page.
// A hidden album is off the website but still there for admins and the
// newsletter. Both work in place on the album list, with Undo.
func TestAlbumFeatureAndHide(t *testing.T) {
	a, h, admin := testApp(t)
	var ids []int
	for _, title := range []string{"Gamma", "Beta", "Alpha"} {
		al, _ := a.store.AddAlbum(title, "")
		a.store.AddGallery(GalleryPhoto{Album: al.ID, Src: "/media/" + title + ".jpg", By: "Pat"})
		ids = append([]int{al.ID}, ids...)
	}
	alpha, beta, gamma := ids[2], ids[1], ids[0]
	n := strconv.Itoa
	// What the album list answers with JavaScript.
	send := func(v url.Values) (string, string) {
		ct, body := form(v)
		req := httptest.NewRequest("POST", "/admin/gallery", body)
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Accept", "application/json")
		req.AddCookie(admin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var j struct {
			OK   bool   `json:"ok"`
			HTML string `json:"html"`
		}
		json.NewDecoder(w.Body).Decode(&j)
		if !j.OK {
			t.Fatalf("%v: %s", v, w.Body.String())
		}
		return j.HTML, j.HTML[strings.Index(j.HTML, "<ul"):]
	}

	// Featuring Beta puts it first, and big.
	html, _ := send(url.Values{"do": {"feature:" + n(beta)}})
	if !strings.Contains(html, "“Beta” is the featured album now") || !strings.Contains(html, `name="undo" value="unfeature:`+n(beta)+`"`) {
		t.Errorf("feature note: %s", html)
	}
	page := do(t, h, nil, "GET", "/gallery", "", nil).Body.String()
	if i, j := strings.Index(page, "album-featured"), strings.Index(page, "Alpha"); i < 0 || !strings.Contains(page[i:j], "Beta") || strings.Count(page, ">Beta<") != 1 {
		t.Error("the featured album should show first, once")
	}

	// Featuring another moves it; Undo puts Beta back.
	_, list := send(url.Values{"do": {"feature:" + n(gamma)}})
	if al, _ := a.store.Album(beta); al.Featured {
		t.Error("only one album is featured")
	}
	if !strings.Contains(list, `value="unfeature:`+n(gamma)+`"`) {
		t.Error("the list should show Gamma as featured")
	}
	html, _ = send(url.Values{"undo": {"unfeature:" + n(gamma) + " feature:" + n(beta)}})
	if al, _ := a.store.Album(beta); !al.Featured || !strings.Contains(html, "Undone.") {
		t.Errorf("undo should feature Beta again: %+v", al)
	}

	// Hiding: off the gallery, its pages and the share page, but not the
	// newsletter's picker. A hidden album isn't featured.
	html, _ = send(url.Values{"do": {"hide:" + n(beta)}})
	if !strings.Contains(html, "Hidden from the website") || !strings.Contains(html, `value="feature:`+n(beta)+`"`) {
		t.Errorf("hidden: %s", html)
	}
	betaAl, _ := a.store.Album(beta)
	if betaAl.Featured {
		t.Error("hiding stops featuring")
	}
	if page := do(t, h, nil, "GET", "/gallery", "", nil).Body.String(); strings.Contains(page, "Beta") {
		t.Error("a hidden album isn't on the gallery page")
	}
	if w := do(t, h, nil, "GET", betaAl.Href(), "", nil); w.Code != 404 {
		t.Errorf("a hidden album's page: %d", w.Code)
	}
	photo := a.store.AlbumPhotos(beta)[0]
	if w := do(t, h, nil, "GET", betaAl.Href()+"/"+n(photo.ID), "", nil); w.Code != 404 {
		t.Errorf("a hidden album's photo page: %d", w.Code)
	}
	if page := do(t, h, nil, "GET", "/share-photos", "", nil).Body.String(); strings.Contains(page, ">Beta<") {
		t.Error("members can't pick a hidden album to share into")
	}
	if strings.Contains(do(t, h, nil, "GET", "/sitemap.xml", "", nil).Body.String(), betaAl.Href()) {
		t.Error("a hidden album isn't in the sitemap")
	}
	if pick := do(t, h, admin, "GET", "/admin/gallery/pick", "", nil).Body.String(); !strings.Contains(pick, `"Beta"`) {
		t.Error("the newsletter can still use a hidden album's photos")
	}
	// Featuring a hidden album shows it again.
	send(url.Values{"do": {"feature:" + n(beta)}})
	if al, _ := a.store.Album(beta); al.Hidden || !al.Featured {
		t.Errorf("featuring shows it: %+v", al)
	}

	// Dragging albums into order.
	send(url.Values{"order": {n(alpha) + "," + n(gamma) + "," + n(beta)}})
	if al := a.store.Albums(); al[0].ID != alpha || al[1].ID != gamma || al[2].ID != beta {
		t.Errorf("dragged order %+v", al)
	}

	// Without JavaScript, from the album's own page.
	here := albumAdminHref(gamma)
	ct, body := form(url.Values{"do": {"hide"}})
	if to := do(t, h, admin, "POST", here, ct, body).Header().Get("Location"); to != here+"?ok=album-hide" {
		t.Errorf("hide from the album page: %s", to)
	}
	if page := do(t, h, admin, "GET", here, "", nil).Body.String(); !strings.Contains(page, "Hidden from the website.") || !strings.Contains(page, `value="show"`) || strings.Contains(page, "View on the website") {
		t.Error("the album page should say it's hidden, with Show")
	}
	ct, body = form(url.Values{"do": {"show:" + n(gamma)}})
	if to := do(t, h, admin, "POST", "/admin/gallery", ct, body).Header().Get("Location"); to != "/admin/gallery?ok=album-show" {
		t.Errorf("show from the list: %s", to)
	}
}

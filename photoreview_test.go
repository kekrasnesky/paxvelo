package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// reviewJSON presses a button on a card on Photos to review the way the
// page does, in the background, and returns the answer.
func reviewJSON(t *testing.T, h http.Handler, c *http.Cookie, v url.Values) (ok bool, html, message string) {
	t.Helper()
	ct, body := form(v)
	r := httptest.NewRequest("POST", "/admin/submitted", body)
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Accept", "application/json")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var j struct {
		OK            bool
		HTML, Message string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil {
		t.Fatalf("answer %d %s", w.Code, w.Body)
	}
	return j.OK, j.HTML, j.Message
}

// Each person's photos are one card, all ticked, with the album filled in
// when it can be. Approving and rejecting happen in place and can be undone.
func TestReviewPhotosInPlace(t *testing.T) {
	a, h, admin := testApp(t)
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	al, _ := a.store.AddAlbum("Club ride", yesterday)
	a.store.AddAlbum("Last spring", "2026-04-01")
	photo := encodeJPEG(t, halves(600, 400, 255))
	if w := sendPhotos(t, h, a, url.Values{"name": {"Pat Rider"}, "agree": {"yes"}},
		map[string][]byte{"a.jpg": photo, "b.jpg": photo, "c.jpg": photo}); w.Code != http.StatusOK {
		t.Fatalf("sending: %d %s", w.Code, w.Body)
	}
	sent := a.store.SubmittedPhotos()
	batch := strconv.Itoa(sent[0].Batch)
	page := do(t, h, admin, "GET", "/admin/submitted", "", nil).Body.String()
	for _, want := range []string{`<option value="` + strconv.Itoa(al.ID) + `" selected>Club ride</option>`, "Picked from the ride’s date",
		`into <span class="sent-to">Club ride</span>`, `class="sent-view"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the card should show %s", want)
		}
	}

	// Approve two, with a caption and the credit fixed on the card.
	id := func(i int) string { return strconv.Itoa(sent[i].ID) }
	ok, html, msg := reviewJSON(t, h, admin, url.Values{"do": {"approve"}, "batch": {batch}, "album-" + batch: {strconv.Itoa(al.ID)},
		"by-" + batch: {"Pat R."}, "caption-" + id(0): {"Up the hill"}, "id": {id(0), id(1)}})
	if !ok {
		t.Fatalf("approving: %s", msg)
	}
	gal := a.store.AlbumPhotos(al.ID)
	if len(gal) != 2 || gal[0].Caption != "Up the hill" || gal[0].By != "Pat R." {
		t.Fatalf("gallery %+v", gal)
	}
	for _, want := range []string{"Added 2 photos to “Club ride”.", `value="unapprove:` + id(0) + `,` + id(1) + `"`,
		`href="/admin/gallery/` + strconv.Itoa(al.ID) + `?tick=` + strconv.Itoa(gal[0].ID) + `,` + strconv.Itoa(gal[1].ID) + `#to-newsletter"`,
		`name="id" value="` + id(2) + `" checked`, "In the gallery"} {
		if !strings.Contains(html, want) {
			t.Errorf("the card after approving should show %s", want)
		}
	}
	if a.store.WaitingPhotos() != 1 {
		t.Errorf("one photo should still wait: %d", a.store.WaitingPhotos())
	}

	// Undo takes them back out of the gallery, files and all.
	if ok, _, msg := reviewJSON(t, h, admin, url.Values{"batch": {batch}, "undo": {"unapprove:" + id(0) + "," + id(1)}}); !ok {
		t.Fatalf("undoing: %s", msg)
	}
	if n := len(a.store.AlbumPhotos(al.ID)); n != 0 {
		t.Errorf("undo should empty the album: %d", n)
	}
	for _, f := range gal[0].files() {
		if mediaFile(a, f) {
			t.Errorf("%s should be gone", f)
		}
	}
	if a.store.WaitingPhotos() != 3 {
		t.Errorf("all three should wait again: %d", a.store.WaitingPhotos())
	}

	// Rejecting hides a photo, and can be undone.
	ok, html, _ = reviewJSON(t, h, admin, url.Values{"do": {"reject"}, "batch": {batch}, "id": {id(2)}})
	if !ok || !strings.Contains(html, "Rejected 1 photo. It’s deleted in 30 days.") || !strings.Contains(html, `value="restore:`+id(2)+`"`) {
		t.Fatalf("rejecting: %s", html)
	}
	if a.store.WaitingPhotos() != 2 {
		t.Errorf("a rejected photo doesn't wait: %d", a.store.WaitingPhotos())
	}
	if ok, _, _ := reviewJSON(t, h, admin, url.Values{"batch": {batch}, "undo": {"restore:" + id(2)}}); !ok || a.store.WaitingPhotos() != 3 {
		t.Error("putting it back should make it wait again")
	}

	// Without JavaScript the buttons go back to the page.
	ct, body := form(url.Values{"do": {"reject"}, "batch": {batch}, "id": {id(2)}})
	if to := do(t, h, admin, "POST", "/admin/submitted", ct, body).Header().Get("Location"); !strings.Contains(to, "ok=submitted-rejected") {
		t.Errorf("rejecting without JavaScript: %s", to)
	}
	// After 30 days a rejected photo is deleted when the page is opened.
	p, _ := a.store.Submitted(sent[2].ID)
	a.store.mu.Lock()
	for _, sp := range a.store.d.SubmittedPhotos {
		if sp.ID == p.ID {
			sp.Rejected = time.Now().Add(-rejectKeep - time.Hour)
		}
	}
	a.store.mu.Unlock()
	do(t, h, admin, "GET", "/admin/submitted", "", nil)
	if _, ok := a.store.Submitted(p.ID); ok {
		t.Error("an old rejected photo should be deleted")
	}
	if _, err := os.Stat(filepath.Join(a.submittedDir(), p.File)); !os.IsNotExist(err) {
		t.Error("its file should be deleted too")
	}

	// Approving into a new album needs its name.
	if ok, _, msg := reviewJSON(t, h, admin, url.Values{"do": {"approve"}, "batch": {batch}, "album-" + batch: {"new"}, "id": {id(0)}}); ok || !strings.Contains(msg, "type a name") {
		t.Errorf("a new album with no name: %v %s", ok, msg)
	}
}

func TestSuggestAlbum(t *testing.T) {
	sent := time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	albums := []Album{{ID: 1, Date: "2026-10-20"}, {ID: 2, Date: "2026-10-30"}, {ID: 3, Date: "2026-10-28"}, {ID: 4}}
	if got := suggestAlbum(albums, sent); got != 2 {
		t.Errorf("the nearest ride before: %d", got)
	}
	if got := suggestAlbum([]Album{{ID: 1, Date: "2026-10-01"}, {ID: 2, Date: "2026-11-05"}}, sent); got != 0 {
		t.Errorf("nothing close: %d", got)
	}
}

// The gallery is where the club's photos live, and the newsletter picks from
// it: from an album's page, or from the issue's editor. The newsletter keeps
// its own copy.
func TestGalleryToNewsletter(t *testing.T) {
	a, h, admin := testApp(t)
	al, _ := a.store.AddAlbum("Halloween ride", "2026-10-31")
	photo := encodeJPEG(t, halves(600, 400, 255))
	bct, bbody := bulkForm(url.Values{"by": {"Sam Snap"}}, photo, 2)
	r := httptest.NewRequest("POST", "/admin/gallery/upload?album="+strconv.Itoa(al.ID), bbody)
	r.Header.Set("Content-Type", bct)
	r.AddCookie(admin)
	h.ServeHTTP(httptest.NewRecorder(), r)
	gal := a.store.AlbumPhotos(al.ID)
	if len(gal) != 2 {
		t.Fatalf("gallery %+v", gal)
	}
	a.store.EditPhotos(map[int]string{gal[0].ID: "Costumes at the start"}, nil, "")
	is, _ := a.store.SaveIssue(Issue{Month: 11, Year: 2026, Blocks: []Block{{ID: "aaaa", Kind: "story"}}})

	// The album's page shows the photos just approved ticked, and can add
	// ticked photos to an issue's photo strip.
	page := do(t, h, admin, "GET", albumAdminHref(al.ID)+"?tick="+strconv.Itoa(gal[1].ID), "", nil).Body.String()
	for _, want := range []string{`name="id" value="` + strconv.Itoa(gal[1].ID) + `" checked`, `id="to-newsletter"`, `value="newsletter"`, "November 2026 (draft)"} {
		if !strings.Contains(page, want) {
			t.Errorf("the album page should show %s", want)
		}
	}
	if strings.Contains(page, `name="id" value="`+strconv.Itoa(gal[0].ID)+`" checked`) {
		t.Error("only the photos asked for are ticked")
	}
	ct, body := form(url.Values{"do": {"newsletter"}, "issue": {strconv.Itoa(is.ID)}, "id": {strconv.Itoa(gal[0].ID), strconv.Itoa(gal[1].ID)}})
	if to := do(t, h, admin, "POST", albumAdminHref(al.ID), ct, body).Header().Get("Location"); !strings.Contains(to, "ok=newsletter-added") || !strings.Contains(to, "n=2") {
		t.Fatalf("adding to the newsletter: %s", to)
	}
	is, _ = a.store.Issue(is.ID)
	if len(is.Blocks) != 3 || is.Blocks[1].Kind != "gallery" || is.Blocks[2].Kind != "share" {
		t.Fatalf("a strip, with the share box under it, should be added: %+v", is.Blocks)
	}
	strip := is.Blocks[1].Photos
	if len(strip) != 2 || strip[0] == gal[0].Src || !mediaFile(a, strip[0]) {
		t.Fatalf("the newsletter should get its own copies: %v", strip)
	}
	// Taking a photo out of the gallery leaves the newsletter's copy.
	ct, body = form(url.Values{"do": {"remove"}, "id": {strconv.Itoa(gal[0].ID)}})
	do(t, h, admin, "POST", albumAdminHref(al.ID), ct, body)
	if !mediaFile(a, strip[0]) || mediaFile(a, gal[0].Src) {
		t.Error("removing from the gallery shouldn't touch the newsletter's copy")
	}

	// The editor's "Choose from the gallery" box.
	if w := do(t, h, nil, "GET", "/admin/gallery/pick", "", nil); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "albums") {
		t.Error("only admins can browse the gallery box")
	}
	var albums struct {
		Albums []struct {
			ID           int
			Title, Label string
		}
	}
	json.Unmarshal(do(t, h, admin, "GET", "/admin/gallery/pick", "", nil).Body.Bytes(), &albums)
	if len(albums.Albums) != 1 || albums.Albums[0].Title != "Halloween ride" || albums.Albums[0].Label != "Oct 31, 2026 · 1 photo" {
		t.Errorf("albums %+v", albums)
	}
	var photos struct {
		Photos []struct {
			ID        int
			Thumb, By string
		}
	}
	json.Unmarshal(do(t, h, admin, "GET", "/admin/gallery/pick?album="+strconv.Itoa(al.ID), "", nil).Body.Bytes(), &photos)
	if len(photos.Photos) != 1 || photos.Photos[0].ID != gal[1].ID || photos.Photos[0].By != "Sam Snap" {
		t.Fatalf("photos %+v", photos)
	}
	ct, body = form(url.Values{"id": {strconv.Itoa(gal[1].ID)}})
	var picked struct{ URLs, Captions []string }
	json.Unmarshal(do(t, h, admin, "POST", "/admin/newsletter/from-gallery", ct, body).Body.Bytes(), &picked)
	if len(picked.URLs) != 1 || picked.URLs[0] == gal[1].Src || !mediaFile(a, picked.URLs[0]) || picked.Captions[0] != "Photo by Sam Snap" {
		t.Errorf("picked %+v", picked)
	}
	if !sitePhoto(picked.URLs[0]) {
		t.Error("an issue should accept the picked photo")
	}
	// The board page uses the same box: the picked photo is kept on Save.
	if page := do(t, h, admin, "GET", "/admin/board", "", nil).Body.String(); !strings.Contains(page, `class="ghost gal-choose"`) {
		t.Error("the board page should offer Choose from the gallery")
	}
	ct, body = form(url.Values{"count": {"1"}, "name0": {"Jane Smith"}, "role0": {"President"}, "order0": {"1"}, "photo_current0": {picked.URLs[0]}})
	do(t, h, admin, "POST", "/admin/board", ct, body)
	if b := a.store.Board(); len(b) != 1 || b[0].Photo != picked.URLs[0] {
		t.Errorf("board %+v", b)
	}
	if got := newsCaption(GalleryPhoto{Caption: "Costumes", By: "Sam"}); got != "Costumes · Photo by Sam" {
		t.Errorf("caption %q", got)
	}
}

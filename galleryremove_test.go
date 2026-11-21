package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// removeJSON presses a photo's remove button (or Undo) the way the album
// page does, in the background.
func removeJSON(t *testing.T, h http.Handler, admin *http.Cookie, album int, do string) (ok bool, n int) {
	t.Helper()
	ct, body := form(url.Values{"do": {do}})
	r := httptest.NewRequest("POST", albumAdminHref(album), body)
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Accept", "application/json")
	r.AddCookie(admin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var j struct {
		OK bool
		N  int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil {
		t.Fatalf("answer %d %s", w.Code, w.Body)
	}
	return j.OK, j.N
}

func removedFile(a *App, src string) bool {
	_, err := os.Stat(filepath.Join(a.removedDir(), strings.TrimPrefix(src, "/media/")))
	return err == nil
}

// Each photo on an album's admin page has its own remove button. A removed
// photo leaves the website at once, and Undo puts it back in its place,
// files and all; after 30 days it's deleted.
func TestRemoveGalleryPhoto(t *testing.T) {
	a, h, admin := testApp(t)
	al, _ := a.store.AddAlbum("Coffee ride", "")
	photo := encodeJPEG(t, halves(800, 600, 255))
	var ids []int
	for i := 0; i < 3; i++ {
		name := "p" + strconv.Itoa(i) + ".jpg"
		os.MkdirAll(a.mediaDir(), 0o755)
		os.WriteFile(filepath.Join(a.mediaDir(), name), photo, 0o644)
		if err := a.addGalleryFile(GalleryPhoto{Album: al.ID, Src: "/media/" + name, By: "Sam"}); err != nil {
			t.Fatal(err)
		}
	}
	ids = albumIDs(a, al.ID)
	mid, _ := a.store.GalleryPhoto(ids[1])

	page := do(t, h, admin, "GET", albumAdminHref(al.ID), "", nil).Body.String()
	if !strings.Contains(page, `value="remove:`+strconv.Itoa(mid.ID)+`"`) {
		t.Fatal("each photo should have its own remove button")
	}

	if ok, n := removeJSON(t, h, admin, al.ID, "remove:"+strconv.Itoa(mid.ID)); !ok || n != 1 {
		t.Fatalf("remove: %v %d", ok, n)
	}
	if got := albumIDs(a, al.ID); !slices.Equal(got, []int{ids[0], ids[2]}) {
		t.Errorf("album after removing: %v", got)
	}
	for _, f := range mid.files() {
		if mediaFile(a, f) {
			t.Errorf("%s should be off the website", f)
		}
	}
	if !removedFile(a, mid.Src) {
		t.Error("the removed photo should be kept for Undo")
	}
	if pub := do(t, h, nil, "GET", al.Href(), "", nil).Body.String(); strings.Contains(pub, mid.Src) || strings.Contains(pub, mid.Thumb()) {
		t.Error("the public album still shows the removed photo")
	}

	if ok, n := removeJSON(t, h, admin, al.ID, "restore:"+strconv.Itoa(mid.ID)); !ok || n != 1 {
		t.Fatalf("undo: %v %d", ok, n)
	}
	if got := albumIDs(a, al.ID); !slices.Equal(got, ids) {
		t.Errorf("Undo should put it back in its place: %v, want %v", got, ids)
	}
	if !mediaFile(a, mid.Src) || removedFile(a, mid.Src) {
		t.Error("Undo should put the files back")
	}
	if ok, _ := removeJSON(t, h, admin, al.ID, "restore:"+strconv.Itoa(mid.ID)); ok {
		t.Error("undoing twice should say it's already done")
	}

	// Removing two together and undoing keeps the order.
	removeJSON(t, h, admin, al.ID, "remove:"+strconv.Itoa(ids[0])+","+strconv.Itoa(ids[2]))
	removeJSON(t, h, admin, al.ID, "restore:"+strconv.Itoa(ids[2])+","+strconv.Itoa(ids[0]))
	if got := albumIDs(a, al.ID); !slices.Equal(got, ids) {
		t.Errorf("order after undoing two: %v, want %v", got, ids)
	}

	// Without JavaScript: the ticked photos, then the page offers Undo.
	ct, body := form(url.Values{"do": {"remove"}, "id": {strconv.Itoa(ids[0])}})
	to := do(t, h, admin, "POST", albumAdminHref(al.ID), ct, body).Header().Get("Location")
	if !strings.Contains(to, "removed="+strconv.Itoa(ids[0])) {
		t.Fatalf("plain remove went to %s", to)
	}
	if page := do(t, h, admin, "GET", to, "", nil).Body.String(); !strings.Contains(page, `value="restore:`+strconv.Itoa(ids[0])+`"`) || !strings.Contains(page, "Removed 1 photo.") {
		t.Error("after removing, the page should offer Undo")
	}

	// 30 days on, it's deleted for good.
	a.store.mu.Lock()
	for _, r := range a.store.d.RemovedGallery {
		r.Removed = time.Now().Add(-rejectKeep - time.Hour)
	}
	a.store.mu.Unlock()
	gone := a.store.d.RemovedGallery[0].GalleryPhoto
	do(t, h, admin, "GET", "/admin/gallery", "", nil)
	if removedFile(a, gone.Src) || len(a.store.d.RemovedGallery) != 0 {
		t.Error("photos removed 30 days ago should be deleted when the gallery opens")
	}
	if ok, _ := removeJSON(t, h, admin, al.ID, "restore:"+strconv.Itoa(gone.ID)); ok {
		t.Error("a deleted photo can't come back")
	}
}

// Removing a photo a member shared marks it rejected on Photos to review, so
// it doesn't come back as waiting; Undo puts it back in the gallery.
func TestRemoveSharedPhoto(t *testing.T) {
	a, h, admin := testApp(t)
	al, _ := a.store.AddAlbum("Century", "")
	photo := encodeJPEG(t, halves(400, 300, 255))
	sendPhotos(t, h, a, url.Values{"name": {"Pat"}, "agree": {"yes"}, "album": {strconv.Itoa(al.ID)}}, map[string][]byte{"a.jpg": photo})
	sent := a.store.SubmittedPhotos()[0]
	if ok, _, msg := reviewJSON(t, h, admin, url.Values{"do": {"approve"}, "batch": {strconv.Itoa(sent.Batch)}, "id": {strconv.Itoa(sent.ID)}, "album-" + strconv.Itoa(sent.Batch): {strconv.Itoa(al.ID)}}); !ok {
		t.Fatal(msg)
	}
	gid := albumIDs(a, al.ID)[0]
	removeJSON(t, h, admin, al.ID, "remove:"+strconv.Itoa(gid))
	if p, _ := a.store.Submitted(sent.ID); p.Status() != "Rejected" || a.store.WaitingPhotos() != 0 {
		t.Errorf("removed shared photo: %q, %d waiting", p.Status(), a.store.WaitingPhotos())
	}
	removeJSON(t, h, admin, al.ID, "restore:"+strconv.Itoa(gid))
	if p, _ := a.store.Submitted(sent.ID); p.Gallery != gid || p.Status() != "In the gallery" {
		t.Errorf("after Undo: %+v", p)
	}
}

// Every album on the public gallery has its own share link, and the share
// page it opens sends the photos to that album, which the admin sees and
// keeps when approving them.
func TestShareToAlbum(t *testing.T) {
	a, h, admin := testApp(t)
	old, _ := a.store.AddAlbum("Old ride", "2020-05-01")
	for i := 0; i < shareAlbums+2; i++ { // so "Old ride" isn't in the latest list
		a.store.AddAlbum("Ride "+strconv.Itoa(i), "")
	}
	hidden, _ := a.store.AddAlbum("Board only", "")
	a.store.HideAlbum(hidden.ID, true)
	photo := encodeJPEG(t, halves(400, 300, 255))
	os.MkdirAll(a.mediaDir(), 0o755)
	os.WriteFile(filepath.Join(a.mediaDir(), "c.jpg"), photo, 0o644)
	a.addGalleryFile(GalleryPhoto{Album: old.ID, Src: "/media/c.jpg", By: "Sam"})

	link := "/share-photos?album=" + strconv.Itoa(old.ID)
	if g := do(t, h, nil, "GET", "/gallery", "", nil).Body.String(); !strings.Contains(g, `href="`+link+`"`) {
		t.Error("each album on the gallery should have its own share link")
	}
	page := do(t, h, nil, "GET", link, "", nil).Body.String()
	if !strings.Contains(page, `name="album" value="`+strconv.Itoa(old.ID)+`"`) || strings.Contains(page, `class="share-album"`) || !strings.Contains(page, "Old ride") {
		t.Error("the share page from an album should send to that album, with no album picker")
	}
	if !strings.Contains(page, `name="website"`) || !strings.Contains(page, `name="ft"`) {
		t.Error("the spam checks should stay")
	}
	if p := do(t, h, nil, "GET", "/share-photos?album="+strconv.Itoa(hidden.ID), "", nil).Body.String(); !strings.Contains(p, `class="share-album"`) {
		t.Error("a hidden album's number shouldn't lock the share page")
	}

	w := sendPhotos(t, h, a, url.Values{"name": {"Pat"}, "agree": {"yes"}, "album": {strconv.Itoa(old.ID)}}, map[string][]byte{"a.jpg": photo})
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("send: %s", w.Body)
	}
	sent := a.store.SubmittedPhotos()[0]
	if sent.Album != old.ID {
		t.Fatalf("sent to album %d, want %d", sent.Album, old.ID)
	}
	review := do(t, h, admin, "GET", "/admin/submitted", "", nil).Body.String()
	if !strings.Contains(review, `<option value="`+strconv.Itoa(old.ID)+`" selected>Old ride</option>`) {
		t.Error("the review card should have the album it was sent to picked")
	}
	reviewJSON(t, h, admin, url.Values{"do": {"approve"}, "batch": {strconv.Itoa(sent.Batch)}, "id": {strconv.Itoa(sent.ID)}, "album-" + strconv.Itoa(sent.Batch): {strconv.Itoa(old.ID)}})
	if p, _ := a.store.Submitted(sent.ID); p.Gallery == 0 || !slices.Contains(albumIDs(a, old.ID), p.Gallery) {
		t.Error("approving should put it in the album it was sent to")
	}
}

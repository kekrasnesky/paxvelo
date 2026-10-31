package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestParseTags(t *testing.T) {
	got, err := parseTags(" Point  Lookout, #halloween,point lookout,, Base ")
	if err != nil || !slices.Equal(got, []string{"Point Lookout", "halloween", "Base"}) {
		t.Errorf("tags %q %v", got, err)
	}
	if _, err := parseTags(strings.Repeat("x", maxTagLen+1)); err == nil {
		t.Error("a long tag should be refused")
	}
	if _, err := parseTags("a,b,c,d,e,f,g"); err == nil {
		t.Error("too many tags should be refused")
	}
}

// Whoever sends photos says which ride they're from, when, and any tags. An
// admin checks those and adds the photos to the public gallery, which can be
// narrowed to a ride or a tag, and can retag them or take them out later.
func TestGallery(t *testing.T) {
	a, h, admin := testApp(t)
	if home := do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(home, `<a href="/gallery">Photo Gallery</a>`) {
		t.Error("the Info menu should link to the gallery")
	}
	if page := do(t, h, nil, "GET", "/gallery", "", nil).Body.String(); !strings.Contains(page, "just getting started") {
		t.Error("an empty gallery should ask for photos")
	}
	share := do(t, h, nil, "GET", "/share-photos", "", nil).Body.String()
	for _, want := range []string{`name="ride"`, `<option value="thnr">Thursday Night Ride (ThNR)</option>`, `name="date"`, `name="tags"`} {
		if !strings.Contains(share, want) {
			t.Errorf("the share page should have %s", want)
		}
	}

	photo := encodeJPEG(t, halves(120, 90, 255))
	good := url.Values{"name": {"Pat Rider"}, "agree": {"yes"}, "ride": {"tnr"}, "date": {"2026-10-27"}, "tags": {"Point Lookout, #halloween"}}
	for field, bad := range map[string]string{"date": "2099-01-01", "tags": strings.Repeat("x", 40)} {
		v := url.Values{}
		for k, vs := range good {
			v[k] = vs
		}
		v.Set(field, bad)
		if w := sendPhotos(t, h, a, v, map[string][]byte{"a.jpg": photo}); w.Code != http.StatusBadRequest {
			t.Errorf("%s %q should be refused: %d", field, bad, w.Code)
		}
	}
	if w := sendPhotos(t, h, a, good, map[string][]byte{"a.jpg": photo, "b.jpg": photo}); w.Code != http.StatusOK {
		t.Fatalf("sending: %d %s", w.Code, w.Body)
	}
	sent := a.store.SubmittedPhotos()
	if len(sent) != 2 || sent[0].Ride != "tnr" || sent[0].Date != "2026-10-27" || !slices.Equal(sent[0].Tags, []string{"Point Lookout", "halloween"}) {
		t.Fatalf("sent-in %+v", sent)
	}
	batch := strconv.Itoa(sent[0].Batch)
	page := do(t, h, admin, "GET", "/admin/submitted", "", nil).Body.String()
	for _, want := range []string{`name="ride-` + batch + `"`, `<option value="tnr" selected>`, `value="2026-10-27"`, `value="Point Lookout, halloween"`, `value="gallery"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Sent-in photos should show %s", want)
		}
	}

	// The admin fixes the tags and adds one photo to the gallery.
	add := func(ids ...int) string {
		v := url.Values{"do": {"gallery"}, "ride-" + batch: {"tnr"}, "date-" + batch: {"2026-10-27"}, "tags-" + batch: {"Point Lookout, Halloween ride"}}
		for _, id := range ids {
			v.Add("id", strconv.Itoa(id))
		}
		ct, body := form(v)
		return do(t, h, admin, "POST", "/admin/submitted", ct, body).Header().Get("Location")
	}
	if to := add(sent[0].ID); !strings.Contains(to, "ok=gallery-added") || !strings.Contains(to, "n=1") {
		t.Fatalf("adding: %s", to)
	}
	gal := a.store.GalleryPhotos()
	if len(gal) != 1 || gal[0].Ride != "tnr" || gal[0].By != "Pat Rider" || !slices.Equal(gal[0].Tags, []string{"Point Lookout", "Halloween ride"}) || !strings.HasPrefix(gal[0].Src, "/media/") {
		t.Fatalf("gallery %+v", gal)
	}
	if w := do(t, h, nil, "GET", gal[0].Src, "", nil); w.Code != http.StatusOK {
		t.Errorf("a gallery photo is public: %d", w.Code)
	}
	if p, _ := a.store.Submitted(sent[0].ID); p.Gallery != gal[0].ID {
		t.Errorf("the sent-in photo should know it's in the gallery: %+v", p)
	}
	if n := a.store.WaitingPhotos(); n != 1 {
		t.Errorf("a photo in the gallery isn't waiting anymore: %d waiting", n)
	}
	if to := add(sent[0].ID); !strings.Contains(to, "n=0") {
		t.Errorf("adding it twice: %s", to)
	}
	if page := do(t, h, admin, "GET", "/admin/submitted", "", nil).Body.String(); !strings.Contains(page, "In the gallery") {
		t.Error("Sent-in photos should say it's in the gallery")
	}

	// The public page, and narrowing it.
	page = do(t, h, nil, "GET", "/gallery", "", nil).Body.String()
	for _, want := range []string{gal[0].Src, `href="/gallery?ride=tnr"`, "<strong>TNR</strong> · Oct 27, 2026 · Pat Rider", `#Halloween ride`, `class="gal-view"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the gallery should show %s", want)
		}
	}
	if page := do(t, h, nil, "GET", "/gallery?tag=halloween+RIDE", "", nil).Body.String(); !strings.Contains(page, gal[0].Src) || !strings.Contains(page, "1 photo tagged “Halloween ride”") {
		t.Error("a tag should match whatever its case")
	}
	if page := do(t, h, nil, "GET", "/gallery?tag=nope", "", nil).Body.String(); strings.Contains(page, gal[0].Src) {
		t.Error("another tag shouldn't show it")
	}

	// Retag it on Website → Gallery.
	retag := func(v url.Values) string {
		v.Set("id", strconv.Itoa(gal[0].ID))
		ct, body := form(v)
		return do(t, h, admin, "POST", "/admin/gallery", ct, body).Header().Get("Location")
	}
	if to := retag(url.Values{"do": {"save"}, "ride": {"sumr"}, "add": {"Sunrise"}, "drop": {"halloween RIDE"}}); !strings.Contains(to, "ok=gallery-saved") {
		t.Fatalf("retag: %s", to)
	}
	if g := a.store.GalleryPhotos()[0]; g.Ride != "sumr" || !slices.Equal(g.Tags, []string{"Point Lookout", "Sunrise"}) {
		t.Errorf("retagged %+v", g)
	}
	if to := retag(url.Values{"do": {"save"}, "ride": {"keep"}, "add": {"a, b, c, d, e"}}); !strings.Contains(to, "err=gallery-tags") {
		t.Errorf("too many tags: %s", to)
	}
	if g := a.store.GalleryPhotos()[0]; g.Ride != "sumr" || len(g.Tags) != 2 {
		t.Errorf("a refused change shouldn't change anything: %+v", g)
	}
	if page := do(t, h, admin, "GET", "/admin/gallery", "", nil).Body.String(); !strings.Contains(page, "<strong>SuMR</strong>") || !strings.Contains(page, "#Sunrise") {
		t.Error("Website → Gallery should list the photo")
	}

	// Take it out: its file goes, and the sent-in photo can be added again.
	if to := retag(url.Values{"do": {"remove"}}); !strings.Contains(to, "ok=gallery-removed") {
		t.Fatalf("remove: %s", to)
	}
	if len(a.store.GalleryPhotos()) != 0 {
		t.Error("the gallery should be empty")
	}
	if _, err := os.Stat(filepath.Join(a.mediaDir(), strings.TrimPrefix(gal[0].Src, "/media/"))); !os.IsNotExist(err) {
		t.Error("its file should be gone")
	}
	if p, _ := a.store.Submitted(sent[0].ID); p.Gallery != 0 {
		t.Errorf("the sent-in photo is out of the gallery: %+v", p)
	}
}

// The gallery shows the newest rides first, a page at a time.
func TestGalleryPages(t *testing.T) {
	a, h, _ := testApp(t)
	for i := range galleryPage + 2 {
		a.store.AddGallery(GalleryPhoto{Src: "/media/p" + strconv.Itoa(i) + ".jpg", Date: "2026-01-" + strconv.Itoa(10+i%20), By: "Pat"})
	}
	a.store.AddGallery(GalleryPhoto{Src: "/media/newest.jpg", Date: "2026-10-30", By: "Pat"})
	a.store.AddGallery(GalleryPhoto{Src: "/media/undated.jpg", By: "Pat"}) // added last, but it has no date
	page := do(t, h, nil, "GET", "/gallery", "", nil).Body.String()
	if !strings.Contains(page, `href="/gallery?page=2">Older photos`) || strings.Count(page, `class="gal-cap"`) != galleryPage {
		t.Error("the first page should hold a page of photos and link to the next")
	}
	if i := strings.Index(page, "/media/newest.jpg"); i < 0 || i > strings.Index(page, "/media/p") {
		t.Error("the newest ride should come first")
	}
	page = do(t, h, nil, "GET", "/gallery?page=2", "", nil).Body.String()
	if strings.Count(page, `class="gal-cap"`) != 4 || !strings.Contains(page, "Newer photos") {
		t.Error("the second page should hold the rest")
	}
	if !strings.Contains(page, "/media/undated.jpg") || strings.LastIndex(page, "/media/undated.jpg") < strings.LastIndex(page, "/media/p") {
		t.Error("a photo with no date should come after the dated ones")
	}
	if w := do(t, h, nil, "GET", "/gallery?page=9", "", nil); w.Code != http.StatusSeeOther {
		t.Errorf("a page past the end goes back to the first: %d", w.Code)
	}
}

// bulkForm is the admin's bulk upload: the fields and n copies of a photo.
func bulkForm(fields url.Values, photo []byte, n int) (string, *bytes.Buffer) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, vs := range fields {
		for _, v := range vs {
			mw.WriteField(k, v)
		}
	}
	for i := range n {
		fw, _ := mw.CreateFormFile("photos", fmt.Sprintf("p%d.jpg", i))
		fw.Write(photo)
	}
	mw.Close()
	return mw.FormDataContentType(), &buf
}

// Admins upload a whole album straight into the gallery, with one ride,
// date, tags and photographer for all of it.
func TestGalleryBulkUpload(t *testing.T) {
	a, h, admin := testApp(t)
	photo := encodeJPEG(t, halves(1800, 1200, 255)) // bigger than the site keeps
	upload := func(c *http.Cookie, fields url.Values, n int, asJSON bool) *httptest.ResponseRecorder {
		ct, body := bulkForm(fields, photo, n)
		r := httptest.NewRequest("POST", "/admin/gallery/upload", body)
		r.Header.Set("Content-Type", ct)
		if asJSON {
			r.Header.Set("Accept", "application/json")
		}
		if c != nil {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	album := url.Values{"ride": {"other"}, "date": {"2014-06-07"}, "tags": {"Leonardtown Crit, race"}, "by": {"Club photographer"}}

	if page := do(t, h, admin, "GET", "/admin/gallery", "", nil).Body.String(); !strings.Contains(page, `action="/admin/gallery/upload"`) {
		t.Fatal("Website → Gallery should have the upload form")
	}
	if w := upload(nil, album, 1, false); w.Code == http.StatusSeeOther && strings.Contains(w.Header().Get("Location"), "ok=") {
		t.Fatal("only admins can upload")
	}
	noBy := url.Values{"ride": {"other"}}
	if w := upload(admin, noBy, 1, true); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Say who took the photos.") {
		t.Errorf("no photographer: %d %s", w.Code, w.Body)
	}
	if w := upload(admin, album, maxBulkPhotos+1, true); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "up to 200") {
		t.Errorf("too many: %d %s", w.Code, w.Body)
	}

	w := upload(admin, album, 3, true)
	var j struct {
		OK    bool
		Added int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil || !j.OK || j.Added != 3 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	gal := a.store.GalleryPhotos()
	if len(gal) != 3 || gal[0].Ride != "other" || gal[0].Date != "2014-06-07" || gal[0].By != "Club photographer" || !slices.Equal(gal[0].Tags, []string{"Leonardtown Crit", "race"}) {
		t.Fatalf("gallery %+v", gal)
	}
	saved, err := os.ReadFile(filepath.Join(a.mediaDir(), strings.TrimPrefix(gal[0].Src, "/media/")))
	if err != nil || len(saved) >= len(photo) {
		t.Errorf("the photo should be saved smaller: %d bytes from %d (%v)", len(saved), len(photo), err)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(saved)); err != nil || max(cfg.Width, cfg.Height) > photoMaxSide {
		t.Errorf("the photo should be at most %d pixels: %+v %v", photoMaxSide, cfg, err)
	}

	// Without JavaScript the form posts and comes back with a message.
	w = upload(admin, album, 1, false)
	if to := w.Header().Get("Location"); !strings.Contains(to, "ok=gallery-uploaded") || !strings.Contains(to, "n=1") {
		t.Errorf("plain upload: %s", to)
	}
	if page := do(t, h, admin, "GET", w.Header().Get("Location"), "", nil).Body.String(); !strings.Contains(page, "Added 1 photo to the gallery.") {
		t.Error("the page should say how many were added")
	}
}

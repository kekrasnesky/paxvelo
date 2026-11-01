package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
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
	"time"
)

// mediaFile says whether a file under /media/ is there.
func mediaFile(a *App, src string) bool {
	_, err := os.Stat(filepath.Join(a.mediaDir(), strings.TrimPrefix(src, "/media/")))
	return err == nil
}

// albumIDs lists an album's photos' IDs in order.
func albumIDs(a *App, album int) []int {
	var out []int
	for _, p := range a.store.AlbumPhotos(album) {
		out = append(out, p.ID)
	}
	return out
}

// A member sends photos with captions and names a new album for them.
// Nothing is public until an admin approves them, which starts the album,
// and then visitors see an album grid, the album's photos, and each photo's
// own page.
func TestGallery(t *testing.T) {
	a, h, admin := testApp(t)
	if home := do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(home, `<a href="/gallery">Photo Gallery</a>`) {
		t.Error("the Info menu should link to the gallery")
	}
	if page := do(t, h, nil, "GET", "/gallery", "", nil).Body.String(); !strings.Contains(page, "just getting started") {
		t.Error("an empty gallery should ask for photos")
	}
	share := do(t, h, nil, "GET", "/share-photos", "", nil).Body.String()
	for _, want := range []string{`name="album"`, `<option value="new">Something else…</option>`, `name="album_name"`, `data-max="30"`} {
		if !strings.Contains(share, want) {
			t.Errorf("the share page should have %s", want)
		}
	}

	photo := encodeJPEG(t, halves(1200, 800, 255))
	fields := url.Values{"name": {"Pat Rider"}, "agree": {"yes"}, "album": {"new"}, "album_name": {"  Halloween   ride "}, "caption": {"Climbing to Point Lookout", ""}}
	if w := sendPhotos(t, h, a, fields, map[string][]byte{"a.jpg": photo, "b.jpg": photo}); w.Code != http.StatusOK {
		t.Fatalf("sending: %d %s", w.Code, w.Body)
	}
	sent := a.store.SubmittedPhotos()
	if len(sent) != 2 || sent[0].AlbumName != "Halloween ride" || sent[0].Caption != "Climbing to Point Lookout" || sent[1].Caption != "" {
		t.Fatalf("sent-in %+v", sent)
	}
	if page := do(t, h, nil, "GET", "/gallery", "", nil).Body.String(); !strings.Contains(page, "just getting started") {
		t.Error("sent-in photos aren't public until they're approved")
	}
	batch := strconv.Itoa(sent[0].Batch)
	page := do(t, h, admin, "GET", "/admin/submitted", "", nil).Body.String()
	for _, want := range []string{`name="album-` + batch + `"`, `<option value="new" selected>`, `value="Halloween ride"`, `value="Climbing to Point Lookout"`,
		`name="by-` + batch + `" value="Pat Rider"`, `name="id" value="` + strconv.Itoa(sent[0].ID) + `" checked`, "All 2",
		`<span>Approve <span class="sent-n">2 photos</span> into <span class="sent-to">Halloween ride</span></span>`} {
		if !strings.Contains(page, want) {
			t.Errorf("Photos to review should show %s", want)
		}
	}

	approve := func(v url.Values, ids ...int) string {
		v.Set("do", "approve")
		for _, id := range ids {
			v.Add("id", strconv.Itoa(id))
		}
		ct, body := form(v)
		return do(t, h, admin, "POST", "/admin/submitted", ct, body).Header().Get("Location")
	}
	if to := approve(url.Values{"album-" + batch: {"new"}, "name-" + batch: {""}}, sent[0].ID); !strings.Contains(to, "err=album-pick") {
		t.Errorf("approving into a new album needs a name: %s", to)
	}
	if to := approve(url.Values{"album-" + batch: {"new"}, "name-" + batch: {"Halloween ride"}}, sent[0].ID); !strings.Contains(to, "ok=gallery-added") || !strings.Contains(to, "n=1") {
		t.Fatalf("approving: %s", to)
	}
	albums := a.store.Albums()
	if len(albums) != 1 || albums[0].Title != "Halloween ride" {
		t.Fatalf("approving should start the album: %+v", albums)
	}
	al := albums[0]
	gal := a.store.AlbumPhotos(al.ID)
	if len(gal) != 1 || gal[0].By != "Pat Rider" || gal[0].Caption != "Climbing to Point Lookout" || gal[0].W != 1200 || gal[0].H != 800 || !gal[0].Sized {
		t.Fatalf("gallery %+v", gal)
	}
	for _, f := range gal[0].files() {
		if !mediaFile(a, f) {
			t.Errorf("%s should be saved", f)
		}
	}
	if p, _ := a.store.Submitted(sent[0].ID); p.Gallery != gal[0].ID {
		t.Errorf("the sent-in photo should know it's in the gallery: %+v", p)
	}
	// The other photo goes into the same album, picked by name.
	if to := approve(url.Values{"album-" + batch: {"new"}, "name-" + batch: {"halloween RIDE"}}, sent[0].ID, sent[1].ID); !strings.Contains(to, "n=1") || !strings.Contains(to, "already=1") {
		t.Errorf("approving again: %s", to)
	}
	if n := len(a.store.Albums()); n != 1 {
		t.Errorf("an album by the same name is used, not a second one: %d albums", n)
	}
	page = do(t, h, admin, "GET", "/admin/submitted", "", nil).Body.String()
	if !strings.Contains(page, "Nothing waiting for review.") || !strings.Contains(page, "Already reviewed (1)") {
		t.Error("approved photos should leave the review queue")
	}

	// What visitors see.
	gal = a.store.AlbumPhotos(al.ID)
	page = do(t, h, nil, "GET", "/gallery", "", nil).Body.String()
	for _, want := range []string{`href="` + al.Href() + `"`, "Halloween ride", "2 photos", copySrc(gal[0].Src, 480)} {
		if !strings.Contains(page, want) {
			t.Errorf("the albums should show %s", want)
		}
	}
	if al.Href() != "/gallery/"+strconv.Itoa(al.ID)+"-halloween-ride" {
		t.Errorf("album address %s", al.Href())
	}
	page = do(t, h, nil, "GET", al.Href(), "", nil).Body.String()
	for _, want := range []string{
		`srcset="` + copySrc(gal[0].Src, 480) + ` 480w, ` + copySrc(gal[0].Src, 960) + ` 960w, ` + gal[0].Src + ` 1200w"`,
		`loading="lazy"`, `width="1200" height="800"`, `data-caption="Climbing to Point Lookout"`, `data-by="Pat Rider"`,
		`href="` + al.Href() + `/` + strconv.Itoa(gal[0].ID) + `"`, `class="gal-view"`, `class="gal-view-share"`, `download`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the album should show %s", want)
		}
	}
	photoPage := al.Href() + "/" + strconv.Itoa(gal[0].ID)
	page = do(t, h, nil, "GET", photoPage, "", nil).Body.String()
	for _, want := range []string{`<meta property="og:image" content="` + a.cfg.SiteURL + gal[0].Src + `"`, "Photo by Pat Rider", "1 of 2", `rel="next"`, `download="paxvelo-`} {
		if !strings.Contains(page, want) {
			t.Errorf("the photo's page should show %s", want)
		}
	}
	for path, to := range map[string]string{
		"/gallery/" + strconv.Itoa(al.ID) + "-old-name":                   al.Href(),
		"/gallery/" + strconv.Itoa(al.ID) + "/" + strconv.Itoa(gal[0].ID): photoPage,
		"/gallery?ride=tnr": "/gallery",
	} {
		if w := do(t, h, nil, "GET", path, "", nil); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != to {
			t.Errorf("%s should go to %s: %d %s", path, to, w.Code, w.Header().Get("Location"))
		}
	}
	for _, path := range []string{"/gallery/99-nope", al.Href() + "/999", "/gallery/x"} {
		if w := do(t, h, nil, "GET", path, "", nil); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if sm := do(t, h, nil, "GET", "/sitemap.xml", "", nil).Body.String(); !strings.Contains(sm, al.Href()) {
		t.Error("the sitemap should list the album")
	}

	// The album they picked is picked for approving.
	w := sendPhotos(t, h, a, url.Values{"name": {"Sam"}, "agree": {"yes"}, "album": {strconv.Itoa(al.ID)}}, map[string][]byte{"c.jpg": photo})
	if w.Code != http.StatusOK {
		t.Fatalf("sending to an album: %d", w.Code)
	}
	latest := a.store.SubmittedPhotos()[0]
	if latest.Album != al.ID {
		t.Errorf("the album they picked should be kept: %+v", latest)
	}
	if page := do(t, h, admin, "GET", "/admin/submitted", "", nil).Body.String(); !strings.Contains(page, `<option value="`+strconv.Itoa(al.ID)+`" selected>Halloween ride</option>`) {
		t.Error("the album they picked should be picked for approving")
	}
}

// Admins start albums, put them in order, rename them, and in each one
// caption photos, order them, pick the cover, move photos to another album,
// and take them out. Deleting an album moves or deletes its photos.
func TestAlbumAdmin(t *testing.T) {
	a, h, admin := testApp(t)
	post := func(path string, v url.Values) string {
		ct, body := form(v)
		return do(t, h, admin, "POST", path, ct, body).Header().Get("Location")
	}
	if to := post("/admin/gallery", url.Values{"title": {"  "}}); !strings.Contains(to, "err=album-title") {
		t.Errorf("an album needs a name: %s", to)
	}
	if to := post("/admin/gallery", url.Values{"title": {"Spring"}, "date": {"2099-01-01"}}); !strings.Contains(to, "err=gallery-date") {
		t.Errorf("a future date: %s", to)
	}
	post("/admin/gallery", url.Values{"title": {"Spring"}})
	to := post("/admin/gallery", url.Values{"title": {"Summer"}, "date": {"2026-07-04"}})
	albums := a.store.Albums()
	if len(albums) != 2 || albums[0].Title != "Summer" || !strings.HasPrefix(to, albumAdminHref(albums[0].ID)) {
		t.Fatalf("a new album goes first, and its page opens: %+v %s", albums, to)
	}
	summer, spring := albums[0], albums[1]
	post("/admin/gallery", url.Values{"move": {strconv.Itoa(spring.ID) + ":up"}})
	if albums := a.store.Albums(); albums[0].ID != spring.ID {
		t.Errorf("moving up: %+v", albums)
	}
	if page := do(t, h, admin, "GET", "/admin/gallery", "", nil).Body.String(); !strings.Contains(page, "Not on the website until it has photos") {
		t.Error("an empty album should say it isn't public")
	}
	if page := do(t, h, nil, "GET", "/gallery", "", nil).Body.String(); strings.Contains(page, "Summer") {
		t.Error("an empty album isn't public")
	}

	for i := range 4 {
		a.store.AddGallery(GalleryPhoto{Album: summer.ID, Src: fmt.Sprintf("/media/s%d.jpg", i), By: []string{"Zoe", "Al", "Zoe", "Al"}[i]})
	}
	ids := albumIDs(a, summer.ID)
	here := albumAdminHref(summer.ID)
	page := do(t, h, admin, "GET", here, "", nil).Body.String()
	for _, want := range []string{`action="/admin/gallery/upload?album=` + strconv.Itoa(summer.ID) + `"`, `name="caption-` + strconv.Itoa(ids[0]) + `"`, `draggable="true"`, `class="cover-on"`, `value="cover:` + strconv.Itoa(ids[1]) + `"`, `>Spring</option>`} {
		if !strings.Contains(page, want) {
			t.Errorf("the album page should have %s", want)
		}
	}

	// Captions and a dragged order save together.
	id := func(i int) string { return strconv.Itoa(ids[i]) }
	if to := post(here, url.Values{"do": {"save"}, "caption-" + id(1): {"  Fireworks  "}, "order": {id(3) + "," + id(1)}}); !strings.Contains(to, "ok=album-saved") {
		t.Fatalf("save: %s", to)
	}
	if got := albumIDs(a, summer.ID); !slices.Equal(got, []int{ids[3], ids[1], ids[0], ids[2]}) {
		t.Errorf("dragged order %v", got)
	}
	if p := a.store.AlbumPhotos(summer.ID)[1]; p.Caption != "Fireworks" {
		t.Errorf("caption %q", p.Caption)
	}
	post(here, url.Values{"do": {"last"}, "id": {id(3)}})
	if got := albumIDs(a, summer.ID); got[3] != ids[3] {
		t.Errorf("move to end: %v", got)
	}
	post(here, url.Values{"do": {"sort-by"}})
	if got := albumIDs(a, summer.ID); !slices.Equal(got, []int{ids[1], ids[3], ids[0], ids[2]}) {
		t.Errorf("sorted by photographer: %v", got)
	}
	post(here, url.Values{"do": {"sort-old"}})
	if got := albumIDs(a, summer.ID); !slices.Equal(got, ids) {
		t.Errorf("sorted oldest first: %v", got)
	}
	if to := post(here, url.Values{"do": {"cover"}}); !strings.Contains(to, "err=none-ticked") {
		t.Errorf("nothing ticked: %s", to)
	}
	if to := post(here, url.Values{"do": {"cover:" + id(2)}}); !strings.Contains(to, "ok=album-cover") {
		t.Errorf("make cover: %s", to)
	}
	if c := a.store.AlbumCards(false)[0]; c.Cover.ID != ids[2] || c.Count != 4 {
		t.Errorf("cover %+v", c)
	}
	post(here, url.Values{"do": {"credit"}, "id": {id(0), id(1)}, "by": {"Club photographer"}})
	if p := a.store.AlbumPhotos(summer.ID); p[0].By != "Club photographer" || p[1].By != "Club photographer" || p[2].By != "Zoe" {
		t.Errorf("credit %+v", p)
	}

	// Moving photos to another album puts them at its end.
	if to := post(here, url.Values{"do": {"move"}, "id": {id(0)}, "to": {strconv.Itoa(spring.ID)}}); !strings.Contains(to, "ok=photos-moved") {
		t.Fatalf("move: %s", to)
	}
	if got := albumIDs(a, spring.ID); !slices.Equal(got, []int{ids[0]}) {
		t.Errorf("spring %v", got)
	}

	// Renaming keeps the old address working.
	old := summer.Href()
	post(here, url.Values{"do": {"rename"}, "title": {"Summer 2026"}, "date": {"2026-07-04"}})
	summer, _ = a.store.Album(summer.ID)
	if summer.Title != "Summer 2026" {
		t.Errorf("rename %+v", summer)
	}
	if w := do(t, h, nil, "GET", old, "", nil); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != summer.Href() {
		t.Errorf("the old address should go to the new one: %d %s", w.Code, w.Header().Get("Location"))
	}

	// Deleting Spring moves its photo back; deleting Summer deletes its photos.
	if to := post(albumAdminHref(spring.ID), url.Values{"do": {"delete"}}); !strings.Contains(to, "err=album-photos") {
		t.Errorf("deleting should say what happens to the photos: %s", to)
	}
	post(albumAdminHref(spring.ID), url.Values{"do": {"delete"}, "move_to": {strconv.Itoa(summer.ID)}})
	if _, ok := a.store.Album(spring.ID); ok || len(a.store.AlbumPhotos(summer.ID)) != 4 {
		t.Errorf("deleting an album can move its photos: %v", albumIDs(a, summer.ID))
	}
	os.MkdirAll(a.mediaDir(), 0o755)
	os.WriteFile(filepath.Join(a.mediaDir(), "s1.jpg"), []byte("x"), 0o644)
	if to := post(here, url.Values{"do": {"delete"}, "move_to": {"0"}}); !strings.Contains(to, "ok=album-deleted") {
		t.Fatalf("delete: %s", to)
	}
	if len(a.store.Albums()) != 0 || len(a.store.GalleryPhotos()) != 0 || mediaFile(a, "/media/s1.jpg") {
		t.Error("deleting an album with its photos should delete their files")
	}
}

// A big album shows a page of photos at a time, with a link to load more.
func TestAlbumPages(t *testing.T) {
	a, h, _ := testApp(t)
	al, _ := a.store.AddAlbum("Big ride", "")
	for i := range galleryPage + 2 {
		a.store.AddGallery(GalleryPhoto{Album: al.ID, Src: "/media/p" + strconv.Itoa(i) + ".jpg", By: "Pat"})
	}
	page := do(t, h, nil, "GET", al.Href(), "", nil).Body.String()
	if !strings.Contains(page, `href="`+al.Href()+`?page=2">Load more photos`) || strings.Count(page, "data-full=") != galleryPage || !strings.Contains(page, `data-total="50"`) {
		t.Error("the first page should hold a page of photos and link to more")
	}
	page = do(t, h, nil, "GET", al.Href()+"?page=2", "", nil).Body.String()
	if strings.Count(page, "data-full=") != 2 || strings.Contains(page, `class="gal-more"`) || !strings.Contains(page, "/media/p49.jpg") {
		t.Error("the second page should hold the rest")
	}
	if w := do(t, h, nil, "GET", al.Href()+"?page=9", "", nil); w.Code != http.StatusSeeOther {
		t.Errorf("a page past the end goes back to the first: %d", w.Code)
	}
	last := a.store.AlbumPhotos(al.ID)[galleryPage+1]
	if page := do(t, h, nil, "GET", al.Href()+"/"+strconv.Itoa(last.ID), "", nil).Body.String(); !strings.Contains(page, `href="`+al.Href()+`?page=2"`) {
		t.Error("a photo's page links back to the album page it's on")
	}
}

// Photos from before albums go into albums by ride and date when the site
// starts, with copies made for them in the background.
func TestAlbumize(t *testing.T) {
	a, _, _ := testApp(t)
	os.MkdirAll(a.mediaDir(), 0o755)
	os.WriteFile(filepath.Join(a.mediaDir(), "old.jpg"), encodeJPEG(t, halves(1000, 600, 255)), 0o644)
	a.store.mu.Lock()
	a.store.d.Gallery = []*GalleryPhoto{
		{ID: 1, Src: "/media/old.jpg", Ride: "tnr", Date: "2026-10-28", By: "Pat"},
		{ID: 2, Src: "/media/b.jpg", By: "Pat"},
		{ID: 3, Src: "/media/c.jpg", Ride: "tnr", Date: "2026-10-28", By: "Sam"},
		{ID: 4, Src: "/media/d.jpg", Date: "2025-05-01", By: "Sam"},
	}
	a.store.save()
	a.store.mu.Unlock()
	s, err := openStore(a.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, al := range s.Albums() {
		titles = append(titles, al.Title)
	}
	if !slices.Equal(titles, []string{"TNR · Oct 28, 2026", "May 1, 2025", "Older photos"}) {
		t.Fatalf("albums %q", titles)
	}
	if got := s.AlbumPhotos(s.Albums()[0].ID); len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Errorf("TNR photos %+v", got)
	}
	a.store = s
	a.sizeOldPhotos()
	if p := a.store.AlbumPhotos(s.Albums()[0].ID)[0]; p.W != 1000 || !p.Sized || !mediaFile(a, copySrc(p.Src, 480)) || !mediaFile(a, copySrc(p.Src, 960)) {
		t.Errorf("the old photo should get its copies: %+v", p)
	}
}

// The narrower copies are made only narrower than the photo, and none for
// see-through photos.
func TestSmallerCopies(t *testing.T) {
	copies, w, h := smallerCopies(encodeJPEG(t, halves(700, 500, 255)))
	if w != 700 || h != 500 || len(copies) != 1 || copies[480] == nil {
		t.Fatalf("copies of a 700 pixel photo: %d %d×%d", len(copies), w, h)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(copies[480])); err != nil || cfg.Width != 480 || cfg.Height != 342 {
		t.Errorf("480 copy %+v %v", cfg, err)
	}
	if c, _, _ := smallerCopies(encodePNG(t, halves(700, 500, 100))); c != nil {
		t.Error("a see-through photo gets no copies")
	}
	if c, _, _ := smallerCopies([]byte("RIFF....WEBP")); c != nil {
		t.Error("a photo Go can't read gets no copies")
	}
}

// PNG and WebP photos lose the hidden details a camera can leave in them.
func TestStripMetadata(t *testing.T) {
	plain := encodePNG(t, halves(20, 20, 255))
	chunk := func(kind, data string) []byte {
		b := make([]byte, 4, 12+len(data))
		b[3] = byte(len(data))
		return append(append(append(b, kind...), data...), 0, 0, 0, 0)
	}
	withExif := append(append(append([]byte{}, plain[:33]...), chunk("eXIf", "GPS")...), plain[33:]...)
	if got := stripPNGMetadata(withExif); !bytes.Equal(got, plain) {
		t.Errorf("the PNG's EXIF should go: %d bytes, want %d", len(got), len(plain))
	}
	riff := func(chunks ...string) []byte {
		body := []byte("WEBP")
		for _, c := range chunks {
			body = append(body, c...)
		}
		n := len(body)
		return append([]byte{'R', 'I', 'F', 'F', byte(n), byte(n >> 8), 0, 0}, body...)
	}
	vp8x := "VP8X\x0a\x00\x00\x00\x0c\x00\x00\x00\x00\x00\x00\x00\x00\x00"
	in := riff(vp8x, "VP8 \x02\x00\x00\x00ab", "EXIF\x03\x00\x00\x00GPS\x00")
	want := riff(strings.Replace(vp8x, "\x0c", "\x00", 1), "VP8 \x02\x00\x00\x00ab")
	if got := stripWebPMetadata(in); !bytes.Equal(got, want) {
		t.Errorf("the WebP's EXIF should go:\n%q\n%q", got, want)
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

// Admins upload a whole album's photos at once, by one photographer.
func TestGalleryBulkUpload(t *testing.T) {
	a, h, admin := testApp(t)
	al, _ := a.store.AddAlbum("Leonardtown Crit", "2014-06-07")
	photo := encodeJPEG(t, halves(1800, 1200, 255)) // bigger than the site keeps
	upload := func(c *http.Cookie, album int, fields url.Values, n int, asJSON bool) *httptest.ResponseRecorder {
		ct, body := bulkForm(fields, photo, n)
		r := httptest.NewRequest("POST", "/admin/gallery/upload?album="+strconv.Itoa(album), body)
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
	by := url.Values{"by": {"Club photographer"}}
	if w := upload(nil, al.ID, by, 1, false); w.Code == http.StatusSeeOther && strings.Contains(w.Header().Get("Location"), "ok=") {
		t.Fatal("only admins can upload")
	}
	if w := upload(admin, al.ID, url.Values{}, 1, true); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Say who took the photos.") {
		t.Errorf("no photographer: %d %s", w.Code, w.Body)
	}
	if w := upload(admin, 99, by, 1, true); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "That album was deleted.") {
		t.Errorf("no album: %d %s", w.Code, w.Body)
	}
	if w := upload(admin, al.ID, by, maxBulkPhotos+1, true); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "up to 200") {
		t.Errorf("too many: %d %s", w.Code, w.Body)
	}

	w := upload(admin, al.ID, by, 3, true)
	var j struct {
		OK    bool
		Added int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil || !j.OK || j.Added != 3 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	gal := a.store.AlbumPhotos(al.ID)
	if len(gal) != 3 || gal[0].By != "Club photographer" || gal[0].W != photoMaxSide || !gal[0].Sized {
		t.Fatalf("album %+v", gal)
	}
	saved, err := os.ReadFile(filepath.Join(a.mediaDir(), strings.TrimPrefix(gal[0].Src, "/media/")))
	if err != nil || len(saved) >= len(photo) {
		t.Errorf("the photo should be saved smaller: %d bytes from %d (%v)", len(saved), len(photo), err)
	}

	// Without JavaScript the form posts and comes back with a message.
	w = upload(admin, al.ID, by, 1, false)
	if to := w.Header().Get("Location"); !strings.HasPrefix(to, albumAdminHref(al.ID)+"?") || !strings.Contains(to, "n=1") {
		t.Errorf("plain upload: %s", to)
	}
	if page := do(t, h, admin, "GET", w.Header().Get("Location"), "", nil).Body.String(); !strings.Contains(page, "Added 1 photo to the album.") {
		t.Error("the page should say how many were added")
	}

	// Taking photos out deletes all their copies.
	ct, body := form(url.Values{"do": {"remove"}, "id": {strconv.Itoa(gal[0].ID)}})
	do(t, h, admin, "POST", albumAdminHref(al.ID), ct, body)
	for _, f := range gal[0].files() {
		if mediaFile(a, f) {
			t.Errorf("%s should be deleted", f)
		}
	}
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// With the club photo PIN, a member's photos skip the review and go straight
// into the album they picked. A wrong PIN is refused, guesses are limited,
// and photos with no album picked still wait for review.
func TestPhotoPIN(t *testing.T) {
	a, h, admin := testApp(t)
	al, _ := a.store.AddAlbum("Saturday ride", "")
	photo := encodeJPEG(t, halves(800, 600, 255))
	send := func(fields url.Values) *httptest.ResponseRecorder {
		fields.Set("name", "Pat Rider")
		fields.Set("agree", "yes")
		return sendPhotos(t, h, a, fields, map[string][]byte{"a.jpg": photo, "b.jpg": photo})
	}
	if page := do(t, h, nil, "GET", "/share-photos", "", nil).Body.String(); strings.Contains(page, `name="pin"`) {
		t.Error("with no PIN set, the share page shouldn't ask for one")
	}
	setPIN := func(v url.Values) string {
		ct, body := form(v)
		return do(t, h, admin, "POST", "/admin/security", ct, body).Header().Get("Location")
	}
	if to := setPIN(url.Values{"pin": {"abc"}}); !strings.Contains(to, "err=pin-short") {
		t.Errorf("a short PIN: %s", to)
	}
	if to := setPIN(url.Values{"pin": {" Velo2026 "}}); !strings.Contains(to, "ok=pin-saved") || a.store.ClubPIN() != "Velo2026" {
		t.Fatalf("setting the PIN: %s %q", to, a.store.ClubPIN())
	}
	if page := do(t, h, nil, "GET", "/share-photos", "", nil).Body.String(); !strings.Contains(page, `name="pin"`) {
		t.Error("the share page should ask for the PIN")
	}

	if w := send(url.Values{"pin": {"nope"}, "album": {strconv.Itoa(al.ID)}}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "isn’t right") {
		t.Errorf("a wrong PIN: %d %s", w.Code, w.Body)
	}
	w := send(url.Values{"pin": {"velo2026"}, "album": {strconv.Itoa(al.ID)}, "caption": {"Coffee stop", ""}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "in the gallery now") || !strings.Contains(w.Body.String(), al.Href()) {
		t.Fatalf("the right PIN, any case: %d %s", w.Code, w.Body)
	}
	gal := a.store.AlbumPhotos(al.ID)
	if len(gal) != 2 || gal[0].Caption != "Coffee stop" || gal[0].By != "Pat Rider" || !gal[0].Sized {
		t.Fatalf("the photos should be in the album: %+v", gal)
	}
	if n := a.store.WaitingPhotos(); n != 0 {
		t.Errorf("nothing should wait for review: %d", n)
	}
	if page := do(t, h, admin, "GET", "/admin/submitted", "", nil).Body.String(); !strings.Contains(page, "Posted with the PIN") {
		t.Error("Sent-in photos should say they were posted with the PIN")
	}

	// A new album they name is started; with no album, they wait for review.
	send(url.Values{"pin": {"Velo2026"}, "album": {"new"}, "album_name": {"Fall colors"}})
	if albums := a.store.Albums(); len(albums) != 2 || albums[0].Title != "Fall colors" || len(a.store.AlbumPhotos(albums[0].ID)) != 2 {
		t.Errorf("a new album with the PIN: %+v", albums)
	}
	if w := send(url.Values{"pin": {"Velo2026"}}); !strings.Contains(w.Body.String(), "an admin will put them in an album") || a.store.WaitingPhotos() != 2 {
		t.Errorf("no album picked: %s, %d waiting", w.Body, a.store.WaitingPhotos())
	}

	// Turning it off: the box goes, and a PIN typed anyway isn't asked about.
	if to := setPIN(url.Values{"pin": {"Velo2026"}, "off": {"1"}}); !strings.Contains(to, "ok=pin-off") || a.store.ClubPIN() != "" {
		t.Errorf("turning off: %s", to)
	}
}

// Guessing the PIN stops after 10 tries an hour, right or wrong.
func TestPhotoPINGuesses(t *testing.T) {
	a, h, _ := testApp(t)
	a.store.SetClubPIN("Velo2026")
	al, _ := a.store.AddAlbum("Saturday ride", "")
	for range 10 {
		a.limit.Allow("pin:192.0.2.1", 10, time.Hour) // httptest's address
	}
	w := sendPhotos(t, h, a, url.Values{"name": {"Pat"}, "agree": {"yes"}, "pin": {"Velo2026"}, "album": {strconv.Itoa(al.ID)}}, map[string][]byte{"a.jpg": encodeJPEG(t, halves(120, 90, 255))})
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("after 10 tries: %d %s", w.Code, w.Body)
	}
}

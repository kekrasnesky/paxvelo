package main

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

// homePhotosForm is the Home photos tab's form: the photos already in the
// row (photo, alt, order, remove) and any new files to add.
func homePhotosForm(t *testing.T, rows [][4]string, files int) (string, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("count", fmt.Sprint(len(rows)))
	for i, r := range rows {
		mw.WriteField(fmt.Sprint("photo", i), r[0])
		mw.WriteField(fmt.Sprint("alt", i), r[1])
		mw.WriteField(fmt.Sprint("order", i), r[2])
		if r[3] != "" {
			mw.WriteField(fmt.Sprint("remove", i), "on")
		}
	}
	for i := range files {
		fw, _ := mw.CreateFormFile("add", fmt.Sprintf("ride%d.jpg", i))
		fw.Write(encodeJPEG(t, halves(120, 90, 255)))
	}
	mw.Close()
	return mw.FormDataContentType(), &buf
}

// The home page starts with the club's three photos, and an admin can
// describe, reorder, remove and add them.
func TestHomePhotos(t *testing.T) {
	a, h, admin := testApp(t)
	a.rebuildContent() // main does this at startup, with the saved photos
	if home := do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(home, `src="/photos/shop-ride.webp"`) || strings.Contains(home, `class="strip-btn`) {
		t.Fatal("the home page should start with the three club photos, and no arrows")
	}
	if w := do(t, h, admin, "GET", "/admin/home-photos", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Room for 12 more") {
		t.Fatalf("admin tab: %d", w.Code)
	}

	// Put ice cream first, drop the kit photo, and add two new ones.
	ct, body := homePhotosForm(t, [][4]string{
		{"/photos/shop-ride.webp", "At the shop", "2", ""},
		{"/photos/ice-cream.webp", "Ice cream stop", "1", ""},
		{"/photos/club-kit.webp", "", "3", "remove"},
	}, 2)
	if w := do(t, h, admin, "POST", "/admin/home-photos", ct, body); w.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	got := a.store.HomePhotos()
	if len(got) != 4 || got[0].Alt != "Ice cream stop" || got[1].Photo != "/photos/shop-ride.webp" || !strings.HasPrefix(got[2].Photo, "/media/") {
		t.Fatalf("home photos: %+v", got)
	}
	home := do(t, h, nil, "GET", "/", "", nil).Body.String()
	if !strings.Contains(home, `alt="Ice cream stop"`) || strings.Contains(home, "club-kit") || !strings.Contains(home, `class="strip-btn next"`) {
		t.Error("the home page should show the new row, with arrows for more than three")
	}

	// The row holds 15: extra photos are turned away with a note.
	var rows [][4]string
	for _, p := range got {
		rows = append(rows, [4]string{p.Photo, p.Alt, "", ""})
	}
	ct, body = homePhotosForm(t, rows, 13)
	w := do(t, h, admin, "POST", "/admin/home-photos", ct, body)
	if n := len(a.store.HomePhotos()); n != maxHomePhotos || !strings.Contains(w.Body.String(), "the last 2 weren’t added") {
		t.Errorf("want %d photos and a note about the 2 left out, got %d", maxHomePhotos, n)
	}

	// A photo address that isn't on this site is dropped.
	ct, body = homePhotosForm(t, [][4]string{{"https://example.com/x.jpg", "", "1", ""}}, 0)
	do(t, h, admin, "POST", "/admin/home-photos", ct, body)
	if n := len(a.store.HomePhotos()); n != 0 {
		t.Errorf("an outside photo should be dropped, got %d", n)
	}
}

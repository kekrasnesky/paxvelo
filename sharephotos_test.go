package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
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

// shareForm is the share page's form, as the page sends it: the fields, a
// fresh form token, and files (name → contents).
func shareForm(a *App, fields url.Values, files map[string][]byte) (string, *bytes.Buffer) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	ts := strconv.FormatInt(time.Now().Add(-10*time.Second).Unix(), 10)
	mw.WriteField("ft", ts+"."+sign(a.formKey(), ts)[:24])
	for k, vs := range fields {
		for _, v := range vs {
			mw.WriteField(k, v)
		}
	}
	for name, b := range files {
		fw, _ := mw.CreateFormFile("photos", name)
		fw.Write(b)
	}
	mw.Close()
	return mw.FormDataContentType(), &buf
}

func sendPhotos(t *testing.T, h http.Handler, a *App, fields url.Values, files map[string][]byte) *httptest.ResponseRecorder {
	t.Helper()
	ct, body := shareForm(a, fields, files)
	r := httptest.NewRequest("POST", "/share-photos", body)
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Anyone can send in photos from the share page, without an account. They
// stay private until an admin adds them to a newsletter's photo strip.
func TestSharePhotos(t *testing.T) {
	a, h, admin := testApp(t)
	if page := do(t, h, nil, "GET", "/share-photos", "", nil).Body.String(); !strings.Contains(page, "Share Your Photos") || !strings.Contains(page, `name="photos"`) {
		t.Fatal("the share page should have the form")
	}
	if home := do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(home, `<a href="/share-photos">Share Your Photos</a>`) {
		t.Error("the Info menu should link to the share page")
	}
	photo := encodeJPEG(t, halves(120, 90, 255))
	good := url.Values{"name": {"Pat Rider"}, "email": {"pat@example.com"}, "note": {"Sunday ride, Point Lookout"}, "agree": {"yes"}}
	for field, bad := range map[string]string{"name": "", "agree": "", "email": "pat@"} {
		v := url.Values{}
		for k, vs := range good {
			v[k] = vs
		}
		v.Set(field, bad)
		if w := sendPhotos(t, h, a, v, map[string][]byte{"a.jpg": photo}); w.Code != http.StatusBadRequest {
			t.Errorf("%s %q should be refused: %d", field, bad, w.Code)
		}
	}
	if w := sendPhotos(t, h, a, good, nil); w.Code != http.StatusBadRequest {
		t.Errorf("no photos should be refused: %d", w.Code)
	}
	if w := sendPhotos(t, h, a, good, map[string][]byte{"notes.txt": []byte("not a photo")}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "notes.txt") {
		t.Errorf("a file that isn't a photo should be refused: %d %s", w.Code, w.Body)
	}

	w := sendPhotos(t, h, a, good, map[string][]byte{"a.jpg": photo, "b.jpg": photo, "notes.txt": []byte("not a photo")})
	var j struct {
		OK     bool
		Detail string
	}
	json.Unmarshal(w.Body.Bytes(), &j)
	if w.Code != http.StatusOK || !j.OK || !strings.Contains(j.Detail, "We got your 2 photos.") || !strings.Contains(j.Detail, "1 couldn’t be used") {
		t.Fatalf("sending: %d %s", w.Code, w.Body)
	}
	got := a.store.SubmittedPhotos()
	if len(got) != 2 || got[0].Name != "Pat Rider" || got[0].Note != "Sunday ride, Point Lookout" || got[0].Batch != got[1].Batch {
		t.Fatalf("sent-in photos %+v", got)
	}
	if _, err := os.Stat(filepath.Join(a.submittedDir(), got[0].File)); err != nil {
		t.Errorf("the photo should be saved: %v", err)
	}
	if w := do(t, h, nil, "GET", "/media/"+got[0].File, "", nil); w.Code == http.StatusOK {
		t.Error("a sent-in photo isn't public until it's used")
	}
	if w := do(t, h, nil, "GET", "/admin/submitted/"+strconv.Itoa(got[0].ID)+"/photo", "", nil); w.Code == http.StatusOK {
		t.Error("only admins can see sent-in photos")
	}

	// Needs attention says there are new ones, until an admin looks.
	attention := func() bool {
		for _, it := range a.attention() {
			if strings.Contains(it.Text, "sent in for the newsletter") {
				return true
			}
		}
		return false
	}
	if !attention() {
		t.Error("new photos should need attention")
	}
	page := do(t, h, admin, "GET", "/admin/submitted", "", nil).Body.String()
	if !strings.Contains(page, "Pat Rider") || !strings.Contains(page, "/admin/submitted/"+strconv.Itoa(got[0].ID)+"/photo") || !strings.Contains(page, ">New<") {
		t.Error("Sent-in photos should list them as new")
	}
	if attention() {
		t.Error("once looked at, they don't need attention")
	}
	if w := do(t, h, admin, "GET", "/admin/submitted/"+strconv.Itoa(got[0].ID)+"/photo?download=1", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Disposition"), "paxvelo-pat-rider-") {
		t.Errorf("download: %d %s", w.Code, w.Header().Get("Content-Disposition"))
	}

	// Add one to an issue's photo strip: it's copied to the public photos.
	is, _ := a.store.SaveIssue(Issue{Month: 11, Year: 2026, Blocks: []Block{{ID: "aaaa", Kind: "story"}, {ID: "bbbb", Kind: "gallery", Photos: []string{"/photos/ride.webp"}}}})
	ct, body := form(url.Values{"do": {"add"}, "issue": {strconv.Itoa(is.ID)}, "id": {strconv.Itoa(got[0].ID)}})
	w = do(t, h, admin, "POST", "/admin/submitted", ct, body)
	if to := w.Header().Get("Location"); !strings.Contains(to, "ok=submitted-added") || !strings.Contains(to, "n=1") {
		t.Fatalf("adding: %s", to)
	}
	is, _ = a.store.Issue(is.ID)
	strip := is.Blocks[1].Photos
	if len(strip) != 2 || !strings.HasPrefix(strip[1], "/media/") {
		t.Fatalf("photo strip %v", strip)
	}
	if w := do(t, h, nil, "GET", strip[1], "", nil); w.Code != http.StatusOK {
		t.Errorf("the used photo should be public now: %d", w.Code)
	}
	if p, _ := a.store.Submitted(got[0].ID); p.Used != strip[1] || p.UsedIn != "November 2026" {
		t.Errorf("used %+v", p)
	}
	if page := do(t, h, admin, "GET", "/admin/submitted?"+url.Values{"ok": {"submitted-added"}, "n": {"1"}, "to": {"November 2026"}}.Encode(), "", nil).Body.String(); !strings.Contains(page, "In November 2026") || !strings.Contains(page, "Added 1 photo to the November 2026 newsletter’s photo strip.") {
		t.Error("the photo should say which issue it's in")
	}

	// Delete the other: its file goes too.
	ct, body = form(url.Values{"do": {"delete"}, "id": {strconv.Itoa(got[1].ID)}})
	do(t, h, admin, "POST", "/admin/submitted", ct, body)
	if _, ok := a.store.Submitted(got[1].ID); ok {
		t.Error("deleted photo is still listed")
	}
	if _, err := os.Stat(filepath.Join(a.submittedDir(), got[1].File)); !os.IsNotExist(err) {
		t.Error("deleted photo's file should be gone")
	}
}

// The page stops taking photos once lots are waiting to be looked at.
func TestSharePhotosWaitingCap(t *testing.T) {
	a, h, _ := testApp(t)
	files := make([]string, maxWaitingPhotos)
	for i := range files {
		files[i] = "x.jpg"
	}
	a.store.AddSubmitted(files, SubmittedPhoto{Name: "Someone"})
	w := sendPhotos(t, h, a, url.Values{"name": {"Pat Rider"}, "agree": {"yes"}}, map[string][]byte{"a.jpg": encodeJPEG(t, halves(120, 90, 255))})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("over the cap: %d", w.Code)
	}
}

// The newsletter's Share your photos block: a box under the photo strip with
// a button to the share page, which new issues keep under their strip.
func TestShareBlock(t *testing.T) {
	a, _, _ := testApp(t)
	is := Issue{Month: 11, Year: 2026, Blocks: []Block{
		{ID: "aaaa", Kind: "gallery", Photos: []string{"/photos/ride.webp"}},
		{ID: "bbbb", Kind: "share", Story: Story{Body: "Tag a friend in the note."}},
	}}
	v, err := a.issueView(is, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := a.site.renderNews(v, is.Path(), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{shareHeading, `href="/share-photos">` + shareButton, "Tag a friend in the note."} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the issue should show %q", want)
		}
	}
	// A new issue after one without the box gets it under the strip.
	next := carryOver([]Block{{Kind: "story"}, {Kind: "gallery"}, {Kind: "rides"}})
	if len(next) != 4 || next[2].Kind != "share" {
		t.Errorf("carried over %+v", next)
	}
	// And keeps its wording.
	if next := carryOver(is.Blocks); len(next) != 2 || next[1].Body != "Tag a friend in the note." {
		t.Errorf("carried over %+v", next)
	}
}

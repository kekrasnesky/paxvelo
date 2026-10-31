package main

import (
	"encoding/json"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Photos members send in for the newsletter, from the public Share your
// photos page (/share-photos). Nobody needs an account or an app: they pick
// photos on their phone, which shrinks them before they upload (site.js), and
// the server checks them and shrinks them again (shrinkPhoto), which also
// drops the hidden location they were taken at. They're kept out of sight, in
// the data folder's "submitted" folder, until an admin looks at them on the
// Email → Sent-in photos screen and adds the good ones to an issue's photo
// strip. The newsletter's "Share your photos" block links to the page.

const (
	maxSharePhotos   = 20       // photos in one go
	maxShareForm     = 64 << 20 // one go, in case the phone couldn't shrink them
	maxWaitingPhotos = 300      // sent in but not yet used or deleted; past this the page asks people to try later
	maxShareNote     = 500      // characters

	shareHeading = "Want to be featured in the newsletter?"
	shareButton  = "Upload your photos here"
)

// SubmittedPhoto is one photo sent in on the share page.
type SubmittedPhoto struct {
	ID        int       `json:"id"`
	Batch     int       `json:"batch"` // photos sent together share one
	File      string    `json:"file"`  // its name in the submitted folder
	Name      string    `json:"name"`
	Email     string    `json:"email,omitempty"`
	Note      string    `json:"note,omitempty"` // what they wrote about the photos
	Submitted time.Time `json:"submitted"`
	IP        string    `json:"ip,omitempty"`
	Seen      bool      `json:"seen,omitempty"`    // an admin has opened Sent-in photos since it came in
	Used      string    `json:"used,omitempty"`    // its address once added to a newsletter
	UsedIn    string    `json:"used_in,omitempty"` // which issue, like "November 2026"
}

func (a *App) submittedDir() string { return filepath.Join(a.cfg.DataDir, "submitted") }

// ---- The share page ----

type shareData struct {
	Token             string
	Name, Email, Note string // what was typed, to show again after a mistake
	Agree             bool
	Message           string // a problem
	Detail            string // after sending: how many came through
	Done, Failed      bool
}

func (a *App) sharePhotosPage(w http.ResponseWriter, r *http.Request) {
	a.site.render(w, http.StatusOK, "share-photos", "/share-photos", "Share Your Photos — "+siteName, shareData{Token: a.formToken()})
}

// sharePhotos receives photos from the share page. With JavaScript it
// answers in JSON and the page updates in place; without, it re-renders the
// page.
func (a *App) sharePhotos(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxShareForm)
	var d shareData
	reply := func(status int) {
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{"ok": d.Done, "message": d.Message, "detail": d.Detail})
			return
		}
		d.Token = a.formToken()
		a.site.render(w, status, "share-photos", "/share-photos", "Share Your Photos — "+siteName, d)
	}
	fail := func(status int, msg string) {
		d.Failed, d.Message = true, msg
		reply(status)
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		fail(http.StatusRequestEntityTooLarge, "Those photos are too big to send together. Try a few at a time.")
		return
	}
	defer r.MultipartForm.RemoveAll()
	d.Name, d.Email, d.Note, d.Agree = r.FormValue("name"), r.FormValue("email"), r.FormValue("note"), r.FormValue("agree") != ""
	if r.FormValue("website") != "" { // the box only bots fill in
		d.Done = true
		reply(http.StatusOK)
		return
	}
	name, note := cleanName(d.Name), strings.TrimSpace(strings.ReplaceAll(d.Note, "\r\n", "\n"))
	email, emailOK := "", true
	if typed := strings.TrimSpace(d.Email); typed != "" {
		email, emailOK = cleanEmail(typed)
	}
	files := r.MultipartForm.File["photos"]
	switch {
	case !a.validFormToken(r.FormValue("ft")):
		fail(http.StatusBadRequest, "This page expired. Reload it and try again.")
	case len(files) == 0:
		fail(http.StatusBadRequest, "Choose at least one photo.")
	case len(files) > maxSharePhotos:
		fail(http.StatusBadRequest, fmt.Sprintf("You can send up to %d photos at a time.", maxSharePhotos))
	case name == "":
		fail(http.StatusBadRequest, "Enter your name, so we know who took them.")
	case !emailOK:
		fail(http.StatusBadRequest, joinProblems["email"])
	case utf8.RuneCountInString(note) > maxShareNote:
		fail(http.StatusBadRequest, fmt.Sprintf("Keep the note under %d characters.", maxShareNote))
	case !d.Agree:
		fail(http.StatusBadRequest, "Tick the box to say you can share these photos with the club.")
	case a.store.WaitingPhotos()+len(files) > maxWaitingPhotos:
		fail(http.StatusServiceUnavailable, "We have a lot of photos waiting to be looked at. Try again in a few days.")
	case !a.limit.Allow("share:"+clientIP(r), 5, time.Hour): // counted once the form is right, so typos never lock anyone out
		fail(http.StatusTooManyRequests, "You’ve sent a lot of photos this hour. Try again later.")
	}
	if d.Failed {
		return
	}

	var saved, skipped []string
	for _, fh := range files {
		file, err := a.saveSubmittedFile(fh)
		if err != nil {
			skipped = append(skipped, err.Error()+".")
		} else if file != "" {
			saved = append(saved, file)
		}
	}
	if len(saved) == 0 {
		fail(http.StatusBadRequest, or(strings.Join(skipped, " "), "Choose at least one photo."))
		return
	}
	if err := a.store.AddSubmitted(saved, SubmittedPhoto{Name: name, Email: email, Note: note, IP: clientIP(r)}); err != nil {
		log.Printf("share photos: %v", err)
		for _, f := range saved {
			os.Remove(filepath.Join(a.submittedDir(), f))
		}
		fail(http.StatusInternalServerError, "Something went wrong on our end. Try again in a minute.")
		return
	}
	d = shareData{Done: true, Name: name}
	d.Detail = fmt.Sprintf("We got your %d %s. We look through them before each newsletter.", len(saved), plural(len(saved), "photo", "photos"))
	if len(skipped) > 0 {
		d.Detail += fmt.Sprintf(" %d couldn’t be used: %s", len(skipped), strings.Join(skipped, " "))
	}
	reply(http.StatusOK)
}

// saveSubmittedFile keeps one sent-in photo in the submitted folder, shrunk
// like any photo, and returns its file name, or "" for an empty file.
func (a *App) saveSubmittedFile(hdr *multipart.FileHeader) (string, error) {
	b, ext, err := readPhotoFile(hdr)
	if err != nil || b == nil {
		return "", err
	}
	if err := os.MkdirAll(a.submittedDir(), 0o755); err != nil {
		return "", err
	}
	name := randHex(8) + ext
	return name, os.WriteFile(filepath.Join(a.submittedDir(), name), b, 0o644)
}

// ---- Admin: Sent-in photos ----

// submittedBatch is the photos one person sent together.
type submittedBatch struct {
	Name, Email, Note string
	Submitted         time.Time
	New               bool
	Photos            []SubmittedPhoto
}

func batches(photos []SubmittedPhoto) []submittedBatch {
	var out []submittedBatch
	for i, p := range photos {
		if i == 0 || photos[i-1].Batch != p.Batch {
			out = append(out, submittedBatch{Name: p.Name, Email: p.Email, Note: p.Note, Submitted: p.Submitted})
		}
		b := &out[len(out)-1]
		b.New = b.New || !p.Seen
		b.Photos = append(b.Photos, p)
	}
	return out
}

func (a *App) submittedPage(w http.ResponseWriter, r *http.Request) {
	photos := a.store.SubmittedPhotos()
	v := adminView{Title: "Sent-in photos", Tab: "submitted", Batches: batches(photos), Issues: a.store.Issues()}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "submitted", v)
	// Shown as new this once.
	if err := a.store.SeeSubmitted(); err != nil {
		log.Printf("sent-in photos: %v", err)
	}
}

// submittedPhoto serves a sent-in photo to admins, or downloads it.
func (a *App) submittedPhoto(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	p, ok := a.store.Submitted(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("download") != "" {
		who := strings.ToLower(strings.Join(strings.FieldsFunc(p.Name, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
		}), "-"))
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="paxvelo-%s-%d%s"`, or(who, "photo"), p.ID, filepath.Ext(p.File)))
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, filepath.Join(a.submittedDir(), p.File))
}

// submittedAction adds the ticked photos to an issue's photo strip, or
// deletes them.
func (a *App) submittedAction(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	var ids []int
	for _, s := range r.Form["id"] {
		if id, err := strconv.Atoi(s); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		back(w, r, "/admin/submitted?err=none-ticked")
		return
	}
	if r.FormValue("do") == "delete" {
		files, err := a.store.DeleteSubmitted(ids)
		if err != nil {
			back(w, r, "/admin/submitted?err=save")
			return
		}
		for _, f := range files {
			os.Remove(filepath.Join(a.submittedDir(), f))
		}
		back(w, r, "/admin/submitted?ok=submitted-deleted")
		return
	}

	issueID, _ := strconv.Atoi(r.FormValue("issue"))
	is, ok := a.store.Issue(issueID)
	if !ok {
		back(w, r, "/admin/submitted?err=gone")
		return
	}
	// The issue's first photo strip, or a new one at the end with the box
	// asking for photos under it.
	strip := slices.IndexFunc(is.Blocks, func(b Block) bool { return b.Kind == "gallery" })
	if strip < 0 {
		is.Blocks = append(is.Blocks, Block{ID: randHex(4), Kind: "gallery"})
		strip = len(is.Blocks) - 1
		if !slices.ContainsFunc(is.Blocks, func(b Block) bool { return b.Kind == "share" }) {
			is.Blocks = append(is.Blocks, Block{ID: randHex(4), Kind: "share"})
		}
	}
	added, full := 0, 0
	var used []SubmittedPhoto
	for _, id := range ids {
		p, ok := a.store.Submitted(id)
		if !ok || (p.Used != "" && containsStr(is.Blocks[strip].Photos, p.Used)) {
			continue
		}
		if len(is.Blocks[strip].Photos) >= maxGallery {
			full++
			continue
		}
		u, err := a.publishSubmitted(p)
		if err != nil {
			log.Printf("sent-in photo %d: %v", id, err)
			back(w, r, "/admin/submitted?err=save")
			return
		}
		is.Blocks[strip].Photos = append(is.Blocks[strip].Photos, u)
		p.Used, p.UsedIn = u, is.Label()
		used = append(used, p)
		added++
	}
	if added > 0 {
		saved, err := a.store.SaveIssue(is)
		if err == nil {
			err = a.store.MarkSubmittedUsed(used)
		}
		if err != nil {
			back(w, r, "/admin/submitted?err=save")
			return
		}
		if saved.IsPublished() {
			a.refreshNewsletter()
		}
	}
	q := url.Values{"ok": {"submitted-added"}, "n": {strconv.Itoa(added)}, "full": {strconv.Itoa(full)}, "to": {is.Label()}, "issue": {strconv.Itoa(is.ID)}}
	back(w, r, "/admin/submitted?"+q.Encode())
}

// publishSubmitted copies a sent-in photo to the public photos, for a
// newsletter, and returns its address.
func (a *App) publishSubmitted(p SubmittedPhoto) (string, error) {
	b, err := os.ReadFile(filepath.Join(a.submittedDir(), p.File))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(a.mediaDir(), 0o755); err != nil {
		return "", err
	}
	name := randHex(8) + filepath.Ext(p.File)
	if err := os.WriteFile(filepath.Join(a.mediaDir(), name), b, 0o644); err != nil {
		return "", err
	}
	return "/media/" + name, nil
}

// submittedAdded is the flash after adding sent-in photos to an issue.
func submittedAdded(q url.Values) string {
	n, _ := strconv.Atoi(q.Get("n"))
	full, _ := strconv.Atoi(q.Get("full"))
	s := fmt.Sprintf("Added %d %s to the %s newsletter’s photo strip. Open the issue to frame them.", n, plural(n, "photo", "photos"), q.Get("to"))
	if n == 0 {
		s = "Those photos are already in the " + q.Get("to") + " newsletter."
	}
	if full > 0 {
		s += fmt.Sprintf(" Its photo strip holds %d, so %d didn’t fit.", maxGallery, full)
	}
	return s
}

// ---- Store ----

// AddSubmitted records photos sent in together, given their file names and
// who sent them.
func (s *Store) AddSubmitted(files []string, from SubmittedPhoto) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.NextSubmitted++
	batch, now := s.d.NextSubmitted, time.Now().UTC()
	for i, f := range files {
		p := from
		if i > 0 {
			s.d.NextSubmitted++
		}
		p.ID, p.Batch, p.File, p.Submitted = s.d.NextSubmitted, batch, f, now
		s.d.SubmittedPhotos = append(s.d.SubmittedPhotos, &p)
	}
	return s.save()
}

// SubmittedPhotos lists the sent-in photos, newest first, keeping each
// batch's photos in the order they were sent.
func (s *Store) SubmittedPhotos() []SubmittedPhoto {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SubmittedPhoto, 0, len(s.d.SubmittedPhotos))
	for _, p := range s.d.SubmittedPhotos {
		out = append(out, *p)
	}
	slices.SortStableFunc(out, func(x, y SubmittedPhoto) int { return y.Batch - x.Batch })
	return out
}

func (s *Store) Submitted(id int) (SubmittedPhoto, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.d.SubmittedPhotos {
		if p.ID == id {
			return *p, true
		}
	}
	return SubmittedPhoto{}, false
}

// WaitingPhotos counts the sent-in photos not yet used in a newsletter.
func (s *Store) WaitingPhotos() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.d.SubmittedPhotos {
		if p.Used == "" {
			n++
		}
	}
	return n
}

// NewSubmitted counts the photos sent in since an admin last looked.
func (s *Store) NewSubmitted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.d.SubmittedPhotos {
		if !p.Seen {
			n++
		}
	}
	return n
}

// SeeSubmitted marks every sent-in photo as looked at.
func (s *Store) SeeSubmitted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, p := range s.d.SubmittedPhotos {
		changed = changed || !p.Seen
		p.Seen = true
	}
	if !changed {
		return nil
	}
	return s.save()
}

// MarkSubmittedUsed records which newsletter sent-in photos went into.
func (s *Store) MarkSubmittedUsed(used []SubmittedPhoto) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range used {
		for _, p := range s.d.SubmittedPhotos {
			if p.ID == u.ID {
				p.Used, p.UsedIn = u.Used, u.UsedIn
			}
		}
	}
	return s.save()
}

// DeleteSubmitted removes sent-in photos and returns their files, for the
// caller to delete. A copy already added to a newsletter stays there.
func (s *Store) DeleteSubmitted(ids []int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var files []string
	s.d.SubmittedPhotos = slices.DeleteFunc(s.d.SubmittedPhotos, func(p *SubmittedPhoto) bool {
		if slices.Contains(ids, p.ID) {
			files = append(files, p.File)
			return true
		}
		return false
	})
	if len(files) == 0 {
		return nil, nil
	}
	return files, s.save()
}

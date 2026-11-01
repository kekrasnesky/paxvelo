package main

import (
	"bytes"
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

// Photos members share for the gallery (and so for the newsletter, which
// picks its photos from the gallery), from the public Share your photos page
// (/share-photos). Nobody needs an account or an app: they pick or drop
// photos, see them as they upload, and can add a caption to each and say
// which album (ride or event) they belong in. The phone shrinks them before
// they upload (site.js), and the server checks them and shrinks them again
// (shrinkPhoto), which also drops the hidden location they were taken at.
// They're kept out of sight, in the data folder's "submitted" folder, until
// an admin reviews them on Website → Photos to review: each person's photos
// are one card, all ticked, and one button approves them into the album
// picked on the card. Rejecting can be undone for 30 days. The newsletter's
// "Share your photos" block links to the page.
//
// Members who know the club PIN (clubpin.go) skip the review: with it, their
// photos go straight into the album they picked, or a new one they named.

const (
	maxSharePhotos   = 30       // photos in one go
	maxShareForm     = 64 << 20 // one go, in case the phone couldn't shrink them
	maxWaitingPhotos = 300      // sent in but not yet used or deleted; past this the page asks people to try later
	maxShareNote     = 500      // characters

	shareHeading = "Got photos from a club ride?"
	shareButton  = "Share your photos"
)

// SubmittedPhoto is one photo sent in on the share page.
type SubmittedPhoto struct {
	ID        int       `json:"id"`
	Batch     int       `json:"batch"` // photos sent together share one
	File      string    `json:"file"`  // its name in the submitted folder
	Name      string    `json:"name"`
	Email     string    `json:"email,omitempty"`
	Note      string    `json:"note,omitempty"`       // what they wrote about the photos
	Caption   string    `json:"caption,omitempty"`    // what they wrote about this one
	Album     int       `json:"album,omitempty"`      // the album they picked
	AlbumName string    `json:"album_name,omitempty"` // or a new one they named
	Ride      string    `json:"ride,omitempty"`       // from before albums (galleryRides)
	Date      string    `json:"date,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	Submitted time.Time `json:"submitted"`
	IP        string    `json:"ip,omitempty"`
	Seen      bool      `json:"seen,omitempty"`    // an admin has opened Photos to review since it came in
	Used      string    `json:"used,omitempty"`    // its address once added to a newsletter (before the newsletter picked from the gallery)
	UsedIn    string    `json:"used_in,omitempty"` // which issue, like "November 2026"
	Gallery   int       `json:"gallery,omitempty"` // its gallery photo's ID, once added
	ViaPIN    bool      `json:"pin,omitempty"`     // sent with the club PIN
	Rejected  time.Time `json:"rejected,omitzero"` // when an admin rejected it; it's deleted rejectKeep later
}

func (a *App) submittedDir() string { return filepath.Join(a.cfg.DataDir, "submitted") }

// ---- The share page ----

type shareData struct {
	Token             string
	Name, Email, Note string // what was typed, to show again after a mistake
	Album, AlbumName  string
	Agree             bool
	Albums            []Album // the latest ones, to pick from
	Max               int
	PINOn             bool   // the club PIN is set
	Message           string // a problem
	Detail            string // after sending: how many came through
	Link              string // and the album they went into, when they skipped the review
	Done, Failed      bool
}

const shareAlbums = 12 // albums to pick from on the share page

func (a *App) shareData(d shareData) shareData {
	d.Token, d.Max, d.PINOn = a.formToken(), maxSharePhotos, a.store.ClubPIN() != ""
	for _, al := range a.store.Albums() {
		if !al.Hidden && len(d.Albums) < shareAlbums {
			d.Albums = append(d.Albums, al)
		}
	}
	return d
}

func (a *App) sharePhotosPage(w http.ResponseWriter, r *http.Request) {
	a.site.render(w, http.StatusOK, "share-photos", "/share-photos", "Share Your Photos — "+siteName, a.shareData(shareData{Album: r.URL.Query().Get("album")}))
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
			json.NewEncoder(w).Encode(map[string]any{"ok": d.Done, "message": d.Message, "detail": d.Detail, "link": d.Link})
			return
		}
		a.site.render(w, status, "share-photos", "/share-photos", "Share Your Photos — "+siteName, a.shareData(d))
	}
	fail := func(status int, msg string) {
		d.Failed, d.Message = true, msg
		reply(status)
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		fail(http.StatusRequestEntityTooLarge, "Those photos are too big to share together. Try a few at a time.")
		return
	}
	defer r.MultipartForm.RemoveAll()
	d.Name, d.Email, d.Note, d.Agree = r.FormValue("name"), r.FormValue("email"), r.FormValue("note"), r.FormValue("agree") != ""
	d.Album, d.AlbumName = r.FormValue("album"), r.FormValue("album_name")
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
	from := SubmittedPhoto{Name: name, Email: email, Note: note, IP: clientIP(r)}
	if id, err := strconv.Atoi(d.Album); err == nil {
		if _, ok := a.store.Album(id); ok {
			from.Album = id
		}
	}
	if from.Album == 0 {
		from.AlbumName = cleanTitle(d.AlbumName, maxAlbumTitle)
	}
	viaPIN, pinProblem := a.checkPIN(r, r.FormValue("pin"))
	from.ViaPIN = viaPIN
	files := r.MultipartForm.File["photos"]
	captions := r.MultipartForm.Value["caption"] // one for each photo, in order, when the page sends them
	switch {
	case !a.validFormToken(r.FormValue("ft")):
		fail(http.StatusBadRequest, "This page expired. Reload it and try again.")
	case len(files) == 0:
		fail(http.StatusBadRequest, "Choose at least one photo.")
	case len(files) > maxSharePhotos:
		fail(http.StatusBadRequest, fmt.Sprintf("You can share up to %d photos at a time.", maxSharePhotos))
	case name == "":
		fail(http.StatusBadRequest, "Enter your name, so we know who took them.")
	case !emailOK:
		fail(http.StatusBadRequest, joinProblems["email"])
	case utf8.RuneCountInString(note) > maxShareNote:
		fail(http.StatusBadRequest, fmt.Sprintf("Keep the note under %d characters.", maxShareNote))
	case !d.Agree:
		fail(http.StatusBadRequest, "Tick the box to say you can share these photos with the club.")
	case pinProblem == pinLimit:
		fail(http.StatusTooManyRequests, pinProblem)
	case pinProblem != "":
		fail(http.StatusBadRequest, pinProblem)
	case a.store.WaitingPhotos()+len(files) > maxWaitingPhotos:
		fail(http.StatusServiceUnavailable, "We have a lot of photos waiting to be looked at. Try again in a few days.")
	case !a.limit.Allow("share:"+clientIP(r), 5, time.Hour): // counted once the form is right, so typos never lock anyone out
		fail(http.StatusTooManyRequests, "You’ve shared a lot of photos this hour. Try again later.")
	}
	if d.Failed {
		return
	}

	var saved []SubmittedPhoto
	var skipped []string
	for i, fh := range files {
		file, err := a.saveSubmittedFile(fh)
		if err != nil {
			skipped = append(skipped, err.Error()+".")
		} else if file != "" {
			p := SubmittedPhoto{File: file}
			if i < len(captions) {
				p.Caption = cleanTitle(captions[i], maxCaption)
			}
			saved = append(saved, p)
		}
	}
	if len(saved) == 0 {
		fail(http.StatusBadRequest, or(strings.Join(skipped, " "), "Choose at least one photo."))
		return
	}
	added, err := a.store.AddSubmitted(saved, from)
	if err != nil {
		log.Printf("share photos: %v", err)
		for _, p := range saved {
			os.Remove(filepath.Join(a.submittedDir(), p.File))
		}
		fail(http.StatusInternalServerError, "Something went wrong on our end. Try again in a minute.")
		return
	}
	d = shareData{Done: true, Name: name}
	n, them := len(saved), plural(len(saved), "photo", "photos")
	switch posted, al := a.postWithPIN(added, from); {
	case posted == n:
		d.Detail, d.Link = fmt.Sprintf("Your %d %s are in the gallery now, in “%s”.", n, them, al.Title), al.Href()
		if n == 1 {
			d.Detail = fmt.Sprintf("Your photo is in the gallery now, in “%s”.", al.Title)
		}
	default:
		d.Detail = fmt.Sprintf("We got your %d %s. An admin looks at every photo before it goes in the gallery, usually within a few days.", n, them)
	}
	if len(skipped) > 0 {
		d.Detail += fmt.Sprintf(" %d couldn’t be used: %s", len(skipped), strings.Join(skipped, " "))
	}
	reply(http.StatusOK)
}

// memberPhotosAlbum is where PIN photos go when the sender picked no album.
const memberPhotosAlbum = "Member photos"

// postWithPIN puts photos sent with the club PIN straight into the
// album picked for them (starting it if they named a new one, or the
// "Member photos" album if they named none), and returns how many went in
// and the album. Any photo that couldn't be added waits for review.
func (a *App) postWithPIN(photos []SubmittedPhoto, from SubmittedPhoto) (int, Album) {
	if !from.ViaPIN {
		return 0, Album{}
	}
	id := from.Album
	if id == 0 {
		var err error
		if id, err = a.albumNamed(or(from.AlbumName, memberPhotosAlbum)); err != nil {
			log.Printf("photos with the PIN: %v", err)
			return 0, Album{}
		}
	}
	al, _ := a.store.Album(id)
	posted := 0
	for _, p := range photos {
		if err := a.publishToAlbum(p, id); err != nil {
			log.Printf("photo %d with the PIN: %v", p.ID, err)
			continue
		}
		posted++
	}
	return posted, al
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

// ---- Admin: Photos to review ----

// rejectKeep is how long a rejected photo is kept, so rejecting can be
// undone. After that it's deleted the next time an admin opens the page.
const rejectKeep = 30 * 24 * time.Hour

// submittedBatch is the photos one person sent together.
type submittedBatch struct {
	Batch             int
	Name, Email, Note string
	Album             int    // the album they picked, or one suggested for them
	AlbumName         string // or the new one they named
	Suggested         bool   // Album is a guess from when they sent them
	Submitted         time.Time
	New               bool
	Waiting           int  // photos not yet approved, rejected or used in a newsletter
	ViaPIN            bool // sent with the club PIN
	Photos            []SubmittedPhoto
}

// Status says where a sent-in photo has gone, or "" while it waits.
func (p SubmittedPhoto) Status() string {
	switch {
	case !p.Rejected.IsZero():
		return "Rejected"
	case p.Used != "" && p.Gallery != 0:
		return "In " + p.UsedIn + " and the gallery"
	case p.Used != "":
		return "In " + p.UsedIn
	case p.Gallery != 0:
		return "In the gallery"
	}
	return ""
}

func batches(photos []SubmittedPhoto) []submittedBatch {
	var out []submittedBatch
	for i, p := range photos {
		if i == 0 || photos[i-1].Batch != p.Batch {
			out = append(out, submittedBatch{Batch: p.Batch, Name: p.Name, Email: p.Email, Note: p.Note,
				Album: p.Album, AlbumName: p.AlbumName, Submitted: p.Submitted, ViaPIN: p.ViaPIN})
		}
		b := &out[len(out)-1]
		b.New = b.New || !p.Seen
		if p.Status() == "" {
			b.Waiting++
		}
		b.Photos = append(b.Photos, p)
	}
	return out
}

// reviewBatches are the cards on Photos to review, newest first, each with
// the album its photos would go in: the one they picked or named, or else
// the album of a ride from just before they sent them.
func (a *App) reviewBatches(albums []Album) []submittedBatch {
	out := batches(a.store.SubmittedPhotos())
	for i := range out {
		b := &out[i]
		if b.Album == 0 && b.AlbumName != "" { // the album they named may be started by now
			if j := slices.IndexFunc(albums, func(al Album) bool { return strings.EqualFold(al.Title, b.AlbumName) }); j >= 0 {
				b.Album = albums[j].ID
			}
		}
		if b.Album == 0 && b.AlbumName == "" {
			b.Album = suggestAlbum(albums, b.Submitted)
			b.Suggested = b.Album != 0
		}
	}
	return out
}

// suggestAlbum is the album of the ride or event nearest before a time,
// up to ten days before (or a day after, for time zones), or 0.
func suggestAlbum(albums []Album, sent time.Time) int {
	best, bestGap := 0, time.Duration(0)
	for _, al := range albums {
		d, err := time.Parse("2006-01-02", al.Date)
		if err != nil {
			continue
		}
		gap := sent.Sub(d)
		if gap < -24*time.Hour || gap > 10*24*time.Hour {
			continue
		}
		gap = max(gap, -gap)
		if best == 0 || gap < bestGap {
			best, bestGap = al.ID, gap
		}
	}
	return best
}

// reviewNote is what a card on Photos to review says after an action: what
// happened, a button to undo it, and links to what's next.
type reviewNote struct {
	Text  string
	Undo  string // the action that undoes it, like "unapprove:12,13"
	Links []noteLink
	code  string // its flash, without JavaScript
}

type noteLink struct{ Label, Href string }

// sentBatchView is one card on Photos to review.
type sentBatchView struct {
	Batch  submittedBatch
	Albums []Album
	Note   *reviewNote
}

func (a *App) submittedPage(w http.ResponseWriter, r *http.Request) {
	if files, err := a.store.PurgeRejected(time.Now().Add(-rejectKeep)); err != nil {
		log.Printf("photos to review: %v", err)
	} else {
		a.removeSubmitted(files)
	}
	v := adminView{Title: "Photos to review", Tab: "submitted", Albums: a.store.Albums()}
	// Cards with photos to review come first; the rest fold away.
	for _, b := range a.reviewBatches(v.Albums) {
		if b.Waiting > 0 {
			v.Batches = append(v.Batches, b)
		} else {
			v.Reviewed = append(v.Reviewed, b)
		}
	}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "submitted", v)
	// Shown as new this once.
	if err := a.store.SeeSubmitted(); err != nil {
		log.Printf("photos to review: %v", err)
	}
}

func (a *App) removeSubmitted(files []string) {
	for _, f := range files {
		os.Remove(filepath.Join(a.submittedDir(), f))
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

// submittedAction acts on one person's photos on Photos to review: it
// approves the ticked ones into the album picked on their card, rejects
// them, or undoes either. The page sends it in the background and swaps in
// the card it answers with; without JavaScript it goes back to the page.
func (a *App) submittedAction(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	do, ids := r.FormValue("do"), tickedIDs(r)
	if u := r.FormValue("undo"); u != "" { // like "unapprove:12,13"
		do, ids = undoAction(u)
	}
	asJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	fail := func(code string) {
		if asJSON {
			replyJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": errorText[code]})
			return
		}
		back(w, r, "/admin/submitted?err="+code)
	}
	if len(ids) == 0 {
		fail("none-ticked")
		return
	}
	var note reviewNote
	flash := url.Values{}
	switch do {
	case "approve":
		res, code := a.approvePhotos(r, ids)
		if code != "" {
			fail(code)
			return
		}
		note = res.note()
		flash = url.Values{"ok": {"gallery-added"}, "n": {strconv.Itoa(res.n)}, "already": {strconv.Itoa(res.already)}}
	case "reject":
		n, err := a.store.RejectSubmitted(ids, time.Now().UTC())
		if err != nil {
			fail("save")
			return
		}
		note = reviewNote{Text: fmt.Sprintf("Rejected %d %s. %s deleted in 30 days.", n, plural(n, "photo", "photos"), plural(n, "It’s", "They’re")),
			Undo: "restore:" + joinIDs(ids)}
		flash.Set("ok", "submitted-rejected")
	case "restore":
		if err := a.store.RestoreSubmitted(ids); err != nil {
			fail("save")
			return
		}
		note = reviewNote{Text: "Put back, ready to review."}
		flash.Set("ok", "submitted-restored")
	case "unapprove":
		var gallery []int
		for _, id := range ids {
			if p, ok := a.store.Submitted(id); ok && p.Gallery != 0 {
				gallery = append(gallery, p.Gallery)
			}
		}
		files, err := a.store.RemoveGallery(gallery)
		if err != nil {
			fail("save")
			return
		}
		a.removeMedia(files)
		note = reviewNote{Text: "Taken back out of the gallery, ready to review again."}
		flash.Set("ok", "submitted-unapproved")
	default:
		fail("none-ticked")
		return
	}
	if !asJSON {
		back(w, r, "/admin/submitted?"+flash.Encode())
		return
	}
	// The card again, as it is now, with the note on top.
	batch, _ := strconv.Atoi(r.FormValue("batch"))
	albums := a.store.Albums()
	var buf bytes.Buffer
	for _, b := range a.reviewBatches(albums) {
		if b.Batch == batch {
			if err := a.site.admin.ExecuteTemplate(&buf, "sent-batch", sentBatchView{b, albums, &note}); err != nil {
				log.Printf("photos to review card: %v", err)
			}
		}
	}
	replyJSON(w, http.StatusOK, map[string]any{"ok": true, "html": buf.String()})
}

// undoAction reads an undo button's value, like "restore:12,13".
func undoAction(v string) (string, []int) {
	do, list, _ := strings.Cut(v, ":")
	var ids []int
	for _, s := range strings.Split(list, ",") {
		if id, err := strconv.Atoi(s); err == nil {
			ids = append(ids, id)
		}
	}
	return do, ids
}

func joinIDs(ids []int) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.Itoa(id)
	}
	return strings.Join(s, ",")
}

func replyJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// publishSubmitted copies a sent-in photo to the public photos and returns
// its address.
func (a *App) publishSubmitted(p SubmittedPhoto) (string, error) {
	return a.copyToMedia(filepath.Join(a.submittedDir(), p.File))
}

// copyToMedia copies a photo file to the public photos under a new name and
// returns its address, so each place using a photo has its own copy and
// taking it out of one place leaves the others alone.
func (a *App) copyToMedia(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(a.mediaDir(), 0o755); err != nil {
		return "", err
	}
	name := randHex(8) + filepath.Ext(path)
	if err := os.WriteFile(filepath.Join(a.mediaDir(), name), b, 0o644); err != nil {
		return "", err
	}
	return "/media/" + name, nil
}

// ---- Store ----

// AddSubmitted records photos sent in together, given each one's file and
// caption, and who sent them, and returns them as recorded.
func (s *Store) AddSubmitted(photos []SubmittedPhoto, from SubmittedPhoto) ([]SubmittedPhoto, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.NextSubmitted++
	batch, now := s.d.NextSubmitted, time.Now().UTC()
	var out []SubmittedPhoto
	for i, f := range photos {
		p := from
		if i > 0 {
			s.d.NextSubmitted++
		}
		p.ID, p.Batch, p.File, p.Caption, p.Submitted = s.d.NextSubmitted, batch, f.File, f.Caption, now
		s.d.SubmittedPhotos = append(s.d.SubmittedPhotos, &p)
		out = append(out, p)
	}
	return out, s.save()
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

// WaitingPhotos counts the sent-in photos still waiting for review: not yet
// approved, rejected, or used in a newsletter.
func (s *Store) WaitingPhotos() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.d.SubmittedPhotos {
		if p.Status() == "" {
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

// RejectSubmitted marks sent-in photos rejected, leaving any already in the
// gallery alone, and returns how many it marked.
func (s *Store) RejectSubmitted(ids []int, at time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.d.SubmittedPhotos {
		if slices.Contains(ids, p.ID) && p.Gallery == 0 && p.Rejected.IsZero() {
			p.Rejected = at
			n++
		}
	}
	return n, s.save()
}

// RestoreSubmitted puts rejected photos back for review.
func (s *Store) RestoreSubmitted(ids []int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.d.SubmittedPhotos {
		if slices.Contains(ids, p.ID) {
			p.Rejected = time.Time{}
		}
	}
	return s.save()
}

// PurgeRejected removes the photos rejected before a time and returns their
// files, for the caller to delete.
func (s *Store) PurgeRejected(before time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var files []string
	s.d.SubmittedPhotos = slices.DeleteFunc(s.d.SubmittedPhotos, func(p *SubmittedPhoto) bool {
		if !p.Rejected.IsZero() && p.Rejected.Before(before) {
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

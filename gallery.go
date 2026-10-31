package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// The public photo gallery (/gallery): photos members sent in on the Share
// your photos page that an admin picked for it on Email → Sent-in photos.
// Whoever sends photos says which club ride they're from and can add their
// own tags ("Point Lookout", "Halloween"), and the gallery can be narrowed to
// one ride or one tag. On Website → Gallery admins can also upload a whole
// album at once (bulk upload), change photos' ride and tags, or take them out.

const (
	galleryPage = 48 // photos a page
	maxTags     = 6  // on one photo
	maxTagLen   = 30 // characters

	maxBulkPhotos = 200       // in one bulk upload
	maxBulkForm   = 256 << 20 // one request; the page sends big albums a few photos at a time
	minDiskFree   = 2 << 30   // bulk uploads stop while the server has less free
)

// galleryRide is a club ride a photo can be from.
type galleryRide struct {
	Key, Short, Name string
}

var galleryRides = []galleryRide{
	{"tnr", "TNR", "Tuesday Night Ride (TNR)"},
	{"thnr", "ThNR", "Thursday Night Ride (ThNR)"},
	{"samr", "SaMR", "Saturday Morning Ride (SaMR)"},
	{"sumr", "SuMR", "Sunday Morning Ride (SuMR)"},
	{"shop", "Shop Ride", "Old Line shop ride"},
	{"other", "Other ride", "Another ride"},
}

func rideFor(key string) (galleryRide, bool) {
	i := slices.IndexFunc(galleryRides, func(r galleryRide) bool { return r.Key == key })
	if i < 0 {
		return galleryRide{}, false
	}
	return galleryRides[i], true
}

// cleanRide returns key if it's one of galleryRides, or "".
func cleanRide(key string) string {
	if _, ok := rideFor(key); ok {
		return key
	}
	return ""
}

// parseTags splits typed tags on commas, tidies their spaces, and drops
// repeats (ignoring case), keeping the first maxTags. It says so when a tag
// is too long.
func parseTags(s string) ([]string, error) {
	var out []string
	for _, t := range strings.Split(s, ",") {
		t = strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(t), "#")), " ")
		if t == "" || slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, t) }) {
			continue
		}
		if utf8.RuneCountInString(t) > maxTagLen {
			return nil, fmt.Errorf("Keep each tag under %d characters.", maxTagLen)
		}
		if len(out) == maxTags {
			return nil, fmt.Errorf("Add up to %d tags.", maxTags)
		}
		out = append(out, t)
	}
	return out, nil
}

// cleanRideDate checks a typed ride date (from a date box, 2026-10-28):
// "" when none was typed, an error when it isn't a real date from the club's
// time or is in the future.
func (a *App) cleanRideDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	d, err := time.ParseInLocation("2006-01-02", s, a.cfg.Location)
	if err != nil || d.Year() < 2000 || d.After(a.today().AddDate(0, 0, 1)) {
		return "", fmt.Errorf("Enter the date of the ride, or leave it empty.")
	}
	return s, nil
}

// GalleryPhoto is one photo in the public gallery.
type GalleryPhoto struct {
	ID    int       `json:"id"`
	Src   string    `json:"src"` // its address, under /media/
	Ride  string    `json:"ride,omitempty"`
	Date  string    `json:"date,omitempty"` // of the ride, 2026-10-28
	Tags  []string  `json:"tags,omitempty"`
	By    string    `json:"by"`
	From  int       `json:"from,omitempty"` // the sent-in photo it came from
	Added time.Time `json:"added"`
}

// RideLabel is the ride's short name, like "TNR".
func (p GalleryPhoto) RideLabel() string {
	r, _ := rideFor(p.Ride)
	return r.Short
}

// DateLabel is the ride's date, like "Oct 28, 2026".
func (p GalleryPhoto) DateLabel() string {
	d, err := time.Parse("2006-01-02", p.Date)
	if err != nil {
		return ""
	}
	return d.Format("Jan 2, 2006")
}

func (p GalleryPhoto) hasTag(tag string) bool {
	return slices.ContainsFunc(p.Tags, func(t string) bool { return strings.EqualFold(t, tag) })
}

// ---- The public page ----

type galleryFilter struct {
	Label, Href string
	Count       int
	On          bool
}

type galleryView struct {
	Photos      []GalleryPhoto
	Total       int // photos matching the filter
	Rides, Tags []galleryFilter
	All         galleryFilter
	Ride, Tag   string // the filter, as a heading: "TNR", "Point Lookout"
	Newer       string // links to the next and previous pages
	Older       string
}

// galleryHref is the gallery's address with a ride or tag picked, and a page.
func galleryHref(ride, tag string, page int) string {
	q := url.Values{}
	if ride != "" {
		q.Set("ride", ride)
	}
	if tag != "" {
		q.Set("tag", tag)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return "/gallery"
	}
	return "/gallery?" + q.Encode()
}

func (a *App) galleryPage(w http.ResponseWriter, r *http.Request) {
	all := a.store.GalleryPhotos()
	ride := cleanRide(r.URL.Query().Get("ride"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)

	v := galleryView{All: galleryFilter{Label: "All photos", Href: "/gallery", Count: len(all), On: ride == "" && tag == ""}}
	// A button for each ride with photos, in the order of galleryRides, and
	// each tag, most used first.
	rides, tags, tagNames := map[string]int{}, map[string]int{}, map[string]string{}
	for _, p := range all {
		if p.Ride != "" {
			rides[p.Ride]++
		}
		for _, t := range p.Tags {
			k := strings.ToLower(t)
			tags[k]++
			if _, ok := tagNames[k]; !ok {
				tagNames[k] = t
			}
		}
	}
	for _, gr := range galleryRides {
		if n := rides[gr.Key]; n > 0 {
			v.Rides = append(v.Rides, galleryFilter{Label: gr.Short, Href: galleryHref(gr.Key, "", 1), Count: n, On: ride == gr.Key})
		}
	}
	for k, n := range tags {
		v.Tags = append(v.Tags, galleryFilter{Label: tagNames[k], Href: galleryHref("", tagNames[k], 1), Count: n, On: strings.EqualFold(tag, k)})
	}
	slices.SortFunc(v.Tags, func(x, y galleryFilter) int {
		if x.Count != y.Count {
			return y.Count - x.Count
		}
		return strings.Compare(strings.ToLower(x.Label), strings.ToLower(y.Label))
	})

	var shown []GalleryPhoto
	for _, p := range all {
		if (ride == "" || p.Ride == ride) && (tag == "" || p.hasTag(tag)) {
			shown = append(shown, p)
		}
	}
	if ride != "" {
		v.Ride = v.Rides[slices.IndexFunc(v.Rides, func(f galleryFilter) bool { return f.On })].Label
	}
	if tag != "" {
		v.Tag = tag
		if k := strings.ToLower(tag); tagNames[k] != "" {
			v.Tag = tagNames[k]
		}
	}
	v.Total = len(shown)
	from := (page - 1) * galleryPage
	if from >= len(shown) && page > 1 {
		http.Redirect(w, r, galleryHref(ride, tag, 1), http.StatusSeeOther)
		return
	}
	v.Photos = shown[from:min(from+galleryPage, len(shown))]
	if page > 1 {
		v.Newer = galleryHref(ride, tag, page-1)
	}
	if from+galleryPage < len(shown) {
		v.Older = galleryHref(ride, tag, page+1)
	}
	a.site.render(w, http.StatusOK, "gallery", "/gallery", "Photo Gallery — "+siteName, v)
}

// ---- Admin: adding sent-in photos ----

// addToGallery copies the ticked sent-in photos into the gallery, with the
// ride, date and tags the admin left on each one's batch.
func (a *App) addToGallery(w http.ResponseWriter, r *http.Request, ids []int) {
	added, already := 0, 0
	for _, id := range ids {
		p, ok := a.store.Submitted(id)
		if !ok {
			continue
		}
		if p.Gallery != 0 {
			already++
			continue
		}
		b := strconv.Itoa(p.Batch)
		ride := cleanRide(r.FormValue("ride-" + b))
		date, err := a.cleanRideDate(r.FormValue("date-" + b))
		if err != nil {
			back(w, r, "/admin/submitted?err=gallery-date")
			return
		}
		tags, err := parseTags(r.FormValue("tags-" + b))
		if err != nil {
			back(w, r, "/admin/submitted?err=gallery-tags")
			return
		}
		src, err := a.publishSubmitted(p)
		if err != nil {
			log.Printf("sent-in photo %d: %v", id, err)
			back(w, r, "/admin/submitted?err=save")
			return
		}
		if err := a.store.AddGallery(GalleryPhoto{Src: src, Ride: ride, Date: date, Tags: tags, By: p.Name, From: p.ID}); err != nil {
			os.Remove(filepath.Join(a.mediaDir(), strings.TrimPrefix(src, "/media/")))
			back(w, r, "/admin/submitted?err=save")
			return
		}
		added++
	}
	q := url.Values{"ok": {"gallery-added"}, "n": {strconv.Itoa(added)}, "already": {strconv.Itoa(already)}}
	back(w, r, "/admin/submitted?"+q.Encode())
}

// galleryAdded is the flash after adding sent-in photos to the gallery.
func galleryAdded(q url.Values) string {
	n, _ := strconv.Atoi(q.Get("n"))
	already, _ := strconv.Atoi(q.Get("already"))
	s := fmt.Sprintf("Added %d %s to the gallery.", n, plural(n, "photo", "photos"))
	if n == 0 {
		s = "Those photos are already in the gallery."
	} else if already > 0 {
		s += fmt.Sprintf(" %d %s already there.", already, plural(already, "was", "were"))
	}
	return s
}

// ---- Admin: Website → Gallery ----

func (a *App) galleryAdminPage(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Gallery", Tab: "gallery", Gallery: a.store.GalleryPhotos(), Rides: galleryRides}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "gallery", v)
}

// galleryUpload is the bulk upload: an admin adds photos straight to the
// gallery, all with the same ride, date, tags and photographer. Each is
// checked and shrunk like any upload. With JavaScript the page shrinks the
// photos first and sends them a few at a time, answering in JSON; without, it
// sends them all at once.
func (a *App) galleryUpload(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(5 * time.Minute)) // an album can be big without JavaScript
	rc.SetWriteDeadline(time.Now().Add(5 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, maxBulkForm)
	asJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	reply := func(code string, added int, skipped []string) {
		if asJSON {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			if code != "" {
				w.WriteHeader(http.StatusBadRequest)
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": code == "", "message": errorText[code], "added": added, "skipped": skipped})
			return
		}
		if code != "" {
			back(w, r, "/admin/gallery?err="+code)
			return
		}
		q := url.Values{"ok": {"gallery-uploaded"}, "n": {strconv.Itoa(added)}, "skipped": {strings.Join(skipped, " ")}}
		back(w, r, "/admin/gallery?"+q.Encode())
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		reply("bigupload", 0, nil)
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["photos"]
	ride := cleanRide(r.FormValue("ride"))
	date, dateErr := a.cleanRideDate(r.FormValue("date"))
	tags, tagsErr := parseTags(r.FormValue("tags"))
	by := cleanName(r.FormValue("by"))
	switch free := diskFree(a.cfg.DataDir); {
	case len(files) == 0:
		reply("none-chosen", 0, nil)
		return
	case len(files) > maxBulkPhotos:
		reply("toomany", 0, nil)
		return
	case by == "":
		reply("gallery-by", 0, nil)
		return
	case dateErr != nil:
		reply("gallery-date", 0, nil)
		return
	case tagsErr != nil:
		reply("gallery-tags", 0, nil)
		return
	case free >= 0 && free < minDiskFree:
		log.Printf("gallery upload refused: %d MB free", free>>20)
		reply("diskfull", 0, nil)
		return
	}
	added := 0
	var skipped []string
	for _, fh := range files {
		src, err := a.savePhotoFile(fh)
		if err != nil {
			skipped = append(skipped, err.Error()+".")
			continue
		}
		if src == "" {
			continue
		}
		if err := a.store.AddGallery(GalleryPhoto{Src: src, Ride: ride, Date: date, Tags: tags, By: by}); err != nil {
			log.Printf("gallery upload: %v", err)
			os.Remove(filepath.Join(a.mediaDir(), strings.TrimPrefix(src, "/media/")))
			reply("save", added, skipped)
			return
		}
		added++
	}
	reply("", added, skipped)
}

// galleryUploaded is the flash after a bulk upload.
func galleryUploaded(q url.Values) string {
	n, _ := strconv.Atoi(q.Get("n"))
	s := fmt.Sprintf("Added %d %s to the gallery.", n, plural(n, "photo", "photos"))
	if sk := q.Get("skipped"); sk != "" {
		s += " Some couldn’t be used: " + sk
	}
	return s
}

// galleryAction changes the ticked photos' ride or tags, or takes them out
// of the gallery.
func (a *App) galleryAction(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	var ids []int
	for _, s := range r.Form["id"] {
		if id, err := strconv.Atoi(s); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		back(w, r, "/admin/gallery?err=none-ticked")
		return
	}
	if r.FormValue("do") == "remove" {
		files, err := a.store.RemoveGallery(ids)
		if err != nil {
			back(w, r, "/admin/gallery?err=save")
			return
		}
		for _, f := range files {
			os.Remove(filepath.Join(a.mediaDir(), strings.TrimPrefix(f, "/media/")))
		}
		back(w, r, "/admin/gallery?ok=gallery-removed")
		return
	}
	add, err := parseTags(r.FormValue("add"))
	if err != nil {
		back(w, r, "/admin/gallery?err=gallery-tags")
		return
	}
	drop, _ := parseTags(r.FormValue("drop"))
	ride := r.FormValue("ride")
	if ride != "keep" {
		ride = cleanRide(ride)
	}
	if err := a.store.RetagGallery(ids, ride, add, drop); err == errTooManyTags {
		back(w, r, "/admin/gallery?err=gallery-tags")
		return
	} else if err != nil {
		back(w, r, "/admin/gallery?err=save")
		return
	}
	back(w, r, "/admin/gallery?ok=gallery-saved")
}

// ---- Store ----

var errTooManyTags = errors.New("too many tags")

// AddGallery puts a photo in the gallery and notes it on the sent-in photo
// it came from.
func (s *Store) AddGallery(p GalleryPhoto) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.NextGallery++
	p.ID, p.Added = s.d.NextGallery, time.Now().UTC()
	s.d.Gallery = append(s.d.Gallery, &p)
	for _, sp := range s.d.SubmittedPhotos {
		if sp.ID == p.From {
			sp.Gallery = p.ID
		}
	}
	return s.save()
}

// GalleryPhotos lists the gallery, newest ride first. Photos with no ride
// date (often old albums) come after the dated ones, newest added first.
func (s *Store) GalleryPhotos() []GalleryPhoto {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]GalleryPhoto, 0, len(s.d.Gallery))
	for _, p := range s.d.Gallery {
		out = append(out, *p)
	}
	slices.SortStableFunc(out, func(x, y GalleryPhoto) int {
		if (x.Date == "") != (y.Date == "") {
			if x.Date == "" {
				return 1
			}
			return -1
		}
		if c := strings.Compare(y.Date, x.Date); c != 0 {
			return c
		}
		return y.ID - x.ID
	})
	return out
}

// RetagGallery sets the photos' ride (unless it's "keep") and adds and drops
// tags. Nothing changes if a photo would end up with too many tags.
func (s *Store) RetagGallery(ids []int, ride string, add, drop []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	retag := func(tags []string) []string {
		tags = slices.DeleteFunc(slices.Clone(tags), func(t string) bool {
			return slices.ContainsFunc(drop, func(d string) bool { return strings.EqualFold(d, t) })
		})
		for _, t := range add {
			if !slices.ContainsFunc(tags, func(o string) bool { return strings.EqualFold(o, t) }) {
				tags = append(tags, t)
			}
		}
		return tags
	}
	for _, p := range s.d.Gallery {
		if slices.Contains(ids, p.ID) && len(retag(p.Tags)) > maxTags {
			return errTooManyTags
		}
	}
	for _, p := range s.d.Gallery {
		if slices.Contains(ids, p.ID) {
			if ride != "keep" {
				p.Ride = ride
			}
			p.Tags = retag(p.Tags)
		}
	}
	return s.save()
}

// RemoveGallery takes photos out of the gallery and returns their addresses,
// for the caller to delete their files. The sent-in photos they came from
// can be added again.
func (s *Store) RemoveGallery(ids []int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var files []string
	s.d.Gallery = slices.DeleteFunc(s.d.Gallery, func(p *GalleryPhoto) bool {
		if slices.Contains(ids, p.ID) {
			files = append(files, p.Src)
			return true
		}
		return false
	})
	for _, sp := range s.d.SubmittedPhotos {
		if slices.Contains(ids, sp.Gallery) {
			sp.Gallery = 0
		}
	}
	if len(files) == 0 {
		return nil, nil
	}
	return files, s.save()
}

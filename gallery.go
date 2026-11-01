package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The public photo gallery (/gallery): albums of club photos, one per ride or
// event, each with a cover photo. It is where the club's photos live: the
// newsletter picks its photos from it. Photos get in two ways: members share
// them on the Share your photos page and an admin approves them into an
// album on Website → Photos to review, or an admin uploads straight into an
// album on Website → Gallery (galleryadmin.go). Each photo is kept at its full size
// (at most 1600 pixels, see shrinkPhoto) and narrower copies beside it
// (galleryWidths), so the grid loads small files and the viewer a big one.
// None of them keep where the photo was taken.

const (
	galleryPage   = 48 // photos a page in an album; "Load more" fetches the next
	maxAlbumTitle = 80 // characters
	maxCaption    = 200

	maxBulkPhotos = 200       // in one bulk upload
	maxBulkForm   = 256 << 20 // one request; the page sends big albums a few photos at a time
	minDiskFree   = 2 << 30   // uploads stop while the server has less free
)

// galleryWidths are the narrower copies made of each gallery photo.
var galleryWidths = []int{480, 960}

// galleryRide is a club ride photos used to be sorted by, before albums.
// Old photos' albums are named after them (albumize).
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

// cleanRideDate checks a typed date (from a date box, 2026-10-28): "" when
// none was typed, an error when it isn't a real date from the club's time or
// is in the future.
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

// cleanTitle tidies an album title or caption's spaces and cuts it to n
// characters.
func cleanTitle(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		s = strings.TrimSpace(string([]rune(s)[:n]))
	}
	return s
}

func dateLabel(date string) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	return d.Format("Jan 2, 2006")
}

// Album is a set of gallery photos, like one ride's. Albums show in the
// order admins put them in, newest added first.
type Album struct {
	ID      int       `json:"id"`
	Title   string    `json:"title"`
	Date    string    `json:"date,omitempty"`  // of the ride or event, 2026-10-28
	Cover   int       `json:"cover,omitempty"` // a photo's ID; the album's first photo when 0 or gone
	Created time.Time `json:"created"`
}

// Href is the album's address, like /gallery/12-halloween-ride. Only the
// number counts, so a renamed album's old links still work.
func (al Album) Href() string {
	if s := slugFor(al.Title); s != "" {
		return "/gallery/" + strconv.Itoa(al.ID) + "-" + s
	}
	return "/gallery/" + strconv.Itoa(al.ID)
}

func (al Album) DateLabel() string { return dateLabel(al.Date) }

// GalleryPhoto is one photo in the public gallery.
type GalleryPhoto struct {
	ID      int       `json:"id"`
	Album   int       `json:"album,omitempty"`
	Src     string    `json:"src"` // the full size, under /media/
	W       int       `json:"w,omitempty"`
	H       int       `json:"h,omitempty"`
	Sized   bool      `json:"sized,omitempty"` // has its narrower copies
	Caption string    `json:"caption,omitempty"`
	By      string    `json:"by"`
	From    int       `json:"from,omitempty"` // the sent-in photo it came from
	Added   time.Time `json:"added"`
	// From before albums: kept for naming old photos' albums.
	Ride string   `json:"ride,omitempty"`
	Date string   `json:"date,omitempty"`
	Tags []string `json:"tags,omitempty"`
}

// copySrc is the address of the photo's copy w pixels wide, beside it:
// /media/abc.jpg → /media/abc-480.jpg.
func copySrc(src string, w int) string {
	return strings.TrimSuffix(src, filepath.Ext(src)) + "-" + strconv.Itoa(w) + ".jpg"
}

// Thumb is the copy for a grid square: the 480 pixel one, or the photo.
func (p GalleryPhoto) Thumb() string {
	if p.Sized && p.W > galleryWidths[0] {
		return copySrc(p.Src, galleryWidths[0])
	}
	return p.Src
}

// Srcset lists the photo's copies for a browser to pick from by screen size.
func (p GalleryPhoto) Srcset() string {
	if !p.Sized || p.W == 0 {
		return ""
	}
	var parts []string
	for _, w := range galleryWidths {
		if w < p.W {
			parts = append(parts, fmt.Sprintf("%s %dw", copySrc(p.Src, w), w))
		}
	}
	return strings.Join(append(parts, fmt.Sprintf("%s %dw", p.Src, p.W)), ", ")
}

// Alt describes the photo to someone who can't see it.
func (p GalleryPhoto) Alt() string {
	return or(p.Caption, "Club ride photo") + " — photo by " + or(p.By, "a member")
}

// Download is the file name it saves as.
func (p GalleryPhoto) Download() string {
	return "paxvelo-" + strconv.Itoa(p.ID) + filepath.Ext(p.Src)
}

// files are all the photo's files under /media/.
func (p GalleryPhoto) files() []string {
	out := []string{p.Src}
	for _, w := range galleryWidths {
		out = append(out, copySrc(p.Src, w))
	}
	return out
}

func (a *App) removeMedia(srcs []string) {
	for _, src := range srcs {
		os.Remove(filepath.Join(a.mediaDir(), strings.TrimPrefix(src, "/media/")))
	}
}

// ---- The public pages ----

type albumCard struct {
	Album
	Cover GalleryPhoto
	Count int
}

type galleryView struct {
	Albums []albumCard
}

// galleryPage is the albums, as a grid of covers.
func (a *App) galleryPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" { // the old ride and tag filters
		http.Redirect(w, r, "/gallery", http.StatusMovedPermanently)
		return
	}
	a.site.render(w, http.StatusOK, "gallery", "/gallery", "Photo Gallery — "+siteName, galleryView{Albums: a.store.AlbumCards()})
}

type albumView struct {
	Album  Album
	Photos []GalleryPhoto
	Total  int
	First  int    // the number of the first photo shown, from 1
	More   string // the next page's address
}

// albumFrom finds the album in an address like /gallery/12-halloween-ride.
func (a *App) albumFrom(r *http.Request) (Album, bool) {
	n, _, _ := strings.Cut(r.PathValue("album"), "-")
	id, err := strconv.Atoi(n)
	if err != nil {
		return Album{}, false
	}
	return a.store.Album(id)
}

// albumPage is one album's photos, a page at a time.
func (a *App) albumPage(w http.ResponseWriter, r *http.Request) {
	al, ok := a.albumFrom(r)
	if !ok {
		a.site.notFound(w, r)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	if "/gallery/"+r.PathValue("album") != al.Href() {
		to := al.Href()
		if page > 1 {
			to += "?page=" + strconv.Itoa(page)
		}
		http.Redirect(w, r, to, http.StatusMovedPermanently)
		return
	}
	photos := a.store.AlbumPhotos(al.ID)
	from := (page - 1) * galleryPage
	if len(photos) == 0 || from >= len(photos) && page > 1 {
		if page > 1 {
			http.Redirect(w, r, al.Href(), http.StatusSeeOther)
		} else {
			a.site.notFound(w, r) // an album shows once it has photos
		}
		return
	}
	v := albumView{Album: al, Photos: photos[from:min(from+galleryPage, len(photos))], Total: len(photos), First: from + 1}
	if from+galleryPage < len(photos) {
		v.More = al.Href() + "?page=" + strconv.Itoa(page+1)
	}
	a.site.render(w, http.StatusOK, "album", al.Href(), al.Title+" — Photo Gallery — "+siteName, v)
}

type photoView struct {
	Album       Album
	Photo       GalleryPhoto
	N, Total    int
	Prev, Next  string
	AlbumAtPage string // the album page it's on
}

func (v photoView) OGImage() string { return v.Photo.Src }

// photoPage is one photo on its own page: where a shared link goes.
func (a *App) photoPage(w http.ResponseWriter, r *http.Request) {
	al, ok := a.albumFrom(r)
	id, _ := strconv.Atoi(r.PathValue("photo"))
	photos := a.store.AlbumPhotos(al.ID)
	i := slices.IndexFunc(photos, func(p GalleryPhoto) bool { return p.ID == id })
	if !ok || i < 0 {
		a.site.notFound(w, r)
		return
	}
	href := func(p GalleryPhoto) string { return al.Href() + "/" + strconv.Itoa(p.ID) }
	if self := href(photos[i]); r.URL.Path != self {
		http.Redirect(w, r, self, http.StatusMovedPermanently)
		return
	}
	v := photoView{Album: al, Photo: photos[i], N: i + 1, Total: len(photos), AlbumAtPage: al.Href()}
	if pg := i/galleryPage + 1; pg > 1 {
		v.AlbumAtPage += "?page=" + strconv.Itoa(pg)
	}
	if i > 0 {
		v.Prev = href(photos[i-1])
	}
	if i+1 < len(photos) {
		v.Next = href(photos[i+1])
	}
	title := or(photos[i].Caption, "Photo by "+photos[i].By)
	a.site.render(w, http.StatusOK, "photo", href(photos[i]), title+" — "+al.Title+" — "+siteName, v)
}

// ---- Adding photos ----

// addGalleryFile puts a photo file already saved under /media/ into an album,
// making its narrower copies first. On a failure the files are removed.
func (a *App) addGalleryFile(p GalleryPhoto) error {
	p.W, p.H, p.Sized = a.makeCopies(p.Src)
	if err := a.store.AddGallery(p); err != nil {
		a.removeMedia(p.files())
		return err
	}
	return nil
}

// makeCopies writes the narrower copies of a photo under /media/, and returns
// its size and whether they were made.
func (a *App) makeCopies(src string) (w, h int, sized bool) {
	b, err := os.ReadFile(filepath.Join(a.mediaDir(), strings.TrimPrefix(src, "/media/")))
	if err != nil {
		return 0, 0, false
	}
	copies, w, h := smallerCopies(b)
	if copies == nil {
		return w, h, false
	}
	for cw, c := range copies {
		if err := os.WriteFile(filepath.Join(a.mediaDir(), strings.TrimPrefix(copySrc(src, cw), "/media/")), c, 0o644); err != nil {
			log.Printf("gallery copy of %s: %v", src, err)
			return w, h, false
		}
	}
	return w, h, true
}

// sizeOldPhotos makes the narrower copies for photos added before there were
// any, in the background at startup.
func (a *App) sizeOldPhotos() {
	for _, p := range a.store.UnsizedPhotos() {
		w, h, sized := a.makeCopies(p.Src)
		if w == 0 {
			continue
		}
		if err := a.store.SetPhotoSize(p.ID, w, h, sized); err != nil {
			log.Printf("gallery sizes: %v", err)
			return
		}
	}
}

// ---- Store ----

var errNotFound = errors.New("not found")

// albumize puts photos from before albums into albums: one for each ride
// and date, like "TNR · Oct 28, 2026", newest first, and undated ones in
// "Older photos". Callers must hold s.mu (it runs as the store opens).
func (s *Store) albumize() {
	type key struct{ ride, date string }
	var keys []key
	for _, p := range s.d.Gallery {
		if p.Album != 0 {
			continue
		}
		k := key{p.Ride, p.Date}
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return
	}
	slices.SortStableFunc(keys, func(x, y key) int {
		if (x.date == "") != (y.date == "") {
			if x.date == "" {
				return 1
			}
			return -1
		}
		return strings.Compare(y.date, x.date)
	})
	made := make(map[key]int)
	var albums []*Album
	for _, k := range keys {
		r, _ := rideFor(k.ride)
		title := r.Short
		switch {
		case k.date != "" && title != "":
			title += " · " + dateLabel(k.date)
		case k.date != "":
			title = dateLabel(k.date)
		case title == "":
			title = "Older photos"
		}
		s.d.NextAlbum++
		albums = append(albums, &Album{ID: s.d.NextAlbum, Title: title, Date: k.date, Created: time.Now().UTC()})
		made[k] = s.d.NextAlbum
	}
	s.d.Albums = append(s.d.Albums, albums...)
	for _, p := range s.d.Gallery {
		if p.Album == 0 {
			p.Album = made[key{p.Ride, p.Date}]
		}
	}
}

// Albums lists the albums in the order admins put them in.
func (s *Store) Albums() []Album {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Album, 0, len(s.d.Albums))
	for _, al := range s.d.Albums {
		out = append(out, *al)
	}
	return out
}

func (s *Store) Album(id int) (Album, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, al := range s.d.Albums {
		if al.ID == id {
			return *al, true
		}
	}
	return Album{}, false
}

// AlbumCards lists each album with photos, with its cover and count. With
// empty set, empty albums are listed too (for admins).
func (s *Store) AlbumCards(empty ...bool) []albumCard {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []albumCard
	for _, al := range s.d.Albums {
		c := albumCard{Album: *al}
		for _, p := range s.d.Gallery {
			if p.Album != al.ID {
				continue
			}
			if c.Count == 0 || p.ID == al.Cover {
				c.Cover = *p
			}
			c.Count++
		}
		if c.Count > 0 || len(empty) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// AddAlbum starts an album at the top of the list.
func (s *Store) AddAlbum(title, date string) (Album, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.NextAlbum++
	al := &Album{ID: s.d.NextAlbum, Title: title, Date: date, Created: time.Now().UTC()}
	s.d.Albums = append([]*Album{al}, s.d.Albums...)
	return *al, s.save()
}

// SaveAlbum renames an album and changes its date.
func (s *Store) SaveAlbum(id int, title, date string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, al := range s.d.Albums {
		if al.ID == id {
			al.Title, al.Date = title, date
			return s.save()
		}
	}
	return errNotFound
}

// MoveAlbum moves an album one place up (-1) or down (+1) the list.
func (s *Store) MoveAlbum(id, by int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.d.Albums, func(al *Album) bool { return al.ID == id })
	j := i + by
	if i < 0 || j < 0 || j >= len(s.d.Albums) {
		return nil
	}
	s.d.Albums[i], s.d.Albums[j] = s.d.Albums[j], s.d.Albums[i]
	return s.save()
}

// DeleteAlbum removes an album. Its photos move to the end of album moveTo,
// or with moveTo 0 are removed too, and their files returned for the caller
// to delete.
func (s *Store) DeleteAlbum(id, moveTo int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if moveTo == id || moveTo != 0 && !slices.ContainsFunc(s.d.Albums, func(al *Album) bool { return al.ID == moveTo }) {
		return nil, errNotFound
	}
	s.d.Albums = slices.DeleteFunc(s.d.Albums, func(al *Album) bool { return al.ID == id })
	var ids []int
	for _, p := range s.d.Gallery {
		if p.Album == id {
			ids = append(ids, p.ID)
		}
	}
	if moveTo != 0 {
		s.movePhotos(ids, moveTo)
		return nil, s.save()
	}
	return s.removeGallery(ids), s.save()
}

// AddGallery puts a photo at the end of its album and notes it on the
// sent-in photo it came from.
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

func (s *Store) GalleryPhoto(id int) (GalleryPhoto, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.d.Gallery {
		if p.ID == id {
			return *p, true
		}
	}
	return GalleryPhoto{}, false
}

// AlbumPhotos lists an album's photos in order.
func (s *Store) AlbumPhotos(album int) []GalleryPhoto {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []GalleryPhoto
	for _, p := range s.d.Gallery {
		if p.Album == album {
			out = append(out, *p)
		}
	}
	return out
}

// GalleryPhotos lists every gallery photo, album by album.
func (s *Store) GalleryPhotos() []GalleryPhoto {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]GalleryPhoto, 0, len(s.d.Gallery))
	for _, al := range s.d.Albums {
		for _, p := range s.d.Gallery {
			if p.Album == al.ID {
				out = append(out, *p)
			}
		}
	}
	return out
}

// UnsizedPhotos lists the photos with no narrower copies or size yet.
func (s *Store) UnsizedPhotos() []GalleryPhoto {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []GalleryPhoto
	for _, p := range s.d.Gallery {
		if p.W == 0 {
			out = append(out, *p)
		}
	}
	return out
}

func (s *Store) SetPhotoSize(id, w, h int, sized bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.d.Gallery {
		if p.ID == id {
			p.W, p.H, p.Sized = w, h, sized
			return s.save()
		}
	}
	return nil
}

// MovePhotos moves photos to the end of another album.
func (s *Store) MovePhotos(ids []int, album int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.ContainsFunc(s.d.Albums, func(al *Album) bool { return al.ID == album }) {
		return errNotFound
	}
	s.movePhotos(ids, album)
	return s.save()
}

func (s *Store) movePhotos(ids []int, album int) {
	var moved []*GalleryPhoto
	s.d.Gallery = slices.DeleteFunc(s.d.Gallery, func(p *GalleryPhoto) bool {
		if slices.Contains(ids, p.ID) {
			p.Album = album
			moved = append(moved, p)
			return true
		}
		return false
	})
	s.d.Gallery = append(s.d.Gallery, moved...)
}

// OrderAlbum puts an album's photos in a new order: ids first, in that
// order, then the rest as they were. With last set, ids go at the end.
func (s *Store) OrderAlbum(album int, ids []int, last bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var slots []int // where the album's photos are in s.d.Gallery
	var picked, rest []*GalleryPhoto
	for i, p := range s.d.Gallery {
		if p.Album != album {
			continue
		}
		slots = append(slots, i)
		if !slices.Contains(ids, p.ID) {
			rest = append(rest, p)
		}
	}
	for _, id := range ids {
		for _, i := range slots {
			if p := s.d.Gallery[i]; p.ID == id && !slices.Contains(picked, p) {
				picked = append(picked, p)
			}
		}
	}
	order := append(picked, rest...)
	if last {
		order = append(rest, picked...)
	}
	for k, i := range slots {
		s.d.Gallery[i] = order[k]
	}
	return s.save()
}

// SetCover makes a photo its album's cover.
func (s *Store) SetCover(album, photo int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, al := range s.d.Albums {
		if al.ID == album {
			al.Cover = photo
			return s.save()
		}
	}
	return errNotFound
}

// EditPhotos changes photos' captions (by ID), and with by set, the credit
// on the photos in credit.
func (s *Store) EditPhotos(captions map[int]string, credit []int, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.d.Gallery {
		if c, ok := captions[p.ID]; ok {
			p.Caption = c
		}
		if by != "" && slices.Contains(credit, p.ID) {
			p.By = by
		}
	}
	return s.save()
}

// RemoveGallery takes photos out of the gallery and returns their files,
// for the caller to delete. The sent-in photos they came from can be
// approved again.
func (s *Store) RemoveGallery(ids []int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := s.removeGallery(ids)
	if len(files) == 0 {
		return nil, nil
	}
	return files, s.save()
}

func (s *Store) removeGallery(ids []int) []string {
	var files []string
	s.d.Gallery = slices.DeleteFunc(s.d.Gallery, func(p *GalleryPhoto) bool {
		if slices.Contains(ids, p.ID) {
			files = append(files, p.files()...)
			return true
		}
		return false
	})
	for _, sp := range s.d.SubmittedPhotos {
		if slices.Contains(ids, sp.Gallery) {
			sp.Gallery = 0
		}
	}
	return files
}

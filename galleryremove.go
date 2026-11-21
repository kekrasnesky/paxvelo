package main

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Removing photos from the gallery. Each photo on an album's admin page has
// its own remove button (and ticked photos can go together); a removed photo
// leaves the website at once, its files move out of the public photos into
// the data folder's "removed" folder, and Undo puts it back in its place.
// Removed photos are deleted for good rejectKeep later, the next time an
// admin opens the gallery. A photo a member shared is marked rejected on
// Photos to review, so it doesn't come back as waiting.

// RemovedPhoto is a gallery photo an admin removed, kept so Undo can put it
// back.
type RemovedPhoto struct {
	GalleryPhoto
	Pos     int       `json:"pos"` // its place in its album
	Removed time.Time `json:"removed"`
}

func (a *App) removedDir() string { return filepath.Join(a.cfg.DataDir, "removed") }

// moveFiles moves a photo's files (its full size and narrower copies)
// between the public photos and the removed folder.
func (a *App) moveFiles(p GalleryPhoto, toRemoved bool) {
	if err := os.MkdirAll(a.removedDir(), 0o755); err != nil {
		log.Printf("removed photos: %v", err)
		return
	}
	for _, src := range p.files() {
		name := strings.TrimPrefix(src, "/media/")
		from, to := filepath.Join(a.mediaDir(), name), filepath.Join(a.removedDir(), name)
		if !toRemoved {
			from, to = to, from
		}
		if err := os.Rename(from, to); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("removed photo %d: %v", p.ID, err)
		}
	}
}

// removePhotos takes photos out of the gallery, keeping them for Undo, and
// returns how many it took out.
func (a *App) removePhotos(ids []int) (int, error) {
	out, err := a.store.TakeOutGallery(ids, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	for _, p := range out {
		a.moveFiles(p, true)
	}
	return len(out), nil
}

// restorePhotos puts removed photos back where they were, and returns how
// many it put back. Photos whose album was deleted since stay removed.
func (a *App) restorePhotos(ids []int) (int, error) {
	out, err := a.store.PutBackGallery(ids)
	if err != nil {
		return 0, err
	}
	for _, p := range out {
		a.moveFiles(p, false)
	}
	return len(out), nil
}

// purgeRemoved deletes the photos removed more than rejectKeep ago.
func (a *App) purgeRemoved() {
	files, err := a.store.PurgeRemovedGallery(time.Now().Add(-rejectKeep))
	if err != nil {
		log.Printf("removed photos: %v", err)
		return
	}
	for _, src := range files {
		os.Remove(filepath.Join(a.removedDir(), strings.TrimPrefix(src, "/media/")))
	}
}

// ---- Store ----

// TakeOutGallery moves photos from the gallery to the removed list, noting
// each one's place in its album, and returns them.
func (s *Store) TakeOutGallery(ids []int, at time.Time) ([]GalleryPhoto, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []GalleryPhoto
	pos := map[int]int{} // album → photos seen so far
	kept := make([]*GalleryPhoto, 0, len(s.d.Gallery))
	for _, p := range s.d.Gallery {
		n := pos[p.Album]
		pos[p.Album]++
		if !slices.Contains(ids, p.ID) {
			kept = append(kept, p)
			continue
		}
		s.d.RemovedGallery = append(s.d.RemovedGallery, &RemovedPhoto{GalleryPhoto: *p, Pos: n, Removed: at})
		out = append(out, *p)
	}
	if len(out) == 0 {
		return nil, nil
	}
	s.d.Gallery = kept
	for _, sp := range s.d.SubmittedPhotos {
		if sp.Gallery != 0 && slices.Contains(ids, sp.Gallery) {
			sp.Gallery, sp.Rejected = 0, at
		}
	}
	return out, s.save()
}

// PutBackGallery returns removed photos to their albums, each in the place
// it had, and returns them. Photos whose album is gone stay removed.
func (s *Store) PutBackGallery(ids []int) ([]GalleryPhoto, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var back []*RemovedPhoto
	s.d.RemovedGallery = slices.DeleteFunc(s.d.RemovedGallery, func(r *RemovedPhoto) bool {
		if slices.Contains(ids, r.ID) && slices.ContainsFunc(s.d.Albums, func(al *Album) bool { return al.ID == r.Album }) {
			back = append(back, r)
			return true
		}
		return false
	})
	if len(back) == 0 {
		return nil, nil
	}
	// Lowest place first, so photos removed together go back in order.
	slices.SortStableFunc(back, func(x, y *RemovedPhoto) int { return x.Pos - y.Pos })
	var out []GalleryPhoto
	for _, r := range back {
		p := r.GalleryPhoto
		at, n := len(s.d.Gallery), 0
		for i, g := range s.d.Gallery {
			if g.Album != p.Album {
				continue
			}
			if n == r.Pos {
				at = i
				break
			}
			n++
			at = i + 1
		}
		s.d.Gallery = slices.Insert(s.d.Gallery, at, &p)
		for _, sp := range s.d.SubmittedPhotos {
			if p.From != 0 && sp.ID == p.From && sp.Gallery == 0 {
				sp.Gallery, sp.Rejected = p.ID, time.Time{}
			}
		}
		out = append(out, p)
	}
	return out, s.save()
}

// RemovedIn lists which of ids are removed photos of an album.
func (s *Store) RemovedIn(album int, ids []int) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int
	for _, r := range s.d.RemovedGallery {
		if r.Album == album && slices.Contains(ids, r.ID) {
			out = append(out, r.ID)
		}
	}
	return out
}

// PurgeRemovedGallery forgets the photos removed before a time and returns
// their files, for the caller to delete.
func (s *Store) PurgeRemovedGallery(before time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var files []string
	s.d.RemovedGallery = slices.DeleteFunc(s.d.RemovedGallery, func(r *RemovedPhoto) bool {
		if r.Removed.Before(before) {
			files = append(files, r.files()...)
			return true
		}
		return false
	})
	if len(files) == 0 {
		return nil, nil
	}
	return files, s.save()
}

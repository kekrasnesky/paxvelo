package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Website → Gallery: admins start, rename, reorder and delete albums, and in
// each album upload photos, caption them, put them in order (by dragging, or
// by date or photographer), pick the cover, and move photos to other albums.
// Sent-in photos are approved into albums on Email → Sent-in photos.

func albumAdminHref(id int) string { return "/admin/gallery/" + strconv.Itoa(id) }

// albumAdmin is one album on its admin page.
type albumAdmin struct {
	Album
	Photos []GalleryPhoto
	Cover  int // the photo shown as its cover
}

func (a *App) galleryAdminPage(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Gallery", Tab: "gallery", AlbumCards: a.store.AlbumCards(true)}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "gallery", v)
}

// galleryAction starts an album, or moves one up or down the list.
func (a *App) galleryAction(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if m := r.FormValue("move"); m != "" { // like "12:up"
		id, dir, _ := strings.Cut(m, ":")
		n, _ := strconv.Atoi(id)
		by := 1
		if dir == "up" {
			by = -1
		}
		if err := a.store.MoveAlbum(n, by); err != nil {
			back(w, r, "/admin/gallery?err=save")
			return
		}
		back(w, r, "/admin/gallery")
		return
	}
	title := cleanTitle(r.FormValue("title"), maxAlbumTitle)
	date, err := a.cleanRideDate(r.FormValue("date"))
	switch {
	case title == "":
		back(w, r, "/admin/gallery?err=album-title")
		return
	case err != nil:
		back(w, r, "/admin/gallery?err=gallery-date")
		return
	}
	al, err := a.store.AddAlbum(title, date)
	if err != nil {
		back(w, r, "/admin/gallery?err=save")
		return
	}
	back(w, r, albumAdminHref(al.ID)+"?ok=album-new")
}

func (a *App) albumAdminPage(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	al, ok := a.store.Album(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	v := adminView{Title: al.Title, Tab: "gallery", Album: &albumAdmin{Album: al, Photos: a.store.AlbumPhotos(id), Cover: al.Cover}}
	if ph := v.Album.Photos; len(ph) > 0 && !slices.ContainsFunc(ph, func(p GalleryPhoto) bool { return p.ID == al.Cover }) {
		v.Album.Cover = ph[0].ID
	}
	for _, o := range a.store.Albums() {
		if o.ID != id {
			v.Albums = append(v.Albums, o)
		}
	}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "album", v)
}

// tickedIDs are the photos ticked on a form.
func tickedIDs(r *http.Request) []int {
	var ids []int
	for _, s := range r.Form["id"] {
		if id, err := strconv.Atoi(s); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// albumAction changes an album, or the ticked photos in it. Captions typed
// on the page, and the order photos were dragged into, are saved with
// whatever button was pressed.
func (a *App) albumAction(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if _, ok := a.store.Album(id); !ok {
		back(w, r, "/admin/gallery?err=album-gone")
		return
	}
	r.ParseForm()
	here := albumAdminHref(id)
	fail := func(code string) { back(w, r, here+"?err="+code) }
	done := func(code string) { back(w, r, here+"?ok="+code) }
	do := r.FormValue("do")

	switch do {
	case "rename":
		title := cleanTitle(r.FormValue("title"), maxAlbumTitle)
		date, err := a.cleanRideDate(r.FormValue("date"))
		if title == "" {
			fail("album-title")
		} else if err != nil {
			fail("gallery-date")
		} else if a.store.SaveAlbum(id, title, date) != nil {
			fail("save")
		} else {
			done("album-saved")
		}
		return
	case "delete":
		moveTo, _ := strconv.Atoi(r.FormValue("move_to"))
		files, err := a.store.DeleteAlbum(id, moveTo)
		if err != nil {
			fail("save")
			return
		}
		a.removeMedia(files)
		back(w, r, "/admin/gallery?ok=album-deleted")
		return
	}

	photos := a.store.AlbumPhotos(id)
	captions := map[int]string{}
	for _, p := range photos {
		if c, ok := r.Form["caption-"+strconv.Itoa(p.ID)]; ok {
			captions[p.ID] = cleanTitle(c[0], maxCaption)
		}
	}
	var order []int
	for _, s := range strings.Split(r.FormValue("order"), ",") {
		if n, err := strconv.Atoi(s); err == nil {
			order = append(order, n)
		}
	}
	if err := a.store.EditPhotos(captions, nil, ""); err != nil {
		fail("save")
		return
	}
	if len(order) > 0 {
		if err := a.store.OrderAlbum(id, order, false); err != nil {
			fail("save")
			return
		}
		photos = a.store.AlbumPhotos(id)
	}

	// Sorting the whole album.
	if strings.HasPrefix(do, "sort-") {
		sorted := slices.Clone(photos)
		slices.SortStableFunc(sorted, func(x, y GalleryPhoto) int {
			switch do {
			case "sort-new":
				return y.ID - x.ID
			case "sort-by":
				if c := strings.Compare(strings.ToLower(x.By), strings.ToLower(y.By)); c != 0 {
					return c
				}
			}
			return x.ID - y.ID
		})
		ids := make([]int, len(sorted))
		for i, p := range sorted {
			ids[i] = p.ID
		}
		if a.store.OrderAlbum(id, ids, false) != nil {
			fail("save")
			return
		}
		done("album-saved")
		return
	}
	if do == "save" {
		done("album-saved")
		return
	}

	// The rest act on the ticked photos.
	ids := tickedIDs(r)
	if len(ids) == 0 {
		fail("none-ticked")
		return
	}
	var err error
	switch do {
	case "first", "last":
		err = a.store.OrderAlbum(id, ids, do == "last")
	case "cover":
		err = a.store.SetCover(id, ids[0])
	case "move":
		to, _ := strconv.Atoi(r.FormValue("to"))
		if err = a.store.MovePhotos(ids, to); err == nil {
			done("photos-moved")
			return
		}
	case "credit":
		by := cleanName(r.FormValue("by"))
		if by == "" {
			fail("gallery-by")
			return
		}
		err = a.store.EditPhotos(nil, ids, by)
	case "remove":
		var files []string
		if files, err = a.store.RemoveGallery(ids); err == nil {
			a.removeMedia(files)
			done("gallery-removed")
			return
		}
	}
	if err != nil {
		fail("save")
		return
	}
	done("album-saved")
}

// galleryUpload is the bulk upload: an admin adds photos straight to an
// album, all by the same photographer. Each is checked and shrunk like any
// upload. With JavaScript the page shrinks the photos first and sends them a
// few at a time, answering in JSON; without, it sends them all at once.
func (a *App) galleryUpload(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(5 * time.Minute)) // an album can be big without JavaScript
	rc.SetWriteDeadline(time.Now().Add(5 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, maxBulkForm)
	asJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	album, _ := strconv.Atoi(r.URL.Query().Get("album"))
	here := albumAdminHref(album)
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
			back(w, r, here+"?err="+code)
			return
		}
		q := url.Values{"ok": {"gallery-uploaded"}, "n": {strconv.Itoa(added)}, "skipped": {strings.Join(skipped, " ")}}
		back(w, r, here+"?"+q.Encode())
	}
	if _, ok := a.store.Album(album); !ok {
		here = "/admin/gallery"
		reply("album-gone", 0, nil)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		reply("bigupload", 0, nil)
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["photos"]
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
		if err := a.addGalleryFile(GalleryPhoto{Album: album, Src: src, By: by}); err != nil {
			log.Printf("gallery upload: %v", err)
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
	s := fmt.Sprintf("Added %d %s to the album.", n, plural(n, "photo", "photos"))
	if sk := q.Get("skipped"); sk != "" {
		s += " Some couldn’t be used: " + sk
	}
	return s
}

// ---- Approving sent-in photos ----

var errNoAlbumName = errors.New("no album name")

// approvePhotos puts the ticked sent-in photos in the gallery, each batch
// in the album picked for it, with the caption and name they were sent with.
func (a *App) approvePhotos(w http.ResponseWriter, r *http.Request, ids []int) {
	added, already := 0, 0
	albums := map[int]int{} // batch → album
	for _, id := range ids {
		p, ok := a.store.Submitted(id)
		if !ok {
			continue
		}
		if p.Gallery != 0 {
			already++
			continue
		}
		album, ok := albums[p.Batch]
		if !ok {
			var err error
			if album, err = a.albumFor(r, p.Batch); err == errNoAlbumName {
				back(w, r, "/admin/submitted?err=album-pick")
				return
			} else if err != nil {
				back(w, r, "/admin/submitted?err=save")
				return
			}
			albums[p.Batch] = album
		}
		src, err := a.publishSubmitted(p)
		if err == nil {
			err = a.addGalleryFile(GalleryPhoto{Album: album, Src: src, Caption: p.Caption, By: p.Name, From: p.ID})
		}
		if err != nil {
			log.Printf("sent-in photo %d: %v", id, err)
			back(w, r, "/admin/submitted?err=save")
			return
		}
		added++
	}
	q := url.Values{"ok": {"gallery-added"}, "n": {strconv.Itoa(added)}, "already": {strconv.Itoa(already)}}
	back(w, r, "/admin/submitted?"+q.Encode())
}

// albumFor is the album picked for a batch on Sent-in photos: one already
// there, or a new one by the name typed (or one already by that name).
func (a *App) albumFor(r *http.Request, batch int) (int, error) {
	b := strconv.Itoa(batch)
	if id, err := strconv.Atoi(r.FormValue("album-" + b)); err == nil {
		if _, ok := a.store.Album(id); ok {
			return id, nil
		}
	}
	title := cleanTitle(r.FormValue("name-"+b), maxAlbumTitle)
	if title == "" {
		return 0, errNoAlbumName
	}
	for _, al := range a.store.Albums() {
		if strings.EqualFold(al.Title, title) {
			return al.ID, nil
		}
	}
	al, err := a.store.AddAlbum(title, "")
	return al.ID, err
}

// galleryAdded is the flash after approving sent-in photos.
func galleryAdded(q url.Values) string {
	n, _ := strconv.Atoi(q.Get("n"))
	already, _ := strconv.Atoi(q.Get("already"))
	s := fmt.Sprintf("Approved %d %s. %s now in the gallery.", n, plural(n, "photo", "photos"), plural(n, "It’s", "They’re"))
	if n == 0 {
		s = "Those photos are already in the gallery."
	} else if already > 0 {
		s += fmt.Sprintf(" %d %s already there.", already, plural(already, "was", "were"))
	}
	return s
}

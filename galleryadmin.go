package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Website → Gallery: admins start, rename, reorder (by dragging), feature,
// hide and delete albums, and in
// each album upload photos, caption them, put them in order (by dragging, or
// by date or photographer), pick the cover, and move photos to other albums.
// Photos members share are approved into albums on Website → Photos to
// review. Ticked photos can go in a newsletter's photo strip, and the
// newsletter editor picks photos from here too: the gallery is where the
// club's photos live.

func albumAdminHref(id int) string { return "/admin/gallery/" + strconv.Itoa(id) }

// albumAdmin is one album on its admin page.
type albumAdmin struct {
	Album
	Photos []GalleryPhoto
	Cover  int   // the photo shown as its cover
	Tick   []int // photos to show ticked, like the ones just approved
}

func (al albumAdmin) Ticked(id int) bool { return slices.Contains(al.Tick, id) }

func (a *App) galleryAdminPage(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Gallery", Tab: "gallery", AlbumList: &albumListView{Cards: a.store.AlbumCards(true)}}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "gallery", v)
}

// albumListView is the album list on Website → Gallery, with what was just
// done and how to undo it.
type albumListView struct {
	Cards []albumCard
	Note  *reviewNote
}

// albumSwitch features, hides or shows an album, for do values like
// "feature:12", and says what it did and how to undo it.
func (a *App) albumSwitch(do string) (reviewNote, error) {
	what, n, _ := strings.Cut(do, ":")
	id, _ := strconv.Atoi(n)
	var before Album
	prior := 0 // the album featured before
	for _, al := range a.store.Albums() {
		if al.ID == id {
			before = al
		}
		if al.Featured {
			prior = al.ID
		}
	}
	if before.ID == 0 {
		return reviewNote{}, errNotFound
	}
	var err error
	var text string
	switch what {
	case "feature":
		err, text = a.store.FeatureAlbum(id, true), "“%s” is the featured album now: it shows first, and big, on the gallery page."
	case "unfeature":
		err, text = a.store.FeatureAlbum(id, false), "“%s” isn’t featured any more."
	case "hide":
		err, text = a.store.HideAlbum(id, true), "“%s” is hidden from the website. Its photos can still go in the newsletter."
	case "show":
		err, text = a.store.HideAlbum(id, false), "“%s” is on the website again."
	default:
		return reviewNote{}, errNotFound
	}
	if err != nil {
		return reviewNote{}, err
	}
	// Undo puts things back as they were, including which album was featured.
	var undo []string
	switch {
	case before.Hidden && what != "hide":
		undo = append(undo, "hide:"+n)
	case before.Featured && what != "feature":
		undo = append(undo, "feature:"+n)
	case what == "feature" && !before.Featured:
		undo = append(undo, "unfeature:"+n)
	case what == "hide" && !before.Hidden:
		undo = append(undo, "show:"+n)
	}
	if what == "feature" && prior != 0 && prior != id {
		undo = append(undo, "feature:"+strconv.Itoa(prior))
	}
	return reviewNote{Text: fmt.Sprintf(text, before.Title), Undo: strings.Join(undo, " ")}, nil
}

// galleryAction starts an album; puts the albums in order (dragged, or one
// place up or down); or features, hides or shows one. With JavaScript it
// answers with the album list as it is now.
func (a *App) galleryAction(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	asJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	answer := func(note *reviewNote, code string) {
		if !asJSON {
			if code != "" {
				back(w, r, "/admin/gallery?err="+code)
			} else if note != nil {
				back(w, r, "/admin/gallery?ok="+note.code)
			} else {
				back(w, r, "/admin/gallery")
			}
			return
		}
		if code != "" {
			replyJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errorText[code]})
			return
		}
		var buf bytes.Buffer
		if err := a.site.admin.ExecuteTemplate(&buf, "album-list", albumListView{a.store.AlbumCards(true), note}); err != nil {
			log.Printf("album list: %v", err)
		}
		replyJSON(w, http.StatusOK, map[string]any{"ok": true, "html": buf.String()})
	}
	if m := r.FormValue("move"); m != "" { // like "12:up"
		id, dir, _ := strings.Cut(m, ":")
		n, _ := strconv.Atoi(id)
		by := 1
		if dir == "up" {
			by = -1
		}
		if err := a.store.MoveAlbum(n, by); err != nil {
			answer(nil, "save")
			return
		}
		answer(nil, "")
		return
	}
	if o := r.FormValue("order"); o != "" { // dragged, like "12,4,9"
		var ids []int
		for _, s := range strings.Split(o, ",") {
			if n, err := strconv.Atoi(s); err == nil {
				ids = append(ids, n)
			}
		}
		if err := a.store.OrderAlbums(ids); err != nil {
			answer(nil, "save")
			return
		}
		answer(nil, "")
		return
	}
	do, undo := r.FormValue("do"), r.FormValue("undo")
	if undo != "" {
		do = undo
	}
	if do != "" { // like "hide:12", or several for an undo
		var note reviewNote
		for i, d := range strings.Fields(do) {
			n, err := a.albumSwitch(d)
			if err != nil {
				answer(nil, "album-gone")
				return
			}
			if i == 0 {
				note = n
				note.code, _, _ = strings.Cut(d, ":")
				note.code = "album-" + note.code
			}
		}
		if undo != "" {
			note = reviewNote{Text: "Undone.", code: "album-undone"}
		}
		answer(&note, "")
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
	_, tick := undoAction(":" + r.URL.Query().Get("tick"))
	v := adminView{Title: al.Title, Tab: "gallery", Issues: a.store.Issues(),
		Album: &albumAdmin{Album: al, Photos: a.store.AlbumPhotos(id), Cover: al.Cover, Tick: tick}}
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
	asJSON := strings.Contains(r.Header.Get("Accept"), "application/json")

	switch what, _, _ := strings.Cut(do, ":"); what {
	case "feature", "unfeature", "hide", "show": // the album's switches
		if _, err := a.albumSwitch(what + ":" + strconv.Itoa(id)); err != nil {
			fail("save")
		} else {
			done("album-" + what)
		}
		return
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
		if r.FormValue("move_to") == "" { // say what happens to its photos
			fail("album-photos")
			return
		}
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
		if asJSON { // a caption or the order, saved as it changes
			replyJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		done("album-saved")
		return
	}
	if c, ok := strings.CutPrefix(do, "cover:"); ok { // the photo's own button
		n, _ := strconv.Atoi(c)
		if a.store.SetCover(id, n) != nil {
			fail("save")
			return
		}
		done("album-cover")
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
	case "newsletter":
		a.galleryToNewsletter(w, r, id, ids)
		return
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

// approved is what approving sent-in photos did.
type approved struct {
	n, already int
	album      Album
	ids        []int // the sent-in photos approved
	gallery    []int // and their gallery photos
}

// note is what the card on Photos to review says after approving.
func (res approved) note() reviewNote {
	if res.n == 0 {
		return reviewNote{Text: "Those photos are already in the gallery."}
	}
	admin := albumAdminHref(res.album.ID)
	return reviewNote{
		Text: fmt.Sprintf("Added %d %s to “%s”.", res.n, plural(res.n, "photo", "photos"), res.album.Title),
		Undo: "unapprove:" + joinIDs(res.ids),
		Links: []noteLink{{"Open the album", admin},
			{"Add to the newsletter", admin + "?tick=" + joinIDs(res.gallery) + "#to-newsletter"}},
	}
}

// approvePhotos puts the ticked sent-in photos in the gallery, each batch
// in the album picked for it, with the caption and credit on its card. It
// returns an error code when it couldn't.
func (a *App) approvePhotos(r *http.Request, ids []int) (approved, string) {
	var res approved
	albums := map[int]int{} // batch → album
	for _, id := range ids {
		p, ok := a.store.Submitted(id)
		if !ok {
			continue
		}
		if p.Gallery != 0 {
			res.already++
			continue
		}
		album, ok := albums[p.Batch]
		if !ok {
			var err error
			if album, err = a.albumFor(r, p.Batch); err == errNoAlbumName {
				return res, "album-pick"
			} else if err != nil {
				return res, "save"
			}
			albums[p.Batch] = album
		}
		// What was typed on the card: each photo's caption, and the credit.
		if c, ok := r.Form["caption-"+strconv.Itoa(id)]; ok {
			p.Caption = cleanTitle(c[0], maxCaption)
		}
		if by := cleanName(r.FormValue("by-" + strconv.Itoa(p.Batch))); by != "" {
			p.Name = by
		}
		if err := a.publishToAlbum(p, album); err != nil {
			log.Printf("sent-in photo %d: %v", id, err)
			return res, "save"
		}
		res.n++
		res.ids = append(res.ids, id)
		if done, ok := a.store.Submitted(id); ok {
			res.gallery = append(res.gallery, done.Gallery)
		}
		res.album, _ = a.store.Album(album)
	}
	return res, ""
}

// albumFor is the album picked for a batch on Photos to review: one already
// there, or a new one by the name typed (or one already by that name).
func (a *App) albumFor(r *http.Request, batch int) (int, error) {
	b := strconv.Itoa(batch)
	if id, err := strconv.Atoi(r.FormValue("album-" + b)); err == nil {
		if _, ok := a.store.Album(id); ok {
			return id, nil
		}
	}
	return a.albumNamed(r.FormValue("name-" + b))
}

// albumNamed is the album with this name, started if there isn't one.
func (a *App) albumNamed(name string) (int, error) {
	title := cleanTitle(name, maxAlbumTitle)
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

// publishToAlbum puts a copy of a sent-in photo in an album, with the
// caption and name it was sent with.
func (a *App) publishToAlbum(p SubmittedPhoto, album int) error {
	src, err := a.publishSubmitted(p)
	if err != nil {
		return err
	}
	return a.addGalleryFile(GalleryPhoto{Album: album, Src: src, Caption: p.Caption, By: p.Name, From: p.ID})
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

// ---- Gallery photos in the newsletter ----

// galleryToNewsletter adds the ticked photos of an album to an issue's
// photo strip.
func (a *App) galleryToNewsletter(w http.ResponseWriter, r *http.Request, album int, ids []int) {
	here := albumAdminHref(album)
	issueID, _ := strconv.Atoi(r.FormValue("issue"))
	is, ok := a.store.Issue(issueID)
	if !ok {
		back(w, r, here+"?err=gone")
		return
	}
	var photos []GalleryPhoto
	for _, p := range a.store.AlbumPhotos(album) {
		if slices.Contains(ids, p.ID) {
			photos = append(photos, p)
		}
	}
	urls, err := a.newsCopies(photos)
	if err != nil {
		log.Printf("gallery to newsletter: %v", err)
		back(w, r, here+"?err=save")
		return
	}
	added, full := addToStrip(&is, urls)
	if added > 0 {
		saved, err := a.store.SaveIssue(is)
		if err != nil {
			a.removeMedia(urls)
			back(w, r, here+"?err=save")
			return
		}
		if saved.IsPublished() {
			a.refreshNewsletter()
		}
	}
	a.removeMedia(urls[added:]) // the ones that didn't fit
	q := url.Values{"ok": {"newsletter-added"}, "n": {strconv.Itoa(added)}, "full": {strconv.Itoa(full)}, "to": {is.Label()}}
	back(w, r, here+"?"+q.Encode())
}

// newsCopies copies gallery photos for a newsletter and returns their
// addresses, in order. The newsletter keeps its own copy, so taking a photo
// out of the gallery doesn't take it out of an issue already sent.
func (a *App) newsCopies(photos []GalleryPhoto) ([]string, error) {
	var urls []string
	for _, p := range photos {
		u, err := a.copyToMedia(filepath.Join(a.mediaDir(), strings.TrimPrefix(p.Src, "/media/")))
		if err != nil {
			a.removeMedia(urls)
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, nil
}

// newsletterAdded is the flash after adding gallery photos to an issue.
func newsletterAdded(q url.Values) string {
	n, _ := strconv.Atoi(q.Get("n"))
	full, _ := strconv.Atoi(q.Get("full"))
	s := fmt.Sprintf("Added %d %s to the %s newsletter’s photo strip. Open the issue to frame them.", n, plural(n, "photo", "photos"), q.Get("to"))
	if n == 0 && full == 0 {
		s = "Those photos are already in the " + q.Get("to") + " newsletter."
	}
	if full > 0 {
		s += fmt.Sprintf(" Its photo strip holds %d, so %d didn’t fit.", maxGallery, full)
	}
	return s
}

// galleryPick answers the newsletter editor's "Choose from the gallery"
// box: the albums with photos, or one album's photos.
func (a *App) galleryPick(w http.ResponseWriter, r *http.Request) {
	type photo struct {
		ID      int    `json:"id"`
		Thumb   string `json:"thumb"`
		Caption string `json:"caption,omitempty"`
		By      string `json:"by"`
	}
	if id, err := strconv.Atoi(r.URL.Query().Get("album")); err == nil {
		out := []photo{}
		for _, p := range a.store.AlbumPhotos(id) {
			out = append(out, photo{p.ID, p.Thumb(), p.Caption, p.By})
		}
		replyJSON(w, http.StatusOK, map[string]any{"photos": out})
		return
	}
	type album struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
		Label string `json:"label"`
		Cover string `json:"cover"`
	}
	out := []album{}
	for _, c := range a.store.AlbumCards(true) {
		if c.Count == 0 {
			continue
		}
		label := fmt.Sprintf("%d %s", c.Count, plural(c.Count, "photo", "photos"))
		if d := c.DateLabel(); d != "" {
			label = d + " · " + label
		}
		out = append(out, album{c.ID, c.Title, label, c.Cover.Thumb()})
	}
	replyJSON(w, http.StatusOK, map[string]any{"albums": out})
}

// galleryPicked copies the gallery photos picked in the newsletter editor
// for the issue, and answers with their addresses and captions.
func (a *App) galleryPicked(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ids := tickedIDs(r)
	var photos []GalleryPhoto
	for _, id := range ids[:min(len(ids), maxGallery)] {
		if p, ok := a.store.GalleryPhoto(id); ok {
			photos = append(photos, p)
		}
	}
	if len(photos) == 0 {
		replyJSON(w, http.StatusOK, map[string]any{"error": "Choose a photo first."})
		return
	}
	urls, err := a.newsCopies(photos)
	if err != nil {
		log.Printf("gallery to newsletter: %v", err)
		replyJSON(w, http.StatusOK, map[string]any{"error": "Something went wrong. Try again."})
		return
	}
	captions := make([]string, len(photos))
	for i, p := range photos {
		captions[i] = newsCaption(p)
	}
	replyJSON(w, http.StatusOK, map[string]any{"urls": urls, "captions": captions})
}

// newsCaption is a gallery photo's caption with its credit, for a
// newsletter story's photo.
func newsCaption(p GalleryPhoto) string {
	s := "Photo by " + p.By
	if p.Caption != "" {
		s = p.Caption + " · " + s
	}
	return cleanTitle(s, maxNewsCaption)
}

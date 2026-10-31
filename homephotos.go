package main

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// The row of photos on the home page, which admins change on the Home photos
// tab. It appears wherever a page says {home-photos}. Phones swipe through
// it; wider screens show three at a time, with arrows when there are more.
// With more than slideshowAfter photos, wider screens also move on by
// themselves (site.js).

const (
	maxHomePhotos  = 15
	slideshowAfter = 6
)

type HomePhoto struct {
	Photo string `json:"photo"`
	Alt   string `json:"alt,omitempty"` // what the photo shows, for screen readers and search engines
	Focus string `json:"focus,omitempty"`
}

func defaultHomePhotos() []HomePhoto {
	return []HomePhoto{
		{Photo: "/photos/shop-ride.webp", Alt: "PaxVelo riders lined up outside Old Line Cycles at dusk"},
		{Photo: "/photos/ice-cream.webp", Alt: "Riders with ice cream after a club ride"},
		{Photo: "/photos/club-kit.webp", Alt: "Four riders in the blue PaxVelo kit"},
	}
}

var homePhotosTpl = template.Must(template.New("").Parse(`{{if .}}<div class="photo-strip{{if gt (len .) 3}} many{{end}}{{if gt (len .) ` + strconv.Itoa(slideshowAfter) + `}} slides{{end}}">
<div class="photos" tabindex="0" role="region" aria-label="Club photos">{{range .}}<img src="{{.Photo}}" alt="{{or .Alt "A PaxVelo club ride"}}" width="900" height="675" loading="lazy" decoding="async"{{with .Focus}} style="object-position:{{.}}"{{end}}>{{end}}</div>
{{if gt (len .) 3}}<button class="strip-btn prev" type="button" aria-label="Previous photos" hidden>‹</button><button class="strip-btn next" type="button" aria-label="More photos" hidden>›</button>{{end}}
</div>
{{end}}`))

func homePhotosWidget(photos []HomePhoto) template.HTML {
	var b bytes.Buffer
	homePhotosTpl.Execute(&b, photos)
	return template.HTML(b.String())
}

// ---- Store ----

func (s *Store) HomePhotos() []HomePhoto {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]HomePhoto(nil), s.d.HomePhotos...)
}

func (s *Store) SetHomePhotos(p []HomePhoto) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.HomePhotos = p
	return s.save()
}

// ---- Admin: Home photos ----

func (a *App) homePhotosAdmin(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Home photos", Tab: "home-photos", HomePhotos: a.store.HomePhotos(), MaxHomePhotos: maxHomePhotos}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "home-photos", v)
}

func (a *App) saveHomePhotos(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxHomePhotos*(maxPhoto+(1<<20)))
	if err := r.ParseMultipartForm(16 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		back(w, r, "/admin/home-photos?err=bigphotos")
		return
	}
	type row struct {
		HomePhoto
		order int
	}
	var rows []row
	removed, added := 0, 0
	count, _ := strconv.Atoi(r.FormValue("count"))
	for i := 0; i < count && i < maxHomePhotos; i++ {
		f := func(name string) string { return strings.TrimSpace(r.FormValue(fmt.Sprintf("%s%d", name, i))) }
		p := HomePhoto{Photo: f("photo"), Alt: cleanName(f("alt")), Focus: cleanFocus(f("focus"))}
		if f("remove") != "" {
			removed++
			continue
		}
		if !sitePhoto(p.Photo) {
			continue
		}
		order, err := strconv.Atoi(f("order"))
		if err != nil {
			order = i + 1
		}
		rows = append(rows, row{p, order*100 + i})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].order < rows[j].order })
	var photos []HomePhoto
	for _, rw := range rows {
		photos = append(photos, rw.HomePhoto)
	}

	// New photos go on the end, as many as there's room for.
	var problems []string
	if r.MultipartForm != nil {
		files := r.MultipartForm.File["add"]
		for i, fh := range files {
			if len(photos) >= maxHomePhotos {
				problems = append(problems, fmt.Sprintf("The row holds %d photos, so the last %d weren’t added.", maxHomePhotos, len(files)-i))
				break
			}
			u, err := a.savePhotoFile(fh)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			if u != "" {
				photos = append(photos, HomePhoto{Photo: u})
				added++
			}
		}
	}
	if err := a.store.SetHomePhotos(photos); err != nil || a.rebuildContent() != nil {
		back(w, r, "/admin/home-photos?err=save")
		return
	}
	if len(problems) > 0 {
		v := adminView{Title: "Home photos", Tab: "home-photos", HomePhotos: a.store.HomePhotos(), MaxHomePhotos: maxHomePhotos,
			Flash: homePhotosSaved(added, removed), Error: strings.Join(problems, " ")}
		a.renderAdmin(w, r, http.StatusOK, "home-photos", v)
		return
	}
	back(w, r, fmt.Sprintf("/admin/home-photos?ok=home-photos&added=%d&removed=%d", added, removed))
}

// homePhotosSaved says what a save changed.
func homePhotosSaved(added, removed int) string {
	var did []string
	if added > 0 {
		did = append(did, fmt.Sprintf("added %d %s", added, plural(added, "photo", "photos")))
	}
	if removed > 0 {
		did = append(did, fmt.Sprintf("removed %d %s", removed, plural(removed, "photo", "photos")))
	}
	if len(did) == 0 {
		return "Saved. The home page is updated."
	}
	s := strings.Join(did, " and ")
	return strings.ToUpper(s[:1]) + s[1:] + ". The home page is updated."
}

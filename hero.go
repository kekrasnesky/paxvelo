package main

import (
	"errors"
	"net/http"
	"strings"
)

// The big photo at the top of the home page. Admins change it on the Home
// photos tab: upload a photo, frame it (one framing for computers' wide box
// and phones' square one), and say what it shows. With none uploaded, the
// home page uses the built-in photos/hero.webp and hero-phone.webp.

type HeroPhoto struct {
	Photo string `json:"photo,omitempty"` // an uploaded photo, or empty for the built-in one
	Alt   string `json:"alt,omitempty"`
	Focus string `json:"focus,omitempty"`
}

const (
	builtinHero    = "/photos/hero.webp"
	builtinHeroAlt = "PaxVelo riders on a group ride along a country road"
)

func (s *Store) Hero() HeroPhoto {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Hero
}

func (s *Store) SetHero(h HeroPhoto) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Hero = h
	return s.save()
}

// saveHero is the Top photo form on the Home photos tab.
func (a *App) saveHero(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPhoto+(1<<20))
	if err := r.ParseMultipartForm(16 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		back(w, r, "/admin/home-photos?err=bigphotos#hero")
		return
	}
	h := a.store.Hero()
	if r.FormValue("builtin") != "" {
		h = HeroPhoto{}
	} else {
		h.Alt = cleanName(strings.TrimSpace(r.FormValue("hero_alt")))
		h.Focus = cleanFocus(r.FormValue("hero_focus"))
		if r.MultipartForm != nil {
			if files := r.MultipartForm.File["hero_photo"]; len(files) > 0 {
				u, err := a.savePhotoFile(files[0])
				if err != nil {
					a.heroError(w, r, err.Error())
					return
				}
				if u != "" {
					// A new photo starts centered: the old framing was for the old photo.
					h.Photo, h.Focus = u, ""
				}
			}
		}
	}
	if err := a.store.SetHero(h); err != nil || a.rebuildContent() != nil {
		back(w, r, "/admin/home-photos?err=save#hero")
		return
	}
	back(w, r, "/admin/home-photos?ok=hero#hero")
}

func (a *App) heroError(w http.ResponseWriter, r *http.Request, msg string) {
	v := adminView{Title: "Home photos", Tab: "home-photos", HomePhotos: a.store.HomePhotos(), MaxHomePhotos: maxHomePhotos, Hero: a.store.Hero(), Error: msg}
	a.renderAdmin(w, r, http.StatusOK, "home-photos", v)
}

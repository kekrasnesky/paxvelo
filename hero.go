package main

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The big photo at the top of the home page. Admins change it on the Home
// photos tab: upload a photo, frame it (one framing for computers' wide box
// and phones' square one), and say what it shows. With none uploaded, the
// home page uses the built-in photos/hero.webp and hero-phone.webp.
//
// Phones get their own small square copy (Phone), cut from the photo where
// the framing points and remade whenever the framing or photo changes, the
// way the built-in hero-phone.webp is: a phone shows a square-ish box, so the
// full wide photo would be mostly wasted download. A WebP photo (Go can't
// read it) has no square copy, and phones get the full photo.

type HeroPhoto struct {
	Photo string `json:"photo,omitempty"` // an uploaded photo, or empty for the built-in one
	Alt   string `json:"alt,omitempty"`
	Focus string `json:"focus,omitempty"`
	Phone string `json:"phone,omitempty"` // the square copy for phones, cut at Focus
}

// heroPhoneSide is the square copy's size, as the built-in hero-phone.webp.
const heroPhoneSide = 800

// heroPhoneCrop cuts the square a phone shows from a photo framed by focus,
// matching what the framing tool previews for phones (focusStyle on a square
// box: object-fit cover at the focus point, zoomed in around it), shrunk to
// heroPhoneSide. It returns nil for a photo it can't read.
func heroPhoneCrop(b []byte, focus string) []byte {
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	fr, ok := parseFocus(focus)
	if !ok {
		fr.X, fr.Y, fr.Z = 50, 50, 1
	}
	r := src.Bounds()
	w, h := float64(r.Dx()), float64(r.Dy())
	s := min(w, h) // the square that covers the box, before zooming
	left, top := (w-s)*fr.X/100, (h-s)*fr.Y/100
	k := 1 - 1/max(fr.Z, 1) // zooming in keeps 1/Z of the square, around the focus point
	left += fr.X / 100 * s * k
	top += fr.Y / 100 * s * k
	s /= max(fr.Z, 1)
	x0, y0, side := r.Min.X+int(left+.5), r.Min.Y+int(top+.5), max(int(s+.5), 1)
	sq := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(sq, sq.Bounds(), src, image.Pt(x0, y0), draw.Src)
	var out bytes.Buffer
	if jpeg.Encode(&out, downscale(sq, heroPhoneSide), &jpeg.Options{Quality: copyQuality}) != nil {
		return nil
	}
	return out.Bytes()
}

// heroPhone makes the square phone copy of an uploaded hero photo and returns
// its address, or "" when there's none to make.
func (a *App) heroPhone(h HeroPhoto) string {
	if !strings.HasPrefix(h.Photo, "/media/") {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(a.mediaDir(), path.Base(h.Photo)))
	if err != nil {
		return ""
	}
	crop := heroPhoneCrop(b, h.Focus)
	if crop == nil {
		return ""
	}
	name := randHex(8) + "-phone.jpg"
	if os.WriteFile(filepath.Join(a.mediaDir(), name), crop, 0o644) != nil {
		return ""
	}
	return "/media/" + name
}

// ensureHeroPhone makes the phone copy for a top photo uploaded before
// phones had their own (run at startup).
func (a *App) ensureHeroPhone() {
	if h := a.store.Hero(); h.Photo != "" && h.Phone == "" {
		if h.Phone = a.heroPhone(h); h.Phone != "" {
			a.store.SetHero(h)
		}
	}
}

// dropHeroPhone deletes a square phone copy that's been replaced; nothing
// else uses it.
func (a *App) dropHeroPhone(u string) {
	if strings.HasPrefix(u, "/media/") && strings.HasSuffix(u, "-phone.jpg") {
		os.Remove(filepath.Join(a.mediaDir(), path.Base(u)))
	}
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
	was := h
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
	if h.Photo != was.Photo || h.Focus != was.Focus || (h.Photo != "" && h.Phone == "") {
		h.Phone = a.heroPhone(h)
	}
	if err := a.store.SetHero(h); err != nil || a.rebuildContent() != nil {
		back(w, r, "/admin/home-photos?err=save#hero")
		return
	}
	if was.Phone != h.Phone {
		a.dropHeroPhone(was.Phone)
	}
	back(w, r, "/admin/home-photos?ok=hero#hero")
}

func (a *App) heroError(w http.ResponseWriter, r *http.Request, msg string) {
	v := adminView{Title: "Home photos", Tab: "home-photos", HomePhotos: a.store.HomePhotos(), MaxHomePhotos: maxHomePhotos, Hero: a.store.Hero(), Error: msg}
	a.renderAdmin(w, r, http.StatusOK, "home-photos", v)
}

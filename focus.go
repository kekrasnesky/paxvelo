package main

import (
	"fmt"
	"html/template"
	"regexp"
	"strconv"
	"strings"
)

// Photo framing. A photo that fills a box of a different shape (CSS
// object-fit: cover) gets cropped, and its framing says which part shows: a
// focus point, as percentages across and down ("40% 30%"), and optionally how
// far it's zoomed in ("40% 30% 1.6", up to 4 times). The middle at full size
// is the default and is stored as "". Admins set it with the framing tool
// (admin.html, "Photo framing"), which draws the photo with this same style,
// so what they see there is what the site shows.

var focusRE = regexp.MustCompile(`^(100|[1-9]?[0-9])% (100|[1-9]?[0-9])%(?: ([1-4](?:\.[0-9]{1,2})?))?$`)

const maxZoom = 4

// framing is a photo's focus point (0–100) and zoom (1–4).
type framing struct{ X, Y, Z float64 }

func parseFocus(f string) (framing, bool) {
	m := focusRE.FindStringSubmatch(f)
	if m == nil {
		return framing{50, 50, 1}, false
	}
	x, _ := strconv.Atoi(m[1])
	y, _ := strconv.Atoi(m[2])
	z := 1.0
	if m[3] != "" {
		z, _ = strconv.ParseFloat(m[3], 64)
	}
	if z < 1 || z > maxZoom {
		return framing{50, 50, 1}, false
	}
	return framing{float64(x), float64(y), z}, true
}

func (fr framing) String() string {
	s := fmt.Sprintf("%g%% %g%%", fr.X, fr.Y)
	if fr.Z > 1 {
		s += " " + strconv.FormatFloat(fr.Z, 'f', -1, 64)
	}
	return s
}

// cleanFocus keeps a photo's framing ("50% 20%", or "50% 20% 1.5" zoomed in)
// or drops anything else. The middle at full size is the default, so it's
// stored as empty.
func cleanFocus(f string) string {
	fr, ok := parseFocus(strings.TrimSpace(f))
	if !ok || (fr.X == 50 && fr.Y == 50 && fr.Z == 1) {
		return ""
	}
	return fr.String()
}

// focusStyle is the style="" that frames a photo filling its box. Zoomed
// in, the photo is scaled up around its focus point and clipped back to its
// box (clip-path), so no wrapper element is needed; a rounded box (an avatar,
// a photo with rounded corners) keeps its corners when its CSS sets --r to
// the corner radius. The scaled box still counts toward the page's width,
// so main clips sideways overflow (site.css), and in a row of photos that
// swipes, a zoomed photo goes in a clipping wrapper (framedInRow). The
// admin's framing tool builds the same style (framingStyle in admin.html):
// change both together.
func focusStyle(f string) template.CSS {
	fr, ok := parseFocus(f)
	if !ok {
		return ""
	}
	s := fmt.Sprintf("object-position:%g%% %g%%", fr.X, fr.Y)
	if fr.Z > 1 {
		k := 1 - 1/fr.Z // how much of each side is cut, as a share of what's past the focus point
		p := func(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) + "%" }
		z := strconv.FormatFloat(fr.Z, 'f', -1, 64)
		s += fmt.Sprintf(";transform:scale(%s);transform-origin:%g%% %g%%;clip-path:inset(%s %s %s %s round calc(var(--r,0px) / %s))",
			z, fr.X, fr.Y, p(fr.Y*k), p((100-fr.X)*k), p((100-fr.Y)*k), p(fr.X*k), z)
	}
	return template.CSS(s)
}

// isZoomed reports whether a framing zooms in.
func isZoomed(f string) bool {
	fr, ok := parseFocus(f)
	return ok && fr.Z > 1
}

var zoomedImgRE = regexp.MustCompile(`<img [^>]*transform:scale\([^>]*>`)

// framedInRow wraps the zoomed photos in a row of photos (<div class="photos">)
// in a clipping span: the row swipes sideways, and a zoomed photo's scaled
// box would otherwise make it scroll past its end.
func framedInRow(h string) string {
	return zoomedImgRE.ReplaceAllStringFunc(h, func(img string) string { return `<span class="framed">` + img + `</span>` })
}

// splitPhotoFocus takes the framing off a page photo's address: in a page's
// text a photo is ![words](photos/ride.webp#focus=40,30,1.5).
func splitPhotoFocus(href string) (string, string) {
	i := strings.Index(href, "#focus=")
	if i < 0 {
		return href, ""
	}
	parts := strings.Split(href[i+len("#focus="):], ",")
	f := ""
	if len(parts) >= 2 {
		f = parts[0] + "% " + parts[1] + "%"
		if len(parts) > 2 {
			f += " " + parts[2]
		}
	}
	return href[:i], cleanFocus(f)
}

// SetPhotoFocus changes gallery photos' framing (by ID).
func (s *Store) SetPhotoFocus(focus map[int]string) error {
	if len(focus) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.d.Gallery {
		if f, ok := focus[p.ID]; ok {
			p.Focus = f
		}
	}
	return s.save()
}

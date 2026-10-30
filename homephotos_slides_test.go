package main

import (
	"strings"
	"testing"
)

func TestHomePhotosSlideshow(t *testing.T) {
	row := func(n int) string {
		var p []HomePhoto
		for range n {
			p = append(p, HomePhoto{Photo: "/photos/a.webp"})
		}
		return string(homePhotosWidget(p))
	}
	if s := row(6); strings.Contains(s, "slides") || !strings.Contains(s, "many") || strings.Contains(s, "strip-pause") {
		t.Errorf("6 photos: arrows, but no slideshow: %s", s)
	}
	if s := row(7); !strings.Contains(s, `class="photo-strip many slides"`) {
		t.Errorf("7 photos should be a slideshow: %s", s)
	}
	// A slideshow can be stopped (WCAG 2.2.2): site.js shows the button when it starts
	if s := row(7); !strings.Contains(s, `class="strip-pause" type="button" aria-label="Pause slideshow" hidden`) {
		t.Errorf("a slideshow needs its pause button: %s", s)
	}
}

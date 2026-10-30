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
	if s := row(6); strings.Contains(s, "slides") || !strings.Contains(s, "many") {
		t.Errorf("6 photos: arrows, but no slideshow: %s", s)
	}
	if s := row(7); !strings.Contains(s, `class="photo-strip many slides"`) {
		t.Errorf("7 photos should be a slideshow: %s", s)
	}
}

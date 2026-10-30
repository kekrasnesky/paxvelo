package main

import "testing"

func TestHomePhotosSaved(t *testing.T) {
	for _, c := range []struct {
		added, removed int
		want           string
	}{
		{0, 0, "Saved. The home page is updated."},
		{1, 0, "Added 1 photo. The home page is updated."},
		{3, 2, "Added 3 photos and removed 2 photos. The home page is updated."},
		{0, 1, "Removed 1 photo. The home page is updated."},
	} {
		if got := homePhotosSaved(c.added, c.removed); got != c.want {
			t.Errorf("homePhotosSaved(%d, %d) = %q", c.added, c.removed, got)
		}
	}
}

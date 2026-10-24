package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestPagesAndBoardAdmin(t *testing.T) {
	_, h, admin := testApp(t)
	for _, path := range []string{"/admin/pages", "/admin/pages/rides.md", "/admin/board"} {
		if w := do(t, h, admin, "GET", path, "", nil); w.Code != 200 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	ct, body := form(url.Values{"text": {"---\nmenu: Our Rides\n---\n# Our Rides\n\nEdited in the admin."}})
	do(t, h, admin, "POST", "/admin/pages/rides.md", ct, body)
	if !strings.Contains(do(t, h, nil, "GET", "/rides", "", nil).Body.String(), "Edited in the admin.") {
		t.Error("page edit didn't show")
	}
	ct, body = form(url.Values{"text": {"no title"}})
	if w := do(t, h, admin, "POST", "/admin/pages/rides.md", ct, body); !strings.Contains(w.Body.String(), "Not saved yet") {
		t.Error("broken page edit wasn't refused")
	}
	ct, body = form(url.Values{"count": {"1"}, "name0": {"Jane Smith"}, "role0": {"President"}, "order0": {"1"}})
	do(t, h, admin, "POST", "/admin/board", ct, body)
	if page := do(t, h, nil, "GET", "/committee", "", nil).Body.String(); !strings.Contains(page, "Jane Smith") || strings.Contains(page, "Vice President") {
		t.Error("board edit didn't show")
	}
}

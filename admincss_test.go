package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A grid column written as plain "1fr" can't shrink below its widest content,
// so one long email, a file picker or a select with long options pushed admin
// cards and fields past the edge of a phone (and main's overflow-x:clip hid
// the rest). Every fr column in admin.css is minmax(0,…fr), and a fixed
// minimum is capped with min(100%,…) so it fits a 320px screen.
func TestAdminGridsShrink(t *testing.T) {
	b, err := os.ReadFile("web/static/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	cols := regexp.MustCompile(`grid-template-columns:([^;}]*)`)
	minmax := regexp.MustCompile(`minmax\(([^()]|\([^()]*\))*\)`)
	fr := regexp.MustCompile(`\d*\.?\d+fr`)
	fixedMin := regexp.MustCompile(`minmax\(\d+(\.\d+)?(px|rem),`)
	for _, m := range cols.FindAllStringSubmatch(string(b), -1) {
		v := m[1]
		if fr.MatchString(minmax.ReplaceAllString(v, "")) {
			t.Errorf("grid-template-columns:%s has a bare fr column; use minmax(0,1fr)", v)
		}
		if fixedMin.MatchString(v) {
			t.Errorf("grid-template-columns:%s has a fixed minimum; use minmax(min(100%%,…),…)", v)
		}
	}
}

// One-off layout classes that the building blocks replaced (docs/look.md):
// none comes back in a template, admin.css or the Go code that writes HTML.
// Use .box .field .field-row .facts .rows .act-row .wrap-row instead.
func TestAdminRetiredClasses(t *testing.T) {
	retired := []string{"send-row", "todo-acts", "when-row", "auto-facts", "sent-tags", "howto", "allowance", "lede",
		"auto-actions", "auto-info", "auto-main", "auto-state", "auto-paused-note", "rsvp-row-sums"}
	files, _ := filepath.Glob("web/templates/*.html")
	gos, _ := filepath.Glob("*.go")
	for _, f := range gos {
		if !strings.HasSuffix(f, "_test.go") {
			files = append(files, f)
		}
	}
	files = append(files, "web/static/admin.css", "web/static/site.js")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range retired {
			if regexp.MustCompile(`(^|[^\w-])` + regexp.QuoteMeta(c) + `($|[^\w-])`).Match(b) {
				t.Errorf("%s uses the retired class %q", f, c)
			}
		}
	}
}

// A row of things that wraps is .wrap-row (or .act-row, .title-row): a class
// of its own sits on the block and keeps only what differs (docs/look.md).
// The two left out size the fields inside them in their own way.
func TestAdminRowsUseBlocks(t *testing.T) {
	b, err := os.ReadFile("web/static/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	ok := map[string]bool{"title-row": true, "act-row": true, "wrap-row": true, "sec-pin": true, "money-pick": true}
	re := regexp.MustCompile(`(?m)(?:^|\})\.([a-z0-9-]+)\{([^}]*)\}`)
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		if strings.Contains(m[2], "display:flex") && strings.Contains(m[2], "flex-wrap:wrap") && !ok[m[1]] {
			t.Errorf(".%s is a wrapping row of its own: put wrap-row on it and keep only what differs", m[1])
		}
	}
}

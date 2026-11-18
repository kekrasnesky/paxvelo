package main

import (
	"os"
	"regexp"
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

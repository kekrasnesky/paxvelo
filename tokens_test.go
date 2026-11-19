package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// adminOwnTokens are the shared tokens admin.css still sets to its own value
// (slightly different shades it had before tokens.css). Setting one of these
// in tokens.css's value is fine; adding to the list is drift.
var adminOwnTokens = []string{"--field", "--muted", "--tint", "--ok", "--ok-bg", "--ok-ink", "--err-bg", "--err-ink",
	"--warn", "--warn-bg", "--warn-ink", "--radius", "--ring", "--shadow-1", "--shadow-2", "--shadow-pop"}

var tokenDefRE = regexp.MustCompile(`(?:^|[;{\s])(--[a-z0-9-]+)\s*:`)

func cssTokens(t *testing.T, file string) []string {
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range tokenDefRE.FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1])
	}
	return out
}

// TestTokensShared keeps site.css and admin.css from drifting apart: a
// colour, corner, shadow or ring token is set in tokens.css, and site.css
// never sets one; admin.css only the ones in adminOwnTokens. No --r token.
func TestTokensShared(t *testing.T) {
	shared := map[string]bool{}
	for _, k := range cssTokens(t, "web/static/tokens.css") {
		shared[k] = true
	}
	if shared["--r"] {
		t.Error("tokens.css sets --r, which focus.go uses on rounded photos")
	}
	for _, k := range []string{"--red", "--ink", "--ok", "--r-sm", "--shadow-1", "--ring"} {
		if !shared[k] {
			t.Errorf("tokens.css lost %s", k)
		}
	}
	own := map[string]bool{}
	for _, k := range adminOwnTokens {
		own[k] = true
	}
	for _, k := range cssTokens(t, "web/static/site.css") {
		if shared[k] {
			t.Errorf("site.css sets %s: it belongs in tokens.css only", k)
		}
	}
	for _, k := range cssTokens(t, "web/static/admin.css") {
		if shared[k] && !own[k] {
			t.Errorf("admin.css sets %s: it belongs in tokens.css only", k)
		}
		if k == "--r" {
			t.Error("admin.css sets --r")
		}
	}
	s, err := newSite(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s.css), "--red-dark:#7e211f") || !strings.Contains(string(s.adminCSS), "--red-dark:#7e211f") {
		t.Error("tokens.css isn't in front of site.css and admin.css")
	}
}

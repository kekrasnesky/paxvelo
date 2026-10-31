package main

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

var inlineHandlerRE = regexp.MustCompile(`\son[a-z]+="`)

// Every script a public page runs is allowed by that page's own security
// policy, and nothing relies on onsubmit="" or onclick="", which the policy
// blocks. Admin pages get the looser policy, and uploaded files none.
func TestContentSecurityPolicy(t *testing.T) {
	a, h, _ := rideApp(t)
	ann, _ := signIn(t, h, "ann", "/calendar") // a club admin, who sees the ride page's cancel form
	ride := "/calendar/ride/" + seriesKey(1, nextWeekday(a, time.Tuesday))

	for _, path := range []string{"/", "/calendar", ride, "/calendar/new", "/join", "/kit", "/newsletter", "/membership", "/gallery", "/share-photos", "/no-such-page"} {
		w := do(t, h, ann, "GET", path, "", nil)
		page, csp := w.Body.Bytes(), w.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self' 'sha256-") || strings.Contains(csp, "unsafe-inline' https://www.instagram") {
			t.Errorf("%s: not the public policy: %q", path, csp)
			continue
		}
		hashes := scriptHashes(page)
		if len(hashes) == 0 {
			t.Errorf("%s: no inline scripts found; has the layout changed?", path)
		}
		for _, hash := range hashes {
			if !strings.Contains(csp, hash) {
				t.Errorf("%s: inline script %s isn't allowed by the policy, so it won't run", path, hash)
			}
		}
		if m := inlineHandlerRE.Find(inlineScriptRE.ReplaceAll(page, nil)); m != nil { // site.js's own comments may say onsubmit=""
			t.Errorf("%s: has an inline event handler (%s…), which the policy blocks: use data-confirm or site.js", path, m)
		}
	}
	if !strings.Contains(do(t, h, ann, "GET", ride, "", nil).Body.String(), `data-confirm="Cancel `) {
		t.Error("the ride page should ask before an admin cancels the ride")
	}

	if csp := do(t, h, nil, "GET", "/admin/login", "", nil).Header().Get("Content-Security-Policy"); csp != adminCSP {
		t.Errorf("admin pages: %q", csp)
	}
	if csp := do(t, h, nil, "GET", "/files/minutes.pdf", "", nil).Header().Get("Content-Security-Policy"); csp != "" {
		t.Errorf("uploaded files shouldn't get a policy: %q", csp)
	}
}

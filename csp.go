package main

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
)

// The Content-Security-Policy tells browsers which scripts, frames, and
// other resources a page may load. If a script ever slipped into a page
// (say, through a bug in how a ride name is shown), the browser would refuse
// to run it.
//
// Public pages may run only the layout's own inline scripts, matched by
// their hashes, plus files from the site and Instagram's embed script.
// The hashes come from the not-found page, rendered once at startup with no
// one's text in it, so layout.html stays the only place those scripts are
// written. Change one there and its hash follows on the next start.
//
// The admin is looser: its pages are full of small inline scripts, and only
// signed-in admins see them.

// inlineScriptRE finds the inline scripts a browser runs: plain <script> and
// speculation rules. JSON-LD (type="application/ld+json") is data that never
// runs, so the policy doesn't cover it.
var inlineScriptRE = regexp.MustCompile(`(?s)<script(?: type="speculationrules")?>(.*?)</script>`)

// scriptHashes returns the CSP source for each inline script in page.
func scriptHashes(page []byte) []string {
	var out []string
	for _, m := range inlineScriptRE.FindAllSubmatch(page, -1) {
		sum := sha256.Sum256(m[1])
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return out
}

func publicCSP(hashes []string) string {
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' " + strings.Join(hashes, " ") + " https://www.instagram.com",
		"style-src 'self' 'unsafe-inline'",  // the inlined site.css, and photo focus points in style=""
		"img-src 'self' data: blob: https:", // route maps come from Strava's servers; blob: for photos previewed before they're sent
		"frame-src https://www.instagram.com",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

var adminCSP = strings.Join([]string{
	"default-src 'self'",
	"script-src 'self' 'unsafe-inline' https://www.instagram.com",
	"style-src 'self' 'unsafe-inline'",
	"img-src 'self' data: blob: https:", // blob: for photos previewed before they're uploaded
	"frame-src https://www.instagram.com",
	"connect-src 'self'",
	"object-src 'none'",
	"base-uri 'self'",
	"frame-ancestors 'none'",
}, "; ")

// cspFor returns the policy for a request's path, or "" for uploaded files
// (PDF minutes and the like), which open in the browser's own viewer.
func cspFor(path, public string) string {
	switch {
	case strings.HasPrefix(path, "/files/"), strings.HasPrefix(path, "/media/"):
		return ""
	case path == "/admin" || strings.HasPrefix(path, "/admin/"):
		return adminCSP
	}
	return public
}

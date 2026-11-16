package main

import (
	"encoding/json"
	"net/http"
)

// The site as a phone app: a member adds it to their home screen (Share →
// Add to Home Screen on an iPhone, Install app on Android) and it opens full
// screen with the club's icon. The manifest names it and points to the
// icons; the service worker (web/static/sw.js, served at /sw.js so it covers
// the whole site) shows pages already opened when there's no signal, and
// /offline for the rest. It's also what push notifications will need.

// appManifest is /manifest.webmanifest.
func (a *App) appManifest(w http.ResponseWriter, r *http.Request) {
	type icon struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose,omitempty"`
	}
	type shortcut struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	m := struct {
		ID          string     `json:"id"`
		Name        string     `json:"name"`
		ShortName   string     `json:"short_name"`
		Description string     `json:"description"`
		StartURL    string     `json:"start_url"`
		Scope       string     `json:"scope"`
		Display     string     `json:"display"`
		Background  string     `json:"background_color"`
		Theme       string     `json:"theme_color"`
		Icons       []icon     `json:"icons"`
		Shortcuts   []shortcut `json:"shortcuts"`
	}{
		ID: "/", Name: siteName, ShortName: siteName, Description: siteDescription,
		StartURL: "/", Scope: "/", Display: "standalone", Background: "#f5f2ec", Theme: "#2d2d2d",
		// A close-up of the logo whose stripes run off the edges, so phones
		// that cut icons into circles or rounded squares can use the same
		// picture.
		Icons: []icon{
			{"/static/img/app-192.png", "192x192", "image/png", "any"},
			{"/static/img/app-512.png", "512x512", "image/png", "any"},
			{"/static/img/app-192.png", "192x192", "image/png", "maskable"},
			{"/static/img/app-512.png", "512x512", "image/png", "maskable"},
		},
		Shortcuts: []shortcut{{"Calendar", "/calendar"}, {"My Membership", "/membership"}, {"Ride Waiver", "/ride-waiver"}},
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	json.NewEncoder(w).Encode(m)
}

// serviceWorker is /sw.js. Browsers check it for a new version on every
// visit; no-cache keeps a stale copy from hiding one.
func (a *App) serviceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(a.site.sw))
}

// offlinePage is what the app shows for a page it hasn't saved when there's
// no signal. The service worker saves it when it's installed.
func (a *App) offlinePage(w http.ResponseWriter, r *http.Request) {
	a.site.render(w, http.StatusOK, "offline", "", "No connection — "+siteName, nil)
}

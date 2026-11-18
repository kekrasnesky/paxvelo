package main

import (
	"net/http"
	"strings"
)

// The admin app's frame is chosen by the server. The app's start URL
// (adminManifest) carries ?app=1, which sets the pv_app cookie: a plain
// switch, path /admin, holding nothing about who the admin is. While it's
// set every admin page draws the app's Home bar in place of the website's
// menu bar, and /admin opens the app's home. ?app=0 on any admin page
// clears it again (the app home links to it), for a phone whose browser
// shares cookies with the installed app.

const appCookie = "pv_app"

// appFrame sets or clears the cookie when an /admin page is opened with
// ?app=, then opens the same page without it, so a reload or a copied link
// doesn't carry the switch along.
func (a *App) appFrame(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && (r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/")) {
			q := r.URL.Query()
			if v := q.Get("app"); v == "1" || v == "0" {
				c := &http.Cookie{Name: appCookie, Value: "1", Path: "/admin", MaxAge: 400 * 24 * 3600,
					HttpOnly: true, Secure: strings.HasPrefix(a.cfg.SiteURL, "https://"), SameSite: http.SameSiteLaxMode}
				if v == "0" {
					c.Value, c.MaxAge = "", -1
				}
				http.SetCookie(w, c)
				q.Del("app")
				to := r.URL.Path
				if len(q) > 0 {
					to += "?" + q.Encode()
				}
				http.Redirect(w, r, to, http.StatusSeeOther)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// inApp reports whether this request comes from the admin app.
func inApp(r *http.Request) bool {
	c, err := r.Cookie(appCookie)
	return err == nil && c.Value == "1"
}

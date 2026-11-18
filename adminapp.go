package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The admin portal as its own phone app. Added to the home screen from any
// admin page, it gets its own name and a black icon (so it isn't mixed up
// with the club's app) and opens on /admin/app: big buttons for the screens
// used most, each with a count of what's waiting there, under a list of what
// needs this admin. Inside the app (display-mode: standalone) every admin
// screen swaps the website's menu bar for a Home button back to the buttons;
// in a browser nothing changes.

const adminAppPath = "/admin/app"

// appTile is one button on the admin app's home.
type appTile struct {
	Label, Href, Icon string
	Badge             int
}

// appTileGroup is a row of buttons under a small heading. Key picks the
// group's color (g-rides, g-people… in admin.css).
type appTileGroup struct {
	Label string
	Tiles []appTile
	Key   string
}

type adminAppView struct {
	Items  []attentionItem
	Rides  []RideOcc // the next few, not cancelled
	Groups []appTileGroup
	First  string // the admin's first name
	// The Alerts panel (adminpush.go): the kinds this admin can pick, and
	// the site's push key. No Kinds = logged in with the setup password.
	Kinds   []struct{ Key, Label string }
	PushKey string
	Search  *appHits // ?q= without scripts (appsearch.go)
}

// appTiles are the buttons, in groups, for this admin's level. Board-only
// screens are left out for website admins.
func (a *App) appTiles(r *http.Request, requests int) []appTileGroup {
	board := a.isBoard(r)
	today := a.today().Format(time.DateOnly)
	unsigned := 0
	if board {
		for _, u := range a.unsignedRiders(a.cfg.ThisYear()) {
			if u.Member.Paid() {
				unsigned++
			}
		}
	}
	groups := []appTileGroup{
		{Label: "Rides", Tiles: []appTile{
			{"Calendar", "/admin/calendar", "calendar", len(a.store.HeldRides())},
			{"Loaner bikes", "/admin/loaners", "bike", a.store.WaitingLoaners(today)},
			{"RSVPs", "/admin/rsvp", "clipboard", 0},
		}},
		{Label: "People", Tiles: []appTile{
			{"Members", "/admin/members", "users", 0},
			{"Waivers", "/admin/waivers", "shield", unsigned},
			{"Stats", "/admin/stats", "chart", 0},
		}},
		{Label: "Email", Tiles: []appTile{
			{"Send an email", "/admin/email", "mail", 0},
			{"Newsletter", "/admin/newsletter", "newspaper", 0},
			{"Send an alert", "/admin/email/alerts", "megaphone", 0},
		}},
		{Label: "Money", Tiles: []appTile{
			{"Requests", requestsPath, "receipt", requests},
			{"Treasurer", "/admin/treasurer", "wallet", 0},
			{"Reports", "/admin/treasurer/reports", "printer", 0},
		}},
		{Label: "Website", Tiles: []appTile{
			{"Review photos", "/admin/submitted", "image", a.store.NewSubmitted()},
			{"Gallery", "/admin/gallery", "images", 0},
			{"Pages", "/admin/pages", "file", 0},
		}},
	}
	for i := range groups {
		groups[i].Key = strings.ToLower(groups[i].Label)
	}
	if board {
		return groups
	}
	var out []appTileGroup
	for _, g := range groups {
		var keep []appTile
		for _, t := range g.Tiles {
			if !boardHref(t.Href) {
				keep = append(keep, t)
			}
		}
		if len(keep) > 0 {
			out = append(out, appTileGroup{g.Label, keep, g.Key})
		}
	}
	return out
}

// adminAppPage is the app's home: what needs you, the next rides, buttons.
func (a *App) adminAppPage(w http.ResponseWriter, r *http.Request) {
	items, requests := a.attentionFor(r)
	if a.isBoard(r) { // the computer Home lists these in its Members card
		for _, m := range a.memberSnapshot().Signups {
			items = append(items, attentionItem{
				Text: fmt.Sprintf("%s joined on the website, not marked paid yet.", m.Name),
				Href: fmt.Sprintf("/admin/members/%d", m.ID), Link: "Open",
				Acts: []attentionAct{{Label: "Mark paid", Icon: "check", Action: fmt.Sprintf("/admin/members/%d/paid", m.ID), Fields: map[string]string{"paid": "1"}, Board: true}},
			})
		}
	}
	items = withReturn(items, adminAppPath)
	rides, _ := a.weekRides()
	if len(rides) > 3 {
		rides = rides[:3]
	}
	name, _ := a.adminName(r)
	v := adminView{Title: "Home", Tab: "app", App: &adminAppView{
		Items: items, Rides: rides, Groups: a.appTiles(r, requests), First: firstName(name),
		Search: a.appSearch(r.URL.Query().Get("q")),
	}}
	if u, ok := a.currentAdmin(r); ok && u.ID != 0 {
		v.App.Kinds, v.App.PushKey = a.alertKinds(r), a.store.PushPublicKey()
	}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "admin-app", v)
}

// adminManifest is /admin-app.webmanifest. It sits outside /admin because
// browsers fetch manifests without the login cookie.
func (a *App) adminManifest(w http.ResponseWriter, r *http.Request) {
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
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		ShortName  string     `json:"short_name"`
		StartURL   string     `json:"start_url"`
		Scope      string     `json:"scope"`
		Display    string     `json:"display"`
		Background string     `json:"background_color"`
		Theme      string     `json:"theme_color"`
		Icons      []icon     `json:"icons"`
		Shortcuts  []shortcut `json:"shortcuts"`
	}{
		ID: adminAppPath, Name: siteName + " Admin", ShortName: "PV Admin",
		StartURL: adminAppPath, Scope: "/admin", Display: "standalone", Background: "#f5f2ec", Theme: "#2d2d2d",
		Icons: []icon{
			{"/static/img/admin-192.png", "192x192", "image/png", "any"},
			{"/static/img/admin-512.png", "512x512", "image/png", "any"},
			{"/static/img/admin-192.png", "192x192", "image/png", "maskable"},
			{"/static/img/admin-512.png", "512x512", "image/png", "maskable"},
		},
		Shortcuts: []shortcut{{"Calendar", "/admin/calendar"}, {"Members", "/admin/members"}, {"Requests", requestsPath}},
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	json.NewEncoder(w).Encode(m)
}

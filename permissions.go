package main

import (
	"net/http"
	"strings"
)

// Admin levels. Every admin login is one of three:
//
//   - Website admin: runs rides and the calendar, emails members about rides
//     and events, reviews shared photos and keeps the gallery, reads minutes,
//     looks members up (for an emergency contact) and sends their own
//     spending requests.
//   - Board: anyone holding a role on Website → Board. Everything the
//     president can do except change who has which powers.
//   - President: the one admin marked president, or the setup password.
//     Only they change permissions: who is on the board and in which role,
//     who is an admin, who is president, and the full backup (it holds the
//     admins' password hashes).
//
// Routes say their level in main.go (auth, boardOnly, president); the menu
// hides what a level can't open (adminNavFor).
type adminLevel int

const (
	levelNone adminLevel = iota
	levelAdmin
	levelBoard
	levelPresident
)

func (a *App) levelOf(u AdminUser) adminLevel {
	switch {
	case isPresident(u):
		return levelPresident
	case len(a.boardRoles(u)) > 0:
		return levelBoard
	}
	return levelAdmin
}

// level is the logged-in admin's level (levelNone when logged out).
func (a *App) level(r *http.Request) adminLevel {
	u, ok := a.currentAdmin(r)
	if !ok {
		return levelNone
	}
	return a.levelOf(u)
}

// isBoard reports whether the logged-in admin is on the board (or president).
func (a *App) isBoard(r *http.Request) bool { return a.level(r) >= levelBoard }

// levelName is how the Admins page labels each login.
func levelName(l adminLevel) string {
	switch l {
	case levelPresident:
		return "President"
	case levelBoard:
		return "Board"
	}
	return "Website admin"
}

// boardTabs are the admin screens only the board (and president) can open.
// Website admins don't see them in the menu.
var boardTabs = map[string]bool{
	"waivers": true, "auto": true, "newsletter": true, "sent-log": true,
	"pages": true, "home-photos": true, "board": true, "instagram": true,
	"expenses": true, "income": true, "budget": true, "reports": true,
	"security": true, "kit": true,
}

// boardHref reports whether an admin link leads to a board-only screen, so
// Home can leave it out for website admins.
func boardHref(href string) bool {
	for _, p := range []string{"/admin/waivers", "/admin/ride-waivers", "/admin/email/auto", "/admin/email/log", "/admin/newsletter",
		"/admin/pages", "/admin/home-photos", "/admin/board", "/admin/instagram", "/admin/security", "/admin/backup", "/admin/kit"} {
		if href == p || strings.HasPrefix(href, p+"/") || strings.HasPrefix(href, p+"?") || strings.HasPrefix(href, p+"#") {
			return true
		}
	}
	return strings.HasPrefix(href, "/admin/treasurer") && !strings.HasPrefix(href, requestsPath)
}

// deny answers a request the admin's level doesn't allow.
func deny(w http.ResponseWriter, r *http.Request, from, code string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Error(w, errorText[code], http.StatusForbidden)
		return
	}
	back(w, r, from+"?err="+code)
}

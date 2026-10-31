package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

// The club PIN: a code admins set on Security → Club PIN & access and share with
// members, so they can post without an account. With it, photos sent in go
// straight into their album (sharephotos.go) and a ride added without
// signing in with Strava goes straight onto the calendar (calendar.go),
// instead of waiting for an admin. Wrong guesses are limited, and admins
// change the PIN whenever it gets around.

const (
	minPIN   = 4  // characters
	maxPIN   = 40 // characters
	pinTries = 10 // an hour, from one connection, right or wrong
)

const (
	pinWrong = "That PIN isn’t right. Check it, or leave it empty and an admin will look first."
	pinLimit = "Too many PIN tries. Try again later, or leave the PIN empty and an admin will look first."
)

// securityPage is Security → Club PIN & access: the club PIN, the other ways
// in, and the backup.
func (a *App) securityPage(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Club PIN & access", Tab: "security", ClubPIN: a.store.ClubPIN(),
		StravaReady: a.stravaReady(), APIOn: a.cfg.CalendarAPIKey != "", CanManage: a.canManage(r)}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "security", v)
}

// securityAction sets the club PIN, or turns it off.
func (a *App) securityAction(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	pin := strings.TrimSpace(r.FormValue("pin"))
	if r.FormValue("off") != "" {
		pin = ""
	}
	switch {
	case pin != "" && (len([]rune(pin)) < minPIN || len([]rune(pin)) > maxPIN):
		back(w, r, "/admin/security?err=pin-short")
	case a.store.SetClubPIN(pin) != nil:
		back(w, r, "/admin/security?err=save")
	case pin == "":
		back(w, r, "/admin/security?ok=pin-off")
	default:
		back(w, r, "/admin/security?ok=pin-saved")
	}
}

// checkPIN reports whether a PIN was typed and, if so, whether it's the
// club's: "" when it's right, or what to tell them. With no PIN set or none
// typed, it says nothing.
func (a *App) checkPIN(r *http.Request, typed string) (used bool, problem string) {
	pin, typed := a.store.ClubPIN(), strings.TrimSpace(typed)
	if pin == "" || typed == "" {
		return false, ""
	}
	if !a.limit.Allow("pin:"+clientIP(r), pinTries, time.Hour) { // every try counts, so it can't be guessed
		return true, pinLimit
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(typed)), []byte(strings.ToLower(pin))) != 1 {
		return true, pinWrong
	}
	return true, ""
}

// ClubPIN is the club PIN, or "" when it's off.
func (s *Store) ClubPIN() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.ClubPIN
}

func (s *Store) SetClubPIN(pin string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.ClubPIN = pin
	return s.save()
}

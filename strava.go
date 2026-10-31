package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Strava: the club's events are imported onto the calendar every hour
// (stravasync.go), and ride pages show their routes and who's going
// (stravainfo.go). Riders don't sign in with Strava: leaders sign up by
// name, and the club PIN (clubpin.go) lets them step down, cancel, or add a
// ride without waiting for an admin.
//
// Strava doesn't let other websites create club events, so leaders who want
// RSVPs post their ride on Strava themselves; the calendar links to it.

func (a *App) stravaReady() bool {
	return a.cfg.StravaClientID != "" && a.cfg.StravaClientSecret != "" && a.cfg.StravaClubID != ""
}

// stravaKey signs the state of the admin's Connect Strava step.
func (a *App) stravaKey() []byte {
	k := sha256.Sum256([]byte("rider\x00" + a.store.Secret()))
	return k[:]
}

type stravaAthlete struct {
	ID        int64  `json:"id"`
	FirstName string `json:"firstname"`
	LastName  string `json:"lastname"`
}

// shortName is how the calendar shows someone from Strava: first name and
// last initial, the way Strava shows members to each other.
func (s stravaAthlete) shortName() string {
	name := strings.TrimSpace(s.FirstName)
	if l := []rune(strings.TrimSpace(s.LastName)); len(l) > 0 {
		name += " " + string(l[0]) + "."
	}
	return cleanName(name)
}

var stravaClient = &http.Client{Timeout: 20 * time.Second}

func stravaDo(req *http.Request, out any) error {
	resp, err := stravaClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("strava answered %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

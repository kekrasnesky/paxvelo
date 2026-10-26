package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Club admins sign in with Strava ("Sign in with Strava" on the calendar) to
// change leaders; riders sign up to lead by name. Strava tells us who they are, whether they belong to the PaxVelo club, and
// whether they're one of its admins, which is what lets someone post club
// events on Strava. Nothing else is read, no password is ever seen, and the
// Strava access key is used once at sign-in and then thrown away.
//
// Strava doesn't let other websites create club events, so leaders still post
// their ride on Strava themselves; the calendar links to it.

const riderCookie = "pv_rider"

// Rider is someone signed in with Strava.
type Rider struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`  // as Strava shows it to other members, like "Jane D."
	Admin bool   `json:"admin"` // a PaxVelo club admin on Strava
	// Checked is when Strava last said so (Unix seconds). The sign-in lasts a month,
	// but the admin part is trusted for a day, so removing someone as a club admin
	// on Strava takes effect the next day, not the next month.
	Checked int64 `json:"checked,omitempty"`
}

func (a *App) stravaReady() bool {
	return a.cfg.StravaClientID != "" && a.cfg.StravaClientSecret != "" && a.cfg.StravaClubID != ""
}

func (a *App) riderKey() []byte {
	k := sha256.Sum256([]byte("rider\x00" + a.store.Secret()))
	return k[:]
}

// rider returns who is signed in with Strava, if anyone.
func (a *App) rider(r *http.Request) (Rider, bool) {
	c, err := r.Cookie(riderCookie)
	if err != nil {
		return Rider{}, false
	}
	exp, rest, _ := strings.Cut(c.Value, ".")
	enc, sig, ok := strings.Cut(rest, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(sign(a.riderKey(), exp+"."+enc))) {
		return Rider{}, false
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() >= n {
		return Rider{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(enc)
	var rd Rider
	if err != nil || json.Unmarshal(b, &rd) != nil || rd.ID == 0 {
		return Rider{}, false
	}
	if rd.Admin && time.Since(time.Unix(rd.Checked, 0)) > riderAdminLife {
		rd.Admin = false // still signed in, but signing in again is needed for admin powers
	}
	return rd, true
}

// riderAdminLife is how long a Strava sign-in keeps its club-admin powers.
const riderAdminLife = 24 * time.Hour

func (a *App) setRider(w http.ResponseWriter, rd *Rider) {
	maxAge, value := -1, ""
	if rd != nil {
		c := *rd
		c.Checked = time.Now().Unix() // called when someone has just signed in
		rd = &c
		maxAge = 30 * 24 * 3600
		b, _ := json.Marshal(rd)
		exp := strconv.FormatInt(time.Now().Add(time.Duration(maxAge)*time.Second).Unix(), 10)
		enc := base64.RawURLEncoding.EncodeToString(b)
		value = exp + "." + enc + "." + sign(a.riderKey(), exp+"."+enc)
	}
	http.SetCookie(w, &http.Cookie{
		Name: riderCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: strings.HasPrefix(a.cfg.SiteURL, "https://"), SameSite: http.SameSiteLaxMode,
	})
}

// safeNext keeps sign-in redirects on this site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		return "/calendar"
	}
	return next
}

// stravaLogin sends the rider to Strava to approve signing in.
func (a *App) stravaLogin(w http.ResponseWriter, r *http.Request) {
	if !a.stravaReady() {
		http.Redirect(w, r, "/calendar?err=nostrava", http.StatusSeeOther)
		return
	}
	next := safeNext(r.URL.Query().Get("next"))
	// The state carries where to go afterwards, signed and short-lived, so the
	// answer from Strava can't be forged or replayed later.
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	payload := ts + "|" + next
	state := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sign(a.riderKey(), "state|"+payload)
	q := url.Values{
		"client_id": {a.cfg.StravaClientID}, "response_type": {"code"}, "approval_prompt": {"auto"},
		"redirect_uri": {a.cfg.SiteURL + "/strava/callback"}, "scope": {"read"}, "state": {state},
	}
	http.Redirect(w, r, a.cfg.StravaAPI+"/oauth/authorize?"+q.Encode(), http.StatusSeeOther)
}

func (a *App) checkState(state string) (string, bool) {
	enc, sig, ok := strings.Cut(state, ".")
	b, err := base64.RawURLEncoding.DecodeString(enc)
	if !ok || err != nil || !hmac.Equal([]byte(sig), []byte(sign(a.riderKey(), "state|"+string(b)))) {
		return "", false
	}
	ts, next, _ := strings.Cut(string(b), "|")
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(n, 0)) > 15*time.Minute {
		return "", false
	}
	return safeNext(next), true
}

// stravaCallback finishes signing in: it swaps Strava's one-time code for the
// rider's name and club membership, then signs them in to the calendar.
func (a *App) stravaCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	next, ok := a.checkState(q.Get("state"))
	if !ok {
		http.Redirect(w, r, "/calendar?err=signin", http.StatusSeeOther)
		return
	}
	withErr := func(code string) string {
		if strings.Contains(next, "?") {
			return next + "&err=" + code
		}
		return next + "?err=" + code
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		http.Redirect(w, r, withErr("denied"), http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	rd, err := a.stravaSignIn(ctx, q.Get("code"))
	switch {
	case errors.Is(err, errNotMember):
		http.Redirect(w, r, withErr("notmember"), http.StatusSeeOther)
		return
	case err != nil:
		log.Printf("strava sign-in: %v", err)
		http.Redirect(w, r, withErr("signin"), http.StatusSeeOther)
		return
	}
	a.setRider(w, &rd)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *App) stravaSignOut(w http.ResponseWriter, r *http.Request) {
	a.setRider(w, nil)
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

var errNotMember = errors.New("not a member of the club on Strava")

type stravaAthlete struct {
	ID        int64  `json:"id"`
	FirstName string `json:"firstname"`
	LastName  string `json:"lastname"`
}

// shortName is how the calendar shows a rider: first name and last initial,
// the way Strava shows members to each other.
func (s stravaAthlete) shortName() string {
	name := strings.TrimSpace(s.FirstName)
	if l := []rune(strings.TrimSpace(s.LastName)); len(l) > 0 {
		name += " " + string(l[0]) + "."
	}
	return cleanName(name)
}

func (a *App) stravaSignIn(ctx context.Context, code string) (Rider, error) {
	form := url.Values{
		"client_id": {a.cfg.StravaClientID}, "client_secret": {a.cfg.StravaClientSecret},
		"code": {code}, "grant_type": {"authorization_code"},
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", a.cfg.StravaAPI+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tok struct {
		AccessToken string        `json:"access_token"`
		Athlete     stravaAthlete `json:"athlete"`
	}
	if err := stravaDo(req, &tok); err != nil {
		return Rider{}, fmt.Errorf("token: %w", err)
	}
	get := func(path string, out any) error {
		req, _ := http.NewRequestWithContext(ctx, "GET", a.cfg.StravaAPI+"/api/v3"+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		return stravaDo(req, out)
	}

	// Their clubs say whether they're in PaxVelo, and whether they're an admin of it.
	var clubs []struct {
		ID    int64 `json:"id"`
		Admin bool  `json:"admin"`
		Owner bool  `json:"owner"`
	}
	if err := get("/athlete/clubs?per_page=200", &clubs); err != nil {
		return Rider{}, fmt.Errorf("clubs: %w", err)
	}
	rd := Rider{ID: tok.Athlete.ID, Name: tok.Athlete.shortName()}
	member := false
	for _, c := range clubs {
		if strconv.FormatInt(c.ID, 10) == a.cfg.StravaClubID {
			member, rd.Admin = true, c.Admin || c.Owner
		}
	}
	if !member {
		return Rider{}, errNotMember
	}

	// While we have a key, refresh the list of club admins the admin portal shows.
	var admins []stravaAthlete
	if err := get("/clubs/"+a.cfg.StravaClubID+"/admins?per_page=200", &admins); err == nil {
		var names []string
		for _, ad := range admins {
			names = append(names, ad.shortName())
		}
		a.store.SetStravaAdmins(names)
	}
	return rd, nil
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

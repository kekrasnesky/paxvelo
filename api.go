package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// The calendar API, for scripts and other tools:
//
//	GET    /api/rides?from=2026-11-01&to=2026-11-30   every ride (no key needed)
//	PUT    /api/rides/{id}                            add or update a ride
//	DELETE /api/rides/{id}                            remove a ride
//	POST   /api/strava/sync                           import the club's Strava events now
//
// Changes need the key from CALENDAR_API_KEY, sent as
// "Authorization: Bearer <key>". {id} is your own lasting name for the ride,
// like "coffee-2026-11-07"; sending the same id again updates that ride, and
// leaders who signed up on the website are kept. CLAUDE.md has examples.

type apiRide struct {
	Key       string      `json:"key"`
	ID        string      `json:"id,omitempty"`
	Date      string      `json:"date"`
	Time      string      `json:"time,omitempty"`
	Name      string      `json:"name"`
	Place     string      `json:"place,omitempty"`
	Details   string      `json:"details,omitempty"`
	StravaURL string      `json:"strava_url,omitempty"`
	Groups    []string    `json:"groups,omitempty"`
	Leaders   []apiLeader `json:"leaders"`
	Cancelled bool        `json:"cancelled,omitempty"`
	URL       string      `json:"url"`
}

type apiLeader struct {
	Group string `json:"group,omitempty"`
	Name  string `json:"name,omitempty"` // empty: this spot needs a leader
}

func (a *App) apiRideOf(o RideOcc) apiRide {
	out := apiRide{Key: o.Key, ID: strings.TrimPrefix(o.External, "api:"), Date: o.Date, Time: o.Time, Name: o.Name, Place: o.PublicPlace(),
		Details: o.Details, StravaURL: o.StravaURL, Groups: o.Groups, Cancelled: o.Cancelled, URL: a.cfg.SiteURL + "/calendar/ride/" + o.Key,
		Leaders: []apiLeader{}}
	if strings.HasPrefix(o.External, "strava:") {
		out.ID = ""
	}
	for _, s := range o.Slots() {
		l := apiLeader{Group: s.Group}
		if s.Leader != nil {
			l.Name = s.Leader.Name
		}
		out.Leaders = append(out.Leaders, l)
	}
	return out
}

func apiJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	apiJSON(w, status, map[string]string{"error": msg})
}

// apiAuth checks the API key.
func (a *App) apiAuth(w http.ResponseWriter, r *http.Request) bool {
	if a.cfg.CalendarAPIKey == "" {
		apiError(w, http.StatusForbidden, "the calendar API is turned off; set CALENDAR_API_KEY in paxvelo.conf")
		return false
	}
	if !a.limit.Allow("api:"+clientIP(r), 600, time.Hour) {
		apiError(w, http.StatusTooManyRequests, "too many requests; wait a bit")
		return false
	}
	given, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	g, k := sha256.Sum256([]byte(given)), sha256.Sum256([]byte(a.cfg.CalendarAPIKey))
	if subtle.ConstantTimeCompare(g[:], k[:]) != 1 {
		apiError(w, http.StatusUnauthorized, "missing or wrong API key")
		return false
	}
	return true
}

// apiRides lists rides. Anyone can read the calendar, so no key is needed.
func (a *App) apiRides(w http.ResponseWriter, r *http.Request) {
	from, to := a.today(), a.today().AddDate(0, 0, 60)
	if t, err := time.Parse("2006-01-02", r.URL.Query().Get("from")); err == nil {
		from = t
	}
	if t, err := time.Parse("2006-01-02", r.URL.Query().Get("to")); err == nil {
		to = t
	}
	if to.Before(from) || to.Sub(from) > 400*24*time.Hour {
		apiError(w, http.StatusBadRequest, "pick a from and to date at most 400 days apart")
		return
	}
	out := []apiRide{}
	for _, o := range a.store.Rides(from, to) {
		out = append(out, a.apiRideOf(o))
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	apiJSON(w, http.StatusOK, out)
}

var apiIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,99}$`)

func (a *App) apiPutRide(w http.ResponseWriter, r *http.Request) {
	if !a.apiAuth(w, r) {
		return
	}
	id := r.PathValue("id")
	if !apiIDRE.MatchString(id) {
		apiError(w, http.StatusBadRequest, "the id can use letters, numbers, and - _ . : (up to 100)")
		return
	}
	var in ExternalRide
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		apiError(w, http.StatusBadRequest, "send a JSON ride: "+err.Error())
		return
	}
	in.External, in.Name, in.Place, in.Details = "api:"+id, cleanName(in.Name), cleanName(in.Place), cleanName(in.Details)
	in.Groups = parseGroups(strings.Join(in.Groups, ","))
	_, dateErr := time.Parse("2006-01-02", in.Date)
	_, timeErr := time.Parse("15:04", in.Time)
	switch {
	case dateErr != nil:
		apiError(w, http.StatusBadRequest, `"date" must look like 2026-11-07`)
		return
	case in.Time != "" && timeErr != nil:
		apiError(w, http.StatusBadRequest, `"time" must look like 08:30 (24-hour)`)
		return
	case in.Name == "":
		apiError(w, http.StatusBadRequest, `"name" is required`)
		return
	case in.StravaURL != "" && !stravaLink(in.StravaURL):
		apiError(w, http.StatusBadRequest, `"strava_url" must start with https://www.strava.com/`)
		return
	}
	o, how, err := a.store.UpsertExternal(in, false)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "couldn’t save the ride")
		return
	}
	status := http.StatusOK
	if how == "added" {
		status = http.StatusCreated
	}
	apiJSON(w, status, a.apiRideOf(o))
}

func (a *App) apiDeleteRide(w http.ResponseWriter, r *http.Request) {
	if !a.apiAuth(w, r) {
		return
	}
	found, err := a.store.RemoveExternal("api:" + r.PathValue("id"))
	switch {
	case err != nil:
		apiError(w, http.StatusInternalServerError, "couldn’t save")
	case !found:
		apiError(w, http.StatusNotFound, "no ride with that id")
	default:
		apiJSON(w, http.StatusOK, map[string]string{"result": "removed (or cancelled, if leaders had signed up)"})
	}
}

func (a *App) apiStravaSync(w http.ResponseWriter, r *http.Request) {
	if !a.apiAuth(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := a.syncStrava(ctx)
	if err != nil {
		apiError(w, http.StatusBadGateway, err.Error())
		return
	}
	apiJSON(w, http.StatusOK, map[string]int{"added": res.Added, "updated": res.Updated, "matched_weekly": res.Linked, "removed": res.Removed})
}

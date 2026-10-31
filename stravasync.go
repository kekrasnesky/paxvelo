package main

import (
	"context"
	"crypto/hmac"
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

// Importing the club's Strava events. An admin connects a Strava account
// that belongs to the club once (Rides tab → Connect Strava); the site then
// reads the club's upcoming events every hour and puts them on the calendar.
// An event that matches a weekly ride (same day and the same name or start
// time) is linked to it instead of being added twice. Leaders signed up on
// the website are kept when an event is updated.
//
// Strava's public API documents club members and activities but not club
// events; this uses the clubs/{id}/group_events address its own apps use.
// If Strava ever turns that off, the Rides tab shows the error and the
// calendar keeps working without the import.

// StravaLink is the connected Strava account used for importing.
type StravaLink struct {
	Athlete      string     `json:"athlete"`
	RefreshToken string     `json:"refresh_token"`
	AccessToken  string     `json:"access_token,omitempty"`
	Expires      int64      `json:"expires,omitempty"`
	LastSync     *time.Time `json:"last_sync,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	LastResult   string     `json:"last_result,omitempty"`
}

// importWindow is how far ahead events are imported.
const importWindow = 120 * 24 * time.Hour

// ---- Store ----

func (s *Store) StravaLink() (StravaLink, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.StravaLink == nil {
		return StravaLink{}, false
	}
	return *s.d.StravaLink, true
}

func (s *Store) SetStravaLink(l *StravaLink) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.StravaLink = l
	return s.save()
}

func (s *Store) updateStravaLink(change func(l *StravaLink)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.StravaLink != nil {
		change(s.d.StravaLink)
		s.saveOrLog()
	}
}

// ExternalRide is a ride from outside the website: a Strava event, or
// anything sent through the calendar API. External is its lasting ID.
type ExternalRide struct {
	External  string   `json:"id"`
	Date      string   `json:"date"`
	Time      string   `json:"time,omitempty"`
	Name      string   `json:"name"`
	Place     string   `json:"place,omitempty"`
	Details   string   `json:"details,omitempty"`
	StravaURL string   `json:"strava_url,omitempty"`
	Groups    []string `json:"groups,omitempty"`
	Cancelled bool     `json:"cancelled,omitempty"`
	// Strava is the event's details, for rides imported from Strava.
	Strava *StravaInfo `json:"-"`
}

type importResult struct{ Added, Updated, Linked, Removed int }

func (r importResult) String() string {
	return fmt.Sprintf("%d added, %d updated, %d matched to weekly rides, %d removed", r.Added, r.Updated, r.Linked, r.Removed)
}

// UpsertExternal adds or updates one outside ride, keeping its leaders. When
// matchSeries is set, a ride on the same day as a weekly ride, with the same
// name or start time, is merged into that weekly ride's date.
func (s *Store) UpsertExternal(x ExternalRide, matchSeries bool) (RideOcc, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, how := s.upsertExternal(x, matchSeries)
	return o, how, s.save()
}

// upsertExternal does UpsertExternal's work; callers must hold s.mu and save.
func (s *Store) upsertExternal(x ExternalRide, matchSeries bool) (RideOcc, string) {
	apply := func(r *CalRide) {
		r.External, r.Date, r.Name, r.Cancelled = x.External, x.Date, x.Name, x.Cancelled
		if x.Time != "" {
			r.Time = x.Time
		}
		if x.Place != "" {
			r.Place = x.Place
		}
		if x.Details != "" {
			r.Details = x.Details
		}
		if x.StravaURL != "" {
			r.StravaURL = x.StravaURL
		}
		if len(x.Groups) > 0 && r.SeriesID == 0 {
			r.Groups = x.Groups
		}
		r.withStrava(x.Strava)
	}
	for _, r := range s.d.CalRides {
		if r.External == x.External {
			name := r.Name
			apply(r)
			if r.SeriesID != 0 {
				r.Name = name // a weekly ride keeps its own name
			}
			return occOf(r), "updated"
		}
	}
	if matchSeries {
		if d, err := time.Parse("2006-01-02", x.Date); err == nil {
			for _, se := range s.d.Series {
				if !se.runsOn(d) || !(strings.EqualFold(se.Name, x.Name) || (x.Time != "" && se.Time == x.Time)) {
					continue
				}
				r, err := s.ride(seriesKey(se.ID, x.Date), true)
				if err != nil || r.External != "" {
					continue
				}
				r.External, r.StravaURL, r.Cancelled = x.External, x.StravaURL, x.Cancelled
				r.withStrava(x.Strava)
				return occOf(r), "linked"
			}
		}
	}
	s.d.NextRide = max(s.d.NextRide, 1)
	r := &CalRide{ID: s.d.NextRide, Added: time.Now().UTC(), AddedBy: "Strava"}
	if !strings.HasPrefix(x.External, "strava:") {
		r.AddedBy = "the calendar API"
	}
	s.d.NextRide++
	apply(r)
	s.d.CalRides = append(s.d.CalRides, r)
	return occOf(r), "added"
}

// withStrava keeps the event's details. Whoever posted the event leads a ride
// that has one leader and nobody signed up yet.
func (r *CalRide) withStrava(st *StravaInfo) {
	if st == nil {
		return
	}
	r.Strava = st
	if st.Organizer != "" && len(r.Groups) == 0 && r.leaderFor("") == nil {
		r.setLeader("", &Leader{Name: st.Organizer, StravaID: st.OrganizerID, SetBy: "Strava", Since: time.Now().UTC()})
	}
}

// RemoveExternal takes an outside ride off the calendar. One that has
// leaders signed up is marked cancelled instead, so nobody's sign-up vanishes.
func (s *Store) RemoveExternal(external string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := s.removeExternal(external)
	return found, s.save()
}

func (s *Store) removeExternal(external string) bool {
	for i, r := range s.d.CalRides {
		if r.External != external {
			continue
		}
		switch {
		case len(r.Leaders) > 0:
			r.Cancelled = true
		case r.SeriesID != 0:
			r.External, r.StravaURL = "", ""
		default:
			s.d.CalRides = append(s.d.CalRides[:i], s.d.CalRides[i+1:]...)
		}
		return true
	}
	return false
}

// ImportStrava replaces the calendar's Strava events between from and to
// with the ones just read from Strava.
func (s *Store) ImportStrava(rides []ExternalRide, from, to string) (importResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res importResult
	seen := map[string]bool{}
	for _, x := range rides {
		seen[x.External] = true
		switch _, how := s.upsertExternal(x, true); how {
		case "added":
			res.Added++
		case "updated":
			res.Updated++
		case "linked":
			res.Linked++
		}
	}
	var gone []string
	for _, r := range s.d.CalRides {
		if strings.HasPrefix(r.External, "strava:") && !seen[r.External] && r.Date >= from && r.Date <= to {
			gone = append(gone, r.External)
		}
	}
	for _, ext := range gone {
		if s.removeExternal(ext) {
			res.Removed++
		}
	}
	return res, s.save()
}

// ---- Connecting ----

func (a *App) stravaConnect(w http.ResponseWriter, r *http.Request) {
	if !a.stravaReady() {
		back(w, r, "/admin/rides?err=nostrava")
		return
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	state := ts + "." + sign(a.stravaKey(), "import|"+ts)
	q := url.Values{
		"client_id": {a.cfg.StravaClientID}, "response_type": {"code"}, "approval_prompt": {"force"},
		"redirect_uri": {a.cfg.SiteURL + "/admin/strava/callback"}, "scope": {"read"}, "state": {state},
	}
	http.Redirect(w, r, a.cfg.StravaAPI+"/oauth/authorize?"+q.Encode(), http.StatusSeeOther)
}

func (a *App) stravaConnectCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ts, sig, _ := strings.Cut(q.Get("state"), ".")
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || !hmac.Equal([]byte(sig), []byte(sign(a.stravaKey(), "import|"+ts))) || time.Since(time.Unix(n, 0)) > 15*time.Minute {
		back(w, r, "/admin/rides?err=connect")
		return
	}
	if q.Get("code") == "" {
		back(w, r, "/admin/rides?err=connect")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tok, err := a.stravaToken(ctx, url.Values{"code": {q.Get("code")}, "grant_type": {"authorization_code"}})
	if err != nil {
		log.Printf("strava connect: %v", err)
		back(w, r, "/admin/rides?err=connect")
		return
	}
	link := &StravaLink{Athlete: tok.Athlete.shortName(), RefreshToken: tok.RefreshToken, AccessToken: tok.AccessToken, Expires: tok.ExpiresAt}
	if err := a.store.SetStravaLink(link); err != nil {
		back(w, r, "/admin/rides?err=save")
		return
	}
	if _, err := a.syncStrava(ctx); err != nil {
		back(w, r, "/admin/rides?ok=connected-error")
		return
	}
	back(w, r, "/admin/rides?ok=connected")
}

func (a *App) stravaDisconnect(w http.ResponseWriter, r *http.Request) {
	a.store.SetStravaLink(nil)
	back(w, r, "/admin/rides?ok=disconnected")
}

func (a *App) stravaSyncNow(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if _, err := a.syncStrava(ctx); err != nil {
		back(w, r, "/admin/rides?err=sync")
		return
	}
	back(w, r, "/admin/rides?ok=synced")
}

type stravaTokenReply struct {
	AccessToken  string        `json:"access_token"`
	RefreshToken string        `json:"refresh_token"`
	ExpiresAt    int64         `json:"expires_at"`
	Athlete      stravaAthlete `json:"athlete"`
}

func (a *App) stravaToken(ctx context.Context, form url.Values) (stravaTokenReply, error) {
	form.Set("client_id", a.cfg.StravaClientID)
	form.Set("client_secret", a.cfg.StravaClientSecret)
	req, _ := http.NewRequestWithContext(ctx, "POST", a.cfg.StravaAPI+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tok stravaTokenReply
	err := stravaDo(req, &tok)
	return tok, err
}

// ---- Syncing ----

var errNotConnected = errors.New("no Strava account is connected for importing events")

type stravaEvent struct {
	ID                  json.Number    `json:"id"`
	Title               string         `json:"title"`
	Description         string         `json:"description"`
	Address             string         `json:"address"`
	UpcomingOccurrences []string       `json:"upcoming_occurrences"`
	Private             bool           `json:"private"`
	SkillLevels         *int           `json:"skill_levels"`
	StartLatLng         []float64      `json:"start_latlng"`
	RouteID             json.Number    `json:"route_id"`
	Organizer           *stravaAthlete `json:"organizing_athlete"`
}

// info is what the calendar keeps from the event.
func (ev stravaEvent) info(going int) *StravaInfo {
	st := &StravaInfo{Description: cleanNotes(ev.Description), Private: ev.Private, Going: going}
	if ev.SkillLevels != nil {
		st.Pace = *ev.SkillLevels
	}
	if len(ev.StartLatLng) == 2 {
		st.Lat, st.Lng = ev.StartLatLng[0], ev.StartLatLng[1]
	}
	if id := ev.RouteID.String(); id != "0" {
		st.RouteID = id
	}
	if ev.Organizer != nil {
		st.Organizer, st.OrganizerID = ev.Organizer.shortName(), ev.Organizer.ID
	}
	return st
}

// cleanNotes tidies an event's notes: plain text, at most 2,000 characters.
func cleanNotes(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if r := []rune(s); len(r) > 2000 {
		s = string(r[:2000]) + "…"
	}
	return s
}

// syncStrava reads the club's upcoming events from Strava and updates the calendar.
func (a *App) syncStrava(ctx context.Context) (importResult, error) {
	res, err := a.readStrava(ctx)
	now := time.Now().UTC()
	a.store.updateStravaLink(func(l *StravaLink) {
		l.LastSync, l.LastError, l.LastResult = &now, "", res.String()
		if err != nil {
			l.LastError, l.LastResult = err.Error(), ""
		}
	})
	if err != nil {
		log.Printf("strava sync: %v", err)
	}
	return res, err
}

func (a *App) readStrava(ctx context.Context) (importResult, error) {
	get, err := a.stravaGetter(ctx)
	if err != nil {
		return importResult{}, err
	}
	var events []stravaEvent
	if err := get("/clubs/"+a.cfg.StravaClubID+"/group_events?upcoming=true&per_page=200", &events); err != nil {
		return importResult{}, fmt.Errorf("reading the club’s events: %w", err)
	}
	from := a.today()
	to := from.Add(importWindow)
	type occ struct {
		ev  stravaEvent
		day time.Time
		at  time.Time
	}
	var occs []occ
	var ids, routes []string
	for _, ev := range events {
		for _, o := range ev.UpcomingOccurrences {
			t, err := time.Parse(time.RFC3339, o)
			if err != nil {
				continue
			}
			t = t.In(a.cfg.Location)
			day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			if day.Before(from) || day.After(to) {
				continue
			}
			if len(occs) == 0 || occs[len(occs)-1].ev.ID != ev.ID {
				ids = append(ids, ev.ID.String())
				if id := ev.RouteID.String(); id != "" && id != "0" {
					routes = append(routes, id)
				}
			}
			occs = append(occs, occ{ev, day, t})
		}
	}
	// The details are extras: if Strava won't give them, the rides still import.
	going := readGoing(get, ids)
	if err := a.readHistory(get); err != nil {
		log.Printf("strava routes: %v", err)
	}
	a.readRoutes(get, routes)
	a.archiveGoing(get)
	var rides []ExternalRide
	for _, o := range occs {
		ev := o.ev
		rides = append(rides, ExternalRide{
			External: "strava:" + ev.ID.String() + ":" + o.day.Format("2006-01-02"),
			Date:     o.day.Format("2006-01-02"), Time: o.at.Format("15:04"), Name: cleanName(ev.Title), Place: cleanName(ev.Address),
			StravaURL: strings.TrimRight(a.cfg.StravaURL, "/") + "/group_events/" + ev.ID.String(),
			Strava:    ev.info(going[ev.ID.String()]),
		})
	}
	return a.store.ImportStrava(rides, from.Format("2006-01-02"), to.Format("2006-01-02"))
}

// stravaSyncLoop imports the club's events every hour while connected.
func (a *App) stravaSyncLoop(ctx context.Context) {
	for {
		if _, ok := a.store.StravaLink(); ok {
			c, cancel := context.WithTimeout(ctx, time.Minute)
			a.syncStrava(c)
			cancel()
		}
		select {
		case <-time.After(time.Hour):
		case <-ctx.Done():
			return
		}
	}
}

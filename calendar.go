package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The public ride calendar: /calendar (a month), /calendar/ride/{key} (one
// ride, where people sign up to lead), and /calendar/new (add a ride). Pages
// show who's signed in, so they're rendered for each visit.

type calDay struct {
	Date                 string
	Day                  int
	InMonth, Today, Past bool
	Rides                []RideOcc
}

type calView struct {
	MonthLabel, Prev, Next string
	Weeks                  [][]calDay
	Days                   []calDay // days of the month that have rides, for phones
	Rider                  *Rider
	StravaReady            bool
	Flash, Error           string
	ClubURL                string

	// One ride
	Ride                                       RideOcc
	Series                                     bool
	Past                                       bool
	CanLead, CanTakeOver, CanStepDown, CanEdit bool
	MembersURL                                 string

	// Adding a ride
	Form CalRide
}

var calFlash = map[string]string{
	"led":       "You’re leading this ride. Thanks! The club has been emailed.",
	"stepped":   "Done. The ride needs a new leader now, and the club has been emailed.",
	"added":     "Ride added to the calendar. The club has been emailed.",
	"cancelled": "Ride cancelled.",
	"restored":  "Ride back on the calendar.",
	"strava":    "Strava event link saved.",
}

var calError = map[string]string{
	"nostrava":  "Signing in with Strava isn’t set up yet. Ask an admin to assign ride leaders for now.",
	"denied":    "Strava sign-in was cancelled.",
	"signin":    "Strava sign-in didn’t work. Try again in a minute.",
	"notmember": "Join the PaxVelo club on Strava first, then sign in again.",
	"taken":     "Someone else just took this ride. Only an admin can change its leader now.",
	"past":      "That ride has already happened.",
	"notyours":  "Only the ride’s leader or a club admin can do that.",
	"form":      "Add at least a date and a name for the ride.",
	"link":      "Paste the event’s Strava link, starting with https://www.strava.com/.",
}

func (a *App) calBase(r *http.Request) calView {
	v := calView{StravaReady: a.stravaReady(), ClubURL: a.cfg.StravaURL,
		Flash: calFlash[r.URL.Query().Get("ok")], Error: calError[r.URL.Query().Get("err")]}
	if rd, ok := a.rider(r); ok {
		v.Rider = &rd
	}
	return v
}

func (a *App) renderCal(w http.ResponseWriter, status int, name, path, title string, v calView) {
	var buf bytes.Buffer
	if err := a.site.dynamic[name].ExecuteTemplate(&buf, "layout", a.site.view(path, title+" — "+siteName, v)); err != nil {
		log.Printf("calendar %s: %v", name, err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (a *App) today() time.Time {
	now := time.Now().In(a.cfg.Location)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// calendarPage shows a month, Sunday to Saturday, with every ride.
func (a *App) calendarPage(w http.ResponseWriter, r *http.Request) {
	today := a.today()
	first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	if m, err := time.Parse("2006-01", r.URL.Query().Get("m")); err == nil && m.Year() > 2000 && m.Year() < 2100 {
		first = m
	}
	start := first.AddDate(0, 0, -int(first.Weekday()))
	last := first.AddDate(0, 1, -1)
	end := last.AddDate(0, 0, 6-int(last.Weekday()))

	byDate := map[string][]RideOcc{}
	for _, o := range a.store.Rides(start, end) {
		byDate[o.Date] = append(byDate[o.Date], o)
	}
	v := a.calBase(r)
	v.MonthLabel = first.Format("January 2006")
	v.Prev, v.Next = first.AddDate(0, -1, 0).Format("2006-01"), first.AddDate(0, 1, 0).Format("2006-01")
	var week []calDay
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		day := calDay{Date: date, Day: d.Day(), InMonth: d.Month() == first.Month(), Today: d.Equal(today), Past: d.Before(today), Rides: byDate[date]}
		week = append(week, day)
		// The phone list skips days already gone in the current month.
		thisMonth := first.Year() == today.Year() && first.Month() == today.Month()
		if day.InMonth && len(day.Rides) > 0 && !(thisMonth && day.Past) {
			v.Days = append(v.Days, day)
		}
		if len(week) == 7 {
			v.Weeks, week = append(v.Weeks, week), nil
		}
	}
	a.renderCal(w, http.StatusOK, "calendar", "/calendar", "Ride Calendar", v)
}

// ridePage shows one ride and what the visitor can do with it.
func (a *App) ridePage(w http.ResponseWriter, r *http.Request) {
	o, ok := a.store.Ride(r.PathValue("key"))
	if !ok {
		a.site.notFound(w, r)
		return
	}
	v := a.calBase(r)
	a.rideRights(&v, o)
	a.renderCal(w, http.StatusOK, "ride", "/calendar", o.Name, v)
}

func (a *App) rideRights(v *calView, o RideOcc) {
	v.Ride, v.Series = o, o.SeriesID != 0
	v.MembersURL = strings.TrimRight(a.cfg.StravaURL, "/") + "/members"
	v.Past = o.Date < a.today().Format("2006-01-02")
	if v.Rider == nil || v.Past || o.Cancelled {
		return
	}
	rd := v.Rider
	isLeader := o.Leader != nil && o.Leader.StravaID == rd.ID
	v.CanLead = o.Leader == nil
	v.CanTakeOver = o.Leader != nil && !isLeader && rd.Admin
	v.CanStepDown = o.Leader != nil && (isLeader || rd.Admin)
	v.CanEdit = isLeader || rd.Admin
}

// back to a ride's page with a message.
func rideBack(w http.ResponseWriter, r *http.Request, key, query string) {
	http.Redirect(w, r, "/calendar/ride/"+url.PathEscape(key)+"?"+query, http.StatusSeeOther)
}

// riderPost checks a sign-up form: posted from this site, by someone signed in.
func (a *App) riderPost(w http.ResponseWriter, r *http.Request) (Rider, bool) {
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return Rider{}, false
	}
	rd, ok := a.rider(r)
	if !ok {
		http.Redirect(w, r, "/strava/login?next="+url.QueryEscape(r.URL.Path), http.StatusSeeOther)
		return Rider{}, false
	}
	return rd, true
}

// leadRide makes the signed-in rider the leader: anyone can take a ride with
// no leader; taking over from someone else is for club admins.
func (a *App) leadRide(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	rd, ok := a.riderPost(w, r)
	if !ok {
		return
	}
	var previous *Leader
	o, err := a.store.UpdateRide(key, func(c *CalRide) error {
		switch {
		case c.Date < a.today().Format("2006-01-02"):
			return errPastRide
		case c.Cancelled:
			return errNoRide
		case c.Leader != nil && c.Leader.StravaID == rd.ID:
			return nil
		case c.Leader != nil && !rd.Admin:
			return errHasLeader
		}
		previous = c.Leader
		c.Leader = &Leader{Name: rd.Name, StravaID: rd.ID, StravaAdmin: rd.Admin, Since: time.Now().UTC()}
		return nil
	})
	if code := rideErrCode(err); code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	subject := fmt.Sprintf("%s will lead %s on %s", rd.Name, o.Name, shortDate(o.Date))
	body := fmt.Sprintf("%s signed up to lead %s on %s.", rd.Name, o.Name, rideWhen(o))
	if previous != nil {
		subject = fmt.Sprintf("%s took over %s on %s", rd.Name, o.Name, shortDate(o.Date))
		body = fmt.Sprintf("%s (a club admin) took over leading %s on %s from %s.", rd.Name, o.Name, rideWhen(o), previous.Name)
	}
	a.notifyRide(subject, body, o, rd.Admin)
	rideBack(w, r, key, "ok=led")
}

// stepDown removes the leader: the leader themselves, or a club admin.
func (a *App) stepDown(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	rd, ok := a.riderPost(w, r)
	if !ok {
		return
	}
	var was string
	o, err := a.store.UpdateRide(key, func(c *CalRide) error {
		if c.Leader == nil {
			return nil
		}
		if c.Leader.StravaID != rd.ID && !rd.Admin {
			return errNotYours
		}
		was, c.Leader = c.Leader.Name, nil
		return nil
	})
	if code := rideErrCode(err); code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	if was != "" {
		who := was + " stepped down from"
		if was != rd.Name {
			who = rd.Name + " (a club admin) removed " + was + " as the leader of"
		}
		a.notifyRide(fmt.Sprintf("%s on %s needs a leader", o.Name, shortDate(o.Date)),
			fmt.Sprintf("%s %s on %s, so it needs a new leader.", who, o.Name, rideWhen(o)), o, true)
	}
	rideBack(w, r, key, "ok=stepped")
}

// setRideStrava saves the link to the ride's Strava event.
func (a *App) setRideStrava(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	rd, ok := a.riderPost(w, r)
	if !ok {
		return
	}
	link := strings.TrimSpace(r.FormValue("strava_url"))
	if link != "" && !strings.HasPrefix(link, "https://www.strava.com/") && !strings.HasPrefix(link, "https://strava.app.link/") {
		rideBack(w, r, key, "err=link")
		return
	}
	_, err := a.store.UpdateRide(key, func(c *CalRide) error {
		if (c.Leader == nil || c.Leader.StravaID != rd.ID) && !rd.Admin {
			return errNotYours
		}
		c.StravaURL = link
		return nil
	})
	if code := rideErrCode(err); code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	rideBack(w, r, key, "ok=strava")
}

// cancelRide takes a ride off (or puts it back on) the calendar: club admins only.
func (a *App) cancelRide(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	rd, ok := a.riderPost(w, r)
	if !ok {
		return
	}
	cancel := r.FormValue("cancel") == "1"
	o, err := a.store.UpdateRide(key, func(c *CalRide) error {
		if !rd.Admin {
			return errNotYours
		}
		c.Cancelled = cancel
		return nil
	})
	if code := rideErrCode(err); code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	if cancel {
		a.notifyRide(fmt.Sprintf("%s on %s is cancelled", o.Name, shortDate(o.Date)),
			fmt.Sprintf("%s cancelled %s on %s.", rd.Name, o.Name, rideWhen(o)), o, true)
		rideBack(w, r, key, "ok=cancelled")
		return
	}
	rideBack(w, r, key, "ok=restored")
}

// newRidePage is the form for adding a ride: just a date, a time, and a name.
func (a *App) newRidePage(w http.ResponseWriter, r *http.Request) {
	v := a.calBase(r)
	v.Form = CalRide{Date: r.URL.Query().Get("date"), Time: "08:00"}
	if _, err := time.Parse("2006-01-02", v.Form.Date); err != nil {
		v.Form.Date = a.today().Format("2006-01-02")
	}
	a.renderCal(w, http.StatusOK, "ride-new", "/calendar", "Add a Ride", v)
}

func (a *App) addRide(w http.ResponseWriter, r *http.Request) {
	rd, ok := a.riderPost(w, r)
	if !ok {
		return
	}
	c := CalRide{
		Date: strings.TrimSpace(r.FormValue("date")), Time: strings.TrimSpace(r.FormValue("time")),
		Name: cleanName(r.FormValue("name")), Place: cleanName(r.FormValue("place")),
		Details: cleanName(r.FormValue("details")), StravaURL: strings.TrimSpace(r.FormValue("strava_url")), AddedBy: rd.Name,
	}
	v := a.calBase(r)
	v.Form = c
	_, dateErr := time.Parse("2006-01-02", c.Date)
	_, timeErr := time.Parse("15:04", c.Time)
	switch {
	case dateErr != nil || c.Name == "":
		v.Error = calError["form"]
	case c.Time != "" && timeErr != nil:
		v.Error = "Pick a start time, or leave it empty."
	case c.Date < a.today().Format("2006-01-02"):
		v.Error = "Pick today or a date coming up."
	case c.StravaURL != "" && !strings.HasPrefix(c.StravaURL, "https://www.strava.com/") && !strings.HasPrefix(c.StravaURL, "https://strava.app.link/"):
		v.Error = calError["link"]
	}
	if v.Error != "" {
		a.renderCal(w, http.StatusBadRequest, "ride-new", "/calendar", "Add a Ride", v)
		return
	}
	lead := r.FormValue("lead") != ""
	if lead {
		c.Leader = &Leader{Name: rd.Name, StravaID: rd.ID, StravaAdmin: rd.Admin, Since: time.Now().UTC()}
	}
	o, err := a.store.AddRide(c)
	if err != nil {
		v.Error = "Couldn’t save the ride. Try again."
		a.renderCal(w, http.StatusInternalServerError, "ride-new", "/calendar", "Add a Ride", v)
		return
	}
	body := fmt.Sprintf("%s added a ride to the calendar: %s on %s.", rd.Name, o.Name, rideWhen(o))
	if lead {
		body += fmt.Sprintf(" %s is leading it.", rd.Name)
	} else {
		body += " It needs a leader."
	}
	a.notifyRide(fmt.Sprintf("New ride: %s on %s", o.Name, shortDate(o.Date)), body, o, !lead || rd.Admin)
	rideBack(w, r, o.Key, "ok=added")
}

var (
	errPastRide = errors.New("past")
	errNotYours = errors.New("not yours")
)

func rideErrCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errHasLeader):
		return "taken"
	case errors.Is(err, errPastRide):
		return "past"
	case errors.Is(err, errNotYours):
		return "notyours"
	}
	log.Printf("ride: %v", err)
	return "signin"
}

func shortDate(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.Format("Mon Jan 2")
}

func rideWhen(o RideOcc) string {
	s := o.DateLabel()
	if o.Time != "" {
		s += " at " + o.TimeLabel()
	}
	if o.Place != "" {
		s += " from " + o.Place
	}
	return s
}

// notifyRide emails the club about a change to a ride. When the leader can't
// post events on Strava, it says so and how to fix it.
func (a *App) notifyRide(subject, body string, o RideOcc, leaderCanPost bool) {
	body += "\n\nRide page: " + a.cfg.SiteURL + "/calendar/ride/" + o.Key
	if o.StravaURL != "" {
		body += "\nStrava event: " + o.StravaURL
	}
	if o.Leader != nil && !leaderCanPost && !o.Leader.StravaAdmin {
		body += fmt.Sprintf("\n\n%s isn’t an admin of the PaxVelo club on Strava, so they can’t post this ride as a club event. "+
			"Either post it for them, or make them an admin: open %s, find %s, and choose Make Admin.",
			o.Leader.Name, strings.TrimRight(a.cfg.StravaURL, "/")+"/members", o.Leader.Name)
	}
	a.mailer.Notify(subject, body)
}

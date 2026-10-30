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
	Days                   []calDay  // days of the month that have rides, for phones
	NoDays                 string    // what the phone list says when Days is empty
	Upcoming               []RideOcc // the next few rides, whatever month is shown
	UpcomingNote           string    // what to say when there are none
	Rider                  *Rider
	StravaReady            bool
	Flash, Error           string
	ClubURL                string
	Member                 bool   // signed in with Strava (a club member) or an admin: sees private ride details
	Token                  string // form token, for spam checks

	// One ride
	Ride       RideOcc
	Spots      []spotView
	Series     bool
	Past       bool
	Leading    bool // the signed-in rider leads a group of this ride
	CanEdit    bool
	MembersURL string

	// Adding a ride
	Form     CalRide
	FormName string
}

var calFlash = map[string]string{
	"led":       "You’re signed up to lead. Thanks! The club has been emailed.",
	"stepped":   "Done. That spot needs a new leader now, and the club has been emailed.",
	"added":     "Ride added to the calendar. The club has been emailed.",
	"held":      "Thanks! A club admin will look at your ride and approve it, and then it shows on the calendar. The club has been emailed.",
	"cancelled": "Ride cancelled.",
	"restored":  "Ride back on the calendar.",
	"strava":    "Strava event link saved.",
}

var calError = map[string]string{
	"nostrava":  "Signing in with Strava isn’t set up yet. Ask an admin to assign ride leaders for now.",
	"denied":    "Strava sign-in was cancelled.",
	"signin":    "Strava sign-in didn’t work. Try again in a minute.",
	"notmember": "Join the PaxVelo club on Strava first, then sign in again.",
	"taken":     "Someone else just took that spot. Only an admin can change its leader now.",
	"noride":    "That ride (or pace group) isn’t on the calendar anymore.",
	"already":   "You’re already leading a group on this ride. Step down from it first to switch.",
	"name":      "Type your name, so riders know who’s leading.",
	"expired":   "This page was open a long time. Try again.",
	"limit":     "Too many sign-ups from your connection. Wait a bit and try again.",
	"past":      "That ride has already happened.",
	"notyours":  "Only the ride’s leader or a club admin can do that.",
	"form":      "Add at least a date and a name for the ride.",
	"link":      "Paste the event’s Strava link, starting with https://www.strava.com/.",
}

func (a *App) calBase(r *http.Request) calView {
	v := calView{StravaReady: a.stravaReady(), ClubURL: a.cfg.StravaURL, Token: a.formToken(),
		Flash: calFlash[r.URL.Query().Get("ok")], Error: calError[r.URL.Query().Get("err")]}
	if rd, ok := a.rider(r); ok {
		v.Rider = &rd
	}
	v.Member = v.Rider != nil || a.loggedIn(r)
	return v
}

func (a *App) renderCal(w http.ResponseWriter, status int, name, path, title string, v calView) {
	var buf bytes.Buffer
	page := a.site.view(path, title+" — "+siteName, v)
	if name == "calendar" {
		page.LD = a.site.ridesLD(v.Upcoming)
	}
	if err := a.site.dynamic[name].ExecuteTemplate(&buf, "layout", page); err != nil {
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

// calMonth is one month of the calendar, Sunday to Saturday, with every ride.
// The public calendar and the admin's Calendar tab both show it.
type calMonth struct {
	MonthLabel, Prev, Next string
	Weeks                  [][]calDay
	Days                   []calDay // days of the month that have rides, for phones
	NoDays                 string   // what the phone list says when Days is empty
}

// month builds the month given as "2006-01", or the current one.
func (a *App) month(m string) calMonth {
	today := a.today()
	first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	if t, err := time.Parse("2006-01", m); err == nil && t.Year() > 2000 && t.Year() < 2100 {
		first = t
	}
	start := first.AddDate(0, 0, -int(first.Weekday()))
	last := first.AddDate(0, 1, -1)
	end := last.AddDate(0, 0, 6-int(last.Weekday()))

	byDate := map[string][]RideOcc{}
	for _, o := range a.store.Rides(start, end) {
		byDate[o.Date] = append(byDate[o.Date], o)
	}
	cm := calMonth{MonthLabel: first.Format("January 2006"),
		Prev: first.AddDate(0, -1, 0).Format("2006-01"), Next: first.AddDate(0, 1, 0).Format("2006-01")}
	thisMonth := first.Year() == today.Year() && first.Month() == today.Month()
	switch {
	case first.Before(today) && !thisMonth:
		cm.NoDays = "No rides this month."
	case thisMonth: // the upcoming rides above already show what's next
	default:
		cm.NoDays = "No rides on the calendar this month yet."
	}
	var week []calDay
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		day := calDay{Date: date, Day: d.Day(), InMonth: d.Month() == first.Month(), Today: d.Equal(today), Past: d.Before(today), Rides: byDate[date]}
		week = append(week, day)
		// The phone list skips days already gone in the current month.
		if day.InMonth && len(day.Rides) > 0 && !(thisMonth && day.Past) {
			cm.Days = append(cm.Days, day)
		}
		if len(week) == 7 {
			cm.Weeks, week = append(cm.Weeks, week), nil
		}
	}
	return cm
}

// calendarPage shows a month, Sunday to Saturday, with every ride.
func (a *App) calendarPage(w http.ResponseWriter, r *http.Request) {
	v := a.calBase(r)
	m := a.month(r.URL.Query().Get("m"))
	v.MonthLabel, v.Prev, v.Next, v.Weeks, v.Days, v.NoDays = m.MonthLabel, m.Prev, m.Next, m.Weeks, m.Days, m.NoDays
	v.Upcoming, v.UpcomingNote = a.upcomingRides()
	a.renderCal(w, http.StatusOK, "calendar", "/calendar", "Ride Calendar", v)
}

// upcomingRides returns the next few rides that aren't cancelled. When there
// are none soon, it says when the next one is (a weekly ride can be out of
// season for months).
func (a *App) upcomingRides() ([]RideOcc, string) {
	const soon = 4
	today := a.today()
	// A ride from earlier today has rolled out, so it isn't upcoming anymore.
	now := time.Now().In(a.cfg.Location).Format("15:04")
	todayS := today.Format("2006-01-02")
	var out []RideOcc
	for _, o := range a.store.Rides(today, today.AddDate(0, 0, 120)) {
		if o.Date == todayS && o.Time != "" && o.Time <= now {
			continue
		}
		if !o.Cancelled && len(out) < soon {
			out = append(out, o)
		}
	}
	if len(out) > 0 {
		return out, ""
	}
	for _, o := range a.store.Rides(today.AddDate(0, 0, 121), today.AddDate(0, 0, 400)) {
		if !o.Cancelled {
			return nil, "No rides until " + o.DateLabel() + "."
		}
	}
	return nil, "No rides are scheduled yet."
}

// ridePage shows one ride and what the visitor can do with it.
func (a *App) ridePage(w http.ResponseWriter, r *http.Request) {
	o, ok := a.store.Ride(r.PathValue("key"))
	if !ok || (o.Held && !a.loggedIn(r)) { // a ride waiting for approval isn't public yet
		a.site.notFound(w, r)
		return
	}
	v := a.calBase(r)
	a.rideRights(&v, o)
	a.renderCal(w, http.StatusOK, "ride", "/calendar", o.Name, v)
}

// spotView is one leader's spot on the ride page, with what the visitor can do.
type spotView struct {
	Slot
	Mine, CanLead, CanTakeOver, CanStepDown bool
}

// rideRights works out what the visitor can do. Anyone can take an empty
// spot by typing their name; changing a filled spot needs a club admin
// (signed in with Strava) or the admin portal, or the leader themselves if
// they signed up while signed in with Strava.
func (a *App) rideRights(v *calView, o RideOcc) {
	v.Ride, v.Series = o, o.SeriesID != 0
	v.MembersURL = strings.TrimRight(a.cfg.StravaURL, "/") + "/members"
	v.Past = o.Date < a.today().Format("2006-01-02")
	open := !v.Past && !o.Cancelled
	if v.Rider != nil {
		_, v.Leading = o.groupOf(*v.Rider)
	}
	for _, s := range o.Slots() {
		sp := spotView{Slot: s}
		if open {
			sp.CanLead = s.Leader == nil && !v.Leading
			if rd := v.Rider; rd != nil {
				sp.Mine = s.Leader != nil && sameLeader(*s.Leader, *rd)
				sp.CanTakeOver = s.Leader != nil && !sp.Mine && rd.Admin && !v.Leading
				sp.CanStepDown = s.Leader != nil && (sp.Mine || rd.Admin)
			}
		}
		v.Spots = append(v.Spots, sp)
	}
	v.CanEdit = open && v.Rider != nil && (v.Leading || v.Rider.Admin)
}

// sameLeader reports whether a leader is this rider: by Strava account when
// there is one, otherwise by name.
func sameLeader(l Leader, rd Rider) bool {
	if rd.ID != 0 {
		return l.StravaID == rd.ID
	}
	return l.StravaID == 0 && strings.EqualFold(l.Name, rd.Name)
}

// signupPost checks a sign-up or add-a-ride form. Signing in with Strava is
// optional: without it, people just type their name. The same spam checks as
// the join form apply: a hidden field only bots fill in, a form token that
// must be a couple of seconds old, and a cap per connection.
func (a *App) signupPost(r *http.Request) (Rider, string) {
	if r.FormValue("website") != "" || !a.validFormToken(r.FormValue("ft")) {
		return Rider{}, "expired"
	}
	rd, signedIn := a.rider(r)
	if !signedIn {
		rd = Rider{Name: cleanName(r.FormValue("leader_name"))}
		if rd.Name == "" {
			return Rider{}, "name"
		}
	}
	if !a.limit.Allow("signup:"+clientIP(r), 20, time.Hour) {
		return Rider{}, "limit"
	}
	return rd, ""
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

// rideAndGroup names a spot in emails, like "Tuesday Night Ride (A group)".
func rideAndGroup(name, group string) string {
	if group == "" {
		return name
	}
	return name + " (" + group + ")"
}

// leadRide puts the signed-in rider in a spot: anyone can take an empty
// spot; taking over from someone else is for club admins. A rider leads at
// most one group per ride.
func (a *App) leadRide(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	rd, code := a.signupPost(r)
	if code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	group := strings.TrimSpace(r.FormValue("group"))
	var previous *Leader
	o, err := a.store.UpdateRide(key, func(c *CalRide) error {
		current := c.leaderFor(group)
		switch {
		case c.Date < a.today().Format("2006-01-02"):
			return errPastRide
		case c.Cancelled || c.Held || !c.hasGroup(group):
			return errNoRide
		case current != nil && sameLeader(*current, rd):
			return nil
		}
		if _, leading := c.groupOf(rd); leading {
			return errAlreadyLeading
		}
		if current != nil && !rd.Admin {
			return errHasLeader
		}
		previous = current
		c.setLeader(group, &Leader{Name: rd.Name, StravaID: rd.ID, StravaAdmin: rd.Admin, Since: time.Now().UTC()})
		return nil
	})
	if code := rideErrCode(err); code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	what := rideAndGroup(o.Name, group)
	subject := fmt.Sprintf("%s will lead %s on %s", rd.Name, what, shortDate(o.Date))
	body := fmt.Sprintf("%s signed up to lead %s on %s.", rd.Name, what, rideWhen(o))
	if previous != nil {
		subject = fmt.Sprintf("%s took over %s on %s", rd.Name, what, shortDate(o.Date))
		body = fmt.Sprintf("%s (a club admin) took over leading %s on %s from %s.", rd.Name, what, rideWhen(o), previous.Name)
	}
	a.notifyRide(subject, body, o, group)
	rideBack(w, r, key, "ok=led")
}

// stepDown empties a spot: its leader, or a club admin, can do it.
func (a *App) stepDown(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	rd, ok := a.riderPost(w, r)
	if !ok {
		return
	}
	group := strings.TrimSpace(r.FormValue("group"))
	var was string
	o, err := a.store.UpdateRide(key, func(c *CalRide) error {
		current := c.leaderFor(group)
		if current == nil {
			return nil
		}
		if !sameLeader(*current, rd) && !rd.Admin {
			return errNotYours
		}
		was = current.Name
		c.setLeader(group, nil)
		return nil
	})
	if code := rideErrCode(err); code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	if was != "" {
		what := rideAndGroup(o.Name, group)
		who := was + " stepped down from"
		if was != rd.Name {
			who = rd.Name + " (a club admin) removed " + was + " as the leader of"
		}
		a.notifyRide(fmt.Sprintf("%s on %s needs a leader", what, shortDate(o.Date)),
			fmt.Sprintf("%s %s on %s, so it needs a new leader.", who, what, rideWhen(o)), o, "")
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
	if link != "" && !stravaLink(link) {
		rideBack(w, r, key, "err=link")
		return
	}
	_, err := a.store.UpdateRide(key, func(c *CalRide) error {
		if _, leading := c.groupOf(rd); !leading && !rd.Admin {
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

func stravaLink(u string) bool {
	return strings.HasPrefix(u, "https://www.strava.com/") || strings.HasPrefix(u, "https://strava.app.link/")
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
			fmt.Sprintf("%s cancelled %s on %s.", rd.Name, o.Name, rideWhen(o)), o, "")
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
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	rd, code := a.signupPost(r)
	c := CalRide{
		Date: strings.TrimSpace(r.FormValue("date")), Time: strings.TrimSpace(r.FormValue("time")),
		Name: cleanName(r.FormValue("name")), Place: cleanName(r.FormValue("place")),
		Details: cleanName(r.FormValue("details")), StravaURL: strings.TrimSpace(r.FormValue("strava_url")), AddedBy: rd.Name,
		Groups: groupsFor(r.FormValue("pace"), nil),
		// Anyone can type a name here, so unless they signed in with Strava
		// (which proves they're in the club) an admin approves the ride first.
		Held: rd.ID == 0,
	}
	v := a.calBase(r)
	v.Form, v.FormName = c, r.FormValue("leader_name")
	_, dateErr := time.Parse("2006-01-02", c.Date)
	_, timeErr := time.Parse("15:04", c.Time)
	switch {
	case code != "":
		v.Error = calError[code]
	case dateErr != nil || c.Name == "":
		v.Error = calError["form"]
	case c.Time != "" && timeErr != nil:
		v.Error = "Pick a start time, or leave it empty."
	case c.Date < a.today().Format("2006-01-02"):
		v.Error = "Pick today or a date coming up."
	case c.StravaURL != "" && !stravaLink(c.StravaURL):
		v.Error = calError["link"]
	}
	if v.Error != "" {
		a.renderCal(w, http.StatusBadRequest, "ride-new", "/calendar", "Add a Ride", v)
		return
	}
	lead := r.FormValue("lead") != ""
	if lead {
		c.setLeader(c.groupNames()[0], &Leader{Name: rd.Name, StravaID: rd.ID, StravaAdmin: rd.Admin, Since: time.Now().UTC()})
	}
	o, err := a.store.AddRide(c)
	if err != nil {
		v.Error = "Couldn’t save the ride. Try again."
		a.renderCal(w, http.StatusInternalServerError, "ride-new", "/calendar", "Add a Ride", v)
		return
	}
	if o.Held {
		a.mailer.Notify(fmt.Sprintf("Ride waiting for approval: %s on %s", o.Name, shortDate(o.Date)),
			fmt.Sprintf("%s (not signed in with Strava) added %s on %s. It isn't on the calendar until an admin approves it: %s/admin/calendar\n\nTo delete it instead, open it there.",
				rd.Name, o.Name, rideWhen(o), a.cfg.SiteURL))
		http.Redirect(w, r, "/calendar?ok=held", http.StatusSeeOther)
		return
	}
	body := fmt.Sprintf("%s added a ride to the calendar: %s on %s.", rd.Name, o.Name, rideWhen(o))
	if len(o.Groups) > 0 {
		body += " Pace groups: " + strings.Join(o.Groups, ", ") + "."
	}
	if lead {
		if g := c.groupNames()[0]; g != "" {
			body += fmt.Sprintf(" %s is leading the %s.", rd.Name, g)
		} else {
			body += fmt.Sprintf(" %s is leading it.", rd.Name)
		}
	}
	if o.Status() != "led" {
		body += " It still needs leaders: sign up on the ride page."
	}
	a.notifyRide(fmt.Sprintf("New ride: %s on %s", o.Name, shortDate(o.Date)), body, o, c.groupNames()[0])
	rideBack(w, r, o.Key, "ok=added")
}

// parseGroups reads pace groups typed as a list ("A, B" or one per line).
func parseGroups(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, g := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '\n' }) {
		g = cleanName(g)
		if g != "" && !seen[g] && len(out) < 8 {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

var (
	errPastRide       = errors.New("past")
	errNotYours       = errors.New("not yours")
	errAlreadyLeading = errors.New("already leading")
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
	case errors.Is(err, errAlreadyLeading):
		return "already"
	case errors.Is(err, errNoRide):
		return "noride"
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

// notifyRide emails the club about a change to a ride. When the leader just
// added (in group) can't post events on Strava, it says so and how to fix it.
func (a *App) notifyRide(subject, body string, o RideOcc, group string) {
	body += "\n\nRide page: " + a.cfg.SiteURL + "/calendar/ride/" + o.Key
	if o.StravaURL != "" {
		body += "\nStrava event: " + o.StravaURL
	}
	var lines []string
	for _, s := range o.Slots() {
		name := "needs a leader"
		if s.Leader != nil {
			name = s.Leader.Name
		}
		if s.Group != "" {
			lines = append(lines, "  "+s.Group+": "+name)
		}
	}
	if len(lines) > 0 {
		body += "\n\nLeaders now:\n" + strings.Join(lines, "\n")
	}
	if l := o.leaderFor(group); l != nil && l.StravaID == 0 && l.SetBy == "" {
		body += fmt.Sprintf("\n\n%s signed up with just their name, so they aren’t linked to a Strava account. "+
			"Make sure the ride is posted on Strava (%s) and that %s knows they’re leading.", l.Name, a.cfg.StravaURL, l.Name)
	} else if l != nil && l.StravaID != 0 && !l.StravaAdmin {
		body += fmt.Sprintf("\n\n%s isn’t an admin of the PaxVelo club on Strava, so they can’t post this ride as a club event. "+
			"Either post it for them, or make them an admin: open %s, find %s, and choose Make Admin.",
			l.Name, strings.TrimRight(a.cfg.StravaURL, "/")+"/members", l.Name)
	}
	a.mailer.Notify(subject, body)
}

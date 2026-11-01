package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
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
	Flash, Error           string
	ClubURL                string
	Member                 bool   // logged in to the admin portal: sees private ride details
	PINOn                  bool   // the club PIN is set
	Token                  string // form token, for spam checks

	// One ride
	Ride      RideOcc
	Spots     []spotView
	Series    bool
	Past      bool
	CanManage bool // open, with a leader, so a leader can step down or cancel with the club PIN

	// Adding a ride
	Form     CalRide
	FormName string
}

var calFlash = map[string]string{
	"led":       "You’re signed up to lead. Thanks!",
	"stepped":   "Done. That spot needs a new leader now.",
	"added":     "Ride added to the calendar. The club has been emailed.",
	"held":      "Thanks! A club admin will look at your ride and approve it, and then it shows on the calendar. The club has been emailed.",
	"cancelled": "Ride cancelled.",
	"restored":  "Ride back on the calendar.",
	"strava":    "Strava event link saved.",
}

var calError = map[string]string{
	"pin":       pinWrong,
	"pinlimit":  pinLimit,
	"pinneeded": "Type the club PIN. Don’t have it? Ask a club admin.",
	"taken":     "Someone else just took that spot. Only an admin can change its leader now.",
	"noride":    "That ride (or pace group) isn’t on the calendar anymore.",
	"already":   "You’re already leading a group on this ride. Step down from it first to switch.",
	"name":      "Type your name, so riders know who’s leading.",
	"expired":   "This page was open a long time. Try again.",
	"limit":     "Too many sign-ups from your connection. Wait a bit and try again.",
	"past":      "That ride has already happened.",
	"notyours":  "That name isn’t leading this ride. Type it the way you signed up, or ask a club admin.",
	"form":      "Add at least a date and a name for the ride.",
	"link":      "Paste the event’s Strava link, starting with https://www.strava.com/.",
}

func (a *App) calBase(r *http.Request) calView {
	return calView{ClubURL: a.cfg.StravaURL, Token: a.formToken(), Member: a.loggedIn(r), PINOn: a.store.ClubPIN() != "",
		Flash: calFlash[r.URL.Query().Get("ok")], Error: calError[r.URL.Query().Get("err")]}
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

	// Once the club's past Strava events are on the calendar, a weekly ride's
	// past date only shows if it was on Strava or someone signed up for it or
	// changed it, so past months show the rides that happened.
	hidePast, todayS := a.store.HasHistory(), today.Format("2006-01-02")
	byDate := map[string][]RideOcc{}
	for _, o := range a.store.Rides(start, end) {
		if hidePast && o.ID == 0 && o.SeriesID != 0 && o.Date < todayS {
			continue
		}
		byDate[o.Date] = append(byDate[o.Date], o)
	}
	cm := calMonth{MonthLabel: first.Format("January 2006"),
		Prev: first.AddDate(0, -1, 0).Format("2006-01"), Next: first.AddDate(0, 1, 0).Format("2006-01")}
	thisMonth := first.Year() == today.Year() && first.Month() == today.Month()
	switch {
	case first.Before(today) && !thisMonth:
		cm.NoDays = "No rides this month."
	case thisMonth: // said below, once it's clear nothing's left this month
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
	if thisMonth && len(cm.Days) == 0 {
		cm.NoDays = "Nothing else on the calendar this month."
	}
	return cm
}

// calendarPage shows a month, Sunday to Saturday, with every ride.
func (a *App) calendarPage(w http.ResponseWriter, r *http.Request) {
	v := a.calBase(r)
	m := a.month(r.URL.Query().Get("m"))
	v.MonthLabel, v.Prev, v.Next, v.Weeks, v.Days, v.NoDays = m.MonthLabel, m.Prev, m.Next, m.Weeks, m.Days, m.NoDays
	v.Upcoming, v.UpcomingNote = a.upcomingRides()
	a.renderCal(w, http.StatusOK, "calendar", "/calendar", "Club Calendar", v)
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

// upcomingTpl is the home page's next rides, wherever a page says
// {upcoming-rides}: the same cards as the calendar's upcoming rides, with
// where each one starts. Phones show three (site.css).
var upcomingTpl = template.Must(template.New("").Parse(`<div class="next-rides">
{{if .Rides}}<ul class="cal-up-list">
{{range .Rides}}<li><a class="cal-ride {{.Status}}" href="/calendar/ride/{{.Key}}">
<span class="cal-up-date">{{.ShortDate}}</span>
<span class="cal-time">{{.TimeLabel}}</span> {{.Name}}
{{with .PublicPlace}}<span class="cal-place">{{.}}</span>{{end}}
<span class="lvls">{{range .Levels}}<span class="lvl lvl-{{.Key}}">{{.Name}}</span>{{end}}</span>
{{if or (eq .Status "open") (eq .Status "partial")}}<span class="cal-lead"><span class="cal-need">{{.LeaderSummary}}</span></span>{{end}}
</a></li>
{{end}}</ul>{{else}}<p class="cal-none">{{.Note}}</p>{{end}}
<p class="more"><a href="/calendar">Full ride calendar</a></p>
</div>`))

func upcomingWidget(rides []RideOcc, note string) template.HTML {
	var b bytes.Buffer
	upcomingTpl.Execute(&b, struct {
		Rides []RideOcc
		Note  string
	}{rides, note})
	return template.HTML(b.String())
}

// upcomingLoop keeps the home page's next rides current. The content pages
// are drawn ahead of time, so once a minute it draws them again if the list
// has changed: a ride added or cancelled, a leader signed up, or a ride
// rolled out.
func (a *App) upcomingLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	last := upcomingWidget(a.upcomingRides())
	for {
		select {
		case <-t.C:
			if now := upcomingWidget(a.upcomingRides()); now != last {
				last = now
				if err := a.rebuildContent(); err != nil {
					log.Printf("redrawing the pages for the next rides: %v", err)
				}
			}
		case <-ctx.Done():
			return
		}
	}
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

// rideICS is one ride as a calendar file, for "Add to my calendar": phones
// open it straight into their calendar app. A ride without a start time is
// an all-day event; one with a time is down for two hours.
func (a *App) rideICS(w http.ResponseWriter, r *http.Request) {
	o, ok := a.store.Ride(r.PathValue("key"))
	if !ok || (o.Held && !a.loggedIn(r)) {
		a.site.notFound(w, r)
		return
	}
	day, err := time.ParseInLocation("2006-01-02", o.Date, a.cfg.Location)
	if err != nil {
		a.site.notFound(w, r)
		return
	}
	link := a.cfg.SiteURL + "/calendar/ride/" + o.Key
	host := strings.TrimPrefix(strings.TrimPrefix(a.cfg.SiteURL, "https://"), "http://")
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//PaxVelo//Ride calendar//EN", "METHOD:PUBLISH", "BEGIN:VEVENT",
		"UID:" + o.Key + "@" + host, "DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z")}
	if t, err := time.ParseInLocation("2006-01-02 15:04", o.Date+" "+o.Time, a.cfg.Location); err == nil {
		lines = append(lines, "DTSTART:"+t.UTC().Format("20060102T150405Z"), "DTEND:"+t.Add(2*time.Hour).UTC().Format("20060102T150405Z"))
	} else {
		lines = append(lines, "DTSTART;VALUE=DATE:"+day.Format("20060102"), "DTEND;VALUE=DATE:"+day.AddDate(0, 0, 1).Format("20060102"))
	}
	lines = append(lines, "SUMMARY:"+icsText(o.Name))
	member := a.loggedIn(r)
	if p := o.PlaceFor(member); p != "" {
		lines = append(lines, "LOCATION:"+icsText(p))
	}
	desc := link
	if d := strings.TrimSpace(o.Details); d != "" {
		desc = d + "\n\n" + link
	}
	lines = append(lines, "DESCRIPTION:"+icsText(desc), "URL:"+link)
	if o.Cancelled {
		lines = append(lines, "STATUS:CANCELLED")
	}
	lines = append(lines, "END:VEVENT", "END:VCALENDAR")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(icsFold(l))
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="paxvelo-ride.ics"`)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(b.String()))
}

// spotView is one leader's spot on the ride page.
type spotView struct {
	Slot
	CanLead bool
}

// rideRights works out what the visitor can do. Anyone can take an empty
// spot by typing their name. A leader steps down, cancels, or adds the
// Strava link by typing their name and the club PIN; anything else about a
// filled spot is for the admin portal.
func (a *App) rideRights(v *calView, o RideOcc) {
	v.Ride, v.Series = o, o.SeriesID != 0
	v.Past = o.Date < a.today().Format("2006-01-02")
	open := !v.Past && !o.Cancelled
	for _, s := range o.Slots() {
		v.Spots = append(v.Spots, spotView{Slot: s, CanLead: open && s.Leader == nil && o.IsRide()})
	}
	v.CanManage = !v.Past && len(o.Leaders) > 0 && o.IsRide() // an event's host is set on the admin portal
}

// sameLeader reports whether a leader is this person, by name.
func sameLeader(l Leader, name string) bool {
	return strings.EqualFold(strings.TrimSpace(l.Name), strings.TrimSpace(name))
}

// signupPost checks a sign-up or add-a-ride form and returns the name typed.
// The same spam checks as the join form apply: a hidden field only bots fill
// in, a form token that must be a couple of seconds old, and a cap per
// connection.
func (a *App) signupPost(r *http.Request) (string, string) {
	if r.FormValue("website") != "" || !a.validFormToken(r.FormValue("ft")) {
		return "", "expired"
	}
	name := cleanName(r.FormValue("leader_name"))
	if name == "" {
		return "", "name"
	}
	if !a.limit.Allow("signup:"+clientIP(r), 20, time.Hour) {
		return "", "limit"
	}
	return name, ""
}

// back to a ride's page with a message.
func rideBack(w http.ResponseWriter, r *http.Request, key, query string) {
	http.Redirect(w, r, "/calendar/ride/"+url.PathEscape(key)+"?"+query, http.StatusSeeOther)
}

// leaderPost checks a leader's form on a ride page (step down, Strava link,
// cancel): posted from this site, with the club PIN and the name they lead
// under. It answers with an error code, or "" and the name.
func (a *App) leaderPost(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return "", "", false
	}
	name := cleanName(r.FormValue("leader_name"))
	used, problem := a.checkPIN(r, r.FormValue("pin"))
	switch {
	case name == "":
		return "", "name", true
	case !used:
		return "", "pinneeded", true
	case problem == pinLimit:
		return "", "pinlimit", true
	case problem != "":
		return "", "pin", true
	}
	return name, "", true
}

// leadRide puts someone in an empty spot by name. Changing a filled spot is
// for the admin portal. Nobody leads more than one group of a ride. The club
// isn't emailed about leaders.
func (a *App) leadRide(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	name, code := a.signupPost(r)
	if code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	group := strings.TrimSpace(r.FormValue("group"))
	_, err := a.store.UpdateRide(key, func(c *CalRide) error {
		current := c.leaderFor(group)
		switch {
		case c.Date < a.today().Format("2006-01-02"):
			return errPastRide
		case c.Cancelled || c.Held || !c.IsRide() || !c.hasGroup(group):
			return errNoRide
		case current != nil && sameLeader(*current, name):
			return nil
		case current != nil:
			return errHasLeader
		}
		if _, leading := c.groupOf(name); leading {
			return errAlreadyLeading
		}
		c.setLeader(group, &Leader{Name: name, Since: time.Now().UTC()})
		return nil
	})
	if code := rideErrCode(err); code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	rideBack(w, r, key, "ok=led")
}

// stepDown empties the spot a leader leads, given their name and the club PIN.
func (a *App) stepDown(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	name, code, ok := a.leaderPost(w, r)
	if !ok {
		return
	}
	if code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	_, err := a.store.UpdateRide(key, func(c *CalRide) error {
		g, leading := c.groupOf(name)
		if !leading {
			return errNotYours
		}
		c.setLeader(g, nil)
		return nil
	})
	if code := rideErrCode(err); code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	rideBack(w, r, key, "ok=stepped")
}

// setRideStrava saves the link to the ride's Strava event: one of its
// leaders, with the club PIN.
func (a *App) setRideStrava(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	name, code, ok := a.leaderPost(w, r)
	if !ok {
		return
	}
	link := strings.TrimSpace(r.FormValue("strava_url"))
	if code == "" && link != "" && !stravaLink(link) {
		code = "link"
	}
	if code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	_, err := a.store.UpdateRide(key, func(c *CalRide) error {
		if _, leading := c.groupOf(name); !leading {
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

// cancelRide takes a ride off (or puts it back on) the calendar: one of its
// leaders, with the club PIN. Admins also can on the admin portal.
func (a *App) cancelRide(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	name, code, ok := a.leaderPost(w, r)
	if !ok {
		return
	}
	if code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	cancel := r.FormValue("cancel") == "1"
	o, err := a.store.UpdateRide(key, func(c *CalRide) error {
		if _, leading := c.groupOf(name); !leading {
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
			fmt.Sprintf("%s cancelled %s on %s.", name, o.Name, rideWhen(o)), o, "")
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
	a.renderCal(w, http.StatusOK, "ride-new", "/calendar", "Add to the Calendar", v)
}

func (a *App) addRide(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	name, code := a.signupPost(r)
	c := CalRide{
		Date: strings.TrimSpace(r.FormValue("date")), Time: strings.TrimSpace(r.FormValue("time")),
		Name: cleanName(r.FormValue("name")), Place: cleanName(r.FormValue("place")),
		Details: cleanName(r.FormValue("details")), StravaURL: strings.TrimSpace(r.FormValue("strava_url")), AddedBy: name,
		Groups: groupsFor(r.FormValue("pace"), nil), Kind: kindFor(r.FormValue("kind")),
		Virtual: r.FormValue("virtual_set"),
		// Anyone can type a name here, so unless they typed the club PIN
		// (which says they're in the club) an admin approves the ride first.
		Held: true,
	}
	if !c.IsRide() { // nobody leads pace groups at a meeting or a party
		c.Groups = nil
	}
	if c.Virtual != "yes" && c.Virtual != "no" {
		c.Virtual = ""
	}
	c.Virtual = readVirtual(r, c.Virtual, c.Name, c.Place, c.Details)
	v := a.calBase(r)
	v.Form, v.FormName = c, r.FormValue("leader_name")
	var pinProblem string
	if code == "" {
		var viaPIN bool
		viaPIN, pinProblem = a.checkPIN(r, r.FormValue("pin"))
		c.Held = !viaPIN || pinProblem != ""
	}
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
	case pinProblem != "":
		v.Error = pinProblem
	}
	if v.Error != "" {
		a.renderCal(w, http.StatusBadRequest, "ride-new", "/calendar", "Add to the Calendar", v)
		return
	}
	lead := r.FormValue("lead") != ""
	if lead {
		c.setLeader(c.groupNames()[0], &Leader{Name: name, Since: time.Now().UTC()})
	}
	o, err := a.store.AddRide(c)
	if err != nil {
		v.Error = "Couldn’t save it. Try again."
		a.renderCal(w, http.StatusInternalServerError, "ride-new", "/calendar", "Add to the Calendar", v)
		return
	}
	if o.Held {
		a.mailer.Notify(fmt.Sprintf("%s waiting for approval: %s on %s", strings.ToUpper(o.Noun()[:1])+o.Noun()[1:], o.Name, shortDate(o.Date)),
			fmt.Sprintf("%s added %s on %s without the club PIN. It isn't on the calendar until an admin approves it: %s/admin/calendar\n\nTo delete it instead, open it there.",
				name, o.Name, rideWhen(o), a.cfg.SiteURL))
		http.Redirect(w, r, "/calendar?ok=held", http.StatusSeeOther)
		return
	}
	body := fmt.Sprintf("%s added %s to the calendar: %s on %s.", name, map[bool]string{true: "a ride", false: "an event"}[o.IsRide()], o.Name, rideWhen(o))
	if len(o.Groups) > 0 {
		body += " Pace groups: " + strings.Join(o.Groups, ", ") + "."
	}
	if lead && !o.IsRide() {
		body += fmt.Sprintf(" %s is hosting.", name)
	} else if lead {
		if g := c.groupNames()[0]; g != "" {
			body += fmt.Sprintf(" %s is leading the %s.", name, g)
		} else {
			body += fmt.Sprintf(" %s is leading it.", name)
		}
	}
	if o.IsRide() && o.Status() != "led" {
		body += " It still needs leaders: sign up on the ride page."
	}
	a.notifyRide(fmt.Sprintf("New %s: %s on %s", o.Noun(), o.Name, shortDate(o.Date)), body, o, c.groupNames()[0])
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
	if l := o.leaderFor(group); l != nil && l.StravaID == 0 && l.SetBy == "" && o.StravaURL == "" {
		body += fmt.Sprintf("\n\n%s signed up on the website. If riders should RSVP on Strava, make sure the ride is posted there (%s).", l.Name, a.cfg.StravaURL)
	}
	a.mailer.Notify(subject, body)
}

// icsText escapes words for a calendar file.
func icsText(s string) string {
	return strings.NewReplacer(`\`, `\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", "").Replace(s)
}

// icsFold ends a calendar file line, folding it every 75 bytes as the format
// asks, without splitting a character.
func icsFold(l string) string {
	var b strings.Builder
	n := 0
	for _, c := range l {
		if k := len(string(c)); n+k > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(c)
		n += len(string(c))
	}
	b.WriteString("\r\n")
	return b.String()
}

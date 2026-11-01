package main

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The admin portal's ride pages: the Calendar tab (every ride, by month), a
// page for adding or editing any ride (its details, pace groups, leaders, or
// cancelling it), and the Rides tab (the weekly rides, the Strava import, and
// who the club's admins on Strava are).

func (a *App) ridesAdmin(w http.ResponseWriter, r *http.Request) {
	today := a.today()
	v := adminView{Title: "Rides", Tab: "rides", Series: a.store.AllSeries(), Upcoming: a.store.Rides(today, today.AddDate(0, 0, 60)),
		StravaReady: a.stravaReady(), Weekdays: weekdays}
	if l, ok := a.store.StravaLink(); ok {
		v.StravaLink = &l
	}
	v.APIEnabled = a.cfg.CalendarAPIKey != ""
	v.Held = a.store.HeldRides()
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "rides", v)
}

// calendarAdmin is the Calendar tab: the month, like the public calendar, but
// every ride opens its admin page.
func (a *App) calendarAdmin(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Calendar", Tab: "calendar", Month: a.month(r.URL.Query().Get("m")), Held: a.store.HeldRides()}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "calendar", v)
}

var weekdays = func() []monthOption {
	var out []monthOption
	for d := time.Sunday; d <= time.Saturday; d++ {
		out = append(out, monthOption{int(d), d.String()})
	}
	return out
}()

// ---- One ride ----

// leaderField is the form field for a group's leader on the ride page.
func leaderField(group string) string { return "leader:" + group }

// rideForm is a ride on its admin page, with a leader box for every group the
// pace menu can choose (the page shows the ones that apply).
type rideForm struct {
	RideOcc
	New, Past bool
	Pace      string
	Leaders   []leaderBox
	// "Repeat every week" on the Add a ride page, and its last date.
	Weekly bool
	Until  string
}

type leaderBox struct {
	Group, Field, Name string
	Note               string // like "Signed up on the calendar"
}

func (a *App) rideFormFor(o RideOcc, isNew bool) rideForm {
	f := rideForm{RideOcc: o, New: isNew, Past: o.Date < a.today().Format("2006-01-02"), Pace: paceOf(o.Groups)}
	groups := append([]string{""}, paceGroups...)
	for _, g := range o.Groups {
		if !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	for _, g := range groups {
		b := leaderBox{Group: g, Field: leaderField(g)}
		if l := o.leaderFor(g); l != nil {
			b.Name = l.Name
			switch {
			case l.SetBy != "":
				b.Note = "Set by " + l.SetBy
			case l.StravaID != 0:
				b.Note = "From the Strava event"
			default:
				b.Note = "Signed up on the calendar"
			}
		}
		f.Leaders = append(f.Leaders, b)
	}
	return f
}

func (a *App) rideEditPage(w http.ResponseWriter, r *http.Request) {
	o, ok := a.store.Ride(r.PathValue("key"))
	if !ok {
		back(w, r, "/admin/calendar?err=noride")
		return
	}
	v := adminView{Title: o.Name, Tab: "calendar", RideForm: a.rideFormFor(o, false)}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "ride", v)
}

func (a *App) newRideAdminPage(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if _, err := time.Parse("2006-01-02", date); err != nil {
		date = a.today().Format("2006-01-02")
	}
	v := adminView{Title: "Add a ride", Tab: "calendar", RideForm: a.rideFormFor(RideOcc{CalRide: CalRide{Date: date, Time: "08:00"}}, true)}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "ride", v)
}

// readRide copies the ride page's fields onto a ride. It returns a problem to
// show, or "".
func readRide(r *http.Request, c *CalRide, dateEditable bool) string {
	c.Name = cleanName(r.FormValue("name"))
	c.Time = strings.TrimSpace(r.FormValue("time"))
	c.Place, c.Details = cleanName(r.FormValue("place")), cleanText(r.FormValue("details"))
	c.StravaURL = strings.TrimSpace(r.FormValue("strava_url"))
	c.Kind = kindFor(r.FormValue("kind"))
	c.Groups = groupsFor(r.FormValue("pace"), c.Groups)
	c.Level = levelFor(r.Form["level"])
	if !c.IsRide() { // one host, no pace groups or level
		c.Groups, c.Level = nil, ""
	}
	if dateEditable {
		c.Date = strings.TrimSpace(r.FormValue("date"))
	}
	_, dateErr := time.Parse("2006-01-02", c.Date)
	_, timeErr := time.Parse("15:04", c.Time)
	switch {
	case c.Name == "" || dateErr != nil:
		return "Give it a name and a date."
	case c.Time != "" && timeErr != nil:
		return "Pick a start time, or leave it empty."
	case c.StravaURL != "" && !stravaLink(c.StravaURL):
		return "Paste the event’s Strava link, starting with https://www.strava.com/."
	}
	return ""
}

// readLeaders sets each group's leader from the ride page.
func readLeaders(r *http.Request, c *CalRide, me string) {
	old := c.Leaders
	c.Leaders = nil
	for _, g := range c.groupNames() {
		var was *Leader
		for i := range old {
			if old[i].Group == g {
				was = &old[i]
			}
		}
		name := cleanName(r.FormValue(leaderField(g)))
		if _, sent := r.Form[leaderField(g)]; !sent && was != nil {
			name = was.Name
		}
		switch {
		case name == "":
		case was != nil && name == was.Name:
			c.Leaders = append(c.Leaders, *was)
		default:
			c.Leaders = append(c.Leaders, Leader{Group: g, Name: name, SetBy: me, Since: time.Now().UTC()})
		}
	}
}

// saveRideAdmin saves a ride's page: its details, pace groups, and leaders.
// Changing leaders isn't emailed to the club; a new date, time or start is.
func (a *App) saveRideAdmin(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	me, _ := a.adminName(r)
	r.ParseForm()
	var before RideOcc
	var problem string
	o, err := a.store.UpdateRide(key, func(c *CalRide) error {
		before = occOf(c)
		if problem = readRide(r, c, c.SeriesID == 0); problem != "" {
			return errNoRide
		}
		readLeaders(r, c, me)
		return nil
	})
	if problem != "" {
		// Show the form again with what was typed.
		f := before.CalRide
		readRide(r, &f, f.SeriesID == 0)
		readLeaders(r, &f, me)
		shown := before
		shown.CalRide = f
		v := adminView{Title: before.Name, Tab: "calendar", RideForm: a.rideFormFor(shown, false), Error: problem}
		a.renderAdmin(w, r, http.StatusBadRequest, "ride", v)
		return
	}
	if err != nil {
		back(w, r, "/admin/calendar?err=noride")
		return
	}
	if moved := rideMoved(before, o); moved != "" {
		a.notifyRide(fmt.Sprintf("%s on %s has changed", o.Name, shortDate(o.Date)),
			fmt.Sprintf("%s changed %s: %s. It’s now on %s.", me, before.Name, moved, rideWhen(o)), o, "")
	}
	back(w, r, "/admin/rides/"+url.PathEscape(o.Key)+"?ok=ride-saved")
}

// rideMoved describes a change to when or where a ride is, or "".
func rideMoved(was, now RideOcc) string {
	var what []string
	if was.Date != now.Date {
		what = append(what, "new date")
	}
	if was.Time != now.Time {
		what = append(what, "new start time")
	}
	if was.Place != now.Place {
		what = append(what, "new start")
	}
	return strings.Join(what, ", ")
}

// addRideAdmin adds a ride or event from the admin portal: on one date, or
// with "Repeat every week", as a weekly ride starting that date.
func (a *App) addRideAdmin(w http.ResponseWriter, r *http.Request) {
	me, _ := a.adminName(r)
	r.ParseForm()
	c := CalRide{AddedBy: me}
	problem := readRide(r, &c, true)
	readLeaders(r, &c, me)
	weekly := r.FormValue("repeat") == "weekly"
	until := strings.TrimSpace(r.FormValue("repeat_until"))
	if problem == "" && weekly {
		_, untilErr := time.Parse("2006-01-02", until)
		switch {
		case c.Time == "":
			problem = "Pick a start time for something that repeats every week."
		case until != "" && (untilErr != nil || until < c.Date):
			problem = "The last date has to be on or after the first one."
		}
	}
	if problem != "" {
		f := a.rideFormFor(RideOcc{CalRide: c}, true)
		f.Weekly, f.Until = weekly, until
		v := adminView{Title: "Add a ride", Tab: "calendar", RideForm: f, Error: problem}
		a.renderAdmin(w, r, http.StatusBadRequest, "ride", v)
		return
	}
	if weekly {
		a.addWeeklyFromRide(w, r, c, until, me)
		return
	}
	o, err := a.store.AddRide(c)
	if err != nil {
		back(w, r, "/admin/calendar?err=save")
		return
	}
	what := "a ride"
	if !o.IsRide() {
		what = strings.ToLower(o.KindName())
	}
	body := fmt.Sprintf("%s added %s to the calendar: %s on %s.", me, what, o.Name, rideWhen(o))
	if st := o.Status(); st == "open" || st == "partial" {
		body += " It still needs leaders: sign up on the ride page."
	}
	a.notifyRide(fmt.Sprintf("New on the calendar: %s on %s", o.Name, shortDate(o.Date)), body, o, "")
	back(w, r, "/admin/rides/"+url.PathEscape(o.Key)+"?ok=ride-added")
}

// addWeeklyFromRide makes the Add a ride form into a weekly ride, from its
// date on (and until its last date, if it has one). Leaders typed on the form
// lead the first date.
func (a *App) addWeeklyFromRide(w http.ResponseWriter, r *http.Request, c CalRide, until, me string) {
	d, _ := time.Parse("2006-01-02", c.Date)
	se := RideSeries{Name: c.Name, Weekday: int(d.Weekday()), Time: c.Time, Place: c.Place, Details: c.Details,
		StravaURL: c.StravaURL, Groups: c.Groups, Level: c.Level, Kind: c.Kind, Starts: c.Date, Ends: until}
	id, err := a.store.AddSeries(se)
	if err != nil {
		back(w, r, "/admin/calendar?err=save")
		return
	}
	key := seriesKey(id, c.Date)
	o, _ := a.store.Ride(key)
	if len(c.Leaders) > 0 {
		o, _ = a.store.UpdateRide(key, func(x *CalRide) error { x.Leaders = c.Leaders; return nil })
	}
	when := "every " + d.Weekday().String() + " from " + shortDate(c.Date)
	if until != "" {
		when += " to " + shortDate(until)
	}
	a.notifyRide(fmt.Sprintf("New on the calendar: %s, every %s", c.Name, d.Weekday()),
		fmt.Sprintf("%s added %s to the calendar, %s at %s.", me, c.Name, when, timeLabel(c.Time)), o, "")
	back(w, r, "/admin/rides/"+url.PathEscape(key)+"?ok=weekly-added")
}

func (a *App) cancelRideAdmin(w http.ResponseWriter, r *http.Request) {
	me, _ := a.adminName(r)
	cancel := r.FormValue("cancel") == "1"
	o, err := a.store.UpdateRide(r.PathValue("key"), func(c *CalRide) error { c.Cancelled = cancel; return nil })
	if err != nil {
		back(w, r, "/admin/calendar?err=save")
		return
	}
	code := "restored"
	if cancel {
		code = "cancelled"
		a.notifyRide(fmt.Sprintf("%s on %s is cancelled", o.Name, shortDate(o.Date)),
			fmt.Sprintf("%s cancelled %s on %s.", me, o.Name, rideWhen(o)), o, "")
	}
	to := "/admin/rides/" + url.PathEscape(o.Key) + "?ok=" + code
	if r.FormValue("return") == "rides" {
		to = "/admin/rides?ok=" + code
	}
	back(w, r, to)
}

// approveRide puts a ride someone added on the website onto the calendar.
func (a *App) approveRide(w http.ResponseWriter, r *http.Request) {
	to := "/admin/calendar"
	if r.FormValue("return") == "rides" {
		to = "/admin/rides"
	}
	if _, err := a.store.UpdateRide(r.PathValue("key"), func(c *CalRide) error { c.Held = false; return nil }); err != nil {
		back(w, r, to+"?err=noride")
		return
	}
	back(w, r, to+"?ok=ride-approved")
}

func (a *App) deleteRideAdmin(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteRide(r.PathValue("key")); err != nil {
		back(w, r, "/admin/calendar?err=save")
		return
	}
	back(w, r, "/admin/calendar?ok=ride-deleted")
}

// ---- Weekly rides ----

// readSeries reads the weekly ride form (adding or editing). It returns an
// error code for errorText, or "".
func readSeries(r *http.Request, se *RideSeries) string {
	wd, err := strconv.Atoi(r.FormValue("weekday"))
	se.Name, se.Weekday, se.Time = cleanName(r.FormValue("name")), wd, strings.TrimSpace(r.FormValue("time"))
	se.Place, se.Details = cleanName(r.FormValue("place")), cleanText(r.FormValue("details"))
	se.StravaURL = strings.TrimSpace(r.FormValue("strava_url"))
	se.Kind = kindFor(r.FormValue("kind"))
	se.Groups = groupsFor(r.FormValue("pace"), se.Groups)
	se.Level = levelFor(r.Form["level"])
	if se.Kind != "" {
		se.Groups, se.Level = nil, ""
	}
	se.Ends = ""
	if until := r.FormValue("ends"); until != "" {
		if _, err := time.Parse("2006-01-02", until); err != nil || until < se.Starts {
			return "ends"
		}
		se.Ends = until
	}
	se.SeasonStart, se.SeasonEnd = "", ""
	if from, until := r.FormValue("season_start"), r.FormValue("season_end"); from != "" || until != "" {
		f, err1 := time.Parse("2006-01-02", from)
		u, err2 := time.Parse("2006-01-02", until)
		if err1 != nil || err2 != nil {
			return "season"
		}
		se.SeasonStart, se.SeasonEnd = f.Format("01-02"), u.Format("01-02")
	}
	if _, terr := time.Parse("15:04", se.Time); se.Name == "" || err != nil || wd < 0 || wd > 6 || terr != nil {
		return "series"
	}
	return ""
}

func (a *App) addSeries(w http.ResponseWriter, r *http.Request) {
	var se RideSeries
	if code := readSeries(r, &se); code != "" {
		back(w, r, "/admin/rides?err="+code)
		return
	}
	if _, err := a.store.AddSeries(se); err != nil {
		back(w, r, "/admin/rides?err=save")
		return
	}
	back(w, r, "/admin/rides?ok=series-added")
}

// seriesForm is a weekly ride on its edit page. The season shows as dates in
// this year, since the form uses date boxes (only the month and day count).
type seriesForm struct {
	RideSeries
	Pace, From, Until string
}

func (a *App) seriesEditPage(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	se, ok := a.store.Series(id)
	if !ok {
		back(w, r, "/admin/rides?err=noride")
		return
	}
	f := seriesForm{RideSeries: se, Pace: paceOf(se.Groups)}
	if se.SeasonStart != "" {
		y := strconv.Itoa(a.today().Year())
		f.From, f.Until = y+"-"+se.SeasonStart, y+"-"+se.SeasonEnd
	}
	v := adminView{Title: se.Name, Tab: "rides", SeriesForm: f, Weekdays: weekdays}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "series", v)
}

func (a *App) saveSeries(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	se, ok := a.store.Series(id)
	if !ok {
		back(w, r, "/admin/rides?err=noride")
		return
	}
	if code := readSeries(r, &se); code != "" {
		back(w, r, fmt.Sprintf("/admin/weekly/%d?err=%s", id, code))
		return
	}
	if err := a.store.UpdateSeries(se, a.today().Format("2006-01-02")); err != nil {
		back(w, r, "/admin/rides?err=save")
		return
	}
	back(w, r, "/admin/rides?ok=series-saved")
}

func (a *App) deleteSeries(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if err := a.store.DeleteSeries(id); err != nil {
		back(w, r, "/admin/rides?err=save")
		return
	}
	back(w, r, "/admin/rides?ok=series-deleted")
}

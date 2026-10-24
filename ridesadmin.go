package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The admin portal's Rides tab: the weekly rides, upcoming rides and their
// leaders (admins can assign or change any leader), and who the club's
// admins on Strava are.

func (a *App) ridesAdmin(w http.ResponseWriter, r *http.Request) {
	today := a.today()
	v := adminView{Title: "Rides", Tab: "rides", Series: a.store.AllSeries(), Upcoming: a.store.Rides(today, today.AddDate(0, 0, 60)),
		StravaReady: a.stravaReady(), Weekdays: weekdays}
	v.StravaAdmins, v.StravaAdminsChecked = a.store.StravaAdmins()
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "rides", v)
}

var weekdays = func() []monthOption {
	var out []monthOption
	for d := time.Sunday; d <= time.Saturday; d++ {
		out = append(out, monthOption{int(d), d.String()})
	}
	return out
}()

func (a *App) addSeries(w http.ResponseWriter, r *http.Request) {
	wd, err := strconv.Atoi(r.FormValue("weekday"))
	se := RideSeries{
		Name: cleanName(r.FormValue("name")), Weekday: wd, Time: strings.TrimSpace(r.FormValue("time")),
		Place: cleanName(r.FormValue("place")), Details: cleanName(r.FormValue("details")),
		StravaURL: strings.TrimSpace(r.FormValue("strava_url")),
	}
	if from, until := r.FormValue("season_start"), r.FormValue("season_end"); from != "" && until != "" {
		f, err1 := time.Parse("2006-01-02", from)
		u, err2 := time.Parse("2006-01-02", until)
		if err1 != nil || err2 != nil {
			back(w, r, "/admin/rides?err=season")
			return
		}
		se.SeasonStart, se.SeasonEnd = f.Format("01-02"), u.Format("01-02")
	}
	if _, terr := time.Parse("15:04", se.Time); se.Name == "" || err != nil || wd < 0 || wd > 6 || terr != nil {
		back(w, r, "/admin/rides?err=series")
		return
	}
	if err := a.store.AddSeries(se); err != nil {
		back(w, r, "/admin/rides?err=save")
		return
	}
	back(w, r, "/admin/rides?ok=series-added")
}

func (a *App) deleteSeries(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if err := a.store.DeleteSeries(id); err != nil {
		back(w, r, "/admin/rides?err=save")
		return
	}
	back(w, r, "/admin/rides?ok=series-deleted")
}

// setLeaderAdmin assigns a leader by name, or clears it, from the admin portal.
func (a *App) setLeaderAdmin(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	me, _ := a.adminName(r)
	name := cleanName(r.FormValue("leader"))
	var was string
	o, err := a.store.UpdateRide(key, func(c *CalRide) error {
		if c.Leader != nil {
			was = c.Leader.Name
		}
		c.Leader = nil
		if name != "" {
			c.Leader = &Leader{Name: name, SetBy: me, Since: time.Now().UTC()}
		}
		return nil
	})
	if err != nil {
		back(w, r, "/admin/rides?err=save")
		return
	}
	switch {
	case name == "" && was != "":
		a.notifyRide(fmt.Sprintf("%s on %s needs a leader", o.Name, shortDate(o.Date)),
			fmt.Sprintf("%s removed %s as the leader of %s on %s.", me, was, o.Name, rideWhen(o)), o, true)
	case name != "" && name != was:
		a.notifyRide(fmt.Sprintf("%s will lead %s on %s", name, o.Name, shortDate(o.Date)),
			fmt.Sprintf("%s made %s the leader of %s on %s.", me, name, o.Name, rideWhen(o)), o, true)
	}
	back(w, r, "/admin/rides?ok=leader")
}

func (a *App) cancelRideAdmin(w http.ResponseWriter, r *http.Request) {
	cancel := r.FormValue("cancel") == "1"
	if _, err := a.store.UpdateRide(r.PathValue("key"), func(c *CalRide) error { c.Cancelled = cancel; return nil }); err != nil {
		back(w, r, "/admin/rides?err=save")
		return
	}
	back(w, r, "/admin/rides?ok=leader")
}

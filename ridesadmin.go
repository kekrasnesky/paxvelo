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
	if l, ok := a.store.StravaLink(); ok {
		v.StravaLink = &l
	}
	v.APIEnabled = a.cfg.CalendarAPIKey != ""
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
		StravaURL: strings.TrimSpace(r.FormValue("strava_url")), Groups: parseGroups(r.FormValue("groups")),
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

// setLeaderAdmin assigns leaders by name, or clears them, from the admin
// portal: one box per pace group (leader0, leader1, …).
func (a *App) setLeaderAdmin(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	me, _ := a.adminName(r)
	r.ParseForm()
	type change struct{ group, was, now string }
	var changes []change
	o, err := a.store.UpdateRide(key, func(c *CalRide) error {
		for i, g := range c.groupNames() {
			field := "leader" + strconv.Itoa(i)
			if _, sent := r.Form[field]; !sent {
				continue
			}
			name, was := cleanName(r.FormValue(field)), ""
			if l := c.leaderFor(g); l != nil {
				was = l.Name
			}
			if name == was {
				continue
			}
			changes = append(changes, change{g, was, name})
			if name == "" {
				c.setLeader(g, nil)
			} else {
				c.setLeader(g, &Leader{Name: name, SetBy: me, Since: time.Now().UTC()})
			}
		}
		return nil
	})
	if err != nil {
		back(w, r, "/admin/rides?err=save")
		return
	}
	for _, ch := range changes {
		what := rideAndGroup(o.Name, ch.group)
		if ch.now == "" {
			a.notifyRide(fmt.Sprintf("%s on %s needs a leader", what, shortDate(o.Date)),
				fmt.Sprintf("%s removed %s as the leader of %s on %s.", me, ch.was, what, rideWhen(o)), o, "")
		} else {
			a.notifyRide(fmt.Sprintf("%s will lead %s on %s", ch.now, what, shortDate(o.Date)),
				fmt.Sprintf("%s made %s the leader of %s on %s.", me, ch.now, what, rideWhen(o)), o, "")
		}
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

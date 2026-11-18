package main

import (
	"encoding/json"
	"time"
)

// Starting values on the Add a ride forms (/admin/rides/new and the public
// /calendar/new): when a date is picked, the start time, place, pace groups
// and level come from the weekly ride on that weekday, or else from the
// latest one-off ride on that weekday. The page fills only fields still
// empty or holding what it filled itself (site.js fillStarts), never one
// someone typed in.

// rideStart is one weekday's starting values.
type rideStart struct {
	Time  string `json:"time,omitempty"`
	Place string `json:"place,omitempty"`
	Pace  string `json:"pace,omitempty"` // the pace menu's value; "" (one leader) is left alone
	Level string `json:"level,omitempty"`
}

// rideStarts are the starting values by weekday (0 = Sunday), as JSON for
// the form's data-starts.
func (a *App) rideStarts() string {
	today := a.today().Format(time.DateOnly)
	out := map[int]rideStart{}
	for _, se := range a.store.AllSeries() {
		if _, ok := out[se.Weekday]; ok || se.Kind != "" || (se.Ends != "" && se.Ends < today) {
			continue
		}
		out[se.Weekday] = rideStart{Time: se.Time, Place: se.Place, Pace: paceOf(se.Groups), Level: se.Level}
	}
	// One-off rides: the latest on or before today, else the soonest after.
	best := map[int]CalRide{}
	for _, c := range a.store.OneOffRides() {
		d, err := time.Parse(time.DateOnly, c.Date)
		if err != nil || !c.IsRide() || c.Held {
			continue
		}
		wd := int(d.Weekday())
		b, ok := best[wd]
		switch {
		case !ok:
		case c.Date <= today && (b.Date > today || c.Date > b.Date):
		case c.Date > today && b.Date > today && c.Date < b.Date:
		default:
			continue
		}
		best[wd] = c
	}
	for wd, c := range best {
		if _, ok := out[wd]; !ok {
			out[wd] = rideStart{Time: c.Time, Place: c.Place, Pace: paceOf(c.Groups), Level: c.Level}
		}
	}
	for wd, s := range out { // the pace menu can't show other groups
		if s.Pace == "keep" {
			s.Pace = ""
			out[wd] = s
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// OneOffRides are the rides added for one date (not a weekly ride's).
func (s *Store) OneOffRides() []CalRide {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []CalRide
	for _, c := range s.d.CalRides {
		if c.SeriesID == 0 {
			out = append(out, *c)
		}
	}
	return out
}

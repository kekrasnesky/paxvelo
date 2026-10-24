package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The ride calendar shows every club ride: the weekly rides (set up by admins
// on the Rides tab) and one-off rides anyone signed in can add. Each ride can
// have one leader. A ride with no leader can be taken by anyone signed in with
// Strava; once it has one, only the leader (stepping down) or an admin can
// change it. Admins here are the club's admins on Strava, plus anyone using
// the admin portal.

// RideSeries is a weekly ride, like the Tuesday Night Ride.
type RideSeries struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Weekday   int    `json:"weekday"` // 0 = Sunday
	Time      string `json:"time"`    // 18:00
	Place     string `json:"place,omitempty"`
	Details   string `json:"details,omitempty"`
	StravaURL string `json:"strava_url,omitempty"`
	// The part of each year it runs, as month-day ("03-01" to "10-10").
	// Empty means all year.
	SeasonStart string `json:"season_start,omitempty"`
	SeasonEnd   string `json:"season_end,omitempty"`
}

// WeekdayName is the series' day, like Tuesday.
func (s RideSeries) WeekdayName() string { return time.Weekday(s.Weekday).String() }

// SeasonLabel describes when the series runs, like "Mar 1 – Oct 10".
func (s RideSeries) SeasonLabel() string {
	if s.SeasonStart == "" || s.SeasonEnd == "" {
		return "All year"
	}
	f := func(md string) string {
		t, err := time.Parse("01-02", md)
		if err != nil {
			return md
		}
		return t.Format("Jan 2")
	}
	return f(s.SeasonStart) + " – " + f(s.SeasonEnd)
}

func (s RideSeries) runsOn(d time.Time) bool {
	if int(d.Weekday()) != s.Weekday {
		return false
	}
	if s.SeasonStart == "" || s.SeasonEnd == "" {
		return true
	}
	md := d.Format("01-02")
	if s.SeasonStart <= s.SeasonEnd {
		return md >= s.SeasonStart && md <= s.SeasonEnd
	}
	return md >= s.SeasonStart || md <= s.SeasonEnd // a season across New Year
}

// Leader is who is leading a ride.
type Leader struct {
	Name        string    `json:"name"`
	StravaID    int64     `json:"strava_id,omitempty"`
	StravaAdmin bool      `json:"strava_admin,omitempty"`
	SetBy       string    `json:"set_by,omitempty"` // an admin who assigned them, if one did
	Since       time.Time `json:"since"`
}

// CalRide is a ride on one date: a one-off ride, or one date of a weekly ride
// once something about that date has changed (a leader, a Strava link).
type CalRide struct {
	ID        int       `json:"id"`
	SeriesID  int       `json:"series_id,omitempty"`
	Date      string    `json:"date"`
	Time      string    `json:"time,omitempty"`
	Name      string    `json:"name"`
	Place     string    `json:"place,omitempty"`
	Details   string    `json:"details,omitempty"`
	StravaURL string    `json:"strava_url,omitempty"`
	Leader    *Leader   `json:"leader,omitempty"`
	Cancelled bool      `json:"cancelled,omitempty"`
	AddedBy   string    `json:"added_by,omitempty"`
	Added     time.Time `json:"added"`
}

// RideOcc is one ride on the calendar.
type RideOcc struct {
	Key string // s3-2026-11-04 for a weekly ride's date, r12 for a one-off ride
	CalRide
	Custom bool // a one-off ride, not a weekly one
}

func (o RideOcc) TimeLabel() string { return timeLabel(o.Time) }
func (o RideOcc) DateLabel() string {
	t, err := time.Parse("2006-01-02", o.Date)
	if err != nil {
		return o.Date
	}
	return t.Format("Monday, January 2, 2006")
}

func timeLabel(hm string) string {
	t, err := time.Parse("15:04", hm)
	if err != nil {
		return hm
	}
	return t.Format("3:04 PM")
}

var (
	errHasLeader = errors.New("this ride already has a leader")
	errNoRide    = errors.New("that ride isn't on the calendar")
)

// ---- Store ----

func seriesKey(id int, date string) string { return fmt.Sprintf("s%d-%s", id, date) }

func (s *Store) seriesByID(id int) *RideSeries {
	for _, se := range s.d.Series {
		if se.ID == id {
			return se
		}
	}
	return nil
}

func occOf(r *CalRide) RideOcc {
	c := *r
	if r.Leader != nil {
		l := *r.Leader
		c.Leader = &l
	}
	if r.SeriesID != 0 {
		return RideOcc{Key: seriesKey(r.SeriesID, r.Date), CalRide: c}
	}
	return RideOcc{Key: "r" + strconv.Itoa(r.ID), CalRide: c, Custom: true}
}

// Rides returns every ride from one date to another (inclusive), by date and time.
func (s *Store) Rides(from, to time.Time) []RideOcc {
	s.mu.Lock()
	defer s.mu.Unlock()
	fromS, toS := from.Format("2006-01-02"), to.Format("2006-01-02")
	var out []RideOcc
	stored := map[string]bool{}
	for _, r := range s.d.CalRides {
		if r.Date >= fromS && r.Date <= toS {
			o := occOf(r)
			stored[o.Key] = true
			out = append(out, o)
		}
	}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		for _, se := range s.d.Series {
			if !se.runsOn(d) {
				continue
			}
			date := d.Format("2006-01-02")
			if key := seriesKey(se.ID, date); !stored[key] {
				out = append(out, RideOcc{Key: key, CalRide: CalRide{SeriesID: se.ID, Date: date, Time: se.Time,
					Name: se.Name, Place: se.Place, Details: se.Details, StravaURL: se.StravaURL}})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Time < out[j].Time
	})
	return out
}

// ride finds (and for a weekly ride's date, creates) the stored ride for a
// key. Callers must hold s.mu.
func (s *Store) ride(key string, create bool) (*CalRide, error) {
	if id, err := strconv.Atoi(strings.TrimPrefix(key, "r")); strings.HasPrefix(key, "r") && err == nil {
		for _, r := range s.d.CalRides {
			if r.ID == id && r.SeriesID == 0 {
				return r, nil
			}
		}
		return nil, errNoRide
	}
	sid, date, ok := strings.Cut(strings.TrimPrefix(key, "s"), "-")
	id, err := strconv.Atoi(sid)
	if !strings.HasPrefix(key, "s") || !ok || err != nil {
		return nil, errNoRide
	}
	for _, r := range s.d.CalRides {
		if r.SeriesID == id && r.Date == date {
			return r, nil
		}
	}
	se := s.seriesByID(id)
	d, err := time.Parse("2006-01-02", date)
	if se == nil || err != nil || !se.runsOn(d) {
		return nil, errNoRide
	}
	r := &CalRide{SeriesID: id, Date: date, Time: se.Time, Name: se.Name, Place: se.Place, Details: se.Details, StravaURL: se.StravaURL, Added: time.Now().UTC()}
	if create {
		s.d.NextRide = max(s.d.NextRide, 1)
		r.ID = s.d.NextRide
		s.d.NextRide++
		s.d.CalRides = append(s.d.CalRides, r)
	}
	return r, nil
}

func (s *Store) Ride(key string) (RideOcc, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.ride(key, false)
	if err != nil {
		return RideOcc{}, false
	}
	return occOf(r), true
}

// UpdateRide changes a ride under the store's lock. change may refuse with an
// error, and then nothing is saved.
func (s *Store) UpdateRide(key string, change func(r *CalRide) error) (RideOcc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.ride(key, false)
	if err != nil {
		return RideOcc{}, err
	}
	test := *r
	if err := change(&test); err != nil {
		return RideOcc{}, err
	}
	if r, err = s.ride(key, true); err != nil {
		return RideOcc{}, err
	}
	id := r.ID
	*r = test
	r.ID = id
	return occOf(r), s.save()
}

func (s *Store) AddRide(r CalRide) (RideOcc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.NextRide = max(s.d.NextRide, 1)
	r.ID, r.SeriesID, r.Added = s.d.NextRide, 0, time.Now().UTC()
	s.d.NextRide++
	c := r
	s.d.CalRides = append(s.d.CalRides, &c)
	return occOf(&c), s.save()
}

func (s *Store) AllSeries() []RideSeries {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RideSeries
	for _, se := range s.d.Series {
		out = append(out, *se)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Weekday != out[j].Weekday {
			return out[i].Weekday < out[j].Weekday
		}
		return out[i].Time < out[j].Time
	})
	return out
}

func (s *Store) AddSeries(se RideSeries) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.NextSeries = max(s.d.NextSeries, 1)
	se.ID = s.d.NextSeries
	s.d.NextSeries++
	s.d.Series = append(s.d.Series, &se)
	return s.save()
}

// DeleteSeries removes a weekly ride. Dates that already have a leader stay
// on the calendar as one-off rides.
func (s *Store) DeleteSeries(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, se := range s.d.Series {
		if se.ID == id {
			s.d.Series = append(s.d.Series[:i], s.d.Series[i+1:]...)
			break
		}
	}
	kept := s.d.CalRides[:0]
	for _, r := range s.d.CalRides {
		if r.SeriesID == id {
			if r.Leader == nil {
				continue
			}
			r.SeriesID = 0
		}
		kept = append(kept, r)
	}
	s.d.CalRides = kept
	return s.save()
}

func (s *Store) StravaAdmins() ([]string, *time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.d.StravaAdmins...), s.d.StravaAdminsChecked
}

func (s *Store) SetStravaAdmins(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.d.StravaAdmins, s.d.StravaAdminsChecked = names, &now
	s.saveOrLog()
}

// defaultSeries is the club's regular weekly rides, added the first time the
// site starts. Admins can change them on the Rides tab.
func defaultSeries() []*RideSeries {
	return []*RideSeries{
		{ID: 1, Name: "Tuesday Night Ride", Weekday: 2, Time: "18:00", Place: "NAS Pax", Details: "Fast · A group 24–28 mph, B group 20–22 mph"},
		{ID: 2, Name: "Thursday Night Ride", Weekday: 4, Time: "18:00", Place: "Park & Ride by NAS Pax Gate 2", Details: "Members · paceline, 20–24 mph, about 40 miles",
			SeasonStart: "03-01", SeasonEnd: "10-10"},
	}
}

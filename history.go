package main

import (
	"net/http"
	"sort"
	"time"
)

// The ride history: every club event Strava still has (back to 2017), and
// who joined each one. Strava only keeps who joined for about 16 months,
// so the site saves each list once the ride is over and keeps it for good.
// Joining an event is a sign-up, not proof someone rode.
//
// Only admins see this. Strava's rules limit showing one person's data to
// others, and most club events are private to the club.

// PastRide is one club event from Strava, kept for the ride history.
type PastRide struct {
	ID        string     `json:"id"`
	Date      string     `json:"date"`
	Time      string     `json:"time,omitempty"`
	Title     string     `json:"title"`
	Organizer string     `json:"organizer,omitempty"` // "Patrick O."
	RouteID   string     `json:"route_id,omitempty"`
	Going     []Joiner   `json:"going,omitempty"`
	Checked   *time.Time `json:"going_checked,omitempty"` // when who joined was last read
}

// Joiner is a rider who joined an event on Strava.
type Joiner struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// final reports whether who joined was read after the ride's day, so it
// won't change anymore.
func (p PastRide) final() bool {
	return p.Checked != nil && p.Checked.Format("2006-01-02") > p.Date
}

// goingKept is how far back Strava still has who joined an event.
const goingKept = 480 * 24 * time.Hour

// maxGoingArchive is how many events' sign-ups one import saves.
const maxGoingArchive = 15

// pastRide turns an event from Strava's list into the history's record of it.
func (a *App) pastRide(id, title, routeID string, occurrences []string, organizer *stravaAthlete) (PastRide, bool) {
	if len(occurrences) == 0 {
		return PastRide{}, false
	}
	t, err := time.Parse(time.RFC3339, occurrences[0])
	if err != nil {
		return PastRide{}, false
	}
	t = t.In(a.cfg.Location)
	p := PastRide{ID: id, Date: t.Format("2006-01-02"), Time: t.Format("15:04"), Title: cleanName(title)}
	if routeID != "0" {
		p.RouteID = routeID
	}
	if organizer != nil {
		p.Organizer = organizer.shortName()
	}
	return p, true
}

// ---- Store ----

// SaveHistory records the club's events, keeping who joined each one.
func (s *Store) SaveHistory(list []PastRide) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.History == nil {
		s.d.History = map[string]*PastRide{}
	}
	for _, p := range list {
		if old := s.d.History[p.ID]; old != nil {
			p.Going, p.Checked = old.Going, old.Checked
		}
		s.d.History[p.ID] = &p
	}
	s.saveOrLog()
}

// goingDue lists the finished rides whose sign-ups haven't been saved since
// the ride, newest first, while Strava still has them.
func (s *Store) goingDue(today time.Time, limit int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	from, to := today.Add(-goingKept).Format("2006-01-02"), today.Format("2006-01-02")
	var due []*PastRide
	for _, p := range s.d.History {
		if p.Date >= from && p.Date < to && !p.final() {
			due = append(due, p)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].Date > due[j].Date })
	var out []string
	for i := 0; i < len(due) && i < limit; i++ {
		out = append(out, due[i].ID)
	}
	return out
}

func (s *Store) saveGoing(id string, going []Joiner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.d.History[id]; p != nil {
		now := time.Now().UTC()
		p.Going, p.Checked = going, &now
		s.saveOrLog()
	}
}

func (s *Store) PastRides() []PastRide {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PastRide
	for _, p := range s.d.History {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].Time > out[j].Time
	})
	return out
}

func (s *Store) routeName(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.d.Routes[id]; r != nil {
		return r.Name
	}
	return ""
}

// archiveGoing saves who joined recent finished rides, a few each import.
func (a *App) archiveGoing(get func(string, any) error) {
	for _, id := range a.store.goingDue(a.today(), maxGoingArchive) {
		var athletes []stravaAthlete
		if err := get("/group_events/"+id+"/athletes?per_page=200", &athletes); err != nil {
			continue
		}
		going := []Joiner{}
		for _, at := range athletes {
			going = append(going, Joiner{ID: at.ID, Name: at.shortName()})
		}
		a.store.saveGoing(id, going)
	}
}

// ---- The admin's History page ----

type historyView struct {
	Years     []yearStat
	Year      string // the year shown, or "" for all of them
	Rides     []historyRow
	Leaders   []countStat
	Riders    []countStat
	Total     int
	Since     string // the first ride with who joined saved, as "Jun 2025"
	Pending   int    // finished rides whose sign-ups are still to be saved
	ClubURL   string
	Connected bool
}

type yearStat struct {
	Year          string
	Rides, Joined int
	Current       bool
}

type countStat struct {
	Name  string
	Count int
}

type historyRow struct {
	PastRide
	Route     string
	StravaURL string
}

func (r historyRow) DateLabel() string {
	t, err := time.Parse("2006-01-02", r.Date)
	if err != nil {
		return r.Date
	}
	return t.Format("Mon, Jan 2")
}

func (a *App) historyAdmin(w http.ResponseWriter, r *http.Request) {
	all := a.store.PastRides()
	_, connected := a.store.StravaLink()
	hv := &historyView{Year: r.URL.Query().Get("y"), ClubURL: a.cfg.StravaURL, Connected: connected, Total: len(all)}
	today := a.today().Format("2006-01-02")
	if hv.Year == "" && len(all) > 0 {
		hv.Year = all[0].Date[:4]
		if all[0].Date > today { // the newest event hasn't happened yet: start on this year
			hv.Year = today[:4]
		}
	}
	byYear := map[string]*yearStat{}
	leaders, riders := map[string]int{}, map[int64]*countStat{}
	since := ""
	for _, p := range all {
		if p.Date > today {
			continue
		}
		y := p.Date[:4]
		if byYear[y] == nil {
			byYear[y] = &yearStat{Year: y}
		}
		byYear[y].Rides++
		byYear[y].Joined += len(p.Going)
		if !p.final() && p.Date >= a.today().Add(-goingKept).Format("2006-01-02") {
			hv.Pending++
		}
		if len(p.Going) > 0 && (since == "" || p.Date < since) {
			since = p.Date
		}
		if hv.Year != "all" && y != hv.Year {
			continue
		}
		if p.Organizer != "" {
			leaders[p.Organizer]++
		}
		for _, j := range p.Going {
			if riders[j.ID] == nil {
				riders[j.ID] = &countStat{Name: j.Name}
			}
			riders[j.ID].Count++
		}
		row := historyRow{PastRide: p, StravaURL: a.cfg.StravaURL + "/group_events/" + p.ID}
		if p.RouteID != "" {
			row.Route = a.store.routeName(p.RouteID)
		}
		hv.Rides = append(hv.Rides, row)
	}
	for _, y := range byYear {
		y.Current = y.Year == hv.Year
		hv.Years = append(hv.Years, *y)
	}
	sort.Slice(hv.Years, func(i, j int) bool { return hv.Years[i].Year > hv.Years[j].Year })
	for n, c := range leaders {
		hv.Leaders = append(hv.Leaders, countStat{n, c})
	}
	for _, c := range riders {
		hv.Riders = append(hv.Riders, *c)
	}
	hv.Leaders, hv.Riders = topCounts(hv.Leaders, 10), topCounts(hv.Riders, 15)
	if t, err := time.Parse("2006-01-02", since); err == nil {
		hv.Since = t.Format("Jan 2006")
	}
	v := adminView{Title: "Ride history", Tab: "history", History: hv}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "history", v)
}

// topCounts sorts counts biggest first, by name for ties, and keeps n.
func topCounts(c []countStat, n int) []countStat {
	sort.Slice(c, func(i, j int) bool {
		if c[i].Count != c[j].Count {
			return c[i].Count > c[j].Count
		}
		return c[i].Name < c[j].Name
	})
	if len(c) > n {
		c = c[:n]
	}
	return c
}

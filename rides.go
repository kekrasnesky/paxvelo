package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The ride calendar shows every club ride: the weekly rides (set up by admins
// on the Rides tab), the club's Strava events, and one-off rides anyone can
// add. A ride can be split into pace groups (like A and B), each with its own
// leader; a ride without groups has one leader. A spot with no leader can be
// taken by anyone who types their name; once it has one, only that leader
// (stepping down, with the club PIN) or an admin can change it.

// RideSeries is a weekly ride, like the Tuesday Night Ride.
type RideSeries struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Weekday   int    `json:"weekday"` // 0 = Sunday
	Time      string `json:"time"`    // 18:00
	Place     string `json:"place,omitempty"`
	Details   string `json:"details,omitempty"`
	StravaURL string `json:"strava_url,omitempty"`
	// Pace groups, each led by its own leader: "A group", "B group", and so on
	// (see paceGroups). None means the ride has one leader.
	Groups []string `json:"groups,omitempty"`
	// Level is how fast it goes ("social", "intermediate", "elite", or two of
	// them), when an admin picked one. Empty means automatic (see Levels).
	Level string `json:"level,omitempty"`
	// The part of each year it runs, as month-day ("03-01" to "10-10").
	// Empty means all year.
	SeasonStart string `json:"season_start,omitempty"`
	SeasonEnd   string `json:"season_end,omitempty"`
	// Its first and last dates (2026-11-04), for one added from the Add a ride
	// page with "Repeat every week". Empty means no limit.
	Starts    string `json:"starts,omitempty"`
	Ends      string `json:"ends,omitempty"`
	Kind      string `json:"kind,omitempty"`       // like CalRide.Kind
	Virtual   string `json:"virtual,omitempty"`    // like CalRide.Virtual
	ZwiftLogo string `json:"zwift_logo,omitempty"` // like CalRide.ZwiftLogo
}

// IsVirtual reports whether the weekly ride is virtual (see CalRide.Virtual).
func (s RideSeries) IsVirtual() bool { return isVirtual(s.Virtual, s.Name, s.Place, s.Details) }

// OnZwift reports whether it shows the Zwift logo (see CalRide.ZwiftLogo).
func (s RideSeries) OnZwift() bool {
	return s.IsVirtual() && isSetting(s.ZwiftLogo, guessZwift(s.Name, s.Place, s.Details))
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
	if day := d.Format("2006-01-02"); (s.Starts != "" && day < s.Starts) || (s.Ends != "" && day > s.Ends) {
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

// Leader is who is leading a ride, or one pace group of it.
type Leader struct {
	Group       string    `json:"group,omitempty"`
	Name        string    `json:"name"`
	StravaID    int64     `json:"strava_id,omitempty"`
	StravaAdmin bool      `json:"strava_admin,omitempty"`
	SetBy       string    `json:"set_by,omitempty"` // an admin who assigned them, if one did
	Since       time.Time `json:"since"`
}

// CalRide is a ride on one date: a one-off ride, or one date of a weekly ride
// once something about that date has changed (a leader, a Strava link).
type CalRide struct {
	ID        int      `json:"id"`
	SeriesID  int      `json:"series_id,omitempty"`
	Date      string   `json:"date"`
	Time      string   `json:"time,omitempty"`
	Name      string   `json:"name"`
	Place     string   `json:"place,omitempty"`
	Details   string   `json:"details,omitempty"`
	StravaURL string   `json:"strava_url,omitempty"`
	Groups    []string `json:"groups,omitempty"`
	Level     string   `json:"level,omitempty"` // like RideSeries.Level
	// Kind is what it is when it isn't a ride: a clinic, a club meeting, and so
	// on (see eventKinds). Empty means a group ride.
	Kind string `json:"kind,omitempty"`
	// Virtual says whether it's ridden on Zwift or an indoor trainer: "yes"
	// or "no" when someone ticked or unticked the box, and empty for
	// automatic, from what its name and description say (see guessVirtual).
	Virtual string `json:"virtual,omitempty"`
	// ZwiftLogo says whether a virtual ride shows the Zwift logo: "no" for
	// one on another app (Rouvy, a Zoom meeting), "yes", or empty for
	// automatic (see guessZwift).
	ZwiftLogo string    `json:"zwift_logo,omitempty"`
	Leaders   []Leader  `json:"leaders,omitempty"`
	External  string    `json:"external,omitempty"` // "strava:<event>:<date>", or an ID sent through the calendar API
	Cancelled bool      `json:"cancelled,omitempty"`
	AddedBy   string    `json:"added_by,omitempty"`
	Added     time.Time `json:"added"`
	// Held is a ride someone added on the website without signing in with Strava.
	// It stays off the calendar, the newsletter and the API until an admin approves it.
	Held bool `json:"held,omitempty"`
	// Strava is what the ride's Strava event says, for imported rides.
	Strava *StravaInfo `json:"strava,omitempty"`
}

// RideOcc is one ride on the calendar.
type RideOcc struct {
	Key string // s3-2026-11-04 for a weekly ride's date, r12 for a one-off ride
	CalRide
	Custom bool   // a one-off ride, not a weekly one
	Route  *Route // the Strava route, when the event has one
}

func (o RideOcc) TimeLabel() string { return timeLabel(o.Time) }

// Slot is one leader's place on a ride: a pace group, or the whole ride.
type Slot struct {
	Group  string // empty for a ride without pace groups
	Leader *Leader
}

// The calendar also has things that aren't rides: clinics, club meetings,
// socials. They have no pace groups or level, and nobody signs up to lead
// them; an admin can name who's hosting.
type eventKind struct{ Key, Name, Host string }

var eventKinds = []eventKind{
	{"", "Group ride", "Leader"},
	{"clinic", "Clinic or skills session", "Instructor"},
	{"meeting", "Club meeting", "Host"},
	{"social", "Social or party", "Host"},
	{"other", "Other event", "Host"},
}

// kindFor reads the admin's "What is it?" choice.
func kindFor(choice string) string {
	for _, k := range eventKinds {
		if k.Key == choice {
			return choice
		}
	}
	return ""
}

func (c CalRide) IsRide() bool { return c.Kind == "" }

// Noun is what to call it in a sentence: "ride" or "event".
func (c CalRide) Noun() string {
	if c.IsRide() {
		return "ride"
	}
	return "event"
}

// kindWords tell an event from a ride by its name, for events imported from
// Strava, where everything is an "event". The first match wins.
var kindWords = []struct{ kind, words string }{
	{"meeting", "meeting agm board"},
	{"clinic", "clinic skills workshop class maintenance lesson"},
	{"social", "party social potluck picnic banquet dinner happy-hour happy celebration holiday awards cookout"},
}

// guessKind is the kind of event a name sounds like, or "" for a ride.
func guessKind(name string) string {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r == '-') })
	for _, k := range kindWords {
		for _, w := range words {
			if slices.Contains(strings.Fields(k.words), w) {
				return k.kind
			}
		}
	}
	return ""
}

// virtualWords and virtualPhrases make a ride virtual automatically when its
// name, place or description mentions them.
var (
	virtualWords   = []string{"zwift", "virtual"}
	virtualPhrases = []string{"trainer ride", "trainer rides"}
)

// guessVirtual reports whether a ride's text says it's virtual.
func guessVirtual(texts ...string) bool {
	text := strings.ToLower(strings.Join(texts, " "))
	words := strings.FieldsFunc(text, func(r rune) bool { return r < 'a' || r > 'z' })
	for _, w := range virtualWords {
		if slices.Contains(words, w) {
			return true
		}
	}
	joined := " " + strings.Join(words, " ") + " "
	for _, p := range virtualPhrases {
		if strings.Contains(joined, " "+p+" ") {
			return true
		}
	}
	return false
}

// otherApps are where a virtual ride can be instead of Zwift.
var otherApps = []string{"rouvy", "mywhoosh", "whoosh", "trainerroad", "systm", "fulgaz", "kinomap", "bkool", "indievelo", "zoom", "teams", "meet"}

// guessZwift reports whether a virtual ride is on Zwift: it is unless its
// words name another app and not Zwift.
func guessZwift(texts ...string) bool {
	words := strings.FieldsFunc(strings.ToLower(strings.Join(texts, " ")), func(r rune) bool { return r < 'a' || r > 'z' })
	if slices.Contains(words, "zwift") {
		return true
	}
	for _, app := range otherApps {
		if slices.Contains(words, app) {
			return false
		}
	}
	return true
}

// isSetting is a yes/no setting: "yes", "no", or empty for automatic.
func isSetting(setting string, auto bool) bool {
	switch setting {
	case "yes":
		return true
	case "no":
		return false
	}
	return auto
}

func isVirtual(setting string, texts ...string) bool {
	return isSetting(setting, guessVirtual(texts...))
}

// IsVirtual reports whether it's ridden on Zwift or an indoor trainer.
func (c CalRide) IsVirtual() bool { return isVirtual(c.Virtual, c.texts()...) }

// texts are the words it's guessed from.
func (c CalRide) texts() []string {
	texts := []string{c.Name, c.Place, c.Details}
	if c.Strava != nil {
		texts = append(texts, c.Strava.Description)
	}
	return texts
}

// OnZwift reports whether a virtual ride shows the Zwift logo.
func (c CalRide) OnZwift() bool {
	return c.IsVirtual() && isSetting(c.ZwiftLogo, guessZwift(c.texts()...))
}

func (c CalRide) kind() eventKind {
	for _, k := range eventKinds {
		if k.Key == c.Kind {
			return k
		}
	}
	return eventKinds[0]
}

// KindName is what it is, like "Club meeting".
func (c CalRide) KindName() string { return c.kind().Name }

// HostLabel names the person running it: Leader, Instructor, or Host.
func (c CalRide) HostLabel() string { return c.kind().Host }

// cleanText tidies a description: plain text that keeps its line breaks, at
// most 1,000 characters.
func cleanText(s string) string {
	var lines []string
	blank := false
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		l = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, l)), " ")
		if l == "" { // at most one empty line in a row
			if blank || len(lines) == 0 {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		lines = append(lines, l)
	}
	s = strings.TrimSpace(strings.Join(lines, "\n"))
	if r := []rune(s); len(r) > 1000 {
		s = string(r[:1000])
	}
	return s
}

// firstLine is the start of a description, for lists with no room for all of
// it: its first line, at most 120 characters.
func firstLine(s string) string {
	line, rest, _ := strings.Cut(s, "\n")
	if r := []rune(line); len(r) > 120 {
		return strings.TrimSpace(string(r[:119])) + "…"
	}
	if strings.TrimSpace(rest) != "" {
		return line + " …"
	}
	return line
}

// The club rides in up to three pace groups. Admins and riders pick how many
// from a menu instead of typing them, so every ride names them the same way.
var paceGroups = []string{"A group", "B group", "C group"}

type paceChoice struct{ Value, Label string }

var paceChoices = []paceChoice{{"", "One leader (no pace groups)"}, {"2", "A and B groups"}, {"3", "A, B and C groups"}}

// groupsFor turns a choice from the pace menu into a ride's groups. "keep"
// keeps groups the menu doesn't offer, like ones from a Strava event.
func groupsFor(choice string, current []string) []string {
	switch choice {
	case "2":
		return append([]string(nil), paceGroups[:2]...)
	case "3":
		return append([]string(nil), paceGroups...)
	case "keep":
		return append([]string(nil), current...)
	}
	return nil
}

// paceOf is the menu choice for a ride's groups, or "keep" when the menu
// doesn't have one.
func paceOf(groups []string) string {
	for _, c := range paceChoices {
		if slices.Equal(groupsFor(c.Value, nil), groups) {
			return c.Value
		}
	}
	return "keep"
}

// tidyGroup turns the pace groups typed before the menu existed, like
// "A group · 24–28 mph", into the menu's names.
func tidyGroup(g string) string {
	if m := oldGroupRE.FindStringSubmatch(g); m != nil {
		return m[1] + " group"
	}
	return g
}

var oldGroupRE = regexp.MustCompile(`^([ABC]) group\b`)

// fitLeaders keeps the leaders whose group the ride still has. When a ride
// goes from one leader to pace groups (or back), that leader moves to the
// first group (or becomes the ride's leader).
func (c *CalRide) fitLeaders() {
	names := c.groupNames()
	var kept []Leader
	for _, l := range c.Leaders {
		switch {
		case slices.Contains(names, l.Group):
		case len(c.Leaders) == 1 && (l.Group == "" || len(c.Groups) == 0):
			l.Group = names[0]
		default:
			continue
		}
		kept = append(kept, l)
	}
	c.Leaders = kept
}

// groupNames is the ride's pace groups, or one unnamed group.
func (c CalRide) groupNames() []string {
	if len(c.Groups) == 0 {
		return []string{""}
	}
	return c.Groups
}

func (c CalRide) Slots() []Slot {
	var out []Slot
	for _, g := range c.groupNames() {
		out = append(out, Slot{Group: g, Leader: c.leaderFor(g)})
	}
	return out
}

func (c CalRide) leaderFor(group string) *Leader {
	for i := range c.Leaders {
		if c.Leaders[i].Group == group {
			l := c.Leaders[i]
			return &l
		}
	}
	return nil
}

func (c CalRide) hasGroup(group string) bool {
	for _, g := range c.groupNames() {
		if g == group {
			return true
		}
	}
	return false
}

// setLeader puts a leader in a group's spot, or empties it (nil).
func (c *CalRide) setLeader(group string, l *Leader) {
	kept := c.Leaders[:0:0]
	for _, x := range c.Leaders {
		if x.Group != group {
			kept = append(kept, x)
		}
	}
	if l != nil {
		l.Group = group
		kept = append(kept, *l)
	}
	c.Leaders = kept
}

// groupOf finds the group someone leads on this ride, by name.
func (c CalRide) groupOf(name string) (string, bool) {
	for _, l := range c.Leaders {
		if sameLeader(l, name) {
			return l.Group, true
		}
	}
	return "", false
}

// Status is how the calendar colors a ride: led, partly led, or open.
func (o RideOcc) Status() string {
	led := 0
	for _, s := range o.Slots() {
		if s.Leader != nil {
			led++
		}
	}
	switch {
	case o.Cancelled:
		return "cancelled"
	case !o.IsRide():
		return "event"
	case led == len(o.groupNames()):
		return "led"
	case led > 0:
		return "partial"
	}
	return "open"
}

// HostLine is the line under an event on the public calendar, whose kind is
// already on its label: who's hosting, or "Cancelled", or "".
func (o RideOcc) HostLine() string {
	if o.Cancelled {
		return "Cancelled"
	}
	if l := o.leaderFor(""); l != nil {
		return o.HostLabel() + ": " + l.Name
	}
	return ""
}

// LeaderSummary is the short line under a ride on the calendar.
func (o RideOcc) LeaderSummary() string {
	if o.Cancelled {
		return "Cancelled"
	}
	if !o.IsRide() {
		if l := o.leaderFor(""); l != nil {
			return o.KindName() + " · " + l.Name
		}
		return o.KindName()
	}
	var names []string
	missing := 0
	for _, s := range o.Slots() {
		if s.Leader == nil {
			missing++
		} else {
			names = append(names, s.Leader.Name)
		}
	}
	switch {
	case missing == 0:
		return strings.Join(names, ", ")
	case len(o.groupNames()) == 1:
		return "Needs a leader"
	case missing == 1:
		return "Needs 1 more leader"
	}
	return fmt.Sprintf("Needs %d leaders", missing)
}

// PublicAddedBy is who added a one-off ride, for its public page: a
// person's name or Strava, never "the calendar API".
func (o RideOcc) PublicAddedBy() string {
	if o.AddedBy == "the calendar API" {
		return ""
	}
	return o.AddedBy
}

// ShortDate is the date as "Thu, Mar 4", for compact lists.
func (o RideOcc) ShortDate() string {
	t, err := time.Parse("2006-01-02", o.Date)
	if err != nil {
		return o.Date
	}
	return t.Format("Mon, Jan 2")
}

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
	c.Leaders = append([]Leader(nil), r.Leaders...)
	c.Groups = append([]string(nil), r.Groups...)
	if r.Strava != nil {
		st := *r.Strava
		c.Strava = &st
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
		if r.Date >= fromS && r.Date <= toS && !r.Held {
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
					Name: se.Name, Place: se.Place, Details: se.Details, StravaURL: se.StravaURL, Groups: append([]string(nil), se.Groups...), Level: se.Level, Kind: se.Kind, Virtual: se.Virtual, ZwiftLogo: se.ZwiftLogo}})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Time < out[j].Time
	})
	for i := range out {
		s.withRoute(&out[i])
	}
	return out
}

// HeldRides returns the rides waiting for an admin's approval, soonest first.
func (s *Store) HeldRides() []RideOcc {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RideOcc
	for _, r := range s.d.CalRides {
		if r.Held {
			out = append(out, occOf(r))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
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
	r := &CalRide{SeriesID: id, Date: date, Time: se.Time, Name: se.Name, Place: se.Place, Details: se.Details, StravaURL: se.StravaURL,
		Groups: append([]string(nil), se.Groups...), Level: se.Level, Kind: se.Kind, Virtual: se.Virtual, ZwiftLogo: se.ZwiftLogo, Added: time.Now().UTC()}
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
	o := occOf(r)
	s.withRoute(&o)
	return o, true
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
	test.Leaders = append([]Leader(nil), r.Leaders...)
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

// AddSeries adds a weekly ride and returns its ID.
func (s *Store) AddSeries(se RideSeries) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.NextSeries = max(s.d.NextSeries, 1)
	se.ID = s.d.NextSeries
	s.d.NextSeries++
	s.d.Series = append(s.d.Series, &se)
	return se.ID, s.save()
}

func (s *Store) Series(id int) (RideSeries, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if se := s.seriesByID(id); se != nil {
		return *se, true
	}
	return RideSeries{}, false
}

// UpdateSeries changes a weekly ride. Its dates from `from` on follow the
// change, except for anything changed on that date alone. A date the ride no
// longer runs on (after a new day or season) stays only if it has leaders,
// as a one-off ride.
func (s *Store) UpdateSeries(se RideSeries, from string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.seriesByID(se.ID)
	if cur == nil {
		return errNoRide
	}
	old := *cur
	*cur = se
	follow := func(field *string, was, now string) {
		if *field == was {
			*field = now
		}
	}
	kept := s.d.CalRides[:0]
	for _, r := range s.d.CalRides {
		if r.SeriesID == se.ID && r.Date >= from {
			if d, _ := time.Parse("2006-01-02", r.Date); !se.runsOn(d) {
				if len(r.Leaders) == 0 {
					continue
				}
				r.SeriesID = 0
			} else {
				follow(&r.Name, old.Name, se.Name)
				follow(&r.Time, old.Time, se.Time)
				follow(&r.Place, old.Place, se.Place)
				follow(&r.Details, old.Details, se.Details)
				follow(&r.StravaURL, old.StravaURL, se.StravaURL)
				follow(&r.Level, old.Level, se.Level)
				follow(&r.Kind, old.Kind, se.Kind)
				follow(&r.Virtual, old.Virtual, se.Virtual)
				follow(&r.ZwiftLogo, old.ZwiftLogo, se.ZwiftLogo)
				if slices.Equal(r.Groups, old.Groups) {
					r.Groups = append([]string(nil), se.Groups...)
					r.fitLeaders()
				}
			}
		}
		kept = append(kept, r)
	}
	s.d.CalRides = kept
	return s.save()
}

// DeleteRide removes a one-off ride. (A weekly ride's date is cancelled
// instead, or it would just come back.)
func (s *Store) DeleteRide(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.ride(key, false)
	if err != nil || r.SeriesID != 0 {
		return errNoRide
	}
	s.d.CalRides = slices.DeleteFunc(s.d.CalRides, func(x *CalRide) bool { return x == r })
	return s.save()
}

// tidyGroups renames pace groups typed before the menu existed.
// Callers must hold s.mu.
func (s *Store) tidyGroups() {
	for _, se := range s.d.Series {
		for i, g := range se.Groups {
			se.Groups[i] = tidyGroup(g)
		}
	}
	for _, r := range s.d.CalRides {
		for i, g := range r.Groups {
			r.Groups[i] = tidyGroup(g)
		}
		for i := range r.Leaders {
			r.Leaders[i].Group = tidyGroup(r.Leaders[i].Group)
		}
	}
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
			if len(r.Leaders) == 0 {
				continue
			}
			r.SeriesID = 0
		}
		kept = append(kept, r)
	}
	s.d.CalRides = kept
	return s.save()
}

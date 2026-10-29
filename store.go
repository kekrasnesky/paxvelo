package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// All club data lives in one human-readable file: data/paxvelo.json.
// At club scale (hundreds or a few thousand members) this is plenty fast,
// needs no database software, and backing up means copying one file.

type Member struct {
	ID           int        `json:"id"`
	Name         string     `json:"name"`
	Email        string     `json:"email"`
	Joined       time.Time  `json:"joined"`
	Unsubscribed *time.Time `json:"unsubscribed,omitempty"`
	Token        string     `json:"token"`                // secret used in unsubscribe links
	Plan         string     `json:"plan,omitempty"`       // "individual" or "family"
	PaidYears    []int      `json:"paid_years,omitempty"` // calendar years their dues were paid
	Phone        string     `json:"phone,omitempty"`
	// Who to call if something happens on a ride.
	EmergencyName  string `json:"emergency_name,omitempty"`
	EmergencyPhone string `json:"emergency_phone,omitempty"`
	// From the join form: their experience (riderLevels) and the rides they
	// want to try (clubRides), which shape the welcome email.
	Level string   `json:"level,omitempty"`
	Rides []string `json:"rides,omitempty"`
}

// memberDetails is what someone fills in to join.
type memberDetails struct {
	Name, Email, Plan, Phone, EmergencyName, EmergencyPhone, Level string
	Rides                                                          []string
}

// update fills in the contact details someone gave, keeping what's already
// on file for anything they left blank.
func (m *Member) update(d memberDetails) {
	for _, f := range []struct {
		to   *string
		from string
	}{
		{&m.Name, d.Name}, {&m.Phone, d.Phone}, {&m.EmergencyName, d.EmergencyName}, {&m.EmergencyPhone, d.EmergencyPhone},
	} {
		if f.from != "" {
			*f.to = f.from
		}
	}
	if d.Level != "" {
		m.Level = d.Level
	}
	if len(d.Rides) > 0 {
		m.Rides = d.Rides
	}
}

// fill adds the contact details someone gave where the member has none yet,
// and never changes what's already on file.
func (m *Member) fill(d memberDetails) {
	for _, f := range []struct {
		to   *string
		from string
	}{
		{&m.Name, d.Name}, {&m.Phone, d.Phone}, {&m.EmergencyName, d.EmergencyName}, {&m.EmergencyPhone, d.EmergencyPhone},
	} {
		if *f.to == "" {
			*f.to = f.from
		}
	}
	if m.Level == "" {
		m.Level = d.Level
	}
	if len(m.Rides) == 0 {
		m.Rides = d.Rides
	}
}

func (m Member) Active() bool { return m.Unsubscribed == nil }

func (m Member) PaidFor(year int) bool {
	for _, y := range m.PaidYears {
		if y == year {
			return true
		}
	}
	return false
}

func (m Member) PlanName() string { return planLabel(m.Plan) }

func (m Member) FirstName() string {
	if f := strings.Fields(m.Name); len(f) > 0 {
		return f[0]
	}
	return ""
}

// Campaign is one email sent to every active member.
type Campaign struct {
	ID        int        `json:"id"`
	Subject   string     `json:"subject"`
	Body      string     `json:"body"`
	Created   time.Time  `json:"created"`
	Finished  *time.Time `json:"finished,omitempty"`
	Status    string     `json:"status"`             // sending, sent, stopped, paused
	Audience  string     `json:"audience,omitempty"` // who it went to, e.g. "Unpaid for 2026"
	Total     int        `json:"total"`
	Sent      int        `json:"sent"`
	Failed    int        `json:"failed"`
	LastError string     `json:"last_error,omitempty"`
	Pending   []int      `json:"pending,omitempty"` // member IDs still to send to
}

type EmailDraft struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type WelcomeEmail struct {
	Enabled bool   `json:"enabled"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type storeData struct {
	Secret       string       `json:"secret"`
	NextMember   int          `json:"next_member_id"`
	NextCampaign int          `json:"next_campaign_id"`
	Members      []*Member    `json:"members"`
	Campaigns    []*Campaign  `json:"campaigns"`
	Welcome      WelcomeEmail `json:"welcome_email"`
	Draft        EmailDraft   `json:"draft"`
	NextIssue    int          `json:"next_issue_id,omitempty"`
	Issues       []*Issue     `json:"newsletter,omitempty"`
	NextMinutes  int          `json:"next_minutes_id,omitempty"`
	Minutes      []*Minutes   `json:"minutes,omitempty"`
	Instagram    []string     `json:"instagram,omitempty"` // posts shown on the home page
	NextAdmin    int          `json:"next_admin_id,omitempty"`
	Admins       []*AdminUser `json:"admins,omitempty"`

	// Ride calendar
	NextRide            int                  `json:"next_ride_id,omitempty"`
	NextSeries          int                  `json:"next_series_id,omitempty"`
	SeriesSeeded        bool                 `json:"series_seeded,omitempty"`
	Series              []*RideSeries        `json:"weekly_rides,omitempty"`
	CalRides            []*CalRide           `json:"rides,omitempty"`
	StravaAdmins        []string             `json:"strava_admins,omitempty"`
	StravaAdminsChecked *time.Time           `json:"strava_admins_checked,omitempty"`
	Routes              map[string]*Route    `json:"strava_routes,omitempty"` // by Strava route ID
	RoutesChecked       *time.Time           `json:"strava_routes_checked,omitempty"`
	StravaClub          *StravaClub          `json:"strava_club,omitempty"`
	History             map[string]*PastRide `json:"ride_history,omitempty"` // the club's Strava events, by event ID

	// Pages and board members edited in the admin
	PageEdits   map[string]PageEdit `json:"page_edits,omitempty"`
	StravaLink  *StravaLink         `json:"strava_link,omitempty"`
	Board       []BoardMember       `json:"board,omitempty"`
	BoardSeeded bool                `json:"board_seeded,omitempty"`
	HomePhotos  []HomePhoto         `json:"home_photos,omitempty"`
	HomeSeeded  bool                `json:"home_photos_seeded,omitempty"`
}

type Store struct {
	mu      sync.Mutex
	path    string
	d       storeData
	version uint64 // goes up with every save, so pages built from the data know when to rebuild
}

// Version changes whenever anything in the club data is saved.
func (s *Store) Version() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

const defaultWelcomeBody = `Hi {first_name},

Welcome to PaxVelo! We're glad you're here.

{ride_tips}

If you haven't paid your {year} dues yet, you can finish up here:
{pay_link}

Before your first club ride, take a couple of minutes to read Bike Safety 101:
{site}/bike-safety

Every ride is posted on Strava, so join the club there too:
https://www.strava.com/clubs/10197

Want to ride in club colors? Our kit is in the team store:
{site}/kit

See you on the road!
PaxVelo`

func openStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "paxvelo.json")}
	b, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &s.d); err != nil {
			return nil, fmt.Errorf("reading %s: %w", s.path, err)
		}
	case errors.Is(err, fs.ErrNotExist):
		s.d.Welcome = WelcomeEmail{Enabled: true, Subject: "Welcome to PaxVelo, {first_name}!", Body: defaultWelcomeBody}
	default:
		return nil, err
	}
	if s.d.Secret == "" {
		s.d.Secret = randHex(32)
	}
	if s.d.NextMember == 0 {
		s.d.NextMember = 1
	}
	if s.d.NextCampaign == 0 {
		s.d.NextCampaign = 1
	}
	if !s.d.BoardSeeded {
		s.d.Board, s.d.BoardSeeded = defaultBoard(), true
	}
	if !s.d.HomeSeeded {
		s.d.HomePhotos, s.d.HomeSeeded = defaultHomePhotos(), true
	}
	if !s.d.SeriesSeeded {
		s.d.Series, s.d.NextSeries, s.d.SeriesSeeded = defaultSeries(), 3, true
	}
	s.tidyGroups()
	s.upgradeIssues()
	return s, s.save()
}

// save writes the whole file atomically: write a temp file, flush it to disk,
// then rename it over the old one. A crash mid-write can't corrupt the data.
// Callers must hold s.mu.
func (s *Store) save() error {
	s.version++
	b, err := json.MarshalIndent(&s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) saveOrLog() {
	if err := s.save(); err != nil {
		log.Printf("saving data: %v", err)
	}
}

func (s *Store) Secret() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Secret
}

// Snapshot is the downloadable backup: all the club data, but not the signing
// key (a restored site makes a new one) or any unused password links.
func (s *Store) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.d
	d.Secret = ""
	d.Admins = make([]*AdminUser, len(s.d.Admins))
	for i, u := range s.d.Admins {
		c := *u
		c.ResetHash, c.ResetExpires = "", nil
		d.Admins[i] = &c
	}
	return json.MarshalIndent(&d, "", "  ")
}

// ---- Members ----

type JoinResult int

const (
	Joined JoinResult = iota
	Rejoined
	AlreadyMember
	OptedOut // on the list but unsubscribed; the public form leaves them that way
)

func (s *Store) byEmail(email string) *Member {
	for _, m := range s.d.Members {
		if m.Email == email {
			return m
		}
	}
	return nil
}

func (s *Store) byID(id int) *Member {
	for _, m := range s.d.Members {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (s *Store) newMember(name, email, plan string) *Member {
	m := &Member{ID: s.d.NextMember, Name: name, Email: email, Joined: time.Now().UTC(), Token: randHex(16), Plan: plan}
	s.d.NextMember++
	s.d.Members = append(s.d.Members, m)
	return m
}

// Join adds a member for an admin. Someone who unsubscribed earlier is
// re-activated rather than duplicated, and someone already on the list gets
// the details given, and (if they haven't paid for this year yet) the chosen plan.
func (s *Store) Join(d memberDetails, year int) (Member, JoinResult, error) {
	return s.join(d, year, true)
}

// JoinPublic adds a member from the public Join form, where anyone can type
// anyone's email. For someone already on the list it only fills in details
// still missing, so nobody can overwrite another member's phone or emergency
// contact, change their plan, or sign them up again after they unsubscribed
// (that returns OptedOut and changes nothing).
func (s *Store) JoinPublic(d memberDetails, year int) (Member, JoinResult, error) {
	return s.join(d, year, false)
}

func (s *Store) join(d memberDetails, year int, admin bool) (Member, JoinResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.byEmail(d.Email); m != nil {
		res := AlreadyMember
		switch {
		case admin:
			if !m.Active() {
				m.Unsubscribed, res = nil, Rejoined
			}
			m.update(d)
			if !m.PaidFor(year) {
				m.Plan = d.Plan
			}
		case !m.Active():
			return *m, OptedOut, nil
		default:
			m.fill(d)
		}
		return *m, res, s.save()
	}
	m := s.newMember(d.Name, d.Email, d.Plan)
	m.update(d)
	return *m, Joined, s.save()
}

// SetPaid marks a member's dues as paid (or not) for a year.
func (s *Store) SetPaid(id, year int, paid bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(id)
	if m == nil {
		return nil
	}
	years := m.PaidYears[:0:0]
	for _, y := range m.PaidYears {
		if y != year {
			years = append(years, y)
		}
	}
	if paid {
		years = append(years, year)
	}
	m.PaidYears = years
	return s.save()
}

// Import adds members in bulk, skipping emails already on the list.
func (s *Store) Import(list []Member) (added, existing int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, in := range list {
		if s.byEmail(in.Email) != nil {
			existing++
			continue
		}
		m := s.newMember(in.Name, in.Email, cleanPlan(in.Plan))
		m.PaidYears = in.PaidYears
		m.Phone, m.EmergencyName, m.EmergencyPhone = in.Phone, in.EmergencyName, in.EmergencyPhone
		m.Unsubscribed = in.Unsubscribed
		if !in.Joined.IsZero() {
			m.Joined = in.Joined
		}
		added++
	}
	if added > 0 {
		err = s.save()
	}
	return added, existing, err
}

func (s *Store) DeleteMember(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.d.Members {
		if m.ID == id {
			s.d.Members = append(s.d.Members[:i], s.d.Members[i+1:]...)
			return s.save()
		}
	}
	return nil
}

func (s *Store) MemberByToken(token string) (Member, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		return Member{}, false
	}
	for _, m := range s.d.Members {
		if m.Token == token {
			return *m, true
		}
	}
	return Member{}, false
}

func (s *Store) Unsubscribe(token string) (Member, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		return Member{}, false, nil
	}
	for _, m := range s.d.Members {
		if m.Token == token {
			if m.Active() {
				now := time.Now().UTC()
				m.Unsubscribed = &now
				return *m, true, s.save()
			}
			return *m, true, nil
		}
	}
	return Member{}, false, nil
}

// Members returns a copy of every member, newest first.
func (s *Store) Members() []Member {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Member, 0, len(s.d.Members))
	for i := len(s.d.Members) - 1; i >= 0; i-- {
		out = append(out, *s.d.Members[i])
	}
	return out
}

func (s *Store) Counts() (active, unsubscribed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.d.Members {
		if m.Active() {
			active++
		} else {
			unsubscribed++
		}
	}
	return
}

// ---- Welcome email and draft ----

func (s *Store) Welcome() WelcomeEmail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Welcome
}

func (s *Store) SetWelcome(w WelcomeEmail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Welcome = w
	return s.save()
}

func (s *Store) Draft() EmailDraft {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Draft
}

func (s *Store) SetDraft(d EmailDraft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Draft = d
	return s.save()
}

// ---- Campaigns ----

func (s *Store) campaign(id int) *Campaign {
	for _, c := range s.d.Campaigns {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// StartCampaign queues an email to members: audience is "all", "paid", or
// "unpaid" (for the given dues year). The background sender picks it up from
// there, and resumes automatically after a restart.
func (s *Store) StartCampaign(subject, body, audience string, year int) (Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &Campaign{ID: s.d.NextCampaign, Subject: subject, Body: body, Created: time.Now().UTC(), Status: "sending", Audience: audienceLabel(audience, year)}
	for _, m := range s.d.Members {
		if m.Active() && inAudience(*m, audience, year) {
			c.Pending = append(c.Pending, m.ID)
		}
	}
	c.Total = len(c.Pending)
	if c.Total == 0 {
		return Campaign{}, errors.New("Nobody matches that group yet, so nothing was sent.")
	}
	s.d.NextCampaign++
	s.d.Campaigns = append(s.d.Campaigns, c)
	s.d.Draft = EmailDraft{}
	return *c, s.save()
}

func (s *Store) finish(c *Campaign, status string) {
	now := time.Now().UTC()
	c.Status = status
	c.Finished = &now
	c.Pending = nil
}

// NextSend returns the next email waiting to go out, if there is one.
func (s *Store) NextSend() (Campaign, Member, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.d.Campaigns {
		if c.Status != "sending" {
			continue
		}
		for len(c.Pending) > 0 {
			if m := s.byID(c.Pending[0]); m != nil && m.Active() {
				out := *c
				out.Pending = nil
				return out, *m, true
			}
			// Member was deleted or unsubscribed after the send started.
			c.Pending = c.Pending[1:]
			c.Total--
		}
		s.finish(c, "sent")
		s.saveOrLog()
	}
	return Campaign{}, Member{}, false
}

func (s *Store) RecordSend(campaignID, memberID int, sendErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.campaign(campaignID)
	if c == nil {
		return
	}
	if len(c.Pending) > 0 && c.Pending[0] == memberID {
		c.Pending = c.Pending[1:]
	}
	if sendErr != nil {
		c.Failed++
		c.LastError = sendErr.Error()
	} else {
		c.Sent++
	}
	if c.Status == "sending" && len(c.Pending) == 0 {
		s.finish(c, "sent")
	}
	s.saveOrLog()
}

// PauseCampaign stops a send that keeps failing (usually a wrong email
// password) and puts the failed members back in the queue for a retry.
func (s *Store) PauseCampaign(id int, reason string, retry []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.campaign(id)
	if c == nil {
		return
	}
	c.Status = "paused"
	c.LastError = reason
	c.Finished = nil
	c.Pending = append(append([]int{}, retry...), c.Pending...)
	c.Failed -= len(retry)
	if c.Failed < 0 {
		c.Failed = 0
	}
	s.saveOrLog()
}

func (s *Store) ResumeCampaign(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.campaign(id); c != nil && c.Status == "paused" {
		c.Status = "sending"
		c.LastError = ""
		return s.save()
	}
	return nil
}

func (s *Store) StopCampaign(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.campaign(id); c != nil && (c.Status == "sending" || c.Status == "paused") {
		s.finish(c, "stopped")
		return s.save()
	}
	return nil
}

// Campaigns returns every campaign, newest first.
func (s *Store) Campaigns() []Campaign {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Campaign, 0, len(s.d.Campaigns))
	for i := len(s.d.Campaigns) - 1; i >= 0; i-- {
		c := *s.d.Campaigns[i]
		c.Pending = nil
		out = append(out, c)
	}
	return out
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func inAudience(m Member, audience string, year int) bool {
	switch audience {
	case "paid":
		return m.PaidFor(year)
	case "unpaid":
		return !m.PaidFor(year)
	}
	return true
}

func audienceLabel(audience string, year int) string {
	switch audience {
	case "paid":
		return fmt.Sprintf("Paid for %d", year)
	case "unpaid":
		return fmt.Sprintf("Not paid for %d", year)
	}
	return "Everyone"
}

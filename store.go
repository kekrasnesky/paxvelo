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
	// When an admin marked each year paid, for the member's page. Years
	// imported or marked before this was kept have no date.
	PaidOn map[int]time.Time `json:"paid_on,omitempty"`
	Phone  string            `json:"phone,omitempty"`
	// Who to call if something happens on a ride.
	EmergencyName  string `json:"emergency_name,omitempty"`
	EmergencyPhone string `json:"emergency_phone,omitempty"`
	// From the join form: their experience (riderLevels), which picks the
	// welcome email's ride advice. Rides are what members who joined before
	// the form was simplified picked (clubRides); nothing sets them now.
	Level string   `json:"level,omitempty"`
	Rides []string `json:"rides,omitempty"`
	// WelcomePending is set for someone who joined on the website, until an
	// admin marks their dues paid, which sends their welcome email. Members
	// added by an admin or imported never get one.
	WelcomePending bool `json:"welcome_pending,omitempty"`
	// MovedSent is when they were sent the site-move email (moved.go).
	MovedSent *time.Time `json:"moved_sent,omitempty"`
	// Unconfirmed is set for someone who joined on the website, or came back
	// after unsubscribing, until they press Confirm on their membership page
	// from the link we email them. Club emails skip them until then, so a
	// mistyped or borrowed address gets nothing more. Paying doesn't wait.
	Unconfirmed bool `json:"unconfirmed,omitempty"`
	// Kinds of club email they turned off on their page (emailtopics.go).
	// Unsubscribed stops everything.
	NoNewsletter bool `json:"no_newsletter,omitempty"`
	NoEvents     bool `json:"no_events,omitempty"`
	// When they ticked "I agree to the club terms" on the join form.
	TermsAgreed *time.Time `json:"terms_agreed,omitempty"`
	// Other adults who ride on their Family plan, each signing their own
	// waiver (household.go).
	Household []HouseholdAdult `json:"household,omitempty"`
	// PendingJoin is the join form filled in for their address, waiting for
	// them to confirm it was them (renewal.go).
	PendingJoin *PendingJoin `json:"pending_join,omitempty"`
}

// memberDetails is what someone fills in to join.
type memberDetails struct {
	Name, Email, Plan, Phone, EmergencyName, EmergencyPhone, Level string
	Terms, Waiver                                                  bool   // ticked the club terms and waiver boxes
	Signature                                                      string // the name typed to sign the waiver
	Drawing                                                        []byte // the signature drawn on the waiver, a PNG
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
	m.agree(d)
}

// agree records when someone first agreed to the club terms. (Their waiver,
// signed every year, is kept apart: see waivers.go.)
func (m *Member) agree(d memberDetails) {
	if d.Terms && m.TermsAgreed == nil {
		now := time.Now().UTC()
		m.TermsAgreed = &now
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
	m.agree(d)
}

func (m Member) Active() bool { return m.Unsubscribed == nil }

// Emailable reports whether club emails go to them: not unsubscribed, and
// their email confirmed if they joined on the website.
func (m Member) Emailable() bool { return m.Active() && !m.Unconfirmed }

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
	Issue     int        `json:"issue,omitempty"`   // the newsletter issue it is, when it's one
	Topic     string     `json:"topic,omitempty"`   // newsletter, events, or "" for club news (emailtopics.go)
}

type EmailDraft struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type WelcomeEmail struct {
	Enabled bool   `json:"enabled"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// Tips is the admin's own {ride_tips} for each riderLevels answer.
	Tips map[string]string `json:"tips,omitempty"`
}

type storeData struct {
	Secret         string         `json:"secret"`
	NextMember     int            `json:"next_member_id"`
	NextCampaign   int            `json:"next_campaign_id"`
	Members        []*Member      `json:"members"`
	Campaigns      []*Campaign    `json:"campaigns"`
	Welcome        WelcomeEmail   `json:"welcome_email"`
	Moved          EmailDraft     `json:"moved_email,omitempty"`   // the site-move email (moved.go); blank = built in
	Confirm        EmailDraft     `json:"confirm_email,omitempty"` // the confirm-your-email email (automations.go); blank = built in
	ConfirmOff     bool           `json:"confirm_off,omitempty"`   // switched off: new joiners aren't asked to confirm
	NextAuto       int            `json:"next_automation_id,omitempty"`
	Automations    []*Automation  `json:"automations,omitempty"`        // admins' own automated emails (automations.go)
	RemovedAuto    *Automation    `json:"removed_automation,omitempty"` // the one removed last, for Undo
	RemovedBuiltin []string       `json:"removed_builtin,omitempty"`    // built-in automations the president deleted (automations.go)
	Draft          EmailDraft     `json:"draft"`
	NextIssue      int            `json:"next_issue_id,omitempty"`
	Issues         []*Issue       `json:"newsletter,omitempty"`
	NextMinutes    int            `json:"next_minutes_id,omitempty"`
	Minutes        []*Minutes     `json:"minutes,omitempty"`
	Instagram      []string       `json:"instagram,omitempty"` // posts picked for the home page
	InstagramLink  *InstagramLink `json:"instagram_link,omitempty"`
	InstaFeed      []InstaPost    `json:"instagram_feed,omitempty"` // the latest posts, from Instagram
	EmailsSent     map[string]int `json:"emails_sent,omitempty"`    // emails sent today, by day (UTC)
	NextAdmin      int            `json:"next_admin_id,omitempty"`
	Admins         []*AdminUser   `json:"admins,omitempty"`

	// Signed insurance waivers (waivers.go), and every version of the
	// waiver's text anyone signed, by version.
	NextWaiver  int               `json:"next_waiver_id,omitempty"`
	Waivers     []*Waiver         `json:"waivers,omitempty"`
	WaiverTexts map[string]string `json:"waiver_texts,omitempty"`
	// Ride waivers from first-time riders (ridewaivers.go).
	NextRideWaiver int           `json:"next_ride_waiver_id,omitempty"`
	RideWaivers    []*RideWaiver `json:"ride_waivers,omitempty"`
	// The home page video's clips (herovideo.go).
	HeroVideo HeroVideo `json:"hero_video,omitzero"`
	// Loaner bikes and the requests for them (loaners.go).
	Loaners LoanerFleet `json:"loaners,omitzero"`
	// Photos members sent in for the newsletter (sharephotos.go).
	NextSubmitted   int               `json:"next_submitted_id,omitempty"`
	SubmittedPhotos []*SubmittedPhoto `json:"submitted_photos,omitempty"`
	// The public photo gallery (gallery.go), album by album.
	ClubPIN     string          `json:"club_pin,omitempty"` // lets members post without review (clubpin.go)
	NextAlbum   int             `json:"next_album_id,omitempty"`
	Albums      []*Album        `json:"albums,omitempty"`
	NextGallery int             `json:"next_gallery_id,omitempty"`
	Gallery     []*GalleryPhoto `json:"gallery,omitempty"`

	// Ride calendar
	NextRide        int                  `json:"next_ride_id,omitempty"`
	NextSeries      int                  `json:"next_series_id,omitempty"`
	Series          []*RideSeries        `json:"weekly_rides,omitempty"`
	CalRides        []*CalRide           `json:"rides,omitempty"`
	Routes          map[string]*Route    `json:"strava_routes,omitempty"` // by Strava route ID
	RoutesChecked   *time.Time           `json:"strava_routes_checked,omitempty"`
	StravaClub      *StravaClub          `json:"strava_club,omitempty"`
	History         map[string]*PastRide `json:"ride_history,omitempty"`          // the club's Strava events, by event ID
	HistoryImported string               `json:"ride_history_imported,omitempty"` // the last past day put on the calendar

	// Pages and board members edited in the admin
	PageEdits   map[string]PageEdit `json:"page_edits,omitempty"`
	StravaLink  *StravaLink         `json:"strava_link,omitempty"`
	Board       []BoardMember       `json:"board,omitempty"`
	BoardSeeded bool                `json:"board_seeded,omitempty"`
	HomePhotos  []HomePhoto         `json:"home_photos,omitempty"`
	HomeSeeded  bool                `json:"home_photos_seeded,omitempty"`
	Hero        HeroPhoto           `json:"hero"` // the photo at the top of the home page (hero.go)

	// The treasurer's books: expenses, income, budgets and saved reports (treasurer.go).
	Treasury Treasury `json:"treasury"`
	// The club kit store and its yearly bulk orders (kit.go).
	Kit KitShop `json:"kit"`
	// BoardLinked: the Board page's people were tied to member records with
	// the same name once, when that link was added (boardroles.go).
	BoardLinked bool `json:"board_linked,omitempty"`
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

Welcome to PaxVelo! Your {year} dues are in, and we're glad you're here.

{ride_tips}

Before your first club ride, take a couple of minutes to read Bike Safety 101:
{site}/bike-safety

Every ride is posted on Strava, so join the club there too:
{strava}

Want to ride in club colors? Our kit is in the team store:
{site}/kit

See you on the road!
PaxVelo`

// oldWelcomeBody is the default from when the welcome email went out at
// signup, before payment. A welcome email nobody changed from it becomes the
// new default, since asking a paid member to pay makes no sense.
const oldWelcomeBody = `Hi {first_name},

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
	// A welcome email nobody changed from an earlier default gets today's:
	// the one from before payment, or the one with the Strava address typed in.
	if b := s.d.Welcome.Body; b == oldWelcomeBody || b == strings.ReplaceAll(defaultWelcomeBody, "{strava}", "https://www.strava.com/clubs/10197") {
		s.d.Welcome.Body = defaultWelcomeBody
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
	s.tidyGroups()
	s.upgradeIssues()
	s.albumize()
	s.relinkWaivers()
	s.seedTreasury()
	s.seedFee()
	s.seedKit()
	s.seedLoaners()
	s.linkBoard()
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
	OptedOut // on the list but unsubscribed; the public form leaves club emails off
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
	s.relinkWaivers()
	return m
}

// Join adds a member for an admin. Someone who unsubscribed earlier is
// re-activated rather than duplicated, and someone already on the list gets
// the details given, and (if they haven't paid for this year yet) the chosen plan.
func (s *Store) Join(d memberDetails, year int) (Member, JoinResult, error) {
	return s.join(d, year, true)
}

// JoinPublic adds a member from the public Join form, where anyone can type
// anyone's email. For someone already on the list it changes nothing (what
// they typed waits for the member to confirm it: renewal.go), except that
// someone who unsubscribed is marked unconfirmed, so confirming turns club
// emails back on (that returns OptedOut, and the emails stay off till then).
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
			m.Unconfirmed = false // an admin vouches for the address
			m.update(d)
			if !m.Paid() {
				m.Plan = d.Plan
			}
		case !m.Active():
			// Back to renew: club emails stay off until they confirm the
			// address is theirs, since anyone could type it. What they typed
			// waits for that too (SetPendingJoin).
			m.Unconfirmed = true
			res = OptedOut
		}
		return *m, res, s.save()
	}
	m := s.newMember(d.Name, d.Email, d.Plan)
	m.update(d)
	m.WelcomePending, m.Unconfirmed = !admin, !admin && !s.d.ConfirmOff
	return *m, Joined, s.save()
}

// ConfirmEmail is the member pressing Confirm on their page, from the link we
// emailed them. It confirms the address, which turns club emails on (back on,
// for someone who came back after unsubscribing), and takes the renewal they
// filled in on the join form, if one is waiting: its details fill in what's
// missing, and the caller signs its waivers. The link proves the inbox is
// theirs.
func (s *Store) ConfirmEmail(token string) (Member, *PendingJoin, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byToken(token)
	if m == nil {
		return Member{}, nil, false, nil
	}
	if !m.Unconfirmed && m.PendingJoin == nil {
		return *m, nil, true, nil
	}
	if m.Unconfirmed {
		m.Unconfirmed, m.Unsubscribed = false, nil
	}
	p := m.PendingJoin
	m.PendingJoin = nil
	if !p.fresh() {
		p = nil // too old to count; they can fill in the form again
	}
	if p != nil {
		m.fill(p.details())
	}
	return *m, p, true, s.save()
}

// SetPaid marks a member's dues as paid (or not) for a year.
func (s *Store) SetPaid(id, year int, paid bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(id)
	if m == nil {
		return nil
	}
	was := m.PaidFor(year)
	years := m.PaidYears[:0:0]
	for _, y := range m.PaidYears {
		if y != year {
			years = append(years, y)
		}
	}
	switch {
	case paid && !was:
		if m.PaidOn == nil {
			m.PaidOn = map[int]time.Time{}
		}
		m.PaidOn[year] = time.Now().UTC()
	case !paid:
		delete(m.PaidOn, year)
	}
	if paid {
		years = append(years, year)
	}
	m.PaidYears = years
	return s.save()
}

// TakeWelcome reports whether a member's welcome email is still to be sent,
// and if so marks it as sent, so it goes out once however often they're
// marked paid.
// It waits until they're both paid for the year and have confirmed their
// email, in either order.
func (s *Store) TakeWelcome(id, year int) (Member, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(id)
	if m == nil {
		return Member{}, false, nil
	}
	if !m.WelcomePending || m.Unconfirmed || !m.Paid() {
		return *m, false, nil
	}
	m.WelcomePending = false
	return *m, true, s.save()
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
			s.linkRideWaivers()
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
	return s.startCampaign(0, "", subject, body, audience, year)
}

// StartCampaignAbout is StartCampaign for an email on a topic members can
// turn off (emailtopics.go); those who did are left out.
func (s *Store) StartCampaignAbout(topic, subject, body, audience string, year int) (Campaign, error) {
	return s.startCampaign(0, cleanTopic(topic), subject, body, audience, year)
}

// StartIssueCampaign queues a newsletter issue's email, like StartCampaign,
// but leaves the draft on Email members alone.
func (s *Store) StartIssueCampaign(issue int, subject, body, audience string, year int) (Campaign, error) {
	return s.startCampaign(issue, topicNewsletter, subject, body, audience, year)
}

func (s *Store) startCampaign(issue int, topic, subject, body, audience string, year int) (Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &Campaign{ID: s.d.NextCampaign, Subject: subject, Body: body, Created: time.Now().UTC(), Status: "sending", Audience: audienceLabel(audience, year), Issue: issue, Topic: topic}
	signed := s.signedWaivers(year)
	for _, m := range s.d.Members {
		if m.Emailable() && m.Wants(topic) && inAudience(*m, audience, year, signed) {
			c.Pending = append(c.Pending, m.ID)
		}
	}
	c.Total = len(c.Pending)
	if c.Total == 0 {
		return Campaign{}, errors.New("Nobody matches that group yet, so nothing was sent.")
	}
	s.d.NextCampaign++
	s.d.Campaigns = append(s.d.Campaigns, c)
	if issue == 0 {
		s.d.Draft = EmailDraft{}
	}
	return *c, s.save()
}

// RecordSent adds a line to Email → Sent emails for an email that went out by
// itself, not through the background sender (the site-move email): already
// "sent", nothing pending, so it is never sent again.
func (s *Store) RecordSent(subject, body, audience string, n int) error {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.d.Campaigns = append(s.d.Campaigns, &Campaign{ID: s.d.NextCampaign, Subject: subject, Body: body, Created: now, Finished: &now,
		Status: "sent", Audience: audience, Total: n, Sent: n})
	s.d.NextCampaign++
	return s.save()
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
			if m := s.byID(c.Pending[0]); m != nil && m.Emailable() && m.Wants(c.Topic) {
				out := *c
				out.Pending = nil
				return out, *m, true
			}
			// Member was deleted, unsubscribed, or turned this kind of
			// email off after the send started.
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

// inAudience reports whether a member is in the group an email goes to:
// "all", "paid" or "unpaid" for the year, "unsigned" (no waiver of their own
// for the year; signed holds who has one), or "level-<riderLevels value>".
func inAudience(m Member, audience string, year int, signed map[int]bool) bool {
	switch audience {
	case "paid":
		return m.Paid()
	case "unpaid":
		return !m.Paid()
	case "unsigned":
		return !signed[m.ID]
	}
	if lv, ok := strings.CutPrefix(audience, "level-"); ok {
		return m.Level == lv
	}
	return true
}

func audienceLabel(audience string, year int) string {
	switch audience {
	case "paid":
		return "Dues paid up"
	case "unpaid":
		return "Dues not paid up"
	case "unsigned":
		return fmt.Sprintf("Haven’t signed the %d waiver", year)
	}
	if lv, ok := strings.CutPrefix(audience, "level-"); ok {
		for _, c := range riderLevels {
			if c.Value == lv {
				return "Riding: " + c.Short
			}
		}
	}
	return "Everyone"
}

// SignedWaivers is which members signed their own waiver for a year.
func (s *Store) SignedWaivers(year int) map[int]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signedWaivers(year)
}

// signedWaivers is who signed their own waiver for a year. Callers hold s.mu.
func (s *Store) signedWaivers(year int) map[int]bool {
	signed := map[int]bool{}
	for _, w := range s.d.Waivers {
		if w.Year == year && !w.Minor && !w.Household {
			signed[w.MemberID] = true
		}
	}
	return signed
}

// emailAudiences are the groups an email can go to, each with how many
// members who get club emails are in it now.
// emailAudiences are the groups an email can go to, each with how many it
// reaches. Events is the count for an email about rides and events, which
// skips members who turned those off; topic is the email's own (newsletter
// for an issue).
func (s *Store) emailAudiences(year int, topic string) []audienceOption {
	s.mu.Lock()
	defer s.mu.Unlock()
	signed := s.signedWaivers(year)
	values := []string{"all", "unpaid", "paid", "unsigned"}
	for _, c := range riderLevels {
		values = append(values, "level-"+c.Value)
	}
	var out []audienceOption
	for _, v := range values {
		n, events := 0, 0
		for _, m := range s.d.Members {
			if m.Emailable() && inAudience(*m, v, year, signed) {
				if m.Wants(topic) {
					n++
				}
				if m.Wants(topicEvents) {
					events++
				}
			}
		}
		label := audienceLabel(v, year)
		out = append(out, audienceOption{v, fmt.Sprintf("%s (%d)", label, n), fmt.Sprintf("%s (%d)", label, events)})
	}
	return out
}

type audienceOption struct{ Value, Label, Events string }

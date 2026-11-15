package main

import (
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// RSVPs (Email → RSVPs): a meeting, party or anything else the club invites
// members to. An admin makes the event, writes the invitation (each member's
// email gets {rsvp_<id>}, a button to their own page), and every member
// answers on /rsvp/<id>?t=<their token>: coming or not, who they're bringing
// and, if the event asks, what they're bringing. Admins see the answers as
// they come in, with the head count. The page is unlisted (noindex, no menu)
// and shows the place only from a member's own link, since it may be a home.
//
// The same module runs every future invitation: a new event is a new item,
// not new code.

type RSVPBook struct {
	Next    int          `json:"next,omitempty"`
	Events  []*RSVPEvent `json:"events,omitempty"`
	Removed *RSVPEvent   `json:"removed,omitempty"` // the last event removed, for Undo
	Dropped *RSVPDropped `json:"dropped,omitempty"` // the last answers removed, for Undo
}

// RSVPDropped are answers an admin took off an event, kept for Undo.
type RSVPDropped struct {
	Event   int         `json:"event"`
	Replies []RSVPReply `json:"replies"`
}

type RSVPEvent struct {
	ID          int         `json:"id"`
	Title       string      `json:"title"`             // "December Meeting"
	Date        string      `json:"date,omitempty"`    // 2006-01-02
	Time        string      `json:"time,omitempty"`    // 15:04
	Place       string      `json:"place,omitempty"`   // shown only from a member's own link
	Details     string      `json:"details,omitempty"` // a few lines: parking, what to expect
	AskBringing bool        `json:"ask_bringing,omitempty"`
	HideGuests  bool        `json:"hide_guests,omitempty"` // members don't see who's coming (they do by default)
	Closed      bool        `json:"closed,omitempty"`      // no more answers
	Closes      string      `json:"closes,omitempty"`      // last day to answer, 2006-01-02; empty = until the event
	Created     time.Time   `json:"created"`
	Replies     []RSVPReply `json:"replies,omitempty"`
}

// RSVPReply is one member's answer. They can change it until it closes.
type RSVPReply struct {
	Member   int       `json:"member"`
	Name     string    `json:"name"`  // as they typed it
	Email    string    `json:"email"` // their email when they answered
	Answer   string    `json:"answer"`
	Guests   int       `json:"guests,omitempty"` // people coming with them
	Who      string    `json:"who,omitempty"`    // who they're bringing
	Bringing string    `json:"bringing,omitempty"`
	Note     string    `json:"note,omitempty"`
	Updated  time.Time `json:"updated"`
}

const (
	rsvpYes   = "yes"
	rsvpMaybe = "maybe"
	rsvpNo    = "no"
)

// rsvpAnswers are the choices, in the order the page shows them.
var rsvpAnswers = []choice{{Value: rsvpYes, Label: "I’ll be there"}, {Value: rsvpMaybe, Label: "Maybe"}, {Value: rsvpNo, Label: "Can’t make it"}}

// maxRSVPGuests keeps a typo (20 guests) out of the head count.
const maxRSVPGuests = 9

// Coming counts them and their guests, for a yes.
func (r RSVPReply) Coming() int {
	if r.Answer != rsvpYes {
		return 0
	}
	return 1 + r.Guests
}

// AnswerLabel is the answer as the admin page says it.
func (r RSVPReply) AnswerLabel() string {
	for _, c := range rsvpAnswers {
		if c.Value == r.Answer {
			return c.Label
		}
	}
	return r.Answer
}

// When is the day and time, like "Saturday, December 12 at 6:00 pm".
func (e RSVPEvent) When() string {
	d, err := time.Parse("2006-01-02", e.Date)
	if err != nil {
		return ""
	}
	s := d.Format("Monday, January 2")
	if t, err := time.Parse("15:04", e.Time); err == nil {
		s += " at " + strings.ToLower(t.Format("3:04 PM"))
	}
	return s
}

// Day is the event's date in the club's time zone (zero if it has none).
func (e RSVPEvent) Day() time.Time {
	d, err := time.ParseInLocation("2006-01-02", e.Date, clubTZ)
	if err != nil {
		return time.Time{}
	}
	return d
}

// IsOpen says whether members can answer today: not closed by hand, not past
// its last day, and not after the event.
func (e RSVPEvent) IsOpen(now time.Time) bool {
	if e.Closed {
		return false
	}
	today := now.In(clubTZ).Format("2006-01-02")
	if e.Closes != "" && today > e.Closes {
		return false
	}
	return e.Date == "" || today <= e.Date
}

// Past says the event has happened.
func (e RSVPEvent) Past(now time.Time) bool {
	return e.Date != "" && now.In(clubTZ).Format("2006-01-02") > e.Date
}

// ClosesLabel is the last day to answer, like "Thursday, December 10".
func (e RSVPEvent) ClosesLabel() string {
	t, err := time.Parse("2006-01-02", e.Closes)
	if err != nil {
		return ""
	}
	return t.Format("Monday, January 2")
}

// Piece is the {placeholder} that becomes each member's own link in an email.
func (e RSVPEvent) Piece() string { return "rsvp_" + strconv.Itoa(e.ID) }

// ReplyFor is a member's answer, if they've given one.
func (e RSVPEvent) ReplyFor(member int) (RSVPReply, bool) {
	for _, r := range e.Replies {
		if r.Member == member {
			return r, true
		}
	}
	return RSVPReply{}, false
}

// RSVPCount is the event's answers added up.
type RSVPCount struct {
	Yes, Maybe, No int
	Guests         int // with the yeses
	Coming         int // yeses and their guests: the head count
}

func (e RSVPEvent) Count() RSVPCount {
	var c RSVPCount
	for _, r := range e.Replies {
		switch r.Answer {
		case rsvpYes:
			c.Yes++
			c.Guests += r.Guests
		case rsvpMaybe:
			c.Maybe++
		case rsvpNo:
			c.No++
		}
		c.Coming += r.Coming()
	}
	return c
}

// ByAnswer is the replies grouped: yes, then maybe, then no, each by name.
func (e RSVPEvent) ByAnswer() []RSVPReply {
	out := slices.Clone(e.Replies)
	rank := map[string]int{rsvpYes: 0, rsvpMaybe: 1, rsvpNo: 2}
	slices.SortStableFunc(out, func(a, b RSVPReply) int {
		if d := rank[a.Answer] - rank[b.Answer]; d != 0 {
			return d
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
}

// Bringing is who said they're bringing something and what, among those
// coming or maybe coming.
func (e RSVPEvent) Bringing() []RSVPReply {
	var out []RSVPReply
	for _, r := range e.ByAnswer() {
		if r.Bringing != "" && r.Answer != rsvpNo {
			out = append(out, r)
		}
	}
	return out
}

// MapAddress is the place as a maps app should look it up: from the first
// part that starts with a number ("Karl’s house, 12 Spoke Lane, Leonardtown"
// → "12 Spoke Lane, Leonardtown"), else all of it.
func (e RSVPEvent) MapAddress() string {
	parts := strings.Split(e.Place, ",")
	for i, p := range parts {
		if p = strings.TrimSpace(p); p != "" && p[0] >= '0' && p[0] <= '9' {
			return strings.TrimSpace(strings.Join(parts[i:], ","))
		}
	}
	return strings.TrimSpace(e.Place)
}

// mapsLink is directions to an address in one maps app: "apple", "google"
// or "waze". Each opens the app on a phone that has it, else the web.
func mapsLink(app, addr string) string {
	q := url.QueryEscape(addr)
	switch app {
	case "apple":
		return "https://maps.apple.com/?daddr=" + q
	case "waze":
		return "https://waze.com/ul?q=" + q + "&navigate=yes"
	}
	return "https://www.google.com/maps/dir/?api=1&destination=" + q
}

// Going is who members see on the page: the yeses, then the maybes, by name.
func (e RSVPEvent) Going() []RSVPReply {
	var out []RSVPReply
	for _, r := range e.ByAnswer() {
		if r.Answer != rsvpNo {
			out = append(out, r)
		}
	}
	return out
}

// rsvpLength is how long an event goes in someone's calendar (like a club
// event on the ride calendar, ridelength.go).
const rsvpLength = 2 * time.Hour

// CalendarTimes are the event's start and end for a calendar app: timed
// with a start time, else the whole day. ok is false without a date.
func (e RSVPEvent) CalendarTimes() (start, end time.Time, allDay, ok bool) {
	day := e.Day()
	if day.IsZero() {
		return
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", e.Date+" "+e.Time, clubTZ); err == nil {
		return t, t.Add(rsvpLength), false, true
	}
	return day, day.AddDate(0, 0, 1), true, true
}

// ---- Store ----

func cloneRSVP(e *RSVPEvent) RSVPEvent {
	c := *e
	c.Replies = slices.Clone(e.Replies)
	return c
}

// RSVPEvents are every event, newest date first (no date last).
func (s *Store) RSVPEvents() []RSVPEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RSVPEvent, 0, len(s.d.RSVP.Events))
	for _, e := range s.d.RSVP.Events {
		out = append(out, cloneRSVP(e))
	}
	slices.SortStableFunc(out, func(a, b RSVPEvent) int {
		if a.Date == b.Date {
			return b.ID - a.ID
		}
		if a.Date == "" || b.Date == "" {
			return len(b.Date) - len(a.Date)
		}
		return strings.Compare(b.Date, a.Date)
	})
	return out
}

// RSVPEvent returns one event.
func (s *Store) RSVPEvent(id int) (RSVPEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.d.RSVP.Events {
		if e.ID == id {
			return cloneRSVP(e), true
		}
	}
	return RSVPEvent{}, false
}

// RemovedRSVP is the last event removed, for Undo.
func (s *Store) RemovedRSVP() *RSVPEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.RSVP.Removed == nil {
		return nil
	}
	c := cloneRSVP(s.d.RSVP.Removed)
	return &c
}

// SaveRSVPEvent adds an event (ID 0) or changes one's details. Replies stay.
func (s *Store) SaveRSVPEvent(e RSVPEvent) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := &s.d.RSVP
	if e.ID == 0 {
		b.Next++
		e.ID, e.Created, e.Replies = b.Next, time.Now().UTC(), nil
		b.Events = append(b.Events, &e)
		return e.ID, s.save()
	}
	for _, old := range b.Events {
		if old.ID == e.ID {
			e.Created, e.Replies = old.Created, old.Replies
			*old = e
			return e.ID, s.save()
		}
	}
	return 0, errNotFound
}

// SetRSVPClosed stops (or starts again) taking answers.
func (s *Store) SetRSVPClosed(id int, closed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.d.RSVP.Events {
		if e.ID == id {
			e.Closed = closed
			return s.save()
		}
	}
	return errNotFound
}

// RemoveRSVPEvent takes an event and its answers out, keeping it for Undo.
func (s *Store) RemoveRSVPEvent(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := &s.d.RSVP
	i := slices.IndexFunc(b.Events, func(e *RSVPEvent) bool { return e.ID == id })
	if i < 0 {
		return errNotFound
	}
	b.Removed = b.Events[i]
	b.Events = slices.Delete(b.Events, i, i+1)
	return s.save()
}

// RestoreRSVPEvent puts the last removed event back, answers and all.
func (s *Store) RestoreRSVPEvent() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := &s.d.RSVP
	if b.Removed == nil {
		return 0, nil
	}
	e := b.Removed
	b.Events, b.Removed = append(b.Events, e), nil
	return e.ID, s.save()
}

var errRSVPClosed = errors.New("this RSVP is closed")

// AnswerRSVP saves a member's answer, replacing the one they gave before.
func (s *Store) AnswerRSVP(id int, m Member, r RSVPReply, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.d.RSVP.Events {
		if e.ID != id {
			continue
		}
		if !e.IsOpen(now) {
			return errRSVPClosed
		}
		r.Member, r.Email, r.Updated = m.ID, m.Email, now.UTC()
		if r.Name == "" {
			r.Name = m.Name
		}
		if r.Answer != rsvpYes {
			r.Guests, r.Who = 0, ""
		}
		if r.Answer == rsvpNo || !e.AskBringing {
			r.Bringing = ""
		}
		if i := slices.IndexFunc(e.Replies, func(x RSVPReply) bool { return x.Member == m.ID }); i >= 0 {
			e.Replies[i] = r
		} else {
			e.Replies = append(e.Replies, r)
		}
		return s.save()
	}
	return errNotFound
}

// DropRSVPReplies takes members' answers off an event (an admin tidying
// up), keeping them for Undo. It says how many went.
func (s *Store) DropRSVPReplies(id int, members []int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.d.RSVP.Events {
		if e.ID != id {
			continue
		}
		var gone []RSVPReply
		e.Replies = slices.DeleteFunc(e.Replies, func(r RSVPReply) bool {
			if slices.Contains(members, r.Member) {
				gone = append(gone, r)
				return true
			}
			return false
		})
		if len(gone) == 0 {
			return 0, nil
		}
		s.d.RSVP.Dropped = &RSVPDropped{Event: id, Replies: gone}
		return len(gone), s.save()
	}
	return 0, errNotFound
}

// DroppedRSVP is the last answers removed, if they were from this event.
func (s *Store) DroppedRSVP(id int) *RSVPDropped {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.d.RSVP.Dropped
	if d == nil || d.Event != id {
		return nil
	}
	return &RSVPDropped{Event: d.Event, Replies: slices.Clone(d.Replies)}
}

// RestoreRSVPReplies puts the last removed answers back (unless the member
// has answered again since).
func (s *Store) RestoreRSVPReplies(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.d.RSVP.Dropped
	if d == nil || d.Event != id {
		return nil
	}
	for _, e := range s.d.RSVP.Events {
		if e.ID != id {
			continue
		}
		for _, r := range d.Replies {
			if !slices.ContainsFunc(e.Replies, func(x RSVPReply) bool { return x.Member == r.Member }) {
				e.Replies = append(e.Replies, r)
			}
		}
		s.d.RSVP.Dropped = nil
		return s.save()
	}
	return errNotFound
}

package main

import (
	"strconv"
	"strings"
	"time"
)

// The RSVP reminder (Email → Automations, "RSVP reminder"): the day before
// an invitation's day, from 9 am club time, everyone who answered I'll be
// there or Maybe gets an email with their answer, the place, directions and
// their link to change it. Each answer is reminded once (RSVPReply.Reminded,
// marked before sending). Admins edit its words and switch it off like the
// welcome email; an invitation can skip it ("Remind them the day before").

const defaultRSVPReminderSubject = "Tomorrow: {event}"

const defaultRSVPReminderBody = `Hi {first_name},

A quick reminder: **{event}** is tomorrow, {when}.

{your_answer}

{place_line}

{directions}

Plans changed? [Change your answer]({rsvp_link}).

See you there!
PaxVelo`

var piecesRSVPReminder = append([]autoPiece{
	{"event", "Invitation name", "Like December Meeting"},
	{"when", "Day and time", "Like Friday, December 4 at 6:00 pm"},
	{"your_answer", "Their answer", "Like: You said you’ll be there, with 2 guests, bringing chili."},
	{"place_line", "Where", "A line with the place, when the invitation has one"},
	{"directions", "Directions button", "Opens directions in their phone’s maps app, when there’s a place"},
	{"rsvp_link", "Their RSVP link", "Their own page to change their answer"}}, piecesMember...)

// RSVPReminderText is the reminder as admins edit it.
func (s *Store) RSVPReminderText() EmailDraft {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.d.RSVP.Reminder
	if e.Subject == "" {
		e.Subject = defaultRSVPReminderSubject
	}
	if e.Body == "" {
		e.Body = defaultRSVPReminderBody
	}
	return e
}

// SetRSVPReminderText saves the reminder's words; the built-in words aren't
// stored, so later changes to them still reach it.
func (s *Store) SetRSVPReminderText(e EmailDraft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Body == defaultRSVPReminderBody {
		e.Body = ""
	}
	if e.Subject == defaultRSVPReminderSubject {
		e.Subject = ""
	}
	s.d.RSVP.Reminder = e
	return s.save()
}

func (s *Store) RSVPReminderOn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.d.RSVP.ReminderOff
}

func (s *Store) SetRSVPReminderOn(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.RSVP.ReminderOff = !on
	return s.save()
}

// MarkRSVPReminded records that a member's answer is being reminded, and
// reports false if it already was (or the answer is gone).
func (s *Store) MarkRSVPReminded(id, member int, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.d.RSVP.Events {
		if e.ID != id {
			continue
		}
		for i := range e.Replies {
			if r := &e.Replies[i]; r.Member == member && r.Reminded == nil {
				t := now.UTC()
				r.Reminded = &t
				s.save()
				return true
			}
		}
	}
	return false
}

// ReminderDay is the day the reminder goes (the day before), or zero when
// this invitation has none.
func (e RSVPEvent) ReminderDay() time.Time {
	if e.NoReminder || e.Day().IsZero() {
		return time.Time{}
	}
	return e.Day().AddDate(0, 0, -1)
}

// Reminded counts the answers already reminded.
func (e RSVPEvent) Reminded() int {
	n := 0
	for _, r := range e.Replies {
		if r.Reminded != nil {
			n++
		}
	}
	return n
}

// ReminderLabel is the reminder's day as the admin page shows it.
func (e RSVPEvent) ReminderLabel() string {
	if d := e.ReminderDay(); !d.IsZero() {
		return d.Format("Mon, Jan 2")
	}
	return ""
}

// answerLine says a member's answer back to them.
func answerLine(e RSVPEvent, r RSVPReply) string {
	var b strings.Builder
	if r.Answer == rsvpMaybe {
		b.WriteString("You said maybe")
	} else {
		b.WriteString("You said you’ll be there")
		if r.Guests > 0 {
			b.WriteString(", with " + plural(r.Guests, "1 guest", strconv.Itoa(r.Guests)+" guests"))
			if r.Who != "" {
				b.WriteString(" (" + r.Who + ")")
			}
		}
	}
	if r.Bringing != "" && e.AskBringing {
		b.WriteString(", bringing " + r.Bringing)
	}
	b.WriteString(".")
	return b.String()
}

// fillRSVPReminder fills in the reminder's pieces for one answer. The links
// become the member's own when the email is personalized (rsvpLinks).
func fillRSVPReminder(s string, e RSVPEvent, r RSVPReply) string {
	place, directions := "", ""
	if e.Place != "" {
		place = "**Where:** " + e.Place
		directions = "[[Directions]({" + e.Piece() + "_directions})]"
	}
	return strings.NewReplacer(
		"{event}", e.Title,
		"{when}", e.When(),
		"{your_answer}", answerLine(e, r),
		"{place_line}", place,
		"{directions}", directions,
		"{rsvp_link}", "{"+e.Piece()+"}",
	).Replace(s)
}

// sampleRSVP is the invitation and answer previews and tests are drawn
// for: the next open invitation, else a made-up one.
func (a *App) sampleRSVP() (RSVPEvent, RSVPReply) {
	now := time.Now()
	e := RSVPEvent{ID: 0, Title: "December Meeting", Date: now.AddDate(0, 0, 1).In(clubTZ).Format("2006-01-02"), Time: "18:00",
		Place: "12 Spoke Lane, Leonardtown", AskBringing: true}
	for _, ev := range a.store.RSVPEvents() {
		if ev.IsOpen(now) && ev.Date != "" {
			e = ev
		}
	}
	return e, RSVPReply{Answer: rsvpYes, Guests: 1, Who: "Alex", Bringing: "chili"}
}

// runRSVPReminders sends the reminders that are due: from 9 am on the day
// before, and until the day itself if the server missed it.
func (a *App) runRSVPReminders(now time.Time) {
	local := now.In(a.cfg.Location)
	if local.Hour() < 9 || !a.store.RSVPReminderOn() {
		return
	}
	today := clubDay(local, a.cfg.Location)
	text := a.store.RSVPReminderText()
	for _, e := range a.store.RSVPEvents() {
		day := e.ReminderDay()
		if day.IsZero() || today.Before(day) || !today.Before(e.Day()) {
			continue
		}
		for _, r := range e.Replies {
			if r.Reminded != nil || (r.Answer != rsvpYes && r.Answer != rsvpMaybe) {
				continue
			}
			m, ok := a.store.MemberByID(r.Member)
			if !ok || !m.Emailable() {
				continue
			}
			// Marked first, so it never goes twice, even if the server stops mid-send.
			if !a.store.MarkRSVPReminded(e.ID, m.ID, now) {
				continue
			}
			a.mailer.sendTo(mailRSVPReminder, 0, "", m, fillRSVPReminder(text.Subject, e, r), fillRSVPReminder(text.Body, e, r), nil)
		}
	}
}

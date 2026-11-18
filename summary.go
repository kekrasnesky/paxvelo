package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// The morning summary: instead of one email to the club inbox (RIDES_EMAIL)
// per notice, notices that can wait are kept and go together in one email
// from 9 am club time, run with the automations. Push alerts to admins'
// phones still go at once (adminpush.go).
//
// Straight away, as before:
//   - a ride cancelled, or its date, start time or start changed
//     (riders may need telling);
//   - anything about a ride today or tomorrow: a first-timer coming, a
//     ride waiting for approval, a loaner bike request, a new ride
//     (the summary would come too late);
//   - everything, while the summary is switched off on Email → Automations.
//
// In the summary: first-timers, rides waiting for approval, loaner bike
// requests and rides added to the calendar, for later days.

// ClubNotice is one notice waiting for the morning summary.
type ClubNotice struct {
	At      time.Time `json:"at"`
	Subject string    `json:"subject"`
	Body    string    `json:"body"`
}

// ClubSummary is the morning summary's state.
type ClubSummary struct {
	Off     bool         `json:"off,omitempty"`     // switched off: every notice goes at once
	Last    string       `json:"last,omitempty"`    // the club day the last one went
	Waiting []ClubNotice `json:"waiting,omitempty"` // notices since then
}

const (
	summaryKey = "morning-summary"
	maxWaiting = 300 // a runaway form can't grow the data file forever
)

func (s *Store) SummaryOn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.d.Summary.Off
}

func (s *Store) SetSummaryOn(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Summary.Off = !on
	return s.save()
}

// KeepNotice adds a notice to the next summary. It reports false when the
// summary is off or full, and the notice should go now.
func (s *Store) KeepNotice(n ClubNotice) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Summary.Off || len(s.d.Summary.Waiting) >= maxWaiting {
		return false
	}
	s.d.Summary.Waiting = append(s.d.Summary.Waiting, n)
	if err := s.save(); err != nil {
		log.Printf("saving a notice for the summary: %v", err)
		s.d.Summary.Waiting = s.d.Summary.Waiting[:len(s.d.Summary.Waiting)-1]
		return false
	}
	return true
}

// WaitingNotices are the notices for the next summary.
func (s *Store) WaitingNotices() []ClubNotice {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ClubNotice(nil), s.d.Summary.Waiting...)
}

// TakeNotices empties the waiting notices for day's summary. It's marked
// before sending, so a restart never sends it twice; nil = nothing to send
// or already sent today.
func (s *Store) TakeNotices(day string) []ClubNotice {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Summary.Last >= day || len(s.d.Summary.Waiting) == 0 {
		return nil
	}
	out := s.d.Summary.Waiting
	s.d.Summary.Last, s.d.Summary.Waiting = day, nil
	if err := s.save(); err != nil {
		log.Printf("saving the summary: %v", err)
	}
	return out
}

// clubNotice emails the club inbox now (urgent, or the summary is off), or
// keeps it for the morning summary.
func (a *App) clubNotice(subject, body string, urgent bool) {
	if urgent || !a.store.KeepNotice(ClubNotice{At: time.Now().UTC(), Subject: subject, Body: body}) {
		a.mailer.Notify(subject, body)
	}
}

// soon reports whether a ride's date is today or tomorrow (or past), when
// the morning summary would be too late.
func (a *App) soon(date string) bool {
	return date <= a.today().AddDate(0, 0, 1).Format(time.DateOnly)
}

// runMorningSummary sends what came in since the last one, from 9 am.
func (a *App) runMorningSummary(now time.Time) {
	local := now.In(a.cfg.Location)
	if local.Hour() < 9 {
		return
	}
	list := a.store.TakeNotices(local.Format(time.DateOnly))
	if len(list) == 0 {
		return
	}
	subject, body := a.summaryEmail(list)
	a.mailer.notify(mailSummary, subject, body)
}

// summaryEmail is the summary of the notices, oldest first.
func (a *App) summaryEmail(list []ClubNotice) (string, string) {
	subject := fmt.Sprintf("Since yesterday: %d %s", len(list), plural(len(list), "notice", "notices"))
	var b strings.Builder
	for i, n := range list {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		fmt.Fprintf(&b, "## %s\n\n%s", n.Subject, n.Body)
	}
	return subject, b.String()
}

// sampleSummary is the preview on Email → Automations: what's waiting, or
// two made-up notices.
func (a *App) sampleSummary() (string, string) {
	list := a.store.WaitingNotices()
	if len(list) == 0 {
		list = []ClubNotice{
			{Subject: "First ride: Pat Rider on Saturday Coffee Ride, Sat Nov 21", Body: "**Pat Rider** said they're coming to Saturday, November 21 at 8:00 AM from Leonardtown Square, their first club ride.\n\n- **Group:** B group"},
			{Subject: "Loaner bike request: Saturday Coffee Ride, Sat Nov 21", Body: "**Sam Rider** asked to borrow a club bike.\n\n- **Bike:** M · Trek Domane"},
		}
	}
	return a.summaryEmail(list)
}

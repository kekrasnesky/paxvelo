package main

import (
	"slices"
	"time"
)

// Dues run a year from the day a member joined, not January to December.
// PaidYears holds the membership years they paid for: year Y runs from their
// join day in Y to their join day in Y+1. So someone who joined on June 3
// and paid for 2026 is paid up until June 3, 2027 (DuesExpire), and shows as
// paid until then. Waivers still go by calendar year.

// DuesExpire is when their dues run out: their join day, in the year after
// the last year they paid for. Zero if they never paid.
func (m Member) DuesExpire() time.Time {
	last := m.LastPaid()
	if last == 0 {
		return time.Time{}
	}
	return m.anniversary(last + 1)
}

// clubTZ is the club's time zone (TIMEZONE), for which calendar day someone
// joined on.
var clubTZ = time.UTC

// anniversary is the start of their join day in a year, in the club's time
// zone (Feb 29 becomes Mar 1).
func (m Member) anniversary(year int) time.Time {
	j := m.Joined.In(clubTZ)
	if m.Joined.IsZero() {
		j = time.Date(year, time.January, 1, 0, 0, 0, 0, clubTZ)
	}
	return time.Date(year, j.Month(), j.Day(), 0, 0, 0, 0, clubTZ)
}

// Paid reports whether their dues are paid up today.
func (m Member) Paid() bool { return m.PaidAt(time.Now()) }

// PaidAt reports whether their dues were paid up at a time.
func (m Member) PaidAt(t time.Time) bool {
	e := m.DuesExpire()
	return !e.IsZero() && t.Before(e.AddDate(0, 0, 1)) // paid through that day
}

// nextPaidYear is the membership year a payment made at a time pays for.
// Paid up: the year after their last, so renewing early adds a full year.
// Otherwise the year whose end is at least six months away: a new member
// gets a year from the day they joined, and a lapsed member coming back gets
// six to eighteen months, ending on their usual join day.
func (m Member) nextPaidYear(t time.Time) int {
	if m.PaidAt(t) {
		return m.LastPaid() + 1
	}
	soon := t.AddDate(0, 6, 0)
	for y := t.Year() - 1; ; y++ {
		if m.anniversary(y + 1).After(soon) {
			return y
		}
	}
}

// Pay records a payment made now, unless their dues are already paid up and
// renew is false (Mark paid on someone already paid does nothing). It
// returns whether anything changed.
func (s *Store) Pay(id int, renew bool) (bool, error) {
	s.mu.Lock()
	m := s.byID(id)
	if m == nil || (!renew && m.Paid()) {
		s.mu.Unlock()
		return false, nil
	}
	year := m.nextPaidYear(time.Now())
	s.mu.Unlock()
	return true, s.SetPaid(id, year, true)
}

// Unpay undoes the latest payment: Undo after Mark paid.
func (s *Store) Unpay(id int) error {
	s.mu.Lock()
	m := s.byID(id)
	if m == nil || len(m.PaidYears) == 0 {
		s.mu.Unlock()
		return nil
	}
	year := slices.Max(m.PaidYears)
	s.mu.Unlock()
	return s.SetPaid(id, year, false)
}

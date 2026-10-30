package main

import (
	"strconv"
	"time"
)

// The email provider allows only so many emails a day (Brevo's free plan:
// 300), so the site counts each email it sends and the Email tab shows how
// many are left today. Emails sent some other way, like from Brevo's own
// website, aren't counted.

// emailDay is the day an email counts toward. Brevo doesn't say when its
// count starts over, so the site uses midnight UTC.
func emailDay(t time.Time) string { return t.UTC().Format("2006-01-02") }

// CountEmail records one email sent.
func (s *Store) CountEmail() {
	s.mu.Lock()
	defer s.mu.Unlock()
	today := emailDay(time.Now())
	if s.d.EmailsSent == nil {
		s.d.EmailsSent = map[string]int{}
	}
	s.d.EmailsSent[today]++
	// Only today matters; drop the days before it.
	for day := range s.d.EmailsSent {
		if day != today {
			delete(s.d.EmailsSent, day)
		}
	}
	s.saveOrLog()
}

// EmailsToday is how many emails the site has sent today.
func (s *Store) EmailsToday() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.EmailsSent[emailDay(time.Now())]
}

// emailAllowance is today's count against the daily limit, for the Email tab.
type emailAllowance struct {
	Sent, Limit int
}

func (e emailAllowance) Left() int { return max(e.Limit-e.Sent, 0) }

// Percent is how much of the limit is used, for the bar.
func (e emailAllowance) Percent() int {
	if e.Limit <= 0 {
		return 0
	}
	return min(e.Sent*100/e.Limit, 100)
}

// Resets is how long until the count starts over.
func (e emailAllowance) Resets() string {
	now := time.Now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	h := int(next.Sub(now).Round(time.Hour) / time.Hour)
	switch {
	case h <= 1:
		return "within the hour"
	default:
		return "in about " + strconv.Itoa(h) + " hours"
	}
}

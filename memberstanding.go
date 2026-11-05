package main

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A member's standing for a year. The list holds everyone who ever joined,
// signed up for club emails, or came over from Squarespace (whose export
// includes newsletter sign-ups and old customers); only those who paid for
// the year are members for it. Unsubscribing is separate: it's about
// emails, not membership.

const (
	standPaid   = "paid"   // paid for the year: a member
	standUnpaid = "unpaid" // paid an earlier year, or joined on the website and not marked paid yet
	standList   = "list"   // never paid: on the email list only
)

// Standing is which of those they are for a year.
func (m Member) Standing(year int) string {
	switch {
	case m.PaidFor(year):
		return standPaid
	case m.WelcomePending || len(m.PaidYears) > 0:
		return standUnpaid
	}
	return standList
}

// LastPaid is the latest year they paid for, or 0 if never.
func (m Member) LastPaid() int {
	if len(m.PaidYears) == 0 {
		return 0
	}
	return slices.Max(m.PaidYears)
}

// YearsPaidText is their paid years as the president's record form shows them.
func (m Member) YearsPaidText() string {
	years := slices.Clone(m.PaidYears)
	slices.Sort(years)
	parts := make([]string, len(years))
	for i, y := range years {
		parts[i] = strconv.Itoa(y)
	}
	return strings.Join(parts, ", ")
}

// parseYears reads "2024, 2025 2026" into sorted years from 2000 to next
// year; ok is false if anything else was typed.
func parseYears(s string, thisYear int) (years []int, ok bool) {
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		y, err := strconv.Atoi(f)
		if err != nil || y < 2000 || y > thisYear+1 {
			return nil, false
		}
		if !slices.Contains(years, y) {
			years = append(years, y)
		}
	}
	slices.Sort(years)
	return years, true
}

// SetRecord changes what the president can fix on a member's record: the
// years they paid, when they joined, and whether club emails go to them.
// It sends nothing: Mark paid is what sends a welcome email.
func (s *Store) SetRecord(id int, years []int, joined time.Time, emails bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(id)
	if m == nil {
		return nil
	}
	for y := range m.PaidOn {
		if !slices.Contains(years, y) {
			delete(m.PaidOn, y)
		}
	}
	m.PaidYears = years
	if !joined.IsZero() {
		m.Joined = joined
	}
	switch {
	case emails && m.Unsubscribed != nil:
		m.Unsubscribed = nil
	case !emails && m.Unsubscribed == nil:
		now := time.Now().UTC()
		m.Unsubscribed = &now
	}
	return s.save()
}

// DeleteMembers takes several people off the list at once.
func (s *Store) DeleteMembers(ids []int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.d.Members[:0]
	n := 0
	for _, m := range s.d.Members {
		if slices.Contains(ids, m.ID) {
			n++
			continue
		}
		kept = append(kept, m)
	}
	s.d.Members = kept
	if n == 0 {
		return 0, nil
	}
	return n, s.save()
}

// deleteMembers is "Delete them" on the ticked rows of the member list.
// Only the president can.
func (a *App) deleteMembers(w http.ResponseWriter, r *http.Request) {
	q := url.Values{}
	if ret, err := url.Parse(r.FormValue("return")); err == nil && ret.Path == "/admin/members" {
		q = ret.Query()
	}
	if !a.canManage(r) {
		q.Set("err", "president-only")
		back(w, r, "/admin/members?"+q.Encode())
		return
	}
	var ids []int
	for _, s := range r.Form["id"] {
		if id, err := strconv.Atoi(s); err == nil {
			ids = append(ids, id)
		}
	}
	n, err := a.store.DeleteMembers(ids)
	if err != nil {
		q.Set("err", "save")
	} else {
		q.Set("ok", "deleted-many")
		q.Set("n", strconv.Itoa(n))
	}
	back(w, r, "/admin/members?"+q.Encode())
}

// saveRecord is the president's Membership record box on a member's page.
func (a *App) saveRecord(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	page := "/admin/members/" + strconv.Itoa(id)
	if !a.canManage(r) {
		back(w, r, page+"?err=president-only")
		return
	}
	years, ok := parseYears(r.FormValue("years"), a.cfg.ThisYear())
	if !ok {
		back(w, r, page+"?err=bad-years")
		return
	}
	var joined time.Time
	if s := strings.TrimSpace(r.FormValue("joined")); s != "" {
		t, err := time.ParseInLocation("2006-01-02", s, a.cfg.Location)
		if err != nil {
			back(w, r, page+"?err=bad-date")
			return
		}
		joined = t.Add(12 * time.Hour).UTC()
	}
	if err := a.store.SetRecord(id, years, joined, r.FormValue("emails") != ""); err != nil {
		back(w, r, page+"?err=save")
		return
	}
	back(w, r, page+"?ok=record-saved")
}

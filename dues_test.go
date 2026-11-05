package main

import (
	"testing"
	"time"
)

func day(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t.Add(12 * time.Hour)
}

// Paid follows each member's expiry, not the calendar year.
func TestDuesFollowExpiry(t *testing.T) {
	m := Member{Joined: day("2024-06-03"), PaidYears: []int{2026}}
	for when, want := range map[string]bool{"2026-11-05": true, "2027-01-15": true, "2027-06-03": true, "2027-06-04": false} {
		if got := m.PaidAt(day(when)); got != want {
			t.Errorf("paid on %s: %v, want %v", when, got, want)
		}
	}
	if (Member{Joined: day("2024-06-03")}).PaidAt(day("2026-01-01")) {
		t.Error("never paid is never paid up")
	}

	for _, c := range []struct {
		name   string
		m      Member
		now    string
		expire string
	}{
		{"new member: a year from the day they joined", Member{Joined: day("2026-11-04")}, "2026-11-05", "2027-11-04"},
		{"early renewal adds a year", Member{Joined: day("2024-06-03"), PaidYears: []int{2026}}, "2027-05-20", "2028-06-03"},
		{"lapsed, back in November with a March join day", Member{Joined: day("2024-03-14"), PaidYears: []int{2024}}, "2026-11-05", "2028-03-14"},
		{"lapsed, back in May with a December join day", Member{Joined: day("2023-12-01"), PaidYears: []int{2024}}, "2026-05-10", "2026-12-01"},
	} {
		c.m.PaidYears = append(c.m.PaidYears, c.m.nextPaidYear(day(c.now)))
		if got := c.m.DuesExpire().Format("2006-01-02"); got != c.expire {
			t.Errorf("%s: expires %s, want %s", c.name, got, c.expire)
		}
	}
}

// Undo takes back the latest payment, whichever year it was for.
func TestUnpay(t *testing.T) {
	a, _, _ := testApp(t)
	year := a.cfg.ThisYear()
	a.store.Import([]Member{{Name: "Pat", Email: "pat@example.com", PaidYears: []int{year}, Joined: time.Now().AddDate(-1, 0, -1)}})
	pat, _ := a.store.MemberByEmail("pat@example.com")
	if !pat.Paid() {
		t.Fatal("Pat is paid up")
	}
	if changed, _ := a.store.Pay(pat.ID, false); changed {
		t.Error("Mark paid on someone paid up changes nothing")
	}
	a.store.Pay(pat.ID, true)
	got, _ := a.store.MemberByID(pat.ID)
	if len(got.PaidYears) != 2 || got.LastPaid() != year+1 {
		t.Errorf("renewing early: %v", got.PaidYears)
	}
	a.store.Unpay(pat.ID)
	if got, _ := a.store.MemberByID(pat.ID); got.LastPaid() != year {
		t.Errorf("undo: %v", got.PaidYears)
	}
}

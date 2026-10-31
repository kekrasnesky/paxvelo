package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Home, where admins land: what needs them (attention.go), the membership
// and the week's rides at a glance, shortcuts to the common jobs, and
// security warnings when there's something to fix. Everything is a count;
// nothing here tracks what any one member does (see stats.go).

const (
	newMemberDays = 30 // "joined recently" on the membership card
	signupDays    = 90 // website signups older than this drop off the waiting list (they're still on the unpaid list)
	maxSignups    = 6  // signups listed on Home; the rest are a link away
	homeRides     = 3  // upcoming rides shown
	backupMaxAge  = 48 * time.Hour
	lowDisk       = 2 * minDiskFree // warn before uploads stop
)

type dashboardView struct {
	Items []attentionItem // Needs attention

	// SetupLogin: logged in with the setup password, so the warning goes first.
	// Intro: the one-time welcome card, until they've seen How the site works.
	SetupLogin, Intro bool

	Members memberSnapshot
	Rides   []RideOcc // the next few, not cancelled
	Off     []RideOcc // cancelled in the next week
	Nudges  []attentionItem
}

type memberSnapshot struct {
	Active, Paid, Unpaid int
	New                  int // active members who joined in the last newMemberDays
	Family, Individual   int // active members on each plan
	PaidFamily           int // of those, paid for this year
	PaidIndividual       int
	Dues                 int // this year's paid memberships at today's prices
	Signups              []Member
	MoreSignups          int // waiting, but not listed
}

func (a *App) memberSnapshot() memberSnapshot {
	var s memberSnapshot
	year := a.cfg.ThisYear()
	now := time.Now()
	for _, m := range a.store.Members() {
		if !m.Active() {
			continue
		}
		paid := m.PaidFor(year)
		s.Active++
		if paid {
			s.Paid++
		} else {
			s.Unpaid++
		}
		if now.Sub(m.Joined) < newMemberDays*24*time.Hour {
			s.New++
		}
		if m.Plan == "family" {
			s.Family++
			if paid {
				s.PaidFamily++
			}
		} else {
			s.Individual++
			if paid {
				s.PaidIndividual++
			}
		}
		// Joined on the website and not marked paid yet: the signup email
		// went to the admins, and this is where they act on it.
		if m.WelcomePending && !paid && now.Sub(m.Joined) < signupDays*24*time.Hour {
			s.Signups = append(s.Signups, m)
		}
	}
	s.Dues = s.PaidIndividual*a.cfg.DuesIndividual + s.PaidFamily*a.cfg.DuesFamily
	slices.SortFunc(s.Signups, func(x, y Member) int { return y.Joined.Compare(x.Joined) })
	if len(s.Signups) > maxSignups {
		s.MoreSignups = len(s.Signups) - maxSignups
		s.Signups = s.Signups[:maxSignups]
	}
	return s
}

// weekRides is the next few rides in the coming week, and any cancelled.
func (a *App) weekRides() (next, off []RideOcc) {
	today := a.today()
	for _, o := range a.store.Rides(today, today.AddDate(0, 0, 6)) {
		switch {
		case o.Cancelled:
			off = append(off, o)
		case len(next) < homeRides:
			next = append(next, o)
		}
	}
	return next, off
}

// GoingLabel is how many riders joined the ride's Strava event, for Home.
func (o RideOcc) GoingLabel() string {
	if o.Strava == nil || o.Strava.Going == 0 {
		return "Nobody joined on Strava yet"
	}
	return fmt.Sprintf("%d joined on Strava", o.Strava.Going)
}

// securityNudges are the security problems worth an admin's attention: the
// club PIN being off, an old backup, and a nearly full disk. Each appears
// only when it applies.
func (a *App) securityNudges() []attentionItem {
	var out []attentionItem
	if a.store.ClubPIN() == "" {
		out = append(out, attentionItem{
			Text: "The club PIN is off, so every photo and ride members send in waits for an admin.",
			Href: "/admin/security", Link: "Set one",
		})
	}
	if age, ok := lastBackup(a.cfg.BackupDir); ok && age > backupMaxAge {
		text := "There’s no nightly backup on the server."
		if age < 1<<62 {
			days := int(age.Hours() / 24)
			text = fmt.Sprintf("The last nightly backup is %d %s old. Check the server’s backup job.", days, plural(days, "day", "days"))
		}
		out = append(out, attentionItem{Text: text, Href: "/admin/security", Link: "Backups"})
	}
	if free := diskFree(a.cfg.DataDir); free >= 0 && free < lowDisk {
		out = append(out, attentionItem{
			Text: fmt.Sprintf("The server has only %.1f GB of disk space left. Photo uploads stop below %d GB.", float64(free)/(1<<30), minDiskFree>>30),
			Href: "/admin/gallery", Link: "Photos",
		})
	}
	return out
}

// lastBackup is how long ago the newest nightly data backup was made, or a
// huge age when the folder has none. ok is false when there's no backup
// folder to look in (like on a laptop), so Home says nothing.
func lastBackup(dir string) (age time.Duration, ok bool) {
	if dir == "" {
		return 0, false
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return 0, false
	}
	var newest time.Time
	for _, f := range files {
		if !strings.HasPrefix(f.Name(), "paxvelo-") || filepath.Ext(f.Name()) != ".json" {
			continue
		}
		if info, err := f.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return 1 << 62, true
	}
	return time.Since(newest), true
}

// The welcome card shows until an admin opens How the site works or presses
// Got it. Accounts remember it; the setup password, which has no account,
// gets the setup warning instead.

func (a *App) showIntro(r *http.Request) bool {
	u, ok := a.currentAdmin(r)
	return ok && u.ID != 0 && !u.SeenGuide
}

func (a *App) seenGuide(r *http.Request) {
	if u, ok := a.currentAdmin(r); ok && u.ID != 0 && !u.SeenGuide {
		a.store.SetSeenGuide(u.ID)
	}
}

func (a *App) dismissIntro(w http.ResponseWriter, r *http.Request) {
	a.seenGuide(r)
	back(w, r, "/admin")
}

func (s *Store) SetSeenGuide(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.adminWhere(func(u *AdminUser) bool { return u.ID == id }); u != nil {
		u.SeenGuide = true
		s.saveOrLog()
	}
}

func (a *App) attentionPage(w http.ResponseWriter, r *http.Request) {
	u, _ := a.currentAdmin(r)
	d := dashboardView{
		Items: a.attention(), SetupLogin: u.ID == 0, Intro: a.showIntro(r),
		Members: a.memberSnapshot(), Nudges: a.securityNudges(),
	}
	d.Rides, d.Off = a.weekRides()
	v := adminView{Title: "Home", Tab: "attention", Year: a.cfg.ThisYear(), Dashboard: &d}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "attention", v)
}

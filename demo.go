package main

// A made-up club for trying the site on a computer: `go run . -demo` fills an
// empty DATA_DIR with members in every state, rides (weekly, one-off,
// waiting, virtual, community, an event), riders, an invitation with
// answers, a loaner request, treasurer lines and a spending request, so
// every admin screen has something on it. Names are invented and every
// address is at example.com, which can't receive mail.
//
// It refuses to touch a data folder that holds anything but a demo club, to
// run with an email server set (SMTP_HOST) or on a real address (SITE_URL),
// so it can't reach real members or send real email.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// demoCheck says whether -demo may run with this config: an empty data
// folder or one it filled before, no email server, a local site address.
func demoCheck(cfg Config) error {
	if cfg.SMTPHost != "" {
		return errors.New("-demo won't run with SMTP_HOST set: it would email invented addresses")
	}
	if !localSite(cfg.SiteURL) {
		return fmt.Errorf("-demo only runs on this computer (SITE_URL is %s)", cfg.SiteURL)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "paxvelo.json")); err == nil {
		s, err := openStore(cfg.DataDir)
		if err != nil {
			return err
		}
		if !s.d.Demo {
			return fmt.Errorf("-demo won't touch %s: it holds real club data; point DATA_DIR at an empty folder", cfg.DataDir)
		}
	}
	return nil
}

// seedDemo fills a new store with the made-up club. A store that already
// is a demo club is left as it is, so changes made while trying the site
// survive a restart.
func seedDemo(s *Store, loc *time.Location) error {
	s.mu.Lock()
	done := s.d.Demo || len(s.d.Members) > 0
	s.mu.Unlock()
	if done {
		return nil
	}
	now := time.Now().In(loc)
	year := now.Year()
	day := func(n int) string { return now.AddDate(0, 0, n).Format("2006-01-02") }
	mail := func(name string) string { return name + "@example.com" }

	type demoMember struct {
		name, email, plan, phone string
		joined                   time.Time
		paid                     bool
		webJoin, unsub           bool
	}
	people := []demoMember{
		{name: "Avery Lindqvist", email: mail("avery"), plan: "individual", phone: "301-555-0101", joined: now.AddDate(-3, 0, 0), paid: true},
		{name: "Jordan Okafor", email: mail("jordan.okafor"), plan: "family", phone: "301-555-0102", joined: now.AddDate(-2, -4, 0), paid: true},
		{name: "Sam Ritter", email: mail("sam"), plan: "individual", phone: "240-555-0103", joined: now.AddDate(-1, -2, 0), paid: true},
		{name: "Priya Natarajan", email: mail("priya"), plan: "individual", joined: now.AddDate(0, -5, 0), paid: true},
		{name: "Theodora Montgomery-Fitzwilliam", email: mail("theodora.montgomery-fitzwilliam.cycling"), plan: "individual", phone: "301-555-0105", joined: now.AddDate(0, -14, 0)}, // lapsed, long name and email
		{name: "Marcus Bell", email: mail("marcus"), plan: "individual", joined: now.AddDate(0, 0, -3), webJoin: true},                                                               // joined on the website, not paid or confirmed
		{name: "Lena Park", email: mail("lena"), plan: "family", joined: now.AddDate(0, 0, -1), webJoin: true},
		{name: "Chris Delgado", email: mail("chris"), joined: now.AddDate(-2, 0, 0)},                 // email list only
		{name: "Robin Achterberg", email: mail("robin"), joined: now.AddDate(-1, 0, 0), unsub: true}, // unsubscribed
		{name: "Gabe Murphy-Ellis", email: mail("gabe"), plan: "individual", joined: now.AddDate(-4, 0, 0), paid: true},
	}
	var list []Member
	for _, p := range people {
		m := Member{Name: p.name, Email: p.email, Plan: p.plan, Phone: p.phone, Joined: p.joined}
		if p.paid {
			m.PaidYears = []int{year}
		}
		if p.unsub {
			t := now.AddDate(0, -2, 0)
			m.Unsubscribed = &t
		}
		list = append(list, m)
	}
	if _, _, err := s.Import(list); err != nil {
		return err
	}
	s.mu.Lock()
	for _, p := range people {
		if m := s.byEmail(p.email); m != nil {
			if p.webJoin {
				m.WelcomePending, m.Unconfirmed = true, true
				m.Level = "new"
			}
			if p.paid {
				m.PaidOn = map[int]time.Time{year: p.joined.AddDate(year-p.joined.Year(), 0, 0)}
			}
			if p.phone != "" {
				m.EmergencyName, m.EmergencyPhone = "Pat "+p.name[len(p.name)-1:], "301-555-0199"
			}
		}
	}
	s.d.Demo = true
	err := s.save()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if jordan, ok := s.MemberByEmail(mail("jordan.okafor")); ok {
		if _, err := s.AddHousehold(jordan.ID, []HouseholdAdult{{Name: "Casey Okafor", Email: mail("casey.okafor")}}, true); err != nil {
			return err
		}
	}

	// Rides: two weekly ones, then a fortnight of one-offs in every state.
	if _, err := s.AddSeries(RideSeries{Name: "Tuesday Night Ride", Weekday: 2, Time: "18:00", Place: "Leonardtown Square", Groups: []string{"A group", "B group"}, Details: "Two pace groups, regroup at the church."}); err != nil {
		return err
	}
	if _, err := s.AddSeries(RideSeries{Name: "Saturday Social", Weekday: 6, Time: "08:30", Place: "Hollywood Volunteer Fire Department", Level: "social"}); err != nil {
		return err
	}
	rides := []CalRide{
		{Date: day(1), Time: "07:30", Name: "Early Coffee Spin", Place: "St. Mary's College, Lot P", Details: "Easy 25 miles to the coffee shop and back."},
		{Date: day(2), Time: "18:00", Name: "Zwift Tuesday Spin", Details: "On Zwift, Watopia Flat route. Join from your trainer."},
		{Date: day(4), Time: "09:00", Name: "Point Lookout Century", Place: "Leonardtown Square, 22745 Washington Street, Leonardtown MD", Groups: []string{"Fast", "Social", "Beginner"}, Details: "100 miles to Point Lookout and back. Rest stop at mile 50."},
		{Date: day(5), Time: "10:00", Name: "Mountain bike at Myrtle Point", Place: "Myrtle Point Park", Unofficial: true},
		{Date: day(6), Time: "08:00", Name: "Gravel Explorer", Place: "Chancellor's Run Park", Held: true, Details: "Mixed gravel and back roads. Posted without the club PIN."},
		{Date: day(9), Time: "19:00", Name: "December Club Meeting", Place: "Leonardtown Library", Kind: "meeting"},
	}
	keys := map[string]string{}
	for _, c := range rides {
		o, err := s.AddRide(c)
		if err != nil {
			return err
		}
		keys[c.Name] = o.Key
	}
	_, err = s.UpdateRide(keys["Point Lookout Century"], func(c *CalRide) error {
		c.Leaders = []Leader{{Group: "Social", Name: "Avery Lindqvist"}}
		c.Riders = []RideRider{
			{ID: randHex(6), Name: "Sam Ritter", Group: "Fast", Added: now.UTC()},
			{ID: randHex(6), Name: "Theodora Montgomery-Fitzwilliam", Email: mail("theodora.montgomery-fitzwilliam.cycling"), Group: "Social", Added: now.UTC()},
			{ID: randHex(6), Name: "Dana Newcomb", Email: mail("dana.newcomb"), Group: "Beginner", First: true, Added: now.UTC()},
		}
		return nil
	})
	if err != nil {
		return err
	}
	if _, err := s.UpdateRide(keys["Early Coffee Spin"], func(c *CalRide) error {
		c.Riders = []RideRider{{ID: randHex(6), Name: "Priya Natarajan", Added: now.UTC()}}
		return nil
	}); err != nil {
		return err
	}

	// A loaner bike request waiting for an answer.
	for _, b := range s.LoanerBikes() {
		if b.Ready {
			o, _ := s.Ride(keys["Early Coffee Spin"])
			if _, err := s.RequestLoaner(LoanerRequest{Bike: b.ID, RideKey: o.Key, RideName: o.Name, RideDate: o.Date, RideTime: o.Time, Name: "Dana Newcomb", Email: mail("dana.newcomb")}); err != nil {
				return err
			}
			break
		}
	}

	// The meeting's invitation, with answers.
	rid, err := s.SaveRSVPEvent(RSVPEvent{Title: "December Club Meeting", Date: day(9), Time: "19:00", Place: "Leonardtown Library, 23250 Hollywood Road, Leonardtown MD", AskBringing: true, Ride: keys["December Club Meeting"], Created: now.UTC()})
	if err != nil {
		return err
	}
	for _, a := range []struct{ email, answer, bringing string }{{mail("avery"), "yes", "Cookies"}, {mail("sam"), "maybe", ""}, {mail("priya"), "no", ""}} {
		if m, ok := s.MemberByEmail(a.email); ok {
			if err := s.AnswerRSVP(rid, m, RSVPReply{Name: m.Name, Email: m.Email, Answer: a.answer, Bringing: a.bringing}, now); err != nil {
				return err
			}
		}
	}

	// Treasurer: a few lines, and a spending request waiting for answers.
	for _, e := range []LedgerEntry{
		{Kind: "expense", Date: day(-12), Cents: 18450, Category: "Club Rides & Promotional Items", What: "Water bottles for the century rest stop", Who: "Avery Lindqvist"},
		{Kind: "expense", Date: day(-30), Cents: 9900, Category: "Insurance", What: "League club insurance"},
		{Kind: "income", Date: day(-20), Cents: 50000, Category: "Sponsorships", What: "Bike shop sponsorship"},
	} {
		e.AddedBy, e.Added = "Demo", now.UTC()
		if _, err := s.AddLedgerEntry(e); err != nil {
			return err
		}
	}
	if _, err := s.AddSpendRequest(SpendRequest{Kind: reqReport, Date: day(-1), Cents: 4599, Category: "Club Rides & Promotional Items", What: "Snacks for the first-timer welcome ride", By: "Sam Ritter", ByEmail: mail("sam"), Status: reqWaiting}); err != nil {
		return err
	}
	if _, err := s.AddAlbum("Fall Century 2026", day(-40)); err != nil {
		return err
	}
	return nil
}

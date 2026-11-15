package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The calendar feed has every ride coming up, names a cancelled one as
// cancelled instead of dropping it, and leaves out rides waiting for approval.
func TestCalendarFeed(t *testing.T) {
	a, h, _ := testApp(t)
	day := func(n int) string { return a.today().AddDate(0, 0, n).Format("2006-01-02") }
	on, err := a.store.AddRide(CalRide{Date: day(2), Time: "08:30", Name: "Coffee Spin", Place: "Leonardtown Square", Details: "Flat, then coffee"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.AddRide(CalRide{Date: day(3), Time: "09:00", Name: "Rainy Loop", Cancelled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.AddRide(CalRide{Date: day(4), Name: "Not Approved Yet", Held: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.AddRide(CalRide{Date: day(-60), Name: "Long Ago"}); err != nil {
		t.Fatal(err)
	}

	w := do(t, h, nil, "GET", "/calendar/feed.ics", "", nil)
	ics := w.Body.String()
	if ct := w.Header().Get("Content-Type"); w.Code != 200 || !strings.HasPrefix(ct, "text/calendar") {
		t.Fatalf("feed = %d, %q", w.Code, ct)
	}
	for _, want := range []string{"BEGIN:VCALENDAR\r\n", "X-WR-CALNAME:PaxVelo\r\n", "REFRESH-INTERVAL;VALUE=DURATION:PT1H\r\n",
		"SUMMARY:Coffee Spin\r\n", "UID:" + on.Key + "@", "LOCATION:Leonardtown Square\r\n", `Flat\, then coffee`,
		"/calendar/ride/" + on.Key, "SUMMARY:Cancelled: Rainy Loop\r\n", "END:VCALENDAR\r\n"} {
		if !strings.Contains(ics, want) {
			t.Errorf("feed is missing %q", want)
		}
	}
	for _, not := range []string{"Not Approved Yet", "Long Ago", "STATUS:CANCELLED"} {
		if strings.Contains(ics, not) {
			t.Errorf("feed shouldn't have %q", not)
		}
	}
	if got := strings.Count(ics, "BEGIN:VEVENT"); got != strings.Count(ics, "END:VEVENT") || got < 2 {
		t.Errorf("feed has %d events", got)
	}
	for _, l := range strings.Split(ics, "\r\n") {
		if len(l) > 75 {
			t.Errorf("feed line longer than 75 bytes: %q", l)
		}
	}
}

// The Subscribe page leads with the visitor's own calendar app, the calendar
// and membership pages link to it, and {calendar_link} in an email is its
// address.
func TestSubscribePage(t *testing.T) {
	a, h, _ := testApp(t)
	host := a.siteHost()
	page := do(t, h, nil, "GET", "/calendar/subscribe", "", nil).Body.String()
	for _, want := range []string{`href="webcal://` + host + `/calendar/feed.ics"`, "calendar.google.com/calendar/render?cid=webcal%3A%2F%2F",
		"outlook.live.com/calendar/0/addfromweb", `value="` + a.cfg.SiteURL + `/calendar/feed.ics"`, `content="noindex"`} {
		if !strings.Contains(page, want) {
			t.Errorf("subscribe page is missing %q", want)
		}
	}
	if g, ap := strings.Index(page, "Google Calendar"), strings.Index(page, "Apple Calendar"); g < 0 || ap < g {
		t.Error("Google Calendar should come first off an Apple device")
	}
	req := httptest.NewRequest("GET", "/calendar/subscribe", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	iphone := rec.Body.String()
	if g, ap := strings.Index(iphone, "Google Calendar"), strings.Index(iphone, "Apple Calendar"); ap < 0 || g < ap {
		t.Error("Apple Calendar should come first on an iPhone")
	}
	if cal := do(t, h, nil, "GET", "/calendar", "", nil).Body.String(); !strings.Contains(cal, `href="/calendar/subscribe"`) {
		t.Error("the calendar page should link to Subscribe")
	}
	if got := a.mailer.personalize("{calendar_link}", Member{Name: "Pat Rider"}); got != a.cfg.SiteURL+"/calendar/subscribe" {
		t.Errorf("{calendar_link} = %q", got)
	}
}

// A welcome email nobody changed from the default before the calendar
// paragraph gets it; one an admin wrote stays.
func TestWelcomeGetsCalendar(t *testing.T) {
	before := strings.Replace(defaultWelcomeBody, welcomeCalendar, "", 1)
	if before == defaultWelcomeBody || !strings.Contains(defaultWelcomeBody, "{calendar_link}") {
		t.Fatal("the default welcome email should have the calendar paragraph")
	}
	if !staleWelcome(before) || !staleWelcome(oldWelcomeBody) || staleWelcome(defaultWelcomeBody) || staleWelcome("Hi {first_name}") {
		t.Error("staleWelcome is wrong about a default")
	}
}

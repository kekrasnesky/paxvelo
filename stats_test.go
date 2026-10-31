package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	testPhone  = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
	testLaptop = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"
)

// count reports a page view the way site.js does.
func count(h http.Handler, c *http.Cookie, ip, ua, body string) {
	r := httptest.NewRequest("POST", "/admin/count", strings.NewReader(body))
	r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	r.Header.Set("User-Agent", ua)
	r.RemoteAddr = ip + ":5000"
	if c != nil {
		r.AddCookie(c)
	}
	h.ServeHTTP(httptest.NewRecorder(), r)
}

func thisWeek(a *App) weekStats { return a.stats.Weeks()[a.stats.weekOf(time.Now())] }

func TestPageViewsCounted(t *testing.T) {
	a, h, admin := testApp(t)
	rides := `{"p":"https://www.paxvelo.club/rides","r":"https://l.instagram.com/"}`
	count(h, nil, "203.0.113.5", testLaptop, rides)
	count(h, nil, "203.0.113.5", testLaptop, `{"p":"https://www.paxvelo.club/rides","r":"https://www.paxvelo.club/"}`)
	count(h, nil, "198.51.100.7", testPhone, `{"p":"https://www.paxvelo.club/calendar/ride/2026-11-03-tnr","r":""}`)
	// None of these count: a search engine, an admin, a junk address, no browser.
	count(h, nil, "192.0.2.1", "Mozilla/5.0 (compatible; Googlebot/2.1)", rides)
	count(h, admin, "192.0.2.2", testLaptop, rides)
	count(h, nil, "192.0.2.3", testLaptop, `{"p":"https://www.paxvelo.club/<script>","r":""}`)
	count(h, nil, "192.0.2.4", "", rides)

	w := thisWeek(a)
	if w.Views != 3 || w.Visitors != 2 || w.Phones != 1 {
		t.Errorf("views, visitors, phones = %d, %d, %d; want 3, 2, 1", w.Views, w.Visitors, w.Phones)
	}
	if p := w.Pages["/rides"]; p == nil || p.Views != 2 || p.Visitors != 1 {
		t.Errorf("Our Rides = %+v, want 2 views by 1 visitor", p)
	}
	if p := w.Pages["/calendar/ride"]; p == nil || p.Views != 1 {
		t.Errorf("ride pages should count together, got %+v", w.Pages)
	}
	if len(w.From) != 1 || w.From["instagram.com"] != 1 {
		t.Errorf("came from = %v, want only instagram.com once (the club's own site doesn't count)", w.From)
	}
}

// The newsletter's main address counts as the issue it shows.
func TestNewsletterCountsAsLatestIssue(t *testing.T) {
	a, h, _ := testApp(t)
	now := time.Now()
	a.store.mu.Lock()
	a.store.d.Issues = []*Issue{{ID: 1, Year: 2026, Month: 9, Published: &now}, {ID: 2, Year: 2026, Month: 10, Published: &now}, {ID: 3, Year: 2026, Month: 11}}
	a.store.mu.Unlock()
	count(h, nil, "203.0.113.5", testLaptop, `{"p":"https://www.paxvelo.club/newsletter","r":""}`)
	if p := thisWeek(a).Pages["/newsletter/2026-10"]; p == nil || p.Views != 1 {
		t.Errorf("pages = %v, want the October issue (the latest published) counted", thisWeek(a).Pages)
	}
}

// Each week counts its own visitors, and last week's codes are forgotten.
func TestNewWeekCountsVisitorsAgain(t *testing.T) {
	a, _, _ := testApp(t)
	mon := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	a.stats.View("/rides", "", "203.0.113.5", testLaptop, mon)
	a.stats.View("/rides", "", "203.0.113.5", testLaptop, mon.AddDate(0, 0, 6)) // Sunday, same week
	a.stats.View("/rides", "", "203.0.113.5", testLaptop, mon.AddDate(0, 0, 7)) // next Monday
	weeks := a.stats.Weeks()
	if w := weeks["2026-10-26"]; w.Views != 2 || w.Visitors != 1 {
		t.Errorf("first week = %d views by %d visitors, want 2 by 1", w.Views, w.Visitors)
	}
	if w := weeks["2026-11-02"]; w.Views != 1 || w.Visitors != 1 {
		t.Errorf("second week = %d views by %d visitors, want 1 by 1", w.Views, w.Visitors)
	}
	if len(a.stats.d.Visitors) != 1 {
		t.Errorf("only this week's visitor codes should be kept, got %d", len(a.stats.d.Visitors))
	}
}

func TestStatsSurviveRestart(t *testing.T) {
	a, _, _ := testApp(t)
	a.stats.View("/rides", "", "203.0.113.5", testLaptop, time.Now())
	a.stats.EmailEvent(4, "0123456789abcdef", "https://example.com/")
	if err := a.stats.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := openStats(a.cfg.DataDir, a.store.Secret(), a.cfg.Location)
	if err != nil {
		t.Fatal(err)
	}
	if w := again.Weeks()[again.weekOf(time.Now())]; w.Views != 1 {
		t.Errorf("views after a restart = %d, want 1", w.Views)
	}
	if e := again.Email(4); e.Opened != 1 || e.Clicked != 1 {
		t.Errorf("email after a restart = %+v, want 1 opened and clicked", e)
	}
	// The same visitor that day is still counted once.
	again.View("/rides", "", "203.0.113.5", testLaptop, time.Now())
	if w := again.Weeks()[again.weekOf(time.Now())]; w.Visitors != 1 {
		t.Errorf("visitors after a restart = %d, want 1", w.Visitors)
	}
}

var (
	pixelRe = regexp.MustCompile(`<img src="(https://www\.paxvelo\.club/e/o/[^"]+)"`)
	clickRe = regexp.MustCompile(`href="(https://www\.paxvelo\.club/e/c/[^"]+)"`)
)

func get(h http.Handler, method, u string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, strings.Replace(strings.ReplaceAll(u, "&amp;", "&"), "https://www.paxvelo.club", "", 1), nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Emails to members count opens and clicks, without the site keeping who
// each copy went to.
func TestEmailOpensAndClicksCounted(t *testing.T) {
	a, h, _ := testApp(t)
	body := "Hi {first_name},\n\n[Lead a ride](/calendar) or see https://example.com/kit?size=m&color=red\n\nYour membership: {my_link}"
	pat := Member{ID: 1, Name: "Pat Rider", Email: "pat@example.com", Token: "pat-secret-token"}
	sam := Member{ID: 2, Name: "Sam Rider", Email: "sam@example.com", Token: "sam-secret-token"}
	copyFor := func(m Member) string {
		links := memberLinks{Mine: a.cfg.membershipURL(m.Token), Unsub: a.cfg.SiteURL + "/unsubscribe?t=" + m.Token, Track: newEmailTrack(a.stats, a.cfg.SiteURL, 7, m)}
		page, err := a.site.renderEmail("News", a.mailer.personalize(body, m), links, a.cfg.SiteURL)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	patCopy, samCopy := copyFor(pat), copyFor(sam)

	clicks := clickRe.FindAllStringSubmatch(patCopy, -1)
	if len(clicks) != 2 {
		t.Fatalf("want the 2 links in the message sent through the site, got %d in:\n%s", len(clicks), patCopy)
	}
	if !strings.Contains(patCopy, `href="https://www.paxvelo.club/membership?t=pat-secret-token"`) || !strings.Contains(patCopy, `href="https://www.paxvelo.club/unsubscribe?t=pat-secret-token"`) {
		t.Error("the member's own links (membership, unsubscribe) should go straight there")
	}

	open := pixelRe.FindStringSubmatch(patCopy)[1]
	if w := get(h, "GET", open); w.Code != 200 || w.Header().Get("Content-Type") != "image/gif" {
		t.Errorf("picture: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	get(h, "GET", open) // opening it again doesn't count twice
	w := get(h, "GET", clicks[1][1])
	if w.Code != http.StatusFound || w.Header().Get("Location") != "https://example.com/kit?size=m&color=red" {
		t.Errorf("click forwarded with %d to %q", w.Code, w.Header().Get("Location"))
	}
	// Sam clicks without loading pictures: that counts as opened too.
	get(h, "HEAD", clickRe.FindStringSubmatch(samCopy)[1]) // a link checker's HEAD doesn't count
	get(h, "GET", clickRe.FindStringSubmatch(samCopy)[1])

	e := a.stats.Email(7)
	if e.Opened != 2 || e.Clicked != 2 {
		t.Errorf("opened, clicked = %d, %d; want 2, 2", e.Opened, e.Clicked)
	}
	people := map[string]int{}
	for _, l := range e.Links {
		people[l.URL] = l.People
	}
	if people["https://www.paxvelo.club/calendar"] != 1 || people["https://example.com/kit?size=m&color=red"] != 1 {
		t.Errorf("link counts = %v", people)
	}

	// A changed address doesn't count, and never forwards anywhere else.
	tampered := strings.Replace(clicks[1][1], "example.com", "evil.example", 1)
	if w := get(h, "GET", tampered); w.Header().Get("Location") != "/" {
		t.Errorf("a changed link forwarded to %q, want the home page", w.Header().Get("Location"))
	}
	get(h, "GET", open[:len(open)-3]+"aaa")
	if e := a.stats.Email(7); e.Opened != 2 || e.Clicked != 2 {
		t.Errorf("changed addresses shouldn't count: %+v", e)
	}

	// Nothing saved says who opened or clicked.
	if err := a.stats.Save(); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(filepath.Join(a.cfg.DataDir, "stats.json"))
	for _, m := range []Member{pat, sam} {
		for _, s := range []string{m.Name, m.Email, m.Token} {
			if strings.Contains(string(saved), s) {
				t.Errorf("stats.json mentions %q", s)
			}
		}
	}
}

// A test email, or any email that isn't a send to members, isn't marked.
func TestOnlyMemberSendsAreMarked(t *testing.T) {
	a, _, _ := testApp(t)
	page, _ := a.site.renderEmail("Reset", "See https://example.com/", memberLinks{}, a.cfg.SiteURL)
	if strings.Contains(page, "/e/") {
		t.Error("an email without tracking shouldn't have counted links or a picture")
	}
}

func TestStatsPage(t *testing.T) {
	a, h, admin := testApp(t)
	a.stats.View("/rides", "instagram.com", "203.0.113.5", testPhone, time.Now())
	if _, _, err := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com"}, 2026); err != nil {
		t.Fatal(err)
	}
	camp, err := a.store.StartCampaign("November rides", "Hi", "all", 2026)
	if err != nil {
		t.Fatal(err)
	}
	a.store.RecordSend(camp.ID, 1, nil)
	a.stats.EmailEvent(camp.ID, "0123456789abcdef", "https://www.paxvelo.club/calendar")

	w := do(t, h, admin, "GET", "/admin/stats", "", nil)
	if w.Code != 200 {
		t.Fatalf("stats page: %d %s", w.Code, w.Body)
	}
	page := w.Body.String()
	for _, want := range []string{"Visitors each week", "Our Rides", "instagram.com", "100%</strong> on a phone", "November rides", "/calendar</a>"} {
		if !strings.Contains(page, want) {
			t.Errorf("stats page should show %q", want)
		}
	}
	if w := do(t, h, nil, "GET", "/admin/stats", "", nil); w.Code == 200 {
		t.Error("the stats page should need a login")
	}
}

// Every public page reports its view, and the browser's security policy
// allows the script that does it.
func TestPagesReportViews(t *testing.T) {
	_, h, _ := testApp(t)
	w := do(t, h, nil, "GET", "/rides", "", nil)
	if !strings.Contains(w.Body.String(), "/admin/count") {
		t.Fatal("public pages should report their views")
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, hash := range scriptHashes(w.Body.Bytes()) {
		if !strings.Contains(csp, hash) {
			t.Errorf("the page's script %s isn't allowed by its CSP", hash)
		}
	}
}

package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stats counts visits to the public pages, and opens and clicks on emails to
// members, for the admin's Stats screen. It keeps totals only: nothing says
// who visited a page or which member opened an email.
//
// Pages: a few lines in site.js report each page view once the page has
// loaded, so a visit costs the visitor nothing, and search engines and other
// robots (which don't run it) aren't counted. Each visitor is counted once a
// week by a code scrambled from their connection (address and browser) and
// the week. The codes are dropped when the week ends and no cookie is set, so
// there's nothing for visitors to agree to.
//
// Emails: each email to members carries a tiny invisible picture, and its
// links pass through the site, all marked with a random code made for that
// one email. The code is never written down with the member it went to, so
// the site can tell that an email was opened, but not whose.
//
// Counting happens in memory. The totals are saved to data/stats.json once a
// minute, apart from the club data, so a busy day never rewrites that file.

type Stats struct {
	mu    sync.Mutex
	path  string
	key   []byte // scrambles visitor codes and signs email links
	loc   *time.Location
	d     statsData
	dirty bool
}

type statsData struct {
	Weeks  map[string]*weekStats `json:"weeks"`  // by the Monday the week starts
	Emails map[int]*emailStats   `json:"emails"` // by sent email (campaign) ID

	// This week's visitors as scrambled codes, so each is counted once, and
	// code+" "+page for each page's visitors. Cleared when the week ends.
	Week         string          `json:"week"`
	Visitors     map[string]bool `json:"visitors"`
	PageVisitors map[string]bool `json:"page_visitors"`
}

type weekStats struct {
	Views    int                   `json:"views"`
	Visitors int                   `json:"visitors"`
	Phones   int                   `json:"phones"` // visitors on a phone
	Pages    map[string]*pageStats `json:"pages"`
	From     map[string]int        `json:"from,omitempty"` // views of people who came from another site, by site
}

type pageStats struct {
	Views    int `json:"views"`
	Visitors int `json:"visitors"`
}

type emailStats struct {
	Opened  int         `json:"opened"`
	Clicked int         `json:"clicked"`
	Links   []linkStats `json:"links,omitempty"`
	// Each email's random code: bit 0 once it's opened, bit 1 once any link
	// is clicked, and bit 2+i once Links[i] is, so nobody counts twice.
	Seen map[string]uint64 `json:"seen,omitempty"`
}

type linkStats struct {
	URL    string `json:"url"`
	People int    `json:"people"`
}

const (
	// Limits, so junk sent to the counter can't grow the file without end.
	maxPagesPerWeek    = 300
	maxSitesPerWeek    = 100
	maxVisitorsPerWeek = 50000
	maxLinksPerEmail   = 60 // fits the bits in emailStats.Seen
	maxSeenPerEmail    = 20000
)

func openStats(dir, secret string, loc *time.Location) (*Stats, error) {
	k := sha256.Sum256([]byte("stats\x00" + secret))
	s := &Stats{path: filepath.Join(dir, "stats.json"), key: k[:], loc: loc}
	b, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &s.d); err != nil {
			return nil, fmt.Errorf("reading %s: %w", s.path, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	if s.d.Weeks == nil {
		s.d.Weeks = map[string]*weekStats{}
	}
	if s.d.Emails == nil {
		s.d.Emails = map[int]*emailStats{}
	}
	return s, nil
}

// run saves the totals once a minute, when anything was counted.
func (s *Stats) run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := s.Save(); err != nil {
				log.Printf("saving stats: %v", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// Save writes the totals if anything changed, the same safe way as the club
// data: a temp file, flushed to disk, renamed over the old one.
func (s *Stats) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	b, err := json.Marshal(&s.d)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// weekOf is the Monday starting t's week in the club's time zone, like "2026-10-26".
func (s *Stats) weekOf(t time.Time) string {
	t = t.In(s.loc)
	back := (int(t.Weekday()) + 6) % 7
	return time.Date(t.Year(), t.Month(), t.Day()-back, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
}

// week returns now's week, starting a new one (and forgetting last week's
// visitor codes) when it has begun. Callers must hold s.mu.
func (s *Stats) week(now time.Time) *weekStats {
	id := s.weekOf(now)
	if s.d.Week != id || s.d.Visitors == nil {
		s.d.Week, s.d.Visitors, s.d.PageVisitors = id, map[string]bool{}, map[string]bool{}
	}
	w := s.d.Weeks[id]
	if w == nil {
		w = &weekStats{}
		s.d.Weeks[id] = w
	}
	if w.Pages == nil {
		w.Pages = map[string]*pageStats{}
	}
	if w.From == nil {
		w.From = map[string]int{}
	}
	return w
}

// View counts one page view. page is the page's address (like /rides), from
// is the site the visitor followed a link from ("" if none), and ip and ua
// (their browser) make the scrambled code that counts each visitor once.
func (s *Stats) View(page, from, ip, ua string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.week(now)
	p := w.Pages[page]
	if p == nil {
		if len(w.Pages) >= maxPagesPerWeek {
			return
		}
		p = &pageStats{}
		w.Pages[page] = p
	}
	w.Views++
	p.Views++
	code := sign(s.key, s.d.Week+"\x00"+ip+"\x00"+ua)[:16]
	if !s.d.Visitors[code] && len(s.d.Visitors) < maxVisitorsPerWeek {
		s.d.Visitors[code] = true
		w.Visitors++
		if strings.Contains(ua, "Mobi") {
			w.Phones++
		}
	}
	if pv := code + " " + page; s.d.Visitors[code] && !s.d.PageVisitors[pv] {
		s.d.PageVisitors[pv] = true
		p.Visitors++
	}
	if _, ok := w.From[from]; from != "" && (ok || len(w.From) < maxSitesPerWeek) {
		w.From[from]++
	}
	s.dirty = true
}

// Weeks returns a copy of every week's totals, by the Monday it starts.
func (s *Stats) Weeks() map[string]weekStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]weekStats, len(s.d.Weeks))
	for id, w := range s.d.Weeks {
		c := weekStats{Views: w.Views, Visitors: w.Visitors, Phones: w.Phones, Pages: map[string]*pageStats{}, From: map[string]int{}}
		for k, p := range w.Pages {
			pc := *p
			c.Pages[k] = &pc
		}
		for k, n := range w.From {
			c.From[k] = n
		}
		out[id] = c
	}
	return out
}

// ---- Emails ----

// emailSig signs what an email's picture or link says, so only addresses the
// site put in an email count, and a click only ever forwards to a link that
// was in one.
func (s *Stats) emailSig(kind string, campaign int, code, link string) string {
	return sign(s.key, kind+"\x00"+strconv.Itoa(campaign)+"\x00"+code+"\x00"+link)[:20]
}

// EmailEvent counts an email as opened (link "") or a link in it as clicked.
// A click counts as an open too, for email apps that don't show pictures.
func (s *Stats) EmailEvent(campaign int, code, link string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.d.Emails[campaign]
	if e == nil {
		e = &emailStats{}
		s.d.Emails[campaign] = e
	}
	if e.Seen == nil {
		e.Seen = map[string]uint64{}
	}
	bits, ok := e.Seen[code]
	if !ok && len(e.Seen) >= maxSeenPerEmail {
		return
	}
	if bits&1 == 0 {
		bits |= 1
		e.Opened++
	}
	if link != "" {
		if bits&2 == 0 {
			bits |= 2
			e.Clicked++
		}
		i := -1
		for j, l := range e.Links {
			if l.URL == link {
				i = j
			}
		}
		if i < 0 && len(e.Links) < maxLinksPerEmail {
			e.Links = append(e.Links, linkStats{URL: link})
			i = len(e.Links) - 1
		}
		if i >= 0 && bits&(1<<(2+i)) == 0 {
			bits |= 1 << (2 + i)
			e.Links[i].People++
		}
	}
	e.Seen[code] = bits
	s.dirty = true
}

// Email returns a copy of one sent email's opens and clicks.
func (s *Stats) Email(campaign int) emailStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.d.Emails[campaign]
	if e == nil {
		return emailStats{}
	}
	return emailStats{Opened: e.Opened, Clicked: e.Clicked, Links: append([]linkStats(nil), e.Links...)}
}

// emailTrack marks one member's copy of an email so its opens and clicks are
// counted. code is random and made for this one email.
type emailTrack struct {
	stats    *Stats
	base     string // the site's address
	campaign int
	code     string
	personal string // the member's own secret: links holding it (My membership) go straight there
}

func newEmailTrack(s *Stats, base string, campaign int, mem Member) *emailTrack {
	return &emailTrack{stats: s, base: strings.TrimRight(base, "/"), campaign: campaign, code: randHex(8), personal: mem.Token}
}

var trackHrefRe = regexp.MustCompile(`href="(https?://[^"]+)"`)

// apply sends the email's web links through the site and adds the picture
// at the end.
func (t *emailTrack) apply(body template.HTML) template.HTML {
	out := trackHrefRe.ReplaceAllStringFunc(string(body), func(m string) string {
		u := html.UnescapeString(trackHrefRe.FindStringSubmatch(m)[1])
		if t.personal != "" && strings.Contains(u, t.personal) {
			return m
		}
		return `href="` + html.EscapeString(t.clickURL(u)) + `"`
	})
	return template.HTML(out + `<img src="` + html.EscapeString(t.openURL()) + `" width="1" height="1" alt="" style="display:block;width:1px;height:1px;border:0">`)
}

func (t *emailTrack) openURL() string {
	return fmt.Sprintf("%s/e/o/%d/%s/%s", t.base, t.campaign, t.code, t.stats.emailSig("o", t.campaign, t.code, ""))
}

func (t *emailTrack) clickURL(u string) string {
	return fmt.Sprintf("%s/e/c/%d/%s/%s?u=%s", t.base, t.campaign, t.code, t.stats.emailSig("c", t.campaign, t.code, u), url.QueryEscape(u))
}

// A see-through picture one pixel square.
var pixelGIF = []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\x00\x00\x00!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")

// emailFrom checks an email picture's or link's address, giving the sent
// email and the code of the member's copy.
func (a *App) emailFrom(r *http.Request, kind, link string) (int, string, bool) {
	c, err := strconv.Atoi(r.PathValue("c"))
	code := r.PathValue("code")
	if err != nil || len(code) != 16 {
		return 0, "", false
	}
	want := a.stats.emailSig(kind, c, code, link)
	return c, code, hmac.Equal([]byte(r.PathValue("sig")), []byte(want))
}

// emailOpened answers an email's picture, counting the email as opened.
// (Link checkers often ask with HEAD first, which doesn't count.)
func (a *App) emailOpened(w http.ResponseWriter, r *http.Request) {
	if c, code, ok := a.emailFrom(r, "o", ""); ok && r.Method == http.MethodGet {
		a.stats.EmailEvent(c, code, "")
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store, private")
	w.Write(pixelGIF)
}

// emailClicked counts a click on a link in an email and forwards to it. An
// address the site didn't sign goes to the home page instead, so nobody can
// use the site to forward people anywhere they like.
func (a *App) emailClicked(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("u")
	c, code, ok := a.emailFrom(r, "c", u)
	if !ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if r.Method == http.MethodGet {
		a.stats.EmailEvent(c, code, u)
	}
	w.Header().Set("Cache-Control", "no-store, private")
	http.Redirect(w, r, u, http.StatusFound)
}

// ---- Page views ----

var (
	statsPathRe = regexp.MustCompile(`^/[a-z0-9/-]{0,80}$`)
	robotRe     = regexp.MustCompile(`(?i)bot|crawl|spider|slurp|headless|lighthouse|preview|facebookexternalhit`)
)

// countView is where site.js reports a page view. It lives under /admin so
// the browser sends an admin's login cookie along, and admins looking over
// the site don't count.
func (a *App) countView(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusNoContent)
	var hit struct {
		Page string `json:"p"` // the page's canonical address
		From string `json:"r"` // document.referrer
	}
	ua, ip := r.UserAgent(), clientIP(r)
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<10)).Decode(&hit); err != nil || ua == "" || robotRe.MatchString(ua) {
		return
	}
	if _, admin := a.adminName(r); admin || !a.limit.Allow("count:"+ip, 300, time.Hour) {
		return
	}
	page, ok := a.statsPage(hit.Page)
	if !ok {
		return
	}
	a.stats.View(page, a.cameFrom(hit.From), ip, ua, time.Now())
}

// statsPage turns a page's canonical address into the page it counts as:
// ride pages count together, and /newsletter as the latest issue it shows.
func (a *App) statsPage(canonical string) (string, bool) {
	u, err := url.Parse(canonical)
	if err != nil {
		return "", false
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	switch {
	case !statsPathRe.MatchString(p):
		return "", false
	case strings.HasPrefix(p, "/calendar/ride/"):
		return "/calendar/ride", true
	case p == "/newsletter":
		if is, ok := a.latestIssue(); ok {
			return is.Path(), true
		}
	}
	return p, true
}

func (a *App) latestIssue() (Issue, bool) {
	for _, is := range a.store.Issues() { // newest first
		if is.IsPublished() {
			return is, true
		}
	}
	return Issue{}, false
}

// cameFrom names the site a visitor followed a link from, like
// "instagram.com", or "" when they didn't, or came from this site.
func (a *App) cameFrom(referrer string) string {
	u, err := url.Parse(referrer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "android-app") {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if site, err := url.Parse(a.cfg.SiteURL); err == nil && strings.EqualFold(siteHost(site.Hostname()), siteHost(host)) {
		return ""
	}
	return siteHost(host)
}

// siteHost drops the parts of a site's name that only say which of its
// servers sent someone, so l.instagram.com and www.instagram.com both count
// as instagram.com.
func siteHost(host string) string {
	for _, p := range []string{"www.", "m.", "l.", "lm.", "out."} {
		if h := strings.TrimPrefix(host, p); h != host && strings.Contains(h, ".") {
			return h
		}
	}
	return host
}

// ---- The Stats screen ----

type statsView struct {
	Week      string // the week shown, by its Monday
	WeekLabel string
	ThisWeek  bool // the week shown is still going
	Shown     weekStats
	PhonePct  int // of the week's visitors
	Bars      []statsBar
	Pages     []statsRow // the week shown, most viewed first
	From      []statsRow
	Issues    []statsRow // every newsletter issue, all weeks together
	Emails    []emailRow
}

// statsBar is one week on the visitors chart.
type statsBar struct {
	Week, Label     string
	Visitors, Views int
	Height          int // percent of the busiest week
	Shown, ThisWeek bool
}

type statsRow struct {
	Label, Href     string
	Views, Visitors int
}

type emailRow struct {
	Campaign
	Opened, Clicked   int
	OpenPct, ClickPct int // of the emails sent
	Links             []statsRow
}

const statsWeeks = 12 // on the chart

func (a *App) statsAdmin(w http.ResponseWriter, r *http.Request) {
	v := a.statsView(r.URL.Query().Get("week"), time.Now())
	a.renderAdmin(w, r, http.StatusOK, "stats", adminView{Title: "Stats", Tab: "stats", Stats: &v})
}

func (a *App) statsView(week string, now time.Time) statsView {
	weeks := a.stats.Weeks()
	current := a.stats.weekOf(now)
	if _, ok := weeks[week]; !ok {
		week = current
	}
	v := statsView{Week: week, ThisWeek: week == current, Shown: weeks[week]}
	if v.Shown.Visitors > 0 {
		v.PhonePct = v.Shown.Phones * 100 / v.Shown.Visitors
	}
	v.WeekLabel = "Week of " + weekLabel(week)
	if v.ThisWeek {
		v.WeekLabel = "This week, since Monday " + weekLabel(week)
	}

	// The chart: the last 12 weeks, oldest first, with the empty ones too.
	start, _ := time.Parse(time.DateOnly, current)
	most := 0
	for i := statsWeeks - 1; i >= 0; i-- {
		id := start.AddDate(0, 0, -7*i).Format(time.DateOnly)
		ws := weeks[id]
		v.Bars = append(v.Bars, statsBar{Week: id, Label: weekLabel(id), Visitors: ws.Visitors, Views: ws.Views, Shown: id == week, ThisWeek: id == current})
		most = max(most, ws.Visitors)
	}
	for i := range v.Bars {
		if most > 0 && v.Bars[i].Visitors > 0 {
			v.Bars[i].Height = max(2, v.Bars[i].Visitors*100/most)
		}
	}

	issues := map[string]string{} // newsletter address → its month
	for _, is := range a.store.Issues() {
		issues[is.Path()] = "Newsletter, " + is.Label()
	}
	titles := map[string]string{}
	for _, p := range a.pageRows() {
		titles[p.Path] = p.Title
	}
	for _, l := range *a.site.infoLinks.Load() { // the menu's shorter names, like "Our Rides"
		titles[l.Path] = l.Label
	}
	label := func(path string) string {
		if t := or(issues[path], or(statsLabels[path], titles[path])); t != "" {
			return t
		}
		return path
	}
	for path, p := range v.Shown.Pages {
		v.Pages = append(v.Pages, statsRow{Label: label(path), Href: path, Views: p.Views, Visitors: p.Visitors})
	}
	byViews(v.Pages)
	for site, n := range v.Shown.From {
		v.From = append(v.From, statsRow{Label: site, Views: n})
	}
	byViews(v.From)

	all := map[string]*statsRow{}
	for _, ws := range weeks {
		for path, p := range ws.Pages {
			if issues[path] == "" {
				continue
			}
			if all[path] == nil {
				all[path] = &statsRow{Label: issues[path], Href: path}
			}
			all[path].Views += p.Views
			all[path].Visitors += p.Visitors
		}
	}
	for _, is := range a.store.Issues() { // newest first
		if row := all[is.Path()]; row != nil {
			v.Issues = append(v.Issues, *row)
		}
	}

	site := strings.TrimRight(a.cfg.SiteURL, "/")
	for i, c := range a.store.Campaigns() { // newest first
		if i == 20 {
			break
		}
		e := a.stats.Email(c.ID)
		row := emailRow{Campaign: c, Opened: e.Opened, Clicked: e.Clicked}
		if c.Sent > 0 {
			row.OpenPct, row.ClickPct = min(100, e.Opened*100/c.Sent), min(100, e.Clicked*100/c.Sent)
		}
		for _, l := range e.Links {
			row.Links = append(row.Links, statsRow{Label: strings.TrimPrefix(l.URL, site), Href: l.URL, Visitors: l.People})
		}
		sort.SliceStable(row.Links, func(i, j int) bool { return row.Links[i].Visitors > row.Links[j].Visitors })
		v.Emails = append(v.Emails, row)
	}
	return v
}

// statsLabels names the pages that aren't in content/.
var statsLabels = map[string]string{
	"/": "Home", "/newsletter": "Newsletter", "/calendar": "Calendar", "/calendar/ride": "Ride pages on the calendar", "/routes": "Routes",
	"/join": "Join", "/membership": "My membership", "/unsubscribe": "Unsubscribe",
	"/share-photos": "Share your photos", "/gallery": "Photo gallery", "/minutes": "Minutes",
}

func weekLabel(id string) string {
	t, err := time.Parse(time.DateOnly, id)
	if err != nil {
		return id
	}
	return t.Format("Jan 2")
}

func byViews(rows []statsRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Views != rows[j].Views {
			return rows[i].Views > rows[j].Views
		}
		return rows[i].Label < rows[j].Label
	})
}

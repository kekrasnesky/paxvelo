package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The Newsletter is the club's monthly newsletter. Issues are written in the
// admin portal (Newsletter tab) and stored with the rest of the club data.
// Published issues are rendered whenever something changes (including the
// ride calendar, which the rides block shows live) and served from memory
// like every other page, at /newsletter (the latest issue) and
// /newsletter/2026-10 (each issue's permanent address).
//
// An issue is a cover, a list of blocks the admin adds, orders, and removes
// (stories, banners, the rides from the calendar, photos, Instagram posts),
// and who publishes it.

const newsName = "PaxVelo Newsletter"

type Story struct {
	Kicker      string `json:"kicker,omitempty"` // the small red line above the heading
	Heading     string `json:"heading,omitempty"`
	Subhead     string `json:"subhead,omitempty"`
	Body        string `json:"body,omitempty"` // same simple formatting as content/*.md
	Photo       string `json:"photo,omitempty"`
	PhotoFocus  string `json:"photo_focus,omitempty"` // the spot to keep in view when cropped, like "50% 20%"
	VideoURL    string `json:"video_url,omitempty"`   // the photo becomes a play button linking here
	VideoLength string `json:"video_length,omitempty"`
	LinkLabel   string `json:"link_label,omitempty"`
	LinkURL     string `json:"link_url,omitempty"`
	Caption     string `json:"caption,omitempty"` // a short line under the photo
}

// Block is one part of an issue. Which fields it uses depends on its kind.
type Block struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // see blockKinds
	Story
	Photos []string          `json:"photos,omitempty"` // a photo strip
	Focus  map[string]string `json:"focus,omitempty"`  // photo address → focus, for a photo strip
	Posts  []string          `json:"posts,omitempty"`  // Instagram post links
	Event  string            `json:"event,omitempty"`  // an event's calendar key, like "r12"
}

type blockKind struct{ Kind, Name, Help string }

var blockKinds = []blockKind{
	{"big", "Big story", "A story with a large photo or video beside it. Good for the lead story."},
	{"story", "Story", "A column with a photo. Stories next to each other share a row, up to three across."},
	{"rides", "Rides from the calendar", "The club’s rides for the 5 weeks after the issue is published, filled in from the calendar. Rides that need leaders get a sign-up link."},
	{"event", "Event from the calendar", "A meeting, party or other club event from the calendar: its day, time, place and host, with buttons to add it to a calendar. It follows the calendar if the event moves or is cancelled."},
	{"banner", "Banner", "A dark band across the page with a button. For one-off asks, like a kit order, a fundraiser, or volunteers."},
	{"gallery", "Photo strip", "A row of up to 8 photos."},
	{"share", "Share your photos", "A box asking readers to send in their ride photos, with a button to the upload page. It goes under the photo strip."},
	{"instagram", "Instagram posts", "Up to 3 posts from the club’s Instagram."},
}

func kindOf(kind string) (blockKind, bool) {
	for _, k := range blockKinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return blockKind{}, false
}

// Name is how the editor and error messages refer to the block.
func (b Block) Name() string {
	k, _ := kindOf(b.Kind)
	if b.Heading != "" {
		return k.Name + " “" + b.Heading + "”"
	}
	return k.Name
}

type Issue struct {
	ID           int        `json:"id"`
	Month        int        `json:"month"` // 1-12; a month can have several drafts but only one published issue
	Year         int        `json:"year"`
	CreatedBy    string     `json:"created_by,omitempty"`
	EditedBy     string     `json:"edited_by,omitempty"`
	Tagline      string     `json:"tagline,omitempty"`
	Cover        string     `json:"cover,omitempty"`
	CoverFocus   string     `json:"cover_focus,omitempty"`
	CoverCaption string     `json:"cover_caption,omitempty"`
	Blocks       []Block    `json:"blocks,omitempty"`
	Published    *time.Time `json:"published,omitempty"`
	Created      time.Time  `json:"created,omitzero"` // when it was started; zero for issues from before this was kept
	Updated      time.Time  `json:"updated"`
	oldIssue
}

func (is Issue) block(id string) Block {
	for _, b := range is.Blocks {
		if b.ID == id {
			return b
		}
	}
	return Block{}
}

// oldIssue is how issues were stored before blocks: a fixed set of sections.
// Issues saved that way are turned into blocks when the data is loaded.
type oldIssue struct {
	Featured     *Story            `json:"featured,omitempty"`
	Recap        *Story            `json:"recap,omitempty"`
	Spotlight    *Story            `json:"spotlight,omitempty"`
	Corner       []oldCorner       `json:"corner,omitempty"`
	Kit          *Story            `json:"kit,omitempty"`
	Gallery      []string          `json:"gallery,omitempty"`
	GalleryFocus map[string]string `json:"gallery_focus,omitempty"`
	Instagram    []string          `json:"instagram,omitempty"`
	Hidden       []string          `json:"hidden,omitempty"`
	OldRides     json.RawMessage   `json:"rides,omitempty"`
}

type oldCorner struct {
	Story
	Off bool `json:"off,omitempty"`
}

// upgrade turns an issue saved with fixed sections into blocks.
func (is *Issue) upgrade() {
	o := is.oldIssue
	if is.Blocks != nil || (o.Featured == nil && o.Recap == nil && o.Spotlight == nil && o.Corner == nil && o.Kit == nil && o.Gallery == nil && o.OldRides == nil) {
		return
	}
	shows := func(key string) bool {
		for _, h := range o.Hidden {
			if h == key {
				return false
			}
		}
		return true
	}
	add := func(kind string, st *Story) {
		if st != nil && (st.Heading != "" || st.Body != "" || st.Photo != "") {
			is.Blocks = append(is.Blocks, Block{ID: randHex(4), Kind: kind, Story: *st})
		}
	}
	if shows("featured") {
		add("big", o.Featured)
	}
	if shows("recap") {
		add("story", o.Recap)
	}
	if shows("rides") {
		is.Blocks = append(is.Blocks, Block{ID: randHex(4), Kind: "rides", Story: Story{Heading: ridesHeading}})
	}
	if shows("spotlight") {
		add("story", o.Spotlight)
	}
	if shows("corner") {
		for _, c := range o.Corner {
			if !c.Off {
				st := c.Story
				add("story", &st)
			}
		}
	}
	if shows("kit") {
		add("banner", o.Kit)
	}
	if shows("instagram") && len(o.Instagram) > 0 {
		is.Blocks = append(is.Blocks, Block{ID: randHex(4), Kind: "instagram", Posts: o.Instagram})
	}
	if shows("gallery") && len(o.Gallery) > 0 {
		is.Blocks = append(is.Blocks, Block{ID: randHex(4), Kind: "gallery", Photos: o.Gallery, Focus: o.GalleryFocus})
	}
	if is.Blocks == nil {
		is.Blocks = []Block{}
	}
	is.oldIssue = oldIssue{}
}

// monthOption is one choice in the admin's month menus.
type monthOption struct {
	N    int
	Name string
}

var months = func() []monthOption {
	var out []monthOption
	for m := time.January; m <= time.December; m++ {
		out = append(out, monthOption{int(m), m.String()})
	}
	return out
}()

func (is Issue) Slug() string { return fmt.Sprintf("%d-%02d", is.Year, is.Month) }
func (is Issue) Path() string { return "/newsletter/" + is.Slug() }
func (is Issue) Label() string {
	if is.Month < 1 || is.Month > 12 {
		return strconv.Itoa(is.Year)
	}
	return time.Month(is.Month).String() + " " + strconv.Itoa(is.Year)
}
func (is Issue) IsPublished() bool {
	return is.Published != nil
}

// order sorts issues by the month they cover, newest last.
func (is Issue) order() int { return is.Year*100 + is.Month }

func validMonth(month, year int) bool {
	return month >= 1 && month <= 12 && year >= 2000 && year <= 2100
}

func nextMonth(month, year int) (int, int) {
	if month == 12 {
		return 1, year + 1
	}
	return month + 1, year
}

// ---- Store ----

func cloneIssue(is *Issue) Issue {
	var out Issue
	b, _ := json.Marshal(is)
	json.Unmarshal(b, &out)
	return out
}

// upgradeIssues turns issues saved with fixed sections into blocks.
// Callers must hold s.mu.
func (s *Store) upgradeIssues() {
	for _, is := range s.d.Issues {
		is.upgrade()
		// The rides block used to be "Up next / Rides coming up".
		for i, b := range is.Blocks {
			if b.Kind == "rides" && b.Kicker == "Up next" && b.Heading == "Rides coming up" {
				is.Blocks[i].Kicker, is.Blocks[i].Heading = "", ridesHeading
			}
		}
	}
}

// Issues returns every issue, newest month first. Within a month the
// published issue comes first, then drafts by most recently edited.
func (s *Store) Issues() []Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Issue, 0, len(s.d.Issues))
	for _, is := range s.d.Issues {
		out = append(out, cloneIssue(is))
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.order() != b.order() {
			return a.order() > b.order()
		}
		if a.IsPublished() != b.IsPublished() {
			return a.IsPublished()
		}
		return a.Updated.After(b.Updated)
	})
	return out
}

// liveFor returns the published issue for a month, other than the one with
// the given ID. Callers must hold s.mu.
func (s *Store) liveFor(month, year, exceptID int) *Issue {
	for _, is := range s.d.Issues {
		if is.ID != exceptID && is.Month == month && is.Year == year && is.IsPublished() {
			return is
		}
	}
	return nil
}

// LiveFor returns the published issue for a month, other than exceptID.
func (s *Store) LiveFor(month, year, exceptID int) (Issue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if is := s.liveFor(month, year, exceptID); is != nil {
		return cloneIssue(is), true
	}
	return Issue{}, false
}

func (s *Store) Issue(id int) (Issue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, is := range s.d.Issues {
		if is.ID == id {
			return cloneIssue(is), true
		}
	}
	return Issue{}, false
}

var errMonthTaken = errors.New("another issue for that month is already published")

// SaveIssue adds a new issue (ID 0) or replaces an existing one. A month can
// have any number of drafts, but only one published issue.
func (s *Store) SaveIssue(in Issue) (Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.IsPublished() && s.liveFor(in.Month, in.Year, in.ID) != nil {
		return in, errMonthTaken
	}
	in.Updated = time.Now().UTC()
	if in.ID == 0 {
		in.Created = in.Updated
		s.d.NextIssue = max(s.d.NextIssue, 1)
		in.ID = s.d.NextIssue
		s.d.NextIssue++
		c := cloneIssue(&in)
		s.d.Issues = append(s.d.Issues, &c)
		return in, s.save()
	}
	for i, is := range s.d.Issues {
		if is.ID == in.ID {
			c := cloneIssue(&in)
			s.d.Issues[i] = &c
			return in, s.save()
		}
	}
	return in, errors.New("that issue no longer exists")
}

func (s *Store) DeleteIssue(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, is := range s.d.Issues {
		if is.ID == id {
			s.d.Issues = append(s.d.Issues[:i], s.d.Issues[i+1:]...)
			return s.save()
		}
	}
	return nil
}

// ---- The rides block ----

// rideWeeks is how far ahead the rides block looks, from the day the issue
// is published (or today, for a draft).
const rideWeeks = 5

// ridesHeading is the rides block's heading unless an admin changes it.
const ridesHeading = "Upcoming Rides"

// rideWindow is the first and last day the issue's rides block covers.
func (a *App) rideWindow(is Issue) (time.Time, time.Time) {
	from := a.today()
	if is.Published != nil {
		p := is.Published.In(a.cfg.Location)
		from = time.Date(p.Year(), p.Month(), p.Day(), 0, 0, 0, 0, time.UTC)
	}
	return from, from.AddDate(0, 0, rideWeeks*7-1)
}

// issueRides is what the rides block shows: every ride in the window that
// isn't cancelled.
func (a *App) issueRides(is Issue) ridesView {
	from, to := a.rideWindow(is)
	today := a.today().Format("2006-01-02")
	v := ridesView{From: from.Format("Jan 2"), To: to.Format("Jan 2"), CalendarURL: "/calendar?m=" + from.Format("2006-01"),
		Events: map[string]shownEvent{}}
	// Events with a block of their own aren't listed again with the rides.
	for _, b := range is.Blocks {
		if b.Kind == "event" && b.Event != "" {
			if o, ok := a.store.Ride(b.Event); ok && !o.Held {
				v.Events[b.Event] = eventShown(o, today)
			}
		}
	}
	for _, o := range a.store.Rides(from, to) {
		if _, own := v.Events[o.Key]; o.Cancelled || own {
			continue
		}
		t, _ := time.Parse("2006-01-02", o.Date)
		sr := shownRide{Key: o.Key, Month: strings.ToUpper(t.Format("Jan")), Day: t.Format("2"), Weekday: t.Format("Monday"),
			Name: o.Name, Time: o.TimeLabel(), Place: o.Place, Details: firstLine(o.Details), Past: o.Date < today}
		if !o.IsRide() { // nobody signs up to lead a clinic or a meeting
			sr.Kind = o.KindName()
			if l := o.leaderFor(""); l != nil {
				sr.Host = o.HostLabel() + ": " + l.Name
			}
			v.Rides = append(v.Rides, sr)
			continue
		}
		for _, sl := range o.Slots() {
			ss := shownSlot{Group: sl.Group}
			if sl.Leader != nil {
				ss.Leader = sl.Leader.Name
			} else {
				sr.Open++
			}
			sr.Slots = append(sr.Slots, ss)
		}
		if sr.Open > 0 && !sr.Past {
			v.Need++
		}
		v.Rides = append(v.Rides, sr)
	}
	return v
}

type ridesView struct {
	From, To    string // like "Oct 24"
	CalendarURL string
	Rides       []shownRide
	Need        int                   // rides still to come that need a leader
	Events      map[string]shownEvent // the issue's Event blocks, by calendar key
}

// shownEvent is a calendar event in an issue's Event block.
type shownEvent struct {
	Key, Kind, Name      string
	Month, Day, When     string // When is like "Thursday, November 20 · 6:00 PM"
	Place, Host, Details string
	Cancelled, Past      bool
}

func eventShown(o RideOcc, today string) shownEvent {
	t, _ := time.Parse("2006-01-02", o.Date)
	e := shownEvent{Key: o.Key, Kind: o.KindName(), Name: o.Name, Month: strings.ToUpper(t.Format("Jan")), Day: t.Format("2"),
		When: t.Format("Monday, January 2"), Place: o.Place, Details: o.Details, Cancelled: o.Cancelled, Past: o.Date < today}
	if o.Time != "" {
		e.When += " · " + o.TimeLabel()
	}
	if l := o.leaderFor(""); l != nil && !o.IsRide() {
		e.Host = o.HostLabel() + ": " + l.Name
	}
	return e
}

// eventChoice is one event to pick in an Event block.
type eventChoice struct{ Key, Label string }

// eventChoices are the events an issue's Event blocks can show: club events
// (not rides) from a month back to six months ahead, and any already picked.
func (a *App) eventChoices(is Issue) []eventChoice {
	today := a.today()
	var out []eventChoice
	label := func(o RideOcc) string {
		t, _ := time.Parse("2006-01-02", o.Date)
		s := t.Format("Mon Jan 2") + " · " + o.KindName() + ": " + o.Name
		if o.Cancelled {
			s += " (cancelled)"
		}
		return s
	}
	for _, o := range a.store.Rides(today.AddDate(0, -1, 0), today.AddDate(0, 6, 0)) {
		if !o.IsRide() {
			out = append(out, eventChoice{o.Key, label(o)})
		}
	}
	for _, b := range is.Blocks {
		if b.Kind == "event" && b.Event != "" && !slices.ContainsFunc(out, func(c eventChoice) bool { return c.Key == b.Event }) {
			if o, ok := a.store.Ride(b.Event); ok {
				out = append(out, eventChoice{o.Key, label(o)})
			}
		}
	}
	return out
}

// addEventBlock puts an event in an issue, after its rides if it has them.
// It reports false when the event is in the issue already.
func addEventBlock(is *Issue, key string) bool {
	if slices.ContainsFunc(is.Blocks, func(b Block) bool { return b.Kind == "event" && b.Event == key }) {
		return false
	}
	at := len(is.Blocks)
	if i := slices.IndexFunc(is.Blocks, func(b Block) bool { return b.Kind == "rides" }); i >= 0 {
		at = i + 1
	}
	is.Blocks = slices.Insert(is.Blocks, at, Block{ID: randHex(4), Kind: "event", Event: key})
	return true
}

type shownRide struct {
	Key, Month, Day, Weekday string
	Name, Time, Place        string
	Details                  string
	Kind, Host               string // for a clinic, a meeting, and so on: "Club meeting", "Host: Sam"
	Slots                    []shownSlot
	Open                     int // spots without a leader
	Past                     bool
}

type shownSlot struct{ Group, Leader string }

// ---- Public pages ----

type shownStory struct {
	Story
	ID   string // the anchor "In this issue" links to
	Body template.HTML
	Link string // resolved, with {kit} and friends filled in
}

// tocItem is one line of "In this issue".
type tocItem struct{ ID, Heading string }

func (st shownStory) Show() bool { return st.Heading != "" || st.Body != "" || st.Photo != "" }

type shownPhoto struct{ URL, Focus string }

// newsPart is one piece of the page: a block, or a row of stories side by side.
type newsPart struct {
	Kind   string // big, row, rides, event, banner, gallery, instagram
	Story  shownStory
	Row    []shownStory
	Rides  ridesView
	Event  shownEvent
	Photos []shownPhoto
	Posts  []string
}

type newsView struct {
	Empty, Preview bool
	ID             int
	Published      bool
	Label, Tagline string
	Cover          string
	CoverFocus     string
	CoverCaption   string
	Contents       []tocItem // "In this issue", shown when there are at least two
	Parts          []newsPart
	Archive        []Issue
	Current        string // path of the issue being shown, to mark it in the archive
}

// newsView lays out an issue for its page. rides is what its rides block shows.
func (s *Site) newsView(is Issue, archive []Issue, rides ridesView) (newsView, error) {
	v := newsView{
		ID: is.ID, Published: is.IsPublished(), Label: is.Label(), Tagline: is.Tagline, Cover: is.Cover, CoverFocus: is.CoverFocus,
		CoverCaption: is.CoverCaption, Archive: archive, Current: is.Path(),
	}
	story := func(b Block) (shownStory, error) {
		out := shownStory{Story: b.Story, ID: "s-" + b.ID}
		if strings.TrimSpace(b.Body) != "" {
			_, intro, body, err := s.md.render(b.Body, 1, pageStyle{})
			if err != nil {
				return out, fmt.Errorf("%s: %w", b.Name(), err)
			}
			out.Body = intro + body
		}
		link, err := s.md.url(strings.TrimSpace(b.LinkURL))
		if err != nil {
			return out, fmt.Errorf("%s, link: %w", b.Name(), err)
		}
		out.Link = link
		return out, nil
	}
	for _, b := range is.Blocks {
		switch b.Kind {
		case "big", "story", "banner":
			st, err := story(b)
			if err != nil {
				return v, err
			}
			if !st.Show() {
				continue
			}
			if st.Heading != "" {
				v.Contents = append(v.Contents, tocItem{st.ID, st.Heading})
			}
			if b.Kind != "story" {
				v.Parts = append(v.Parts, newsPart{Kind: b.Kind, Story: st})
				continue
			}
			// Stories next to each other share a row, up to three across.
			if n := len(v.Parts); n > 0 && v.Parts[n-1].Kind == "row" && len(v.Parts[n-1].Row) < 3 {
				v.Parts[n-1].Row = append(v.Parts[n-1].Row, st)
			} else {
				v.Parts = append(v.Parts, newsPart{Kind: "row", Row: []shownStory{st}})
			}
		case "share":
			st, err := story(b)
			if err != nil {
				return v, err
			}
			v.Parts = append(v.Parts, newsPart{Kind: "share", Story: st})
		case "rides":
			p := newsPart{Kind: "rides", Story: shownStory{Story: b.Story, ID: "s-" + b.ID}, Rides: rides}
			v.Parts = append(v.Parts, p)
			v.Contents = append(v.Contents, tocItem{p.Story.ID, or(b.Heading, ridesHeading)})
		case "event":
			e, ok := rides.Events[b.Event]
			if !ok {
				continue
			}
			st, err := story(b)
			if err != nil {
				return v, err
			}
			v.Parts = append(v.Parts, newsPart{Kind: "event", Story: st, Event: e})
			v.Contents = append(v.Contents, tocItem{st.ID, e.Name})
		case "gallery":
			p := newsPart{Kind: "gallery"}
			for _, g := range b.Photos {
				p.Photos = append(p.Photos, shownPhoto{g, b.Focus[g]})
			}
			if len(p.Photos) > 0 {
				v.Parts = append(v.Parts, p)
			}
		case "instagram":
			if len(b.Posts) > 0 {
				v.Parts = append(v.Parts, newsPart{Kind: "instagram", Posts: b.Posts})
			}
		}
	}
	if len(v.Contents) < 2 {
		v.Contents = nil
	}
	return v, nil
}

// issueView lays out an issue with its rides from the calendar.
func (a *App) issueView(is Issue, archive []Issue) (newsView, error) {
	return a.site.newsView(is, archive, a.issueRides(is))
}

func (s *Site) renderNews(v newsView, path, title string) ([]byte, error) {
	var buf bytes.Buffer
	view := s.view(path, title, v)
	view.Desc = "The PaxVelo Newsletter, every month: stories from the road, upcoming rides, and club news."
	if err := s.dynamic["newsletter"].ExecuteTemplate(&buf, "layout", view); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// refreshNewsletter re-renders every published issue. An issue that fails to
// render keeps its previous page, so a mistake never takes the newsletter down.
func (a *App) refreshNewsletter() {
	// One refresh at a time, so an older snapshot can never replace a newer one.
	a.site.newsRefresh.Lock()
	defer a.site.newsRefresh.Unlock()
	version, day := a.store.Version(), a.today()
	var published []Issue
	for _, is := range a.store.Issues() {
		if is.IsPublished() {
			published = append(published, is)
		}
	}
	a.site.newsMu.RLock()
	old := a.site.news
	a.site.newsMu.RUnlock()
	pages := map[string]*Page{}
	page := func(path string, b []byte) *Page {
		return &Page{Path: path, html: b, gz: gzipBytes(b), etag: etag(b)}
	}
	for i, is := range published {
		v, err := a.issueView(is, published)
		var b []byte
		if err == nil {
			b, err = a.site.renderNews(v, is.Path(), "Newsletter, "+is.Label()+" — "+siteName)
		}
		if err != nil {
			log.Printf("newsletter %s: %v", is.Slug(), err)
			if p := old[is.Path()]; p != nil {
				pages[is.Path()] = p
			}
			continue
		}
		pages[is.Path()] = page(is.Path(), b)
		if i == 0 {
			// The latest issue, at the address flyers and emails can always use.
			v.Current = is.Path()
			if b, err = a.site.renderNews(v, "/newsletter", "Newsletter — "+siteName); err == nil {
				pages["/newsletter"] = page("/newsletter", b)
			}
		}
	}
	if pages["/newsletter"] == nil {
		if p := old["/newsletter"]; p != nil && len(published) > 0 {
			pages["/newsletter"] = p
		} else if b, err := a.site.renderNews(newsView{Empty: true}, "/newsletter", "Newsletter — "+siteName); err == nil {
			pages["/newsletter"] = page("/newsletter", b)
		}
	}
	a.site.newsMu.Lock()
	a.site.news, a.site.newsVersion, a.site.newsDay = pages, version, day
	a.site.newsMu.Unlock()
}

func (a *App) newsletterPage(w http.ResponseWriter, r *http.Request) {
	path := "/newsletter"
	if slug := r.PathValue("slug"); slug != "" {
		path += "/" + slug
	}
	// The rides block shows the calendar as it is now, so re-render when the
	// club's data (or the date) has changed since the last render.
	a.site.newsMu.RLock()
	stale := a.site.newsVersion != a.store.Version() || !a.site.newsDay.Equal(a.today())
	a.site.newsMu.RUnlock()
	if stale {
		a.refreshNewsletter()
	}
	a.site.newsMu.RLock()
	p := a.site.news[path]
	a.site.newsMu.RUnlock()
	if p == nil {
		a.site.notFound(w, r)
		return
	}
	a.site.servePage(p).ServeHTTP(w, r)
}

// ---- Photos uploaded in the admin ----

// maxPhoto is the most one photo can be. Pages shrink photos before they
// upload (web/static/photos.js), so this is for the rare one that couldn't be.
const maxPhoto = 20 << 20

var photoTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}

func (a *App) mediaDir() string { return filepath.Join(a.cfg.DataDir, "media") }

// savePhoto stores the photo uploaded in a form field and returns its
// address, or "" if no file was chosen.
func (a *App) savePhoto(r *http.Request, field string) (string, error) {
	if r.MultipartForm == nil || len(r.MultipartForm.File[field]) == 0 {
		return "", nil
	}
	return a.savePhotoFile(r.MultipartForm.File[field][0])
}

// readPhotoFile reads one uploaded photo, shrunk to a size that loads
// quickly (see shrinkPhoto), with its file extension. It returns nil for an
// empty file.
func readPhotoFile(hdr *multipart.FileHeader) ([]byte, string, error) {
	if hdr.Size == 0 {
		return nil, "", nil
	}
	f, err := hdr.Open()
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPhoto+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > maxPhoto {
		return nil, "", fmt.Errorf("%s is over 20 MB. Make it smaller at squoosh.app and try again", hdr.Filename)
	}
	ext, ok := photoTypes[http.DetectContentType(b)]
	if !ok {
		return nil, "", fmt.Errorf("%s isn’t a photo this site can show. Use JPG, PNG, or WebP", hdr.Filename)
	}
	b, ext = shrinkPhoto(b, ext)
	return b, ext, nil
}

// savePhotoFile stores one uploaded photo under a random name, shrunk to a
// size that loads quickly (see shrinkPhoto).
func (a *App) savePhotoFile(hdr *multipart.FileHeader) (string, error) {
	b, ext, err := readPhotoFile(hdr)
	if err != nil || b == nil {
		return "", err
	}
	if err := os.MkdirAll(a.mediaDir(), 0o755); err != nil {
		return "", err
	}
	name := randHex(8) + ext
	if err := os.WriteFile(filepath.Join(a.mediaDir(), name), b, 0o644); err != nil {
		return "", err
	}
	return "/media/" + name, nil
}

// uploadNewsPhotos saves photos the moment they're chosen on the edit page,
// so they show (and can be framed or removed) right away. They join the
// issue when it's saved.
func (a *App) uploadNewsPhotos(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxGallery*(maxPhoto+(1<<20)))
	w.Header().Set("Content-Type", "application/json")
	reply := func(status int, v any) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		reply(http.StatusBadRequest, map[string]string{"error": "Those photos are too big together. Add a few at a time."})
		return
	}
	var urls, problems []string
	for _, fh := range r.MultipartForm.File["photos"] {
		if len(urls) >= maxGallery {
			break
		}
		u, err := a.savePhotoFile(fh)
		if err != nil {
			problems = append(problems, err.Error()+".")
		} else if u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 && len(problems) == 0 {
		problems = append(problems, "Choose a photo first.")
	}
	reply(http.StatusOK, map[string]any{"urls": urls, "error": strings.Join(problems, " ")})
}

// ---- Admin ----

const maxBlocks, maxGallery, maxNewsInstagram, maxNewsCaption = 24, 8, 3, 160

// addToStrip adds photos to an issue's first photo strip, or to a new one at
// the end with the box asking for photos under it, and returns how many went
// in and how many didn't fit. Photos already in the strip are left out.
func addToStrip(is *Issue, urls []string) (added, full int) {
	strip := slices.IndexFunc(is.Blocks, func(b Block) bool { return b.Kind == "gallery" })
	if strip < 0 {
		is.Blocks = append(is.Blocks, Block{ID: randHex(4), Kind: "gallery"})
		strip = len(is.Blocks) - 1
		if !slices.ContainsFunc(is.Blocks, func(b Block) bool { return b.Kind == "share" }) {
			is.Blocks = append(is.Blocks, Block{ID: randHex(4), Kind: "share"})
		}
	}
	b := &is.Blocks[strip]
	for _, u := range urls {
		switch {
		case containsStr(b.Photos, u):
		case len(b.Photos) >= maxGallery:
			full++
		default:
			b.Photos = append(b.Photos, u)
			added++
		}
	}
	return added, full
}

func (a *App) newsletterAdmin(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Newsletter", Tab: "newsletter", Issues: a.store.Issues(), Months: months}
	v.Flash, v.Error = flashFrom(r)
	// Suggest this month, or the month after the latest published issue.
	now := time.Now().In(a.cfg.Location)
	v.NewMonth, v.NewYear = int(now.Month()), now.Year()
	for _, is := range v.Issues {
		if is.IsPublished() {
			if is.order() >= v.NewYear*100+v.NewMonth {
				v.NewMonth, v.NewYear = nextMonth(is.Month, is.Year)
			}
			break
		}
	}
	a.renderAdmin(w, r, http.StatusOK, "newsletter", v)
}

// carryOver is how a new draft starts from the last issue: the same blocks
// (except one-off banners), keeping the regular features' headings and links
// but not last month's text and photos.
func carryOver(blocks []Block) []Block {
	out := []Block{}
	for _, b := range blocks {
		if b.Kind == "banner" {
			continue
		}
		n := Block{ID: randHex(4), Kind: b.Kind}
		n.Kicker, n.Heading, n.LinkLabel, n.LinkURL = b.Kicker, b.Heading, b.LinkLabel, b.LinkURL
		if b.Kind == "share" {
			n.Body = b.Body // its standing ask, not news
		}
		out = append(out, n)
	}
	// The box asking for photos goes under the photo strip, including in
	// issues that follow one from before it existed.
	if !slices.ContainsFunc(out, func(b Block) bool { return b.Kind == "share" }) {
		if i := slices.IndexFunc(out, func(b Block) bool { return b.Kind == "gallery" }); i >= 0 {
			out = slices.Insert(out, i+1, Block{ID: randHex(4), Kind: "share"})
		}
	}
	return out
}

// createIssue starts a new draft. It follows the latest published issue (or
// the latest draft), so the regular features carry over and only the news
// needs writing; the very first issue starts from a sample.
func (a *App) createIssue(w http.ResponseWriter, r *http.Request) {
	month, _ := strconv.Atoi(r.FormValue("month"))
	year, _ := strconv.Atoi(r.FormValue("year"))
	if !validMonth(month, year) {
		back(w, r, "/admin/newsletter?err=month")
		return
	}
	is := sampleIssue()
	if list := a.store.Issues(); len(list) > 0 {
		last := list[0]
		for _, x := range list {
			if x.IsPublished() {
				last = x
				break
			}
		}
		is = Issue{Tagline: last.Tagline, Cover: last.Cover, CoverFocus: last.CoverFocus, Blocks: carryOver(last.Blocks)}
	}
	me, _ := a.adminName(r)
	is.Month, is.Year, is.CreatedBy, is.EditedBy = month, year, me, me
	is, err := a.store.SaveIssue(is)
	if err != nil {
		back(w, r, "/admin/newsletter?err=save")
		return
	}
	back(w, r, fmt.Sprintf("/admin/newsletter/%d?ok=created", is.ID))
}

// copyIssue starts a new draft for the same month from an existing issue,
// for trying a different version.
func (a *App) copyIssue(w http.ResponseWriter, r *http.Request) {
	is, ok := a.issueFromPath(w, r)
	if !ok {
		return
	}
	me, _ := a.adminName(r)
	is.ID, is.Published, is.CreatedBy, is.EditedBy, is.Created = 0, nil, me, me, time.Time{}
	is, err := a.store.SaveIssue(is)
	if err != nil {
		back(w, r, "/admin/newsletter?err=save")
		return
	}
	back(w, r, fmt.Sprintf("/admin/newsletter/%d?ok=copied", is.ID))
}

func (a *App) issueFromPath(w http.ResponseWriter, r *http.Request) (Issue, bool) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	is, ok := a.store.Issue(id)
	if !ok {
		back(w, r, "/admin/newsletter?err=gone")
	}
	return is, ok
}

func (a *App) editIssuePage(w http.ResponseWriter, r *http.Request) {
	is, ok := a.issueFromPath(w, r)
	if !ok {
		return
	}
	a.renderIssueForm(w, r, is, "")
}

func (a *App) renderIssueForm(w http.ResponseWriter, r *http.Request, is Issue, problem string) {
	v := adminView{Title: is.Label() + " newsletter", Tab: "newsletter", Issue: is, Months: months, IssueRides: a.issueRides(is), EventChoices: a.eventChoices(is)}
	// Emailing it: the groups to send to, who to send a test to, and when it went out.
	v.Audiences, v.IssueSends, v.Live, v.Year = a.store.emailAudiences(a.cfg.ThisYear()), a.issueCampaigns(is.ID), a.mailer.live(), a.cfg.ThisYear()
	v.TestTo = a.cfg.AdminEmail
	if u, ok := a.currentAdmin(r); ok && u.Email != "" {
		v.TestTo = u.Email
	}
	if live, ok := a.store.LiveFor(is.Month, is.Year, is.ID); ok {
		v.MonthLive = &live
	}
	v.Flash, v.Error = flashFrom(r)
	if problem != "" {
		v.Flash, v.Error = "", problem
	} else if _, err := a.issueView(is, nil); err != nil {
		v.Error = "Fix this before publishing: " + err.Error()
	}
	a.renderAdmin(w, r, http.StatusOK, "issue", v)
}

var blockIDRE = regexp.MustCompile(`^[0-9a-f]{4,16}$`)

// saveIssue handles the edit form. Text is saved even when something needs
// fixing, so nobody loses their writing.
func (a *App) saveIssue(w http.ResponseWriter, r *http.Request) {
	old, ok := a.issueFromPath(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 80<<20)
	if err := r.ParseMultipartForm(16 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		a.renderIssueForm(w, r, old, "The upload was too big. Add a few photos at a time.")
		return
	}
	is := old
	var problems []string
	note := func(err error) {
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	val := func(name string) string { return strings.TrimSpace(r.FormValue(name)) }
	// The form carries each photo's current address (photos upload as soon as
	// they're picked), so photos aren't lost when an edit is fixed and saved again.
	photo := func(prefix, current string) string {
		if v, ok := r.Form[prefix+"photo_current"]; ok {
			current = ""
			if len(v) > 0 && sitePhoto(v[0]) {
				current = v[0]
			}
		}
		u, err := a.savePhoto(r, prefix+"photo_file")
		note(err)
		if u != "" {
			return u
		}
		return current
	}
	// A new photo starts centered; otherwise keep the spot picked on the form.
	focusFor := func(prefix string) string {
		if r.MultipartForm != nil && len(r.MultipartForm.File[prefix+"photo_file"]) > 0 && r.MultipartForm.File[prefix+"photo_file"][0].Size > 0 {
			return ""
		}
		return cleanFocus(val(prefix + "photo_focus"))
	}
	story := func(prefix string, cur Story) Story {
		return Story{
			Kicker: val(prefix + "kicker"), Heading: val(prefix + "heading"), Subhead: val(prefix + "subhead"),
			Body: strings.ReplaceAll(val(prefix+"body"), "\r\n", "\n"), Photo: photo(prefix, cur.Photo),
			PhotoFocus: focusFor(prefix),
			VideoURL:   val(prefix + "video_url"), VideoLength: val(prefix + "video_length"),
			LinkLabel: val(prefix + "link_label"), LinkURL: val(prefix + "link_url"), Caption: val(prefix + "caption"),
		}
	}
	month, _ := strconv.Atoi(r.FormValue("month"))
	year, _ := strconv.Atoi(r.FormValue("year"))
	if validMonth(month, year) {
		is.Month, is.Year = month, year
	}
	is.EditedBy, _ = a.adminName(r)
	is.Tagline = val("tagline")
	is.Cover = photo("cover.", old.Cover)
	is.CoverFocus = focusFor("cover.")
	is.CoverCaption = val("cover.caption")

	// The blocks, in the order the form lists them ("id:kind"). Each block's
	// fields start with "b.<id>.". Only the edit form sends them (with
	// blocks_form), so other posts leave the blocks as they were.
	var entries []string
	if r.FormValue("blocks_form") != "" {
		is.Blocks, entries = []Block{}, r.Form["block"]
	}
	for _, entry := range entries {
		id, kind, _ := strings.Cut(entry, ":")
		if _, known := kindOf(kind); !known || !blockIDRE.MatchString(id) || is.block(id).ID != "" || len(is.Blocks) >= maxBlocks {
			continue
		}
		p, cur := "b."+id+".", old.block(id)
		b := Block{ID: id, Kind: kind}
		switch kind {
		case "big", "story", "banner":
			b.Story = story(p, cur.Story)
		case "rides":
			b.Kicker, b.Heading = val(p+"kicker"), val(p+"heading")
		case "event":
			if o, ok := a.store.Ride(val(p + "event")); ok {
				b.Event = o.Key
			} else {
				b.Event = cur.Event
			}
			b.Body = strings.ReplaceAll(val(p+"body"), "\r\n", "\n")
		case "share":
			b.Heading, b.LinkLabel = val(p+"heading"), val(p+"link_label")
			b.Body = strings.ReplaceAll(val(p+"body"), "\r\n", "\n")
		case "gallery":
			focus := r.Form[p+"focus"]
			b.Focus = map[string]string{}
			for i, g := range r.Form[p+"photos"] {
				if !sitePhoto(g) || len(b.Photos) >= maxGallery || containsStr(b.Photos, g) {
					continue
				}
				b.Photos = append(b.Photos, g)
				if i < len(focus) {
					if f := cleanFocus(focus[i]); f != "" {
						b.Focus[g] = f
					}
				}
			}
		case "instagram":
			posts, err := parseInstagramPosts(val(p+"posts"), maxNewsInstagram)
			note(err)
			if err == nil {
				b.Posts = posts
			} else {
				b.Posts = cur.Posts
			}
		}
		is.Blocks = append(is.Blocks, b)
	}

	// The save buttons can also publish or preview, so neither ever skips
	// changes that haven't been saved yet.
	then := r.URL.Query().Get("then")
	_, renderErr := a.issueView(is, nil)
	if renderErr != nil && (is.IsPublished() || then == "publish") {
		// A live issue only ever holds a version that can be shown: otherwise the
		// page would disappear the next time the site restarts. Keep the edit in
		// the form instead, ready to fix.
		problems = append(problems, "Not saved yet, so the live newsletter is unchanged. Fix this and save again: "+renderErr.Error())
		a.renderIssueForm(w, r, is, strings.Join(problems, " "))
		return
	}
	if then == "publish" && !is.IsPublished() && len(problems) == 0 {
		now := time.Now().UTC()
		is.Published = &now
	}
	saved, err := a.store.SaveIssue(is)
	if errors.Is(err, errMonthTaken) {
		// Only one issue per month can be live. Save the edit, but as a draft
		// (or, for the live issue, without the month change).
		live, _ := a.store.LiveFor(is.Month, is.Year, is.ID)
		msg := fmt.Sprintf("%s already has a published issue (edited by %s). ", is.Label(), or(live.EditedBy, "someone"))
		if old.IsPublished() {
			is.Month, is.Year = old.Month, old.Year
			msg += "Your changes were saved, but the month wasn’t changed."
		} else {
			is.Published = nil
			msg += "This one was saved as a draft. To publish it instead, unpublish the other one first."
		}
		problems = append(problems, msg)
		saved, err = a.store.SaveIssue(is)
	}
	if err != nil {
		a.renderIssueForm(w, r, is, "Couldn’t save: "+err.Error())
		return
	}
	if renderErr != nil {
		problems = append(problems, "Saved, but fix this before it can go live: "+renderErr.Error())
	}
	if saved.IsPublished() || old.IsPublished() {
		a.refreshNewsletter()
	}
	if len(problems) > 0 {
		a.renderIssueForm(w, r, saved, strings.Join(problems, " "))
		return
	}
	switch {
	case then == "preview":
		back(w, r, fmt.Sprintf("/admin/newsletter/%d/preview", saved.ID))
	case then == "publish" && !old.IsPublished():
		back(w, r, fmt.Sprintf("/admin/newsletter/%d?ok=published", saved.ID))
	case saved.IsPublished():
		back(w, r, fmt.Sprintf("/admin/newsletter/%d?ok=updated", saved.ID))
	default:
		back(w, r, fmt.Sprintf("/admin/newsletter/%d?ok=saved", saved.ID))
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// sitePhoto reports whether u is a photo this site serves: one uploaded in
// the admin or one from content/photos.
func sitePhoto(u string) bool {
	return (strings.HasPrefix(u, "/media/") || strings.HasPrefix(u, "/photos/")) && !strings.Contains(u, "..")
}

// unpublishIssue takes an issue off the website. (Publishing goes through the
// save button, so it always includes the latest edits.)
func (a *App) unpublishIssue(w http.ResponseWriter, r *http.Request) {
	is, ok := a.issueFromPath(w, r)
	if !ok {
		return
	}
	is.Published = nil
	if _, err := a.store.SaveIssue(is); err != nil {
		back(w, r, fmt.Sprintf("/admin/newsletter/%d?err=save", is.ID))
		return
	}
	a.refreshNewsletter()
	back(w, r, fmt.Sprintf("/admin/newsletter/%d?ok=unpublished", is.ID))
}

func (a *App) deleteIssue(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if err := a.store.DeleteIssue(id); err != nil {
		back(w, r, "/admin/newsletter?err=save")
		return
	}
	a.refreshNewsletter()
	back(w, r, "/admin/newsletter?ok=issue-deleted")
}

func (a *App) previewIssue(w http.ResponseWriter, r *http.Request) {
	is, ok := a.issueFromPath(w, r)
	if !ok {
		return
	}
	v, err := a.issueView(is, nil)
	if err != nil {
		a.renderIssueForm(w, r, is, "Fix this to see the preview: "+err.Error())
		return
	}
	v.Preview = true
	b, err := a.site.renderNews(v, is.Path(), "Preview: Newsletter, "+is.Label())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// emailIssue drafts an email announcing the issue and opens the Email tab.
// If the issue has a rides block, it lists the rides that still need leaders.
func (a *App) emailIssue(w http.ResponseWriter, r *http.Request) {
	is, ok := a.issueFromPath(w, r)
	if !ok {
		return
	}
	body := fmt.Sprintf("Hi {first_name},\n\nThe %s issue of the PaxVelo Newsletter is out:\n%s%s", is.Label(), a.cfg.SiteURL, is.Path())
	var headings []string
	hasRides, asksPhotos := false, false
	for _, b := range is.Blocks {
		if (b.Kind == "big" || b.Kind == "story") && b.Heading != "" && len(headings) < 3 {
			headings = append(headings, b.Heading)
		}
		hasRides = hasRides || b.Kind == "rides"
		asksPhotos = asksPhotos || b.Kind == "share"
	}
	var events []string
	today := a.today().Format("2006-01-02")
	for _, b := range is.Blocks {
		if o, ok := a.store.Ride(b.Event); b.Kind == "event" && ok && !o.Cancelled && !o.Held && o.Date >= today {
			e := eventShown(o, today)
			events = append(events, fmt.Sprintf("- %s: %s\n  %s/calendar/ride/%s", e.When, e.Name, a.cfg.SiteURL, e.Key))
		}
	}
	if len(headings) > 0 {
		body += "\n\nIn this issue: " + strings.Join(headings, ", ")
		if hasRides {
			body += ", and the rides coming up"
		}
		body += "."
	}
	if hasRides {
		var need []string
		for _, rd := range a.issueRides(is).Rides {
			if rd.Open > 0 && !rd.Past {
				line := fmt.Sprintf("- %s %s %s: %s", rd.Weekday, rd.Month, rd.Day, rd.Name)
				if rd.Open > 1 {
					line += fmt.Sprintf(" (%d leaders needed)", rd.Open)
				}
				need = append(need, line+"\n  "+a.cfg.SiteURL+"/calendar/ride/"+rd.Key)
			}
		}
		if len(need) > 0 {
			body += "\n\nThese rides still need a leader. Open one and type your name to lead it:\n" + strings.Join(need, "\n")
		}
	}
	if len(events) > 0 {
		body += "\n\nComing up:\n" + strings.Join(events, "\n")
	}
	if asksPhotos {
		body += "\n\nGot photos from a club ride? Share them in the club’s photo gallery, where we pick photos for the newsletter, straight from your phone:\n" + a.cfg.SiteURL + "/share-photos"
	}
	body += "\n\nSee you on the road!\nPaxVelo"
	if d := a.store.Draft(); d.Subject != "" || d.Body != "" {
		// Don't write over an email someone is still working on.
		back(w, r, "/admin/email?err=draft")
		return
	}
	if err := a.store.SetDraft(EmailDraft{Subject: newsName + ": " + is.Label(), Body: body}); err != nil {
		back(w, r, "/admin/email?err=save")
		return
	}
	back(w, r, "/admin/email?ok=drafted")
}

// sampleIssue is the starting point for the very first issue: a few blocks
// filled in with example text to replace, so it's clear what goes where.
func sampleIssue() Issue {
	return Issue{
		Tagline: "Stories from the road.",
		Cover:   "/photos/hero.webp",
		Blocks: []Block{
			{ID: randHex(4), Kind: "big", Story: Story{
				Kicker: "Rider of the month", Heading: "Meet Sarah M.", Subhead: "The people behind the pedals.",
				Body:  "This month we’re shining the spotlight on **Sarah M.**, a longtime PaxVelo member and the kind of rider who always brings good vibes (and great coffee).\n\nHer advice for new riders: show up, sit in, and ask questions.",
				Photo: "/photos/ride.webp",
			}},
			{ID: randHex(4), Kind: "story", Story: Story{
				Kicker: "Last month", Heading: "September highlights",
				Body:  "From fast Tuesday pushes to scenic weekend rides, here are the moments that made the month.",
				Photo: "/photos/hero.webp", LinkLabel: "More rides on Strava", LinkURL: "{strava}",
			}},
			{ID: randHex(4), Kind: "story", Story: Story{
				Kicker: "Ride leader spotlight", Heading: "Chris T.",
				Body:  "Get to know the faces that keep our rides rolling: his favorite route, his go-to ride snack, and why he gives back to the club.",
				Photo: "/photos/giving.webp",
			}},
			{ID: randHex(4), Kind: "story", Story: Story{
				Kicker: "New to PaxVelo?", Heading: "New Rider Corner",
				Body:      "Start with Bike Safety 101 and a weekend social ride. Everyone was new once.",
				LinkLabel: "Read Bike Safety 101", LinkURL: "/bike-safety",
			}},
			{ID: randHex(4), Kind: "rides", Story: Story{Heading: ridesHeading}},
			{ID: randHex(4), Kind: "gallery", Photos: []string{"/photos/hero.webp", "/photos/ride.webp", "/photos/giving.webp"}},
			{ID: randHex(4), Kind: "share"},
		},
	}
}

// storyForm feeds the "story" admin template: which form fields to show for
// one story, and the story's current values.
type storyForm struct {
	P    string // form field prefix, like "b.1a2b."
	S    Story
	Opts string // extra fields: kicker, subhead, video, button
}

func (f storyForm) Has(opt string) bool { return strings.Contains(" "+f.Opts+" ", " "+opt+" ") }

func storyFields(prefix string, s Story, opts string) storyForm { return storyForm{prefix, s, opts} }

func photoStory(photo, focus, caption string) Story {
	return Story{Photo: photo, PhotoFocus: focus, Caption: caption}
}

// blockForm feeds the "block" admin template: one block on the edit page.
type blockForm struct {
	Block
	P      string // its form fields' prefix
	Kind   blockKind
	Rides  ridesView
	Events []eventChoice // for an Event block
}

func blockFields(b Block, rides ridesView, events []eventChoice) blockForm {
	k, _ := kindOf(b.Kind)
	return blockForm{Block: b, P: "b." + b.ID + ".", Kind: k, Rides: rides, Events: events}
}

// stripItem feeds the "strip-item" admin template: one photo in a photo strip.
type stripItem struct{ P, URL, Focus string }

func stripPhoto(prefix, url, focus string) stripItem { return stripItem{prefix, url, focus} }

// framerView feeds the "framer" admin template: a photo, its framing, the
// form field the framing is saved in, and where the photo shows (the shapes
// the framing tool previews: see FRAME_SHAPES in admin.html).
type framerView struct{ URL, Focus, Field, Shapes string }

func framer(url, focus, field, shapes string) framerView {
	return framerView{url, focus, field, shapes}
}

// or returns s, or def if s is empty.
func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

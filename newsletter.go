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
	"sort"
	"strconv"
	"strings"
	"time"
)

// The Paceline is the club's monthly newsletter. Issues are written in the
// admin portal (Newsletter tab) and stored with the rest of the club data.
// Published issues are rendered once whenever something changes and served
// from memory like every other page, at /newsletter (the latest issue) and
// /newsletter/2026-10 (each issue's permanent address).

type Story struct {
	Kicker      string `json:"kicker,omitempty"` // the small red line above the heading
	Heading     string `json:"heading,omitempty"`
	Subhead     string `json:"subhead,omitempty"`
	Body        string `json:"body,omitempty"` // same simple formatting as content/*.md
	Photo       string `json:"photo,omitempty"`
	PhotoAlt    string `json:"photo_alt,omitempty"`
	PhotoFocus  string `json:"photo_focus,omitempty"` // the spot to keep in view when cropped, like "50% 20%"
	VideoURL    string `json:"video_url,omitempty"`   // the photo becomes a play button linking here
	VideoLength string `json:"video_length,omitempty"`
	LinkLabel   string `json:"link_label,omitempty"`
	LinkURL     string `json:"link_url,omitempty"`
	Quote       string `json:"quote,omitempty"`
	QuoteBy     string `json:"quote_by,omitempty"`
}

type Ride struct {
	Date    string `json:"date"` // 2006-01-02
	Name    string `json:"name"`
	Time    string `json:"time,omitempty"`
	Place   string `json:"place,omitempty"`
	Details string `json:"details,omitempty"`
	URL     string `json:"url,omitempty"` // the Strava event; blank means the club page
}

type Issue struct {
	ID           int               `json:"id"`
	Month        int               `json:"month"` // 1-12; a month can have several drafts but only one published issue
	Year         int               `json:"year"`
	CreatedBy    string            `json:"created_by,omitempty"`
	EditedBy     string            `json:"edited_by,omitempty"`
	Tagline      string            `json:"tagline,omitempty"`
	Cover        string            `json:"cover,omitempty"`
	CoverFocus   string            `json:"cover_focus,omitempty"`
	Featured     Story             `json:"featured"`
	Recap        Story             `json:"recap"`
	Rides        []Ride            `json:"rides,omitempty"`
	Spotlight    Story             `json:"spotlight"`
	Corner       []Story           `json:"corner,omitempty"` // the row of three smaller stories
	Kit          Story             `json:"kit"`
	Gallery      []string          `json:"gallery,omitempty"`
	GalleryFocus map[string]string `json:"gallery_focus,omitempty"` // photo address → focus
	Instagram    []string          `json:"instagram,omitempty"`     // Instagram post links
	Closing      string            `json:"closing,omitempty"`
	Published    *time.Time        `json:"published,omitempty"`
	Updated      time.Time         `json:"updated"`
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

// ---- Public pages ----

type shownStory struct {
	Story
	Body template.HTML
	Link string // resolved, with {kit} and friends filled in
}

func (st shownStory) Show() bool { return st.Heading != "" || st.Body != "" }

type shownPhoto struct{ URL, Focus string }

type shownRide struct {
	Ride
	Month, Day string
	Link       string
}

type newsView struct {
	Empty, Preview bool
	ID             int
	Published      bool
	Label, Tagline string
	Cover          string
	CoverFocus     string
	Featured       shownStory
	Recap          shownStory
	Rides          []shownRide
	Spotlight      shownStory
	Corner         []shownStory
	Kit            shownStory
	Gallery        []shownPhoto
	Instagram      []string
	Closing        string
	StravaURL      string
	Archive        []Issue
	Current        string // path of the issue being shown, to mark it in the archive
}

func (s *Site) newsView(is Issue, archive []Issue) (newsView, error) {
	v := newsView{
		ID: is.ID, Published: is.IsPublished(), Label: is.Label(), Tagline: is.Tagline, Cover: is.Cover, CoverFocus: is.CoverFocus,
		Instagram: is.Instagram, Closing: is.Closing, StravaURL: s.cfg.StravaURL, Archive: archive, Current: is.Path(),
	}
	story := func(name string, st Story) (shownStory, error) {
		out := shownStory{Story: st}
		if strings.TrimSpace(st.Body) != "" {
			_, intro, body, err := s.md.render(st.Body, 1, pageStyle{})
			if err != nil {
				return out, fmt.Errorf("%s: %w", name, err)
			}
			out.Body = intro + body
		}
		link, err := s.md.url(strings.TrimSpace(st.LinkURL))
		if err != nil {
			return out, fmt.Errorf("%s, link: %w", name, err)
		}
		out.Link = link
		return out, nil
	}
	var err error
	if v.Featured, err = story("Featured story", is.Featured); err != nil {
		return v, err
	}
	if v.Recap, err = story("Last month", is.Recap); err != nil {
		return v, err
	}
	if v.Spotlight, err = story("Spotlight", is.Spotlight); err != nil {
		return v, err
	}
	if v.Kit, err = story("Kit", is.Kit); err != nil {
		return v, err
	}
	for i, c := range is.Corner {
		sc, err := story(fmt.Sprintf("Story %d in the row of three", i+1), c)
		if err != nil {
			return v, err
		}
		if sc.Show() {
			v.Corner = append(v.Corner, sc)
		}
	}
	for _, g := range is.Gallery {
		v.Gallery = append(v.Gallery, shownPhoto{g, is.GalleryFocus[g]})
	}
	for _, r := range is.Rides {
		sr := shownRide{Ride: r, Link: r.URL}
		if sr.Link == "" {
			sr.Link = s.cfg.StravaURL
		}
		if t, err := time.Parse("2006-01-02", r.Date); err == nil {
			sr.Month, sr.Day = strings.ToUpper(t.Format("Jan")), t.Format("2")
		}
		v.Rides = append(v.Rides, sr)
	}
	return v, nil
}

func (s *Site) renderNews(v newsView, path, title string) ([]byte, error) {
	var buf bytes.Buffer
	view := s.view(path, title, v)
	view.Desc = "The Paceline, PaxVelo’s monthly newsletter: stories from the road, upcoming rides, and club news."
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
		v, err := a.site.newsView(is, published)
		var b []byte
		if err == nil {
			b, err = a.site.renderNews(v, is.Path(), "The Paceline, "+is.Label()+" — "+siteName)
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
			if b, err = a.site.renderNews(v, "/newsletter", "The Paceline — "+siteName); err == nil {
				pages["/newsletter"] = page("/newsletter", b)
			}
		}
	}
	if pages["/newsletter"] == nil {
		if p := old["/newsletter"]; p != nil && len(published) > 0 {
			pages["/newsletter"] = p
		} else if b, err := a.site.renderNews(newsView{Empty: true}, "/newsletter", "The Paceline — "+siteName); err == nil {
			pages["/newsletter"] = page("/newsletter", b)
		}
	}
	a.site.newsMu.Lock()
	a.site.news = pages
	a.site.newsMu.Unlock()
}

func (a *App) newsletterPage(w http.ResponseWriter, r *http.Request) {
	path := "/newsletter"
	if slug := r.PathValue("slug"); slug != "" {
		path += "/" + slug
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

const maxPhoto = 8 << 20

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

// savePhotoFile stores one uploaded photo under a random name.
func (a *App) savePhotoFile(hdr *multipart.FileHeader) (string, error) {
	if hdr.Size == 0 {
		return "", nil
	}
	f, err := hdr.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPhoto+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxPhoto {
		return "", fmt.Errorf("%s is over 8 MB. Make it smaller at squoosh.app and try again", hdr.Filename)
	}
	ext, ok := photoTypes[http.DetectContentType(b)]
	if !ok {
		return "", fmt.Errorf("%s isn’t a photo this site can show. Use JPG, PNG, or WebP", hdr.Filename)
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

// ---- Admin ----

const maxRides, maxGallery, cornerCount, maxNewsInstagram = 8, 8, 3, 3

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

// createIssue starts a new draft. It copies the latest published issue (or
// the latest draft) so the regular sections (new rider corner, kit, closing
// line) carry over and only the news needs writing; the very first issue
// starts from a sample.
func (a *App) createIssue(w http.ResponseWriter, r *http.Request) {
	month, _ := strconv.Atoi(r.FormValue("month"))
	year, _ := strconv.Atoi(r.FormValue("year"))
	if !validMonth(month, year) {
		back(w, r, "/admin/newsletter?err=month")
		return
	}
	is := sampleIssue()
	// Sample rides on the first Tuesday, Thursday, and Saturday of the month
	// (or from today, for this month).
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, a.cfg.Location)
	if now := time.Now().In(a.cfg.Location); now.After(start) && now.Before(start.AddDate(0, 1, 0)) {
		start = now
	}
	for i, wd := range []time.Weekday{time.Tuesday, time.Thursday, time.Saturday} {
		d := start.AddDate(0, 0, (int(wd)-int(start.Weekday())+7)%7)
		is.Rides[i].Date = d.Format("2006-01-02")
	}
	sortRides(is.Rides)
	if list := a.store.Issues(); len(list) > 0 {
		is = list[0]
		for _, x := range list {
			if x.IsPublished() {
				is = x
				break
			}
		}
		is.Rides = nil
		is.Featured, is.Recap, is.Spotlight = Story{Kicker: is.Featured.Kicker}, Story{Kicker: is.Recap.Kicker}, Story{Kicker: is.Spotlight.Kicker}
	}
	me, _ := a.adminName(r)
	is.ID, is.Month, is.Year, is.Published, is.CreatedBy, is.EditedBy = 0, month, year, nil, me, me
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
	is.ID, is.Published, is.CreatedBy, is.EditedBy = 0, nil, me, me
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
	v := adminView{Title: is.Label() + " newsletter", Tab: "newsletter", Issue: is, Months: months}
	if live, ok := a.store.LiveFor(is.Month, is.Year, is.ID); ok {
		v.MonthLive = &live
	}
	v.Flash, v.Error = flashFrom(r)
	if problem != "" {
		v.Flash, v.Error = "", problem
	} else if _, err := a.site.newsView(is, nil); err != nil {
		v.Error = "Fix this before publishing: " + err.Error()
	}
	// Room for a couple more rides than there are now.
	for want := min(max(len(is.Rides)+2, 4), maxRides); len(is.Rides) < want; {
		is.Rides = append(is.Rides, Ride{})
	}
	for len(is.Corner) < cornerCount {
		is.Corner = append(is.Corner, Story{})
	}
	v.Issue = is
	a.renderAdmin(w, r, http.StatusOK, "issue", v)
}

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
	// The form carries each photo's current address, so photos uploaded with an
	// edit that couldn't be saved aren't lost when it's fixed and saved again.
	photo := func(prefix, current string) string {
		if v, ok := r.Form[prefix+"photo_current"]; ok {
			current = ""
			if len(v) > 0 && sitePhoto(v[0]) {
				current = v[0]
			}
		}
		if r.FormValue(prefix+"photo_remove") != "" {
			current = ""
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
			Body: strings.ReplaceAll(val(prefix+"body"), "\r\n", "\n"), Photo: photo(prefix, cur.Photo), PhotoAlt: val(prefix + "photo_alt"),
			PhotoFocus: focusFor(prefix),
			VideoURL:   val(prefix + "video_url"), VideoLength: val(prefix + "video_length"),
			LinkLabel: val(prefix + "link_label"), LinkURL: val(prefix + "link_url"),
			Quote: val(prefix + "quote"), QuoteBy: val(prefix + "quote_by"),
		}
	}
	month, _ := strconv.Atoi(r.FormValue("month"))
	year, _ := strconv.Atoi(r.FormValue("year"))
	if validMonth(month, year) {
		is.Month, is.Year = month, year
	}
	is.EditedBy, _ = a.adminName(r)
	is.Tagline, is.Closing = val("tagline"), val("closing")
	if v, ok := r.Form["instagram"]; ok {
		posts, err := parseInstagramPosts(strings.Join(v, "\n"), maxNewsInstagram)
		note(err)
		if err == nil {
			is.Instagram = posts
		}
	}
	is.Cover = photo("cover.", old.Cover)
	is.CoverFocus = focusFor("cover.")
	is.Featured = story("featured.", old.Featured)
	is.Recap = story("recap.", old.Recap)
	is.Spotlight = story("spotlight.", old.Spotlight)
	is.Kit = story("kit.", old.Kit)
	is.Corner = nil
	for i := 0; i < cornerCount; i++ {
		var cur Story
		if i < len(old.Corner) {
			cur = old.Corner[i]
		}
		is.Corner = append(is.Corner, story(fmt.Sprintf("corner%d.", i), cur))
	}
	is.Rides = nil
	for i := 0; i < maxRides; i++ {
		p := fmt.Sprintf("ride%d.", i)
		rd := Ride{Date: val(p + "date"), Name: val(p + "name"), Time: val(p + "time"), Place: val(p + "place"), Details: val(p + "details"), URL: val(p + "url")}
		if rd.Name != "" {
			is.Rides = append(is.Rides, rd)
		}
	}
	sortRides(is.Rides)
	gallery := old.Gallery
	if r.FormValue("gallery_form") != "" {
		gallery = nil
		for _, g := range r.Form["gallery_current"] {
			if sitePhoto(g) {
				gallery = append(gallery, g)
			}
		}
	}
	is.Gallery, is.GalleryFocus = nil, map[string]string{}
	for i, g := range gallery {
		if r.FormValue(fmt.Sprintf("gallery%d_remove", i)) == "" {
			is.Gallery = append(is.Gallery, g)
			if f := cleanFocus(val(fmt.Sprintf("gallery%d_focus", i))); f != "" {
				is.GalleryFocus[g] = f
			} else if f := old.GalleryFocus[g]; f != "" && r.FormValue("gallery_form") == "" {
				is.GalleryFocus[g] = f
			}
		}
	}
	var uploads []*multipart.FileHeader
	if r.MultipartForm != nil {
		uploads = r.MultipartForm.File["gallery_files"]
	}
	for _, fh := range uploads {
		if len(is.Gallery) >= maxGallery {
			problems = append(problems, fmt.Sprintf("The photo strip holds %d photos, so some weren’t added.", maxGallery))
			break
		}
		u, err := a.savePhotoFile(fh)
		note(err)
		if u != "" {
			is.Gallery = append(is.Gallery, u)
		}
	}

	// The save buttons can also publish or preview, so neither ever skips
	// changes that haven't been saved yet.
	then := r.URL.Query().Get("then")
	_, renderErr := a.site.newsView(is, nil)
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

// sitePhoto reports whether u is a photo this site serves: one uploaded in
// the admin or one from content/photos.
func sitePhoto(u string) bool {
	return (strings.HasPrefix(u, "/media/") || strings.HasPrefix(u, "/photos/")) && !strings.Contains(u, "..")
}

// sortRides puts rides in date order, with undated ones ("TBA") last.
func sortRides(rides []Ride) {
	sort.SliceStable(rides, func(i, j int) bool {
		a, b := rides[i].Date, rides[j].Date
		if a == "" || b == "" {
			return a != "" && b == ""
		}
		return a < b
	})
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
	v, err := a.site.newsView(is, nil)
	if err != nil {
		a.renderIssueForm(w, r, is, "Fix this to see the preview: "+err.Error())
		return
	}
	v.Preview = true
	b, err := a.site.renderNews(v, is.Path(), "Preview: The Paceline, "+is.Label())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// emailIssue drafts an email announcing the issue and opens the Email tab.
func (a *App) emailIssue(w http.ResponseWriter, r *http.Request) {
	is, ok := a.issueFromPath(w, r)
	if !ok {
		return
	}
	body := fmt.Sprintf("Hi {first_name},\n\nThe %s issue of The Paceline, PaxVelo’s newsletter, is out:\n%s%s", is.Label(), a.cfg.SiteURL, is.Path())
	if is.Featured.Heading != "" {
		body += "\n\nIn this issue: " + is.Featured.Heading
		if is.Spotlight.Heading != "" {
			body += ", " + is.Spotlight.Heading
		}
		body += ", and the rides coming up this month."
	}
	body += "\n\nSee you on the road!\nPaxVelo"
	if d := a.store.Draft(); d.Subject != "" || d.Body != "" {
		// Don't write over an email someone is still working on.
		back(w, r, "/admin/email?err=draft")
		return
	}
	if err := a.store.SetDraft(EmailDraft{Subject: "The Paceline: " + is.Label(), Body: body}); err != nil {
		back(w, r, "/admin/email?err=save")
		return
	}
	back(w, r, "/admin/email?ok=drafted")
}

// sampleIssue is the starting point for the very first issue: every section
// filled in with example text to replace, so it's clear what goes where.
func sampleIssue() Issue {
	return Issue{
		Tagline: "Stories from the road.",
		Cover:   "/photos/hero.webp",
		Featured: Story{
			Kicker: "Rider of the month", Heading: "Meet Sarah M.", Subhead: "The people behind the pedals.",
			Body:  "This month we’re shining the spotlight on **Sarah M.**, a longtime PaxVelo member, weekend warrior, and the kind of rider who always brings good vibes (and great coffee).\n\nSarah started with the Saturday social ride and now pulls on the Thursday paceline. Her advice for new riders: show up, sit in, and ask questions.",
			Photo: "/photos/ride.webp", PhotoAlt: "Sarah on a club ride",
			LinkLabel: "Watch her interview", Quote: "PaxVelo has pushed me to be a stronger rider and a better version of myself, on and off the bike.", QuoteBy: "Sarah M.",
		},
		Recap: Story{
			Kicker: "Last month in motion", Heading: "September highlights",
			Body:  "From fast Tuesday pushes to scenic weekend rides, here are the moments that made the month.",
			Photo: "/photos/hero.webp", PhotoAlt: "Riders on a country road",
			LinkLabel: "More rides on Strava", LinkURL: "{strava}",
		},
		Rides: []Ride{
			{Name: "Tuesday Night Ride", Time: "6:00 PM", Place: "NAS Pax", Details: "Fast · A and B groups"},
			{Name: "Thursday Night Ride", Time: "6:00 PM", Place: "Park & Ride, Gate 2", Details: "Paceline · about 40 mi"},
			{Name: "Weekend Social", Time: "8:00 AM", Place: "Leonardtown", Details: "Casual · all levels"},
		},
		Spotlight: Story{
			Kicker: "Ride leader spotlight", Heading: "Chris T.",
			Body:  "Get to know the faces that keep our rides rolling. This month it’s **Chris T.**, our Thursday ride leader: his favorite route, his go-to ride snack, and why he gives back to the club.",
			Photo: "/photos/giving.webp", PhotoAlt: "Chris at the start of a ride",
			LinkLabel: "Read more",
		},
		Corner: []Story{
			{Kicker: "Buy · Sell · Trade", Heading: "The Swap Meet", Body: "Looking for a new wheelset? Got a bike that needs a new home? Post it in the #swap-meet channel on our Discord.", LinkLabel: "Visit the swap meet", LinkURL: "https://discord.gg/8TtR4pu4C2"},
			{Kicker: "New to PaxVelo?", Heading: "New Rider Corner", Body: "Whether you’re just getting started or getting back in the saddle, start with Bike Safety 101 and the Weekend Social ride. Everyone was new once.", LinkLabel: "Read Bike Safety 101", LinkURL: "/bike-safety"},
			{Kicker: "Riding for a bigger purpose", Heading: "Community Miles", Body: "Proceeds from the St. Mary’s Century support local causes chosen by our members. Thanks to your miles, we’re making a difference, one ride at a time.", Photo: "/photos/giving.webp", PhotoAlt: "Club members with a donation check", LinkLabel: "Our impact", LinkURL: "/"},
		},
		Kit: Story{
			Kicker: "Calling all designers", Heading: "Design our next kit",
			Body:      "We’re looking for club members to help design the next PaxVelo kit. Sketches, color ideas, and full designs are all welcome. Email [PaxVeloClub@gmail.com](mailto:PaxVeloClub@gmail.com) to get involved.",
			LinkLabel: "See our current kit", LinkURL: "{kit}",
		},
		Gallery: []string{"/photos/hero.webp", "/photos/ride.webp", "/photos/giving.webp"},
		Closing: "Your next ride starts here.",
	}
}

// storyForm feeds the "story" admin template: which form fields to show for
// one story, and the story's current values.
type storyForm struct {
	P    string // form field prefix, like "featured."
	S    Story
	Opts string // extra fields: kicker, subhead, video, quote
}

func (f storyForm) Has(opt string) bool { return strings.Contains(" "+f.Opts+" ", " "+opt+" ") }

func storyFields(prefix string, s Story, opts string) storyForm { return storyForm{prefix, s, opts} }

func photoStory(photo, focus string) Story { return Story{Photo: photo, PhotoFocus: focus} }

// framerView feeds the "framer" admin template: a photo, its focus, and the
// form field the focus is saved in.
type framerView struct{ URL, Focus, Field string }

func framer(url, focus, field string) framerView { return framerView{url, focus, field} }

// or returns s, or def if s is empty.
func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

var focusRE = regexp.MustCompile(`^(100|[1-9]?[0-9])% (100|[1-9]?[0-9])%$`)

// cleanFocus keeps a photo focus point ("50% 20%") or drops anything else.
// The middle is the default, so it's stored as empty.
func cleanFocus(f string) string {
	if !focusRE.MatchString(f) || f == "50% 50%" {
		return ""
	}
	return f
}

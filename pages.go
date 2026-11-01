package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Pages can be edited two ways: on GitHub (content/*.md), or in the admin
// portal's Pages tab. An edit made in the admin is kept with the club data
// and wins over the GitHub version of that page until someone presses
// "Go back to the GitHub version". The board members are edited on their own
// Board tab, with photos, and appear wherever a page says {board}.

type PageEdit struct {
	Text     string    `json:"text"`
	EditedBy string    `json:"edited_by,omitempty"`
	Edited   time.Time `json:"edited"`
}

type BoardMember struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	Photo string `json:"photo,omitempty"`
	Focus string `json:"focus,omitempty"`
}

// contentData is what admins change that the content pages show.
type contentData struct {
	Instagram  []string
	InstaFeed  []InstaPost
	Board      []BoardMember
	HomePhotos []HomePhoto
	Pages      map[string]string // file name → text edited in the admin
	// Upcoming is the next few rides, for {upcoming-rides}, and what to say
	// when there are none.
	Upcoming     []RideOcc
	UpcomingNote string
}

func (a *App) contentData() contentData {
	d := contentData{Instagram: a.store.Instagram(), InstaFeed: a.store.InstaFeed(), Board: a.store.Board(), HomePhotos: a.store.HomePhotos(), Pages: map[string]string{}}
	d.Upcoming, d.UpcomingNote = a.upcomingRides()
	for file, e := range a.store.PageEdits() {
		d.Pages[file] = e.Text
	}
	return d
}

// rebuildContent re-renders every content page with the latest admin changes.
func (a *App) rebuildContent() error { return a.site.renderContent(a.contentData()) }

var boardTpl = template.Must(template.New("").Parse(`<ul class="board">
{{range .}}<li>{{if .Photo}}<img class="avatar" src="{{.Photo}}" alt="" width="160" height="160" loading="lazy" decoding="async"{{with .Focus}} style="object-position:{{.}}"{{end}}>{{else}}<span class="avatar" aria-hidden="true"></span>{{end}}<strong>{{.Name}}</strong><span>{{.Role}}</span></li>
{{end}}</ul>
`))

func boardWidget(board []BoardMember) template.HTML {
	var b bytes.Buffer
	boardTpl.Execute(&b, board)
	return template.HTML(b.String())
}

// ---- Store ----

func (s *Store) PageEdits() map[string]PageEdit {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]PageEdit{}
	for k, v := range s.d.PageEdits {
		out[k] = v
	}
	return out
}

// SetPageEdit saves an edited page, or removes the edit (e == nil).
func (s *Store) SetPageEdit(file string, e *PageEdit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.PageEdits == nil {
		s.d.PageEdits = map[string]PageEdit{}
	}
	if e == nil {
		delete(s.d.PageEdits, file)
	} else {
		s.d.PageEdits[file] = *e
	}
	return s.save()
}

func (s *Store) Board() []BoardMember {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]BoardMember(nil), s.d.Board...)
}

func (s *Store) SetBoard(b []BoardMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Board = b
	return s.save()
}

func defaultBoard() []BoardMember {
	return []BoardMember{{Name: "Karl Krasnesky", Role: "President"}, {Name: "Allie Kampmeyer", Role: "Vice President"}, {Name: "Barry Murphy", Role: "Treasurer"}, {Name: "Gabe Murray", Role: "Secretary"}}
}

// ---- Admin: Pages ----

type pageRow struct {
	File, Path, Title string
	Edit              *PageEdit
	New               bool // added in the admin: there's no GitHub version
	InMenu            bool
}

func (a *App) pageRows() []pageRow {
	edits := a.store.PageEdits()
	var rows []pageRow
	for _, p := range *a.site.pageList.Load() {
		row := pageRow{File: p.File, Path: p.Path, Title: strings.TrimSuffix(p.Title, " — "+siteName), InMenu: p.Menu != ""}
		if e, ok := edits[p.File]; ok {
			row.Edit = &e
		}
		_, err := fs.Stat(a.site.content, p.File)
		row.New = err != nil
		rows = append(rows, row)
	}
	return rows
}

func (a *App) pagesAdmin(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Pages", Tab: "pages", Pages: a.pageRows()}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "pages", v)
}

// takenPaths are addresses the site already uses for something other than a
// content page, so a new page can't have them.
var takenPaths = []string{"admin", "api", "calendar", "files", "gallery", "home", "join", "kit", "media", "minutes", "newsletter",
	"photos", "static", "strava", "unsubscribe", "membership", "info", "cart", "account", "insurancewaiver", "tnrinfo", "thnr-info", "robots", "favicon"}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slugFor turns a page title into an address, like "Group Ride Etiquette" →
// "group-ride-etiquette".
func slugFor(title string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	return s
}

// newPageText is what a new page starts with: its title and a sample section
// showing the formatting, ready to replace. order is its place in the Info menu.
func newPageText(title string, inMenu bool, order int) string {
	var b strings.Builder
	if inMenu {
		fmt.Fprintf(&b, "---\nmenu: %s\norder: %d\n---\n\n", title, order)
	}
	fmt.Fprintf(&b, "# %s\n\nA sentence or two about what this page is for. Replace this text with your own.\n\n", title)
	b.WriteString("## A section heading\n\nWrite the section here. To make words **bold** or turn them into a [link](https://www.paxvelo.club), select them and pick from the buttons that appear.\n\n")
	b.WriteString("- A list item\n- Another list item\n")
	return b.String()
}

// createPage adds a new page from the Pages tab and opens it to edit.
func (a *App) createPage(w http.ResponseWriter, r *http.Request) {
	title := cleanName(r.FormValue("title"))
	slug := slugFor(r.FormValue("address"))
	if slug == "" {
		slug = slugFor(title)
	}
	file := slug + ".md"
	_, onGitHub := fs.Stat(a.site.content, file)
	_, exists := a.pageRow(file)
	switch {
	case title == "":
		back(w, r, "/admin/pages?err=page-title")
		return
	case !slugRE.MatchString(slug):
		back(w, r, "/admin/pages?err=page-address")
		return
	case exists || onGitHub == nil || slices.Contains(takenPaths, slug):
		back(w, r, "/admin/pages?err=page-taken")
		return
	}
	// In the Info menu, it goes after the pages already there, but before the
	// site's own pages and Contact Us.
	order := 1
	for _, p := range *a.site.pageList.Load() {
		if p.Order < menuLast {
			order = max(order, p.Order+1)
		}
	}
	order = min(order, menuLast-1)
	text := newPageText(title, r.FormValue("menu") != "", order)
	if err := a.site.checkPage(file, text); err != nil {
		back(w, r, "/admin/pages?err=save")
		return
	}
	me, _ := a.adminName(r)
	if err := a.store.SetPageEdit(file, &PageEdit{Text: text, EditedBy: me, Edited: time.Now().UTC()}); err != nil || a.rebuildContent() != nil {
		back(w, r, "/admin/pages?err=save")
		return
	}
	back(w, r, "/admin/pages/"+file+"?ok=page-created")
}

// deletePage removes a page that was added in the admin. (Pages from GitHub
// can only go back to their GitHub version.)
func (a *App) deletePage(w http.ResponseWriter, r *http.Request) {
	row, ok := a.pageRow(r.PathValue("file"))
	if !ok || !row.New {
		back(w, r, "/admin/pages")
		return
	}
	if err := a.store.SetPageEdit(row.File, nil); err != nil || a.rebuildContent() != nil {
		back(w, r, "/admin/pages?err=save")
		return
	}
	back(w, r, "/admin/pages?ok=page-deleted")
}

func (a *App) pageRow(file string) (pageRow, bool) {
	for _, p := range a.pageRows() {
		if p.File == file {
			return p, true
		}
	}
	return pageRow{}, false
}

// pageSource is the page's current text: the admin's edit, or the GitHub version.
func (a *App) pageSource(file string) string {
	if e, ok := a.store.PageEdits()[file]; ok {
		return e.Text
	}
	b, _ := fs.ReadFile(a.site.content, file)
	return string(b)
}

func (a *App) editPagePage(w http.ResponseWriter, r *http.Request) {
	row, ok := a.pageRow(r.PathValue("file"))
	if !ok {
		back(w, r, "/admin/pages")
		return
	}
	v := adminView{Title: "Edit " + row.Title, Tab: "pages", Page: row, PageText: a.pageSource(row.File), Links: a.editorLinks()}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "page-edit", v)
}

func (a *App) savePage(w http.ResponseWriter, r *http.Request) {
	row, ok := a.pageRow(r.PathValue("file"))
	if !ok {
		back(w, r, "/admin/pages")
		return
	}
	text := strings.ReplaceAll(r.FormValue("text"), "\r\n", "\n")
	if err := a.site.checkPage(row.File, text); err != nil {
		v := adminView{Title: "Edit " + row.Title, Tab: "pages", Page: row, PageText: text, Links: a.editorLinks(),
			Error: "Not saved yet, so the page is unchanged. Fix this and save again: " + err.Error()}
		a.renderAdmin(w, r, http.StatusOK, "page-edit", v)
		return
	}
	me, _ := a.adminName(r)
	if err := a.store.SetPageEdit(row.File, &PageEdit{Text: text, EditedBy: me, Edited: time.Now().UTC()}); err != nil {
		back(w, r, "/admin/pages/"+row.File+"?err=save")
		return
	}
	if err := a.rebuildContent(); err != nil {
		back(w, r, "/admin/pages/"+row.File+"?err=save")
		return
	}
	back(w, r, "/admin/pages/"+row.File+"?ok=page-saved")
}

// previewPage answers the editor's live preview: the page as the site would
// show it with the text typed so far (nothing is saved), or what's wrong with
// the text. The editor shows it in a frame that runs no scripts.
func (a *App) previewPage(w http.ResponseWriter, r *http.Request) {
	row, ok := a.pageRow(r.PathValue("file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	text := strings.ReplaceAll(r.FormValue("text"), "\r\n", "\n")
	b, err := a.site.previewPage(row.File, text, a.contentData())
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(err.Error()))
		return
	}
	// No scripts, and no fade-in: the preview redraws as people type. It's
	// drawn as for a browser that runs scripts (the phone menu folded away),
	// like nearly every visitor's.
	b = scriptTag.ReplaceAll(b, nil)
	b = bytes.Replace(b, []byte(`<html lang="en" class="no-js">`), []byte(`<html lang="en" class="js">`), 1)
	b = bytes.Replace(b, []byte("</head>"), []byte("<style>*,*::before,*::after{animation:none!important;transition:none!important}</style></head>"), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// scriptTag finds a page's scripts, which the preview leaves out.
var scriptTag = regexp.MustCompile(`(?s)<script\b.*?</script>`)

// editorLink is a page offered in the editor's link box.
type editorLink struct{ Path, Title string }

// editorLinks are the site's pages, for the editor's link box.
func (a *App) editorLinks() []editorLink {
	var out []editorLink
	for _, p := range a.pageRows() {
		if p.Path == "/" {
			p.Title = "Home page"
		}
		out = append(out, editorLink{p.Path, p.Title})
	}
	return append(out, editorLink{"/calendar", "Ride Calendar"}, editorLink{"/routes", "Club Routes"}, editorLink{"/gallery", "Photo Gallery"},
		editorLink{"/share-photos", "Share Your Photos"}, editorLink{"/membership", "Join / Membership"}, editorLink{"/newsletter", "Newsletter"})
}

func (a *App) resetPage(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if err := a.store.SetPageEdit(file, nil); err != nil || a.rebuildContent() != nil {
		back(w, r, "/admin/pages?err=save")
		return
	}
	back(w, r, "/admin/pages/"+file+"?ok=page-reset")
}

// pagePhoto stores a photo for a page and answers with the line to put in it.
func (a *App) pagePhoto(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPhoto+(1<<20))
	w.Header().Set("Content-Type", "application/json")
	reply := func(status int, m map[string]string) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(m)
	}
	if err := r.ParseMultipartForm(maxPhoto + (1 << 20)); err != nil {
		reply(http.StatusBadRequest, map[string]string{"error": "That photo is over 20 MB. Make it smaller at squoosh.app and try again."})
		return
	}
	u, err := a.savePhoto(r, "photo")
	if err == nil && u == "" {
		err = errors.New("choose a photo first")
	}
	if err != nil {
		reply(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	reply(http.StatusOK, map[string]string{"markdown": "![Describe the photo](" + u + ")"})
}

// ---- Admin: Board ----

func (a *App) boardAdmin(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Board members", Tab: "board", Board: append(a.store.Board(), BoardMember{})}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "board", v)
}

func (a *App) saveBoard(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 80<<20)
	if err := r.ParseMultipartForm(16 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		back(w, r, "/admin/board?err=bigfile")
		return
	}
	type row struct {
		BoardMember
		order int
	}
	var rows []row
	var problems []string
	count, _ := strconv.Atoi(r.FormValue("count"))
	for i := 0; i < count && i < 40; i++ {
		f := func(name string) string { return strings.TrimSpace(r.FormValue(fmt.Sprintf("%s%d", name, i))) }
		m := BoardMember{Name: cleanName(f("name")), Role: cleanName(f("role")), Focus: cleanFocus(f("focus"))}
		if f("remove") != "" || (m.Name == "" && m.Role == "") {
			continue
		}
		if p := f("photo_current"); sitePhoto(p) && f("photo_remove") == "" {
			m.Photo = p
		}
		u, err := a.savePhoto(r, fmt.Sprintf("photo%d_file", i))
		if err != nil {
			problems = append(problems, err.Error())
		}
		if u != "" {
			m.Photo, m.Focus = u, ""
		}
		if m.Photo == "" {
			m.Focus = ""
		}
		order, err := strconv.Atoi(f("order"))
		if err != nil {
			order = i + 1
		}
		rows = append(rows, row{m, order*100 + i})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].order < rows[j].order })
	var board []BoardMember
	for _, rw := range rows {
		board = append(board, rw.BoardMember)
	}
	if err := a.store.SetBoard(board); err != nil || a.rebuildContent() != nil {
		back(w, r, "/admin/board?err=save")
		return
	}
	if len(problems) > 0 {
		v := adminView{Title: "Board members", Tab: "board", Board: append(a.store.Board(), BoardMember{}),
			Error: "Saved, but: " + strings.Join(problems, " ")}
		a.renderAdmin(w, r, http.StatusOK, "board", v)
		return
	}
	back(w, r, "/admin/board?ok=board")
}

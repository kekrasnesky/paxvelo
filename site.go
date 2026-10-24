package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The public pages come from content/*.md (see content.go). Their file names
// match the old Squarespace addresses so existing links keep working.

type navLink struct{ Path, Label string }

const siteDescription = "Southern Maryland’s Premier Cycling Club"

// siteName is the club's name in page titles and link previews.
const siteName = "PaxVelo Cycling Club"

type Page struct {
	Path     string
	html, gz []byte
	etag     string
}

type Site struct {
	cfg         Config
	content     fs.FS                            // the content folder
	pageTpl     map[string]*template.Template    // "home" and "page"
	pagePaths   []string                         // every content page's address
	pages       atomic.Pointer[map[string]*Page] // content pages, rebuilt when the Instagram posts change
	contentMu   sync.Mutex
	dynamic     map[string]*template.Template
	email       *template.Template
	admin       *template.Template
	static      fs.FS
	photos      fs.FS
	infoLinks   []navLink
	md          *mdRenderer
	newsMu      sync.RWMutex
	newsRefresh sync.Mutex
	minutes     atomic.Pointer[Page] // the public meeting minutes page
	minutesMu   sync.Mutex
	news        map[string]*Page // the newsletter, rendered when it changes
	css         template.CSS
	js          template.JS
	adminCSS    template.CSS
	version     string
	notFound404 []byte
}

// view is what every public template receives.
type view struct {
	Title, Desc, Path, SiteURL, V string
	CSS                           template.CSS
	JS                            template.JS
	Club                          clubInfo
	Data                          any
}

// clubInfo holds the settings pages show: dues and outside links.
type clubInfo struct {
	DuesIndividual, DuesFamily   int
	KitURL, StravaURL, DonateURL string
}

func init() {
	mime.AddExtensionType(".woff2", "font/woff2")
	mime.AddExtensionType(".webp", "image/webp")
}

func newSite(cfg Config) (*Site, error) {
	s := &Site{cfg: cfg, dynamic: map[string]*template.Template{}}
	static, err := fs.Sub(webFS, "web/static")
	if err != nil {
		return nil, err
	}
	s.static = static
	content, err := fs.Sub(contentFS, "content")
	if err != nil {
		return nil, err
	}
	if s.photos, err = fs.Sub(content, "photos"); err != nil {
		return nil, err
	}

	// Asset version: changes whenever any static file or photo changes, so browsers
	// can cache fonts and images forever and still pick up new versions instantly.
	h := sha256.New()
	for _, dir := range []fs.FS{static, s.photos} {
		err = fs.WalkDir(dir, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(dir, p)
			h.Write([]byte(p))
			h.Write(b)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	s.version = hex.EncodeToString(h.Sum(nil))[:10]

	read := func(name string) (string, error) {
		b, err := fs.ReadFile(static, name)
		return strings.ReplaceAll(string(b), "__V__", s.version), err
	}
	css, err := read("site.css")
	if err != nil {
		return nil, err
	}
	js, err := read("site.js")
	if err != nil {
		return nil, err
	}
	adminCSS, err := read("admin.css")
	if err != nil {
		return nil, err
	}
	s.css, s.js, s.adminCSS = template.CSS(css), template.JS(js), template.CSS(adminCSS)

	funcs := template.FuncMap{
		"infoLinks":   func() []navLink { return s.infoLinks },
		"isInfo":      s.isInfo,
		"date":        func(t time.Time) string { return t.In(cfg.Location).Format("Jan 2, 2006") },
		"datetime":    func(t time.Time) string { return t.In(cfg.Location).Format("Jan 2, 2006, 3:04 PM") },
		"statusLabel": statusLabel,
		"progress":    progress,
		"hasPrefix":   strings.HasPrefix,
		"story":       storyFields,
		"photoStory":  photoStory,
		"inc":         func(i int) int { return i + 1 },
		"instagram":   instagramEmbeds,
		"lines":       func(l []string) string { return strings.Join(l, "\n") },
		"external":    func(u string) bool { return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") },
	}
	base, err := template.New("").Funcs(funcs).ParseFS(webFS, "web/templates/layout.html")
	if err != nil {
		return nil, err
	}
	parse := func(file string) (*template.Template, error) {
		t, err := base.Clone()
		if err != nil {
			return nil, err
		}
		return t.ParseFS(webFS, "web/templates/"+file)
	}

	// Pieces a page can drop in with a line like {membership-plans}.
	md := &mdRenderer{
		vars: map[string]string{
			"strava": cfg.StravaURL, "kit": cfg.KitURL, "donate": cfg.DonateURL,
			"dues-individual": strconv.Itoa(cfg.DuesIndividual), "dues-family": strconv.Itoa(cfg.DuesFamily),
		},
		widgets: map[string]template.HTML{},
		photos:  s.photos,
		version: s.version,
	}
	widgets, err := base.Clone() // base itself must never run, or it can't be cloned
	if err != nil {
		return nil, err
	}
	for _, w := range []string{"membership-plans", "social-links"} {
		var buf bytes.Buffer
		if err := widgets.ExecuteTemplate(&buf, w, s.view("", "", nil)); err != nil {
			return nil, err
		}
		md.widgets[w] = template.HTML(buf.String())
	}
	md.widgets["instagram"] = instagramWidget(nil)
	s.md, s.content = md, content
	pages, err := loadContent(content, md)
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		s.pagePaths = append(s.pagePaths, p.Path)
		if p.Menu != "" {
			s.infoLinks = append(s.infoLinks, navLink{p.Path, p.Menu})
		}
	}
	s.infoLinks = append(s.infoLinks, navLink{"/minutes", "Meeting Minutes"})
	s.pageTpl = map[string]*template.Template{}
	for _, name := range []string{"home", "page"} {
		if s.pageTpl[name], err = parse(name + ".html"); err != nil {
			return nil, err
		}
	}
	if err := s.renderContent(nil); err != nil {
		return nil, err
	}
	for _, name := range []string{"join", "unsubscribe", "notfound", "newsletter", "minutes"} {
		if s.dynamic[name], err = parse(name + ".html"); err != nil {
			return nil, err
		}
	}
	var nf bytes.Buffer
	if err := s.dynamic["notfound"].ExecuteTemplate(&nf, "layout", s.view("", "Page not found — "+siteName, nil)); err != nil {
		return nil, err
	}
	s.notFound404 = nf.Bytes()

	if s.email, err = template.New("").ParseFS(webFS, "web/templates/email.html"); err != nil {
		return nil, err
	}
	if s.admin, err = template.New("").Funcs(funcs).ParseFS(webFS, "web/templates/admin.html"); err != nil {
		return nil, err
	}
	return s, nil
}

// renderContent renders every page in the content folder, with the home
// page's Instagram posts. It runs at startup and when the posts change.
func (s *Site) renderContent(instagram []string) error {
	s.contentMu.Lock()
	defer s.contentMu.Unlock()
	md := *s.md
	md.widgets = map[string]template.HTML{}
	for k, v := range s.md.widgets {
		md.widgets[k] = v
	}
	md.widgets["instagram"] = instagramWidget(instagram)
	list, err := loadContent(s.content, &md)
	if err != nil {
		return err
	}
	pages := map[string]*Page{}
	for _, p := range list {
		t := s.pageTpl["page"]
		if p.Path == "/" {
			t = s.pageTpl["home"]
		}
		v := s.view(p.Path, p.Title, p)
		v.Desc = p.Desc
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, "layout", v); err != nil {
			return fmt.Errorf("content/%s: %w", p.File, err)
		}
		pages[p.Path] = &Page{Path: p.Path, html: buf.Bytes(), gz: gzipBytes(buf.Bytes()), etag: etag(buf.Bytes())}
	}
	s.pages.Store(&pages)
	return nil
}

// serveContent serves the current version of a content page.
func (s *Site) serveContent(path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := (*s.pages.Load())[path]; p != nil {
			s.servePage(p).ServeHTTP(w, r)
			return
		}
		s.notFound(w, r)
	})
}

func (s *Site) view(path, title string, data any) view {
	return view{
		Title: title, Desc: siteDescription, Path: path, SiteURL: s.cfg.SiteURL, V: s.version,
		CSS: s.css, JS: s.js, Data: data,
		Club: clubInfo{
			DuesIndividual: s.cfg.DuesIndividual, DuesFamily: s.cfg.DuesFamily,
			KitURL: s.cfg.KitURL, StravaURL: s.cfg.StravaURL, DonateURL: s.cfg.DonateURL,
		},
	}
}

// servePage serves a pre-rendered page straight from memory, gzipped when the
// browser supports it, with a short cache so repeat visits are instant.
func (s *Site) servePage(p *Page) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=300")
		h.Set("ETag", p.etag)
		h.Set("Vary", "Accept-Encoding")
		if r.Header.Get("If-None-Match") == p.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		body := p.html
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
			body = p.gz
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method != http.MethodHead {
			w.Write(body)
		}
	})
}

// render executes a dynamic page (join, unsubscribe) for one request.
func (s *Site) render(w http.ResponseWriter, status int, name, path, title string, data any) {
	var buf bytes.Buffer
	if err := s.dynamic[name].ExecuteTemplate(&buf, "layout", s.view(path, title, data)); err != nil {
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (s *Site) notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	w.Write(s.notFound404)
}

// staticFiles serves fonts, images, and photos under prefix. Versioned URLs
// (?v=...) are cached by browsers for a year, since a new version gets a new URL.
func (s *Site) staticFiles(prefix string, dir fs.FS) http.Handler {
	files := http.StripPrefix(prefix, http.FileServer(http.FS(dir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			s.notFound(w, r)
			return
		}
		if r.URL.Query().Has("v") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Site) renderEmail(subject, text, unsubURL string) (string, error) {
	var b bytes.Buffer
	err := s.email.ExecuteTemplate(&b, "email", map[string]any{
		"Subject": subject, "Body": textToHTML(text), "SiteURL": s.cfg.SiteURL, "UnsubURL": unsubURL,
	})
	return b.String(), err
}

func (s *Site) isInfo(path string) bool {
	for _, l := range s.infoLinks {
		if l.Path == path {
			return true
		}
	}
	return false
}

func statusLabel(status string) string {
	switch status {
	case "sending":
		return "Sending"
	case "sent":
		return "Sent"
	case "stopped":
		return "Stopped"
	case "paused":
		return "Paused"
	}
	return status
}

func progress(c Campaign) string {
	s := fmt.Sprintf("%d of %d sent", c.Sent, c.Total)
	if c.Failed > 0 {
		s += fmt.Sprintf(", %d failed", c.Failed)
	}
	return s
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func etag(b []byte) string {
	sum := sha256.Sum256(b)
	return `W/"` + hex.EncodeToString(sum[:8]) + `"`
}

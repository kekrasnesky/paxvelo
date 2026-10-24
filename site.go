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
	"time"
)

// The public pages. Paths match the old Squarespace site so existing links keep working.
// To add a page: create web/templates/<file>, add a line here, and (if it belongs
// in the Info menu) add it to infoLinks below.
var pageList = []struct{ Path, File, Title string }{
	{"/", "home.html", "PAXVELO"},
	{"/committee", "committee.html", "Executive Board Members — PAXVELO"},
	{"/rides", "rides.html", "Our Rides — PAXVELO"},
	{"/bike-safety", "bike-safety.html", "Bike Safety 101 — PAXVELO"},
}

type navLink struct{ Path, Label string }

var infoLinks = []navLink{
	{"/committee", "Executive Board Members"},
	{"/rides", "Our Rides"},
	{"/bike-safety", "Bike Safety 101"},
}

const siteDescription = "Southern Maryland’s Premier Cycling Club"

type Page struct {
	Path     string
	html, gz []byte
	etag     string
}

type Site struct {
	cfg         Config
	pages       []*Page
	dynamic     map[string]*template.Template
	email       *template.Template
	admin       *template.Template
	static      fs.FS
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

	// Asset version: changes whenever any static file changes, so browsers can
	// cache fonts and images forever and still pick up new versions instantly.
	h := sha256.New()
	err = fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(static, p)
		h.Write([]byte(p))
		h.Write(b)
		return err
	})
	if err != nil {
		return nil, err
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
		"infoLinks":   func() []navLink { return infoLinks },
		"isInfo":      isInfo,
		"date":        func(t time.Time) string { return t.In(cfg.Location).Format("Jan 2, 2006") },
		"datetime":    func(t time.Time) string { return t.In(cfg.Location).Format("Jan 2, 2006, 3:04 PM") },
		"statusLabel": statusLabel,
		"progress":    progress,
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

	for _, p := range pageList {
		t, err := parse(p.File)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, "layout", s.view(p.Path, p.Title, nil)); err != nil {
			return nil, fmt.Errorf("%s: %w", p.File, err)
		}
		s.pages = append(s.pages, &Page{Path: p.Path, html: buf.Bytes(), gz: gzipBytes(buf.Bytes()), etag: etag(buf.Bytes())})
	}
	for _, name := range []string{"join", "unsubscribe", "notfound"} {
		if s.dynamic[name], err = parse(name + ".html"); err != nil {
			return nil, err
		}
	}
	var nf bytes.Buffer
	if err := s.dynamic["notfound"].ExecuteTemplate(&nf, "layout", s.view("", "Page not found — PAXVELO", nil)); err != nil {
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

// staticFiles serves fonts and images. Versioned URLs (?v=...) are cached by
// browsers for a year, since a new version gets a new URL.
func (s *Site) staticFiles() http.Handler {
	files := http.StripPrefix("/static/", http.FileServer(http.FS(s.static)))
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

func isInfo(path string) bool {
	for _, l := range infoLinks {
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

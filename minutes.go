package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Meeting minutes are uploaded in the admin portal (Minutes tab) as PDF or
// Word files and listed on the public /minutes page once published. Like the
// newsletter, the page is rendered whenever something changes and served
// from memory.

type Minutes struct {
	ID         int        `json:"id"`
	Date       string     `json:"date"` // the meeting date, 2006-01-02
	Title      string     `json:"title"`
	Summary    string     `json:"summary,omitempty"`
	File       string     `json:"file"`      // /files/<random>.pdf
	FileName   string     `json:"file_name"` // the name it was uploaded with
	Size       int64      `json:"size"`
	Published  *time.Time `json:"published,omitempty"`
	UploadedBy string     `json:"uploaded_by,omitempty"`
	Updated    time.Time  `json:"updated"`
}

func (m Minutes) IsPublished() bool { return m.Published != nil }

// DateLabel is the meeting date as people write it, like October 7, 2026.
func (m Minutes) DateLabel() string {
	t, err := time.Parse("2006-01-02", m.Date)
	if err != nil {
		return m.Date
	}
	return t.Format("January 2, 2006")
}

func (m Minutes) Year() string {
	if len(m.Date) >= 4 {
		return m.Date[:4]
	}
	return ""
}

// Kind is the file type shown next to the download link.
func (m Minutes) Kind() string {
	if strings.HasSuffix(m.File, ".docx") {
		return "Word"
	}
	return "PDF"
}

// SizeLabel is the file size, like 240 KB or 1.3 MB.
func (m Minutes) SizeLabel() string {
	if m.Size < 1<<20 {
		return fmt.Sprintf("%d KB", max(1, m.Size>>10))
	}
	return fmt.Sprintf("%.1f MB", float64(m.Size)/(1<<20))
}

// ---- Store ----

// AllMinutes returns every upload, newest meeting first.
func (s *Store) AllMinutes() []Minutes {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Minutes, 0, len(s.d.Minutes))
	for _, m := range s.d.Minutes {
		c := *m
		if m.Published != nil {
			t := *m.Published
			c.Published = &t
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out
}

func (s *Store) AddMinutes(m Minutes) (Minutes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.NextMinutes = max(s.d.NextMinutes, 1)
	m.ID = s.d.NextMinutes
	s.d.NextMinutes++
	m.Updated = time.Now().UTC()
	c := m
	s.d.Minutes = append(s.d.Minutes, &c)
	return m, s.save()
}

func (s *Store) SetMinutesPublished(id int, published bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.d.Minutes {
		if m.ID == id {
			m.Published = nil
			if published {
				now := time.Now().UTC()
				m.Published = &now
			}
			m.Updated = time.Now().UTC()
			return s.save()
		}
	}
	return nil
}

// DeleteMinutes removes an upload and returns it, so its file can be deleted.
func (s *Store) DeleteMinutes(id int) (Minutes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.d.Minutes {
		if m.ID == id {
			s.d.Minutes = append(s.d.Minutes[:i], s.d.Minutes[i+1:]...)
			return *m, s.save()
		}
	}
	return Minutes{}, nil
}

// ---- Public page ----

type minutesYear struct {
	Year string
	List []Minutes
}

// refreshMinutes re-renders the public minutes page.
func (a *App) refreshMinutes() {
	a.site.minutesMu.Lock()
	defer a.site.minutesMu.Unlock()
	var years []minutesYear
	for _, m := range a.store.AllMinutes() {
		if !m.IsPublished() {
			continue
		}
		if len(years) == 0 || years[len(years)-1].Year != m.Year() {
			years = append(years, minutesYear{Year: m.Year()})
		}
		years[len(years)-1].List = append(years[len(years)-1].List, m)
	}
	var buf bytes.Buffer
	if err := a.site.dynamic["minutes"].ExecuteTemplate(&buf, "layout", a.site.view("/minutes", "Meeting Minutes — "+siteName, years)); err != nil {
		log.Printf("minutes page: %v", err)
		return
	}
	b := buf.Bytes()
	a.site.minutes.Store(&Page{Path: "/minutes", html: b, gz: gzipBytes(b), etag: etag(b)})
}

func (a *App) minutesPage(w http.ResponseWriter, r *http.Request) {
	a.site.servePage(a.site.minutes.Load()).ServeHTTP(w, r)
}

// ---- Files ----

const maxMinutesFile = 20 << 20

func (a *App) filesDir() string { return filepath.Join(a.cfg.DataDir, "files") }

// saveDocument stores an uploaded PDF or Word file under a random name.
func (a *App) saveDocument(hdr *multipart.FileHeader) (path string, size int64, err error) {
	f, err := hdr.Open()
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxMinutesFile+1))
	if err != nil {
		return "", 0, err
	}
	if len(b) > maxMinutesFile {
		return "", 0, errors.New("that file is over 20 MB")
	}
	var ext string
	switch {
	case bytes.HasPrefix(b, []byte("%PDF-")):
		ext = ".pdf"
	case bytes.HasPrefix(b, []byte("PK\x03\x04")) && strings.HasSuffix(strings.ToLower(hdr.Filename), ".docx"):
		ext = ".docx"
	default:
		return "", 0, errors.New("upload the minutes as a PDF or a Word (.docx) file")
	}
	if err := os.MkdirAll(a.filesDir(), 0o755); err != nil {
		return "", 0, err
	}
	name := randHex(8) + ext
	if err := os.WriteFile(filepath.Join(a.filesDir(), name), b, 0o644); err != nil {
		return "", 0, err
	}
	return "/files/" + name, int64(len(b)), nil
}

// serveFiles serves uploaded documents. Word files download instead of
// opening in the browser, since browsers can't show them.
func (a *App) serveFiles() http.Handler {
	files := a.site.staticFiles("/files/", os.DirFS(a.filesDir()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".docx") {
			w.Header().Set("Content-Disposition", "attachment")
		}
		files.ServeHTTP(w, r)
	})
}

// ---- Admin ----

func (a *App) minutesAdmin(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Meeting minutes", Tab: "minutes", Minutes: a.store.AllMinutes()}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "minutes", v)
}

func (a *App) addMinutes(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMinutesFile+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		back(w, r, "/admin/minutes?err=bigfile")
		return
	}
	date := strings.TrimSpace(r.FormValue("date"))
	if _, err := time.Parse("2006-01-02", date); err != nil {
		back(w, r, "/admin/minutes?err=date")
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 || files[0].Size == 0 {
		back(w, r, "/admin/minutes?err=nofile")
		return
	}
	path, size, err := a.saveDocument(files[0])
	if err != nil {
		v := adminView{Title: "Meeting minutes", Tab: "minutes", Minutes: a.store.AllMinutes(), Error: "Not uploaded: " + err.Error() + "."}
		a.renderAdmin(w, r, http.StatusOK, "minutes", v)
		return
	}
	title := cleanName(r.FormValue("title"))
	if title == "" {
		title = "Club meeting"
	}
	me, _ := a.adminName(r)
	m := Minutes{Date: date, Title: title, Summary: strings.TrimSpace(r.FormValue("summary")), File: path,
		FileName: filepath.Base(files[0].Filename), Size: size, UploadedBy: me}
	if r.FormValue("publish") != "" {
		now := time.Now().UTC()
		m.Published = &now
	}
	if _, err := a.store.AddMinutes(m); err != nil {
		back(w, r, "/admin/minutes?err=save")
		return
	}
	a.refreshMinutes()
	if m.IsPublished() {
		back(w, r, "/admin/minutes?ok=minutes-published")
	} else {
		back(w, r, "/admin/minutes?ok=minutes-added")
	}
}

func (a *App) publishMinutes(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	publish := r.FormValue("publish") == "1"
	if err := a.store.SetMinutesPublished(id, publish); err != nil {
		back(w, r, "/admin/minutes?err=save")
		return
	}
	a.refreshMinutes()
	if publish {
		back(w, r, "/admin/minutes?ok=minutes-published")
	} else {
		back(w, r, "/admin/minutes?ok=minutes-hidden")
	}
}

func (a *App) deleteMinutes(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	m, err := a.store.DeleteMinutes(id)
	if err != nil {
		back(w, r, "/admin/minutes?err=save")
		return
	}
	if name := strings.TrimPrefix(m.File, "/files/"); name != "" && name != m.File && !strings.ContainsAny(name, `/\`) {
		os.Remove(filepath.Join(a.filesDir(), name))
	}
	a.refreshMinutes()
	back(w, r, "/admin/minutes?ok=minutes-deleted")
}

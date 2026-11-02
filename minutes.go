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
// Word files. They're listed on the /minutes page, which nothing on the
// website links to: it opens for anyone with the link, and each meeting has
// its own (/minutes#m3) to share by email or from a newsletter. The page and
// the files are kept out of search engines and the sitemap.

type Minutes struct {
	ID         int       `json:"id"`
	Date       string    `json:"date"` // the meeting date, 2006-01-02
	Title      string    `json:"title"`
	Summary    string    `json:"summary,omitempty"`
	File       string    `json:"file"`               // /files/<random>.pdf
	FileName   string    `json:"file_name"`          // the name it was uploaded with
	Original   string    `json:"original,omitempty"` // the Word file a PDF was made from
	Size       int64     `json:"size"`
	UploadedBy string    `json:"uploaded_by,omitempty"`
	Updated    time.Time `json:"updated"`
}

// DateLabel is the meeting date as people write it, like October 7, 2026.
func (m Minutes) DateLabel() string {
	t, err := time.Parse("2006-01-02", m.Date)
	if err != nil {
		return m.Date
	}
	return t.Format("January 2, 2006")
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
		out = append(out, *m)
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

// ---- Files ----

const maxMinutesFile = 20 << 20

func (a *App) filesDir() string { return filepath.Join(a.cfg.DataDir, "files") }

// saveDocument stores an uploaded PDF or Word file under a random name. A
// Word file is turned into a PDF (docpdf.go), so anyone can read it in their
// browser, and the Word file is kept as the original; without the converter
// it stays a Word file.
func (a *App) saveDocument(hdr *multipart.FileHeader) (path, original string, size int64, err error) {
	f, err := hdr.Open()
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxMinutesFile+1))
	if err != nil {
		return "", "", 0, err
	}
	if len(b) > maxMinutesFile {
		return "", "", 0, errors.New("that file is over 20 MB")
	}
	var ext string
	switch {
	case bytes.HasPrefix(b, []byte("%PDF-")):
		ext = ".pdf"
	case bytes.HasPrefix(b, []byte("PK\x03\x04")) && strings.HasSuffix(strings.ToLower(hdr.Filename), ".docx"):
		ext = ".docx"
	default:
		return "", "", 0, errors.New("upload the minutes as a PDF or a Word (.docx) file")
	}
	if err := os.MkdirAll(a.filesDir(), 0o755); err != nil {
		return "", "", 0, err
	}
	name := randHex(8)
	if err := os.WriteFile(filepath.Join(a.filesDir(), name+ext), b, 0o644); err != nil {
		return "", "", 0, err
	}
	if ext == ".docx" {
		pdf, err := a.wordToPDF(b)
		if err == nil {
			err = os.WriteFile(filepath.Join(a.filesDir(), name+".pdf"), pdf, 0o644)
		}
		if err == nil {
			return "/files/" + name + ".pdf", "/files/" + name + ".docx", int64(len(pdf)), nil
		}
		if err != errNoConverter {
			log.Printf("minutes %s: %v", hdr.Filename, err)
		}
	}
	return "/files/" + name + ext, "", int64(len(b)), nil
}

// serveFiles serves uploaded minutes. Word files download instead of opening
// in the browser, since browsers can't show them.
func (a *App) serveFiles() http.HandlerFunc {
	files := a.site.staticFiles("/files/", os.DirFS(a.filesDir()))
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex")
		if strings.HasSuffix(r.URL.Path, ".docx") {
			w.Header().Set("Content-Disposition", "attachment")
		}
		files.ServeHTTP(w, r)
	}
}

// minutesPage is the public list of meeting minutes, newest first.
func (a *App) minutesPage(w http.ResponseWriter, r *http.Request) {
	a.site.render(w, http.StatusOK, "minutes", "/minutes", "Meeting Minutes — "+siteName, struct{ List []Minutes }{a.store.AllMinutes()})
}

// ---- Admin ----

func (a *App) minutesAdmin(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Meeting minutes", Tab: "minutes", Minutes: a.store.AllMinutes(), CanConvert: officeProgram() != ""}
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
	path, original, size, err := a.saveDocument(files[0])
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
		FileName: filepath.Base(files[0].Filename), Original: original, Size: size, UploadedBy: me}
	if _, err := a.store.AddMinutes(m); err != nil {
		back(w, r, "/admin/minutes?err=save")
		return
	}
	switch {
	case original != "":
		back(w, r, "/admin/minutes?ok=minutes-converted")
	case strings.HasSuffix(path, ".docx"):
		back(w, r, "/admin/minutes?ok=minutes-word")
	default:
		back(w, r, "/admin/minutes?ok=minutes-added")
	}
}

func (a *App) deleteMinutes(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	m, err := a.store.DeleteMinutes(id)
	if err != nil {
		back(w, r, "/admin/minutes?err=save")
		return
	}
	for _, file := range []string{m.File, m.Original} {
		if name := strings.TrimPrefix(file, "/files/"); name != "" && name != file && !strings.ContainsAny(name, `/\`) {
			os.Remove(filepath.Join(a.filesDir(), name))
		}
	}
	back(w, r, "/admin/minutes?ok=minutes-deleted")
}

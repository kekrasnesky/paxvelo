package main

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for LibreOffice: run with
// PAXVELO_FAKE_OFFICE set, it "converts" the file it's given by writing a
// PDF next to it, as soffice --convert-to pdf --outdir does.
func TestMain(m *testing.M) {
	if os.Getenv("PAXVELO_FAKE_OFFICE") != "" {
		args, out := os.Args[1:], ""
		for i, a := range args {
			if a == "--outdir" && i+1 < len(args) {
				out = args[i+1]
			}
		}
		in := args[len(args)-1]
		name := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in)) + ".pdf"
		os.WriteFile(filepath.Join(out, name), []byte("%PDF-1.4 converted"), 0o644)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func uploadMinutes(t *testing.T, a *App, name string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	h, admin := a.routes(), adminCookie(a)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("date", "2026-10-07")
	mw.WriteField("title", "October club meeting")
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(content)
	mw.Close()
	return do(t, h, admin, "POST", "/admin/minutes", mw.FormDataContentType(), &buf)
}

// Word minutes become PDFs when LibreOffice is there, keeping the Word file;
// without it they stay Word files, and the admin says so. Word files from
// before are turned into PDFs at startup.
func TestMinutesWordToPDF(t *testing.T) {
	docx := []byte("PK\x03\x04 a pretend Word file")
	old := officeProgram
	defer func() { officeProgram = old }()

	a, _, _ := testApp(t)
	officeProgram = func() string { return "" }
	if w := uploadMinutes(t, a, "minutes.docx", docx); !strings.Contains(w.Header().Get("Location"), "ok=minutes-word") {
		t.Fatalf("without LibreOffice: %s", w.Header().Get("Location"))
	}
	m := a.store.AllMinutes()[0]
	if !strings.HasSuffix(m.File, ".docx") || m.Original != "" {
		t.Fatalf("without LibreOffice the Word file stays: %+v", m)
	}

	// LibreOffice arrives: the old Word file becomes a PDF at startup.
	t.Setenv("PAXVELO_FAKE_OFFICE", "1")
	officeProgram = func() string { return os.Args[0] }
	a.convertOldMinutes()
	m = a.store.AllMinutes()[0]
	if !strings.HasSuffix(m.File, ".pdf") || !strings.HasSuffix(m.Original, ".docx") || m.Kind() != "PDF" {
		t.Fatalf("after converting: %+v", m)
	}
	if b, _ := os.ReadFile(filepath.Join(a.filesDir(), strings.TrimPrefix(m.File, "/files/"))); !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Error("the PDF should be saved")
	}

	// New Word uploads are turned into PDFs straight away.
	if w := uploadMinutes(t, a, "november.docx", docx); !strings.Contains(w.Header().Get("Location"), "ok=minutes-converted") {
		t.Fatalf("with LibreOffice: %s", w.Header().Get("Location"))
	}
	var nov Minutes
	for _, x := range a.store.AllMinutes() {
		if x.FileName == "november.docx" {
			nov = x
		}
	}
	if !strings.HasSuffix(nov.File, ".pdf") || !strings.HasSuffix(nov.Original, ".docx") {
		t.Errorf("new Word upload: %+v", nov)
	}
	page := do(t, a.routes(), adminCookie(a), "GET", "/admin/minutes", "", nil).Body.String()
	if !strings.Contains(page, "made from the") {
		t.Error("the admin should link the original Word file")
	}
}

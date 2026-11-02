package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Word files turned into PDFs, so minutes open in any browser. The site has
// no code of its own for this: it runs LibreOffice, without a window, when
// it's installed on the server (deploy/setup.sh installs it). Without it,
// Word files stay as they are.

var errNoConverter = errors.New("LibreOffice isn't installed on the server")

// convertMu runs one conversion at a time: LibreOffice is big for a small server.
var convertMu sync.Mutex

// officeProgram is LibreOffice's command, or "" when it isn't installed.
var officeProgram = func() string {
	for _, name := range []string{"soffice", "libreoffice"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// wordToPDF turns a Word file into a PDF.
func (a *App) wordToPDF(docx []byte) ([]byte, error) {
	prog := officeProgram()
	if prog == "" {
		return nil, errNoConverter
	}
	convertMu.Lock()
	defer convertMu.Unlock()
	dir, err := os.MkdirTemp("", "paxvelo-minutes-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "minutes.docx")
	if err := os.WriteFile(in, docx, 0o600); err != nil {
		return nil, err
	}
	// LibreOffice keeps its settings in a profile folder. The service may only
	// write to the data folder (deploy/paxvelo.service), so it goes there.
	profile, err := filepath.Abs(filepath.Join(a.cfg.DataDir, "office-profile"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, "--headless", "--norestore", "--nolockcheck", "--nodefault",
		"-env:UserInstallation="+(&url.URL{Scheme: "file", Path: filepath.ToSlash(profile)}).String(),
		"--convert-to", "pdf", "--outdir", dir, in)
	cmd.Env = append(os.Environ(), "HOME="+dir)
	out, runErr := cmd.CombinedOutput()
	pdf, err := os.ReadFile(filepath.Join(dir, "minutes.pdf"))
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, fmt.Errorf("LibreOffice couldn't convert it (%v): %s", runErr, bytes.TrimSpace(out))
	}
	return pdf, nil
}

// convertOldMinutes turns minutes still kept as Word files into PDFs, once
// LibreOffice is on the server. It runs at startup; the Word file is kept.
func (a *App) convertOldMinutes() {
	if officeProgram() == "" {
		return
	}
	for _, m := range a.store.AllMinutes() {
		name := strings.TrimPrefix(m.File, "/files/")
		if !strings.HasSuffix(name, ".docx") || name == m.File || strings.ContainsAny(name, `/\`) {
			continue
		}
		if err := a.minutesToPDF(m.ID, name); err != nil {
			log.Printf("minutes %d (%s): %v", m.ID, m.FileName, err)
			continue
		}
		log.Printf("minutes %d (%s) turned into a PDF", m.ID, m.FileName)
	}
}

// minutesToPDF makes the PDF for minutes kept as a Word file (name, in the
// files folder) and switches them to it.
func (a *App) minutesToPDF(id int, name string) error {
	b, err := os.ReadFile(filepath.Join(a.filesDir(), name))
	if err != nil {
		return err
	}
	pdf, err := a.wordToPDF(b)
	if err != nil {
		return err
	}
	pdfName := strings.TrimSuffix(name, ".docx") + ".pdf"
	if err := os.WriteFile(filepath.Join(a.filesDir(), pdfName), pdf, 0o644); err != nil {
		return err
	}
	return a.store.SetMinutesPDF(id, "/files/"+pdfName, int64(len(pdf)))
}

// SetMinutesPDF switches minutes to their PDF, keeping the Word file as the original.
func (s *Store) SetMinutesPDF(id int, file string, size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.d.Minutes {
		if m.ID == id {
			m.Original, m.File, m.Size = m.File, file, size
			return s.save()
		}
	}
	return nil
}

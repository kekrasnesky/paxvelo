package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// The sent log: every email the site sends or tries to send, newest last,
// so admins can check what went out, to whom, and whether it worked (Email →
// Sent log). It keeps who, when, what kind and the subject, never the body:
// some emails carry private links, like a password link. It's a file of its
// own (data/email-log.jsonl), one email a line; once it's over 4 MB the
// oldest half is dropped, which leaves years of a club's email.

const emailLogMax = 4 << 20

// Kinds of email, as the sent log names them.
const (
	mailCampaign   = "Email to members"
	mailWelcome    = "Welcome email"
	mailTest       = "Test email"
	mailClub       = "Notice to the club"
	mailSignup     = "New member alert"
	mailPassword   = "Admin password link"
	mailWaiverLink = "Waiver link"
	mailMyLink     = "Membership link"
)

type sentEmail struct {
	Time     time.Time `json:"t"`
	Kind     string    `json:"kind"`
	Campaign int       `json:"campaign,omitempty"` // for an email to members, which one
	To       string    `json:"to"`
	Name     string    `json:"name,omitempty"`
	Subject  string    `json:"subject"`
	Result   string    `json:"result"` // "sent", "failed", or "test mode" (logged on the server, not sent)
	Error    string    `json:"error,omitempty"`
}

func (e sentEmail) Failed() bool { return e.Result == "failed" }

type EmailLog struct {
	mu    sync.Mutex
	path  string
	fails []time.Time // when emails failed, read from the file once, for FailedSince
	read  bool
}

func openEmailLog(dataDir string) *EmailLog {
	return &EmailLog{path: filepath.Join(dataDir, "email-log.jsonl")}
}

// Add records one email. A log that can't be written is reported in the
// server log; the email itself has already gone.
func (l *EmailLog) Add(e sentEmail) {
	if l == nil {
		return
	}
	b, _ := json.Marshal(e)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		log.Printf("sent log: %v", err)
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("sent log: %v", err)
		return
	}
	f.Write(append(b, '\n'))
	size, _ := f.Seek(0, 1)
	f.Close()
	if e.Failed() && l.read {
		l.fails = append(l.fails, e.Time)
	}
	if size > emailLogMax {
		l.trim()
	}
}

// trim drops the oldest half of the log. Callers hold l.mu.
func (l *EmailLog) trim() {
	b, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	b = b[len(b)/2:]
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	tmp := l.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, l.path)
	}
}

// All returns every email in the log, newest first.
func (l *EmailLog) All() []sentEmail {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.all()
}

// all reads the log. Callers hold l.mu.
func (l *EmailLog) all() []sentEmail {
	f, err := os.Open(l.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []sentEmail
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e sentEmail
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	slices.Reverse(out)
	return out
}

// FailedSince counts the emails that failed since a time.
func (l *EmailLog) FailedSince(t time.Time) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.read {
		for _, e := range l.all() {
			if e.Failed() {
				l.fails = append(l.fails, e.Time)
			}
		}
		l.read = true
	}
	n := 0
	for _, f := range l.fails {
		if !f.Before(t) {
			n++
		}
	}
	return n
}

// ---- Admin: Email → Sent log ----

type sentLogView struct {
	Rows                   []sentEmail
	Kinds                  []string
	Kind, Result, Q        string
	Shown, Total, More     int
	Today, Week, WeekFails int
	Live                   bool
}

const sentLogPage = 300

func (a *App) sentLogPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := &sentLogView{Kind: q.Get("kind"), Result: q.Get("result"), Q: strings.TrimSpace(q.Get("q")), Live: a.mailer.live(),
		Kinds: []string{mailCampaign, mailWelcome, mailClub, mailSignup, mailWaiverLink, mailMyLink, mailPassword, mailTest}}
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, a.cfg.Location)
	lq := strings.ToLower(v.Q)
	all := a.mailer.log.All()
	v.Total = len(all)
	for _, e := range all {
		if !e.Time.Before(day) {
			v.Today++
		}
		if now.Sub(e.Time) < 7*24*time.Hour {
			v.Week++
			if e.Failed() {
				v.WeekFails++
			}
		}
		if (v.Kind != "" && e.Kind != v.Kind) || (v.Result == "failed" && !e.Failed()) ||
			(lq != "" && !strings.Contains(strings.ToLower(e.To+" "+e.Name+" "+e.Subject), lq)) {
			continue
		}
		v.Shown++
		if len(v.Rows) < sentLogPage {
			v.Rows = append(v.Rows, e)
		}
	}
	v.More = v.Shown - len(v.Rows)
	av := adminView{Title: "Sent log", Tab: "sent-log", SentLog: v}
	a.renderAdmin(w, r, http.StatusOK, "sent-log", av)
}

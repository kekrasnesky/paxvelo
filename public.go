package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type joinData struct {
	Token, Name, Email, Plan string
	Message                  string // error text, or the headline after joining
	Detail                   string // the sentence under the headline
	PayURL, PayLabel         string // PayPal button, when dues are owed
	OK, Failed               bool
}

func (a *App) joinPage(w http.ResponseWriter, r *http.Request) {
	d := joinData{Token: a.formToken(), Plan: cleanPlan(r.URL.Query().Get("plan"))}
	a.site.render(w, http.StatusOK, "join", "/join", "Become a Member — PAXVELO", d)
}

// join handles the membership form. It saves the member, then hands them the
// PayPal link for their plan. With JavaScript it answers in JSON and the page
// updates in place; without JavaScript it re-renders the page.
func (a *App) join(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	r.ParseForm()
	rawName, rawEmail, plan := r.FormValue("name"), r.FormValue("email"), cleanPlan(r.FormValue("plan"))
	reply := func(status int, d joinData) {
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{
				"ok": d.OK, "message": d.Message, "detail": d.Detail, "pay_url": d.PayURL, "pay_label": d.PayLabel,
			})
			return
		}
		d.Token, d.Plan = a.formToken(), plan
		if !d.OK {
			d.Failed, d.Name, d.Email = true, rawName, rawEmail
		}
		a.site.render(w, status, "join", "/join", "Become a Member — PAXVELO", d)
	}
	fail := func(status int, msg string) { reply(status, joinData{Message: msg}) }

	// Spam checks: a hidden field only bots fill in, a signed timestamp that
	// proves the form was loaded at least a couple of seconds ago, and (below)
	// a cap on signups per connection. No CAPTCHA needed.
	if r.FormValue("website") != "" {
		reply(http.StatusOK, joinData{OK: true, Message: "Thanks!"})
		return
	}
	if !a.validFormToken(r.FormValue("ft")) {
		fail(http.StatusBadRequest, "This form expired. Reload the page and try again.")
		return
	}
	name := cleanName(rawName)
	if name == "" {
		fail(http.StatusBadRequest, "Enter your full name.")
		return
	}
	email, ok := cleanEmail(rawEmail)
	if !ok {
		fail(http.StatusBadRequest, "Enter a valid email address, like name@example.com.")
		return
	}
	// Only count well-formed signups, so typos never lock anyone out.
	if !a.limit.Allow("join:"+clientIP(r), 5, time.Hour) {
		fail(http.StatusTooManyRequests, "Too many signups from your connection. Wait a bit and try again.")
		return
	}

	year := a.cfg.ThisYear()
	m, result, err := a.store.Join(name, email, plan, year)
	if err != nil {
		log.Printf("join %s: %v", email, err)
		fail(http.StatusInternalServerError, "Something went wrong on our end. Try again in a minute.")
		return
	}
	if result != AlreadyMember {
		go a.mailer.SendWelcome(m)
	}

	d := joinData{OK: true}
	first := m.FirstName()
	dues := a.cfg.Dues(m.Plan)
	owes := !m.PaidFor(year)
	switch {
	case result == AlreadyMember && !owes:
		d.Message = fmt.Sprintf("You’re already a member, %s.", first)
		d.Detail = fmt.Sprintf("Your %d dues are paid. See you on the road!", year)
	case result == AlreadyMember:
		d.Message = fmt.Sprintf("You’re already on the list, %s.", first)
		d.Detail = fmt.Sprintf("Pay your $%d %s dues for %d to finish up.", dues, strings.ToLower(m.PlanName()), year)
	default:
		d.Message = fmt.Sprintf("Thanks, %s! One last step.", first)
		d.Detail = fmt.Sprintf("Pay your $%d %s dues for %d to finish joining.", dues, strings.ToLower(m.PlanName()), year)
	}
	if owes {
		if link := a.cfg.PayLink(m.Plan); link != "" {
			d.PayURL, d.PayLabel = link, fmt.Sprintf("Pay $%d with PayPal", dues)
		} else {
			d.Detail += " We’ll email you how to pay."
		}
	}
	reply(http.StatusOK, d)
}

// ---- Unsubscribe ----

type unsubData struct {
	Token, Email string
	State        string // ask, done, invalid
}

// unsubscribePage asks before unsubscribing, because email scanners open
// links automatically and would otherwise unsubscribe people by accident.
func (a *App) unsubscribePage(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	m, ok := a.store.MemberByToken(t)
	d := unsubData{Token: t, Email: m.Email, State: "ask"}
	switch {
	case !ok:
		d.State = "invalid"
	case !m.Active():
		d.State = "done"
	}
	a.site.render(w, http.StatusOK, "unsubscribe", "/unsubscribe", "Unsubscribe — PAXVELO", d)
}

// unsubscribe handles both the button on the page and the one-click
// unsubscribe button Gmail and Yahoo show next to the sender's name.
func (a *App) unsubscribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	r.ParseForm()
	t := r.URL.Query().Get("t")
	if t == "" {
		t = r.PostFormValue("t")
	}
	m, ok, err := a.store.Unsubscribe(t)
	if err != nil {
		log.Printf("unsubscribe: %v", err)
	}
	d := unsubData{Email: m.Email, State: "done"}
	if !ok {
		d.State = "invalid"
	}
	a.site.render(w, http.StatusOK, "unsubscribe", "/unsubscribe", "Unsubscribe — PAXVELO", d)
}

// ---- Form token ----

func (a *App) formKey() []byte {
	k := sha256.Sum256([]byte("join-form\x00" + a.store.Secret()))
	return k[:]
}

func (a *App) formToken() string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	return ts + "." + sign(a.formKey(), ts)[:24]
}

func (a *App) validFormToken(tok string) bool {
	ts, sig, ok := strings.Cut(tok, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(sign(a.formKey(), ts)[:24])) {
		return false
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	age := time.Since(time.Unix(n, 0))
	return age >= 2*time.Second && age <= 7*24*time.Hour
}

func sign(key []byte, msg string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// ---- Input cleanup ----

func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100])
	}
	return s
}

func cleanEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || addr.Name != "" {
		return "", false
	}
	_, domain, _ := strings.Cut(s, "@")
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", false
	}
	return s, true
}

// clientIP returns the visitor's address. Behind Caddy on the same machine,
// the real address is the last entry Caddy adds to X-Forwarded-For.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return host
}

// ---- Rate limiting ----

type Limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *Limiter { return &Limiter{hits: map[string][]time.Time{}} }

// Allow reports whether key has made fewer than n attempts in the last window,
// and records this attempt if so.
func (l *Limiter) Allow(key string, n int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 10000 {
		l.hits = map[string][]time.Time{} // never grows without bound
	}
	now := time.Now()
	recent := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < window {
			recent = append(recent, t)
		}
	}
	if len(recent) >= n {
		l.hits[key] = recent
		return false
	}
	l.hits[key] = append(recent, now)
	return true
}

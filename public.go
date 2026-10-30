package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
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
	Token, Plan      string
	Form             memberDetails // what was typed, to show again after a mistake
	First, Last      string        // the typed name, in its two boxes
	Message          string        // error text, or the headline after joining
	Detail           string        // the sentence under the headline
	PayURL, PayLabel string        // PayPal button, when dues are owed
	Today            string        // the date beside the waiver signature
	OK, Failed       bool
}

// signedDate is today's date, for the waiver signature.
func (a *App) signedDate() string { return a.today().Format("January 2, 2006") }

// joinDetails reads the join form: name (one box, or first and last), email,
// plan, phone, and an emergency contact. On a problem it returns the
// problem's code (see joinProblems). The public form needs every field and
// US phone numbers; an admin adding someone by hand needs only the name and
// email.
func joinDetails(r *http.Request, all bool) (memberDetails, string) {
	name := r.FormValue("name")
	first, last := cleanName(r.FormValue("first")), cleanName(r.FormValue("last"))
	if first != "" || last != "" {
		name = first + " " + last
	}
	d := memberDetails{
		Name: cleanName(name), Plan: cleanPlan(r.FormValue("plan")),
		Phone: cleanPhone(r.FormValue("phone")), EmergencyName: cleanName(r.FormValue("emergency_name")),
		EmergencyPhone: cleanPhone(r.FormValue("emergency_phone")),
		Level:          cleanLevel(r.FormValue("level")),
		Terms:          r.FormValue("terms") != "",
		Waiver:         r.FormValue("waiver") != "", Signature: cleanName(r.FormValue("signature")),
	}
	email, ok := cleanEmail(r.FormValue("email"))
	d.Email = email
	badPhone := func(field, clean string) bool {
		if all {
			return len(phoneDigits(clean)) != 10
		}
		return r.FormValue(field) != "" && clean == ""
	}
	switch {
	case r.Form.Has("first") && first == "":
		return d, "first"
	case r.Form.Has("last") && last == "":
		return d, "last"
	case d.Name == "":
		return d, "name"
	case !ok:
		return d, "email"
	case badPhone("phone", d.Phone):
		return d, "phone"
	case all && d.Level == "":
		return d, "level"
	case all && d.EmergencyName == "":
		return d, "emergency"
	case badPhone("emergency_phone", d.EmergencyPhone):
		return d, "emergency-phone"
	case all && !d.Terms:
		return d, "terms"
	case all && !d.Waiver:
		return d, "waiver"
	case all && !strings.EqualFold(d.Signature, d.Name):
		return d, "signature"
	}
	if all {
		var ok bool
		if d.Drawing, ok = drawnSignature(r); !ok {
			return d, "drawing"
		}
	}
	return d, ""
}

// joinProblems are the join form's messages, written to the rider.
var joinProblems = map[string]string{
	"first":           "Enter your first name.",
	"last":            "Enter your last name.",
	"name":            "Enter your full name.",
	"email":           "Enter a valid email address, like name@example.com.",
	"phone":           "Enter a 10-digit phone number, like 301-555-0123.",
	"level":           "Pick the one that sounds most like you.",
	"emergency":       "Enter the name of someone we can call in an emergency.",
	"emergency-phone": "Enter your emergency contact’s 10-digit phone number, like 301-555-0123.",
	"terms":           "Tick the box to agree to the club terms.",
	"waiver":          "Tick the box to agree to the insurance waiver.",
	"signature":       "To sign the waiver, type your first and last name just as you entered them above.",
	"drawing":         "Sign in the box with your finger, a stylus or your mouse.",
}

// phoneDigits keeps just the digits of a phone number.
func phoneDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// cleanPhone tidies a phone number, writing US numbers as 301-555-0123. It
// returns "" for something that isn't a phone number (too few or too many
// digits, or letters).
func cleanPhone(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsFunc(s, unicode.IsLetter) {
		return ""
	}
	digits := phoneDigits(s)
	if len(digits) == 11 && digits[0] == '1' {
		digits = digits[1:]
	}
	switch {
	case len(digits) == 10:
		return digits[:3] + "-" + digits[3:6] + "-" + digits[6:]
	case len(digits) >= 8 && len(digits) <= 15:
		return strings.Join(strings.Fields(s), " ") // an international number, as typed
	}
	return ""
}

func (a *App) joinPage(w http.ResponseWriter, r *http.Request) {
	d := joinData{Token: a.formToken(), Plan: cleanPlan(r.URL.Query().Get("plan")), Today: a.signedDate()}
	a.site.render(w, http.StatusOK, "join", "/join", "Become a Member — "+siteName, d)
}

// join handles the membership form. It saves the member, then hands them the
// PayPal link for their plan. With JavaScript it answers in JSON and the page
// updates in place; without JavaScript it re-renders the page.
func (a *App) join(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSignatureForm)
	r.ParseForm()
	plan := cleanPlan(r.FormValue("plan"))
	typed := memberDetails{
		Name: r.FormValue("name"), Email: r.FormValue("email"), Phone: r.FormValue("phone"),
		EmergencyName: r.FormValue("emergency_name"), EmergencyPhone: r.FormValue("emergency_phone"),
		Level: r.FormValue("level"), Terms: r.FormValue("terms") != "",
		Waiver: r.FormValue("waiver") != "", Signature: r.FormValue("signature"),
	}
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
		d.Token, d.Plan, d.Today = a.formToken(), plan, a.signedDate()
		if !d.OK {
			d.Failed, d.Form = true, typed
			d.First, d.Last = r.FormValue("first"), r.FormValue("last")
		}
		a.site.render(w, status, "join", "/join", "Become a Member — "+siteName, d)
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
	details, problem := joinDetails(r, true)
	if problem != "" {
		fail(http.StatusBadRequest, joinProblems[problem])
		return
	}
	// Only count well-formed signups, so typos never lock anyone out.
	if !a.limit.Allow("join:"+clientIP(r), 5, time.Hour) {
		fail(http.StatusTooManyRequests, "Too many signups from your connection. Wait a bit and try again.")
		return
	}

	year := a.cfg.ThisYear()
	m, result, err := a.store.JoinPublic(details, year)
	if err != nil {
		log.Printf("join %s: %v", details.Email, err)
		fail(http.StatusInternalServerError, "Something went wrong on our end. Try again in a minute.")
		return
	}
	if result == OptedOut {
		// Nobody can sign up an address that opted out: it has to come from the club.
		reply(http.StatusOK, joinData{OK: true, Message: "You’re already on our list.",
			Detail: "You unsubscribed from club emails earlier, so we haven’t turned them back on. To get them again, email " + or(a.cfg.AdminEmail, "the club") + "."})
		return
	}
	if result == Joined {
		go a.mailer.NotifySignup(m) // their welcome email waits until an admin marks them paid
	}
	// This year's waiver, unless they already signed one. Someone already on
	// the list keeps the name on file, so the record shows both that and the
	// name typed.
	if _, _, err := a.store.SignWaiver(Waiver{MemberID: m.ID, Year: year, Signature: details.Signature, Drawing: details.Drawing, Where: "Join form", IP: clientIP(r)}, a.site.waiver); err != nil {
		log.Printf("join %s: waiver: %v", details.Email, err)
	}

	d := joinData{OK: true, Form: memberDetails{Email: m.Email}} // the email, for "Email me my membership page link"
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
	a.site.render(w, http.StatusOK, "unsubscribe", "/unsubscribe", "Unsubscribe — "+siteName, d)
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
	a.site.render(w, http.StatusOK, "unsubscribe", "/unsubscribe", "Unsubscribe — "+siteName, d)
}

// ---- Sitemap ----

// sitemap lists the public pages for search engines: the content pages, the
// calendar, minutes, the join form, and every published newsletter.
func (a *App) sitemap(w http.ResponseWriter, r *http.Request) {
	type loc struct {
		Loc string `xml:"loc"`
	}
	var urls []loc
	for _, p := range *a.site.pageList.Load() {
		urls = append(urls, loc{a.cfg.SiteURL + p.Path})
	}
	for _, path := range []string{"/calendar", "/routes", "/join", "/minutes", "/newsletter"} {
		urls = append(urls, loc{a.cfg.SiteURL + path})
	}
	for _, is := range a.store.Issues() {
		if is.IsPublished() {
			urls = append(urls, loc{a.cfg.SiteURL + is.Path()})
		}
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
		URLs    []loc    `xml:"url"`
	}{URLs: urls})
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

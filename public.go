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
	Adults           []adultRow    // other adults on a Family plan, as typed
	Riders           []riderRow    // riders under 18 the parent or guardian signs for, as typed
	Guardian         bool          // they ticked the box to sign for them
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
			return !phoneOK(r.FormValue(field))
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
	case all && !signedAs(d.Signature, d.Name):
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
	"phone":           "Enter a 10-digit phone number, like 301-555-0123. Outside the US, start with + and the country code.",
	"level":           "Pick the one that sounds most like you.",
	"emergency":       "Enter the name of someone we can call in an emergency.",
	"emergency-phone": "Enter your emergency contact’s 10-digit phone number, like 301-555-0123. Outside the US, start with + and the country code.",
	"terms":           "Tick the box to agree to the club terms.",
	"waiver":          "Tick the box to agree to the insurance waiver.",
	"signature":       "To sign the waiver, type your first and last name as you entered them above.",
	"drawing":         "Sign in the box with your finger, a stylus or your mouse.",
	"adult-name":      "Enter the first and last name of each adult on your family membership.",
	"adult-email":     "Enter each adult’s email address, so we can send them their waiver to sign.",
	"adult-self":      "List the other adults with their own email addresses. You sign your own waiver below.",
	"rider-name":      "Enter each rider’s first and last name.",
	"rider-age":       "Enter each rider’s age, under 18. Riders 18 and over join and sign for themselves.",
	"guardian":        "To sign for the riders under 18 you listed, tick the box to say you’re their parent or legal guardian.",
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

// phoneOK reports whether a typed phone number will do on the public forms:
// a US number (10 digits), or a number from another country typed with its +
// code, like +44 20 7946 0958. Without the +, a US number missing a digit
// would pass as a foreign one.
func phoneOK(typed string) bool {
	clean := cleanPhone(typed)
	return len(phoneDigits(clean)) == 10 || clean != "" && strings.HasPrefix(strings.TrimSpace(typed), "+")
}

// signedAs reports whether the name typed to sign a waiver is the signer's:
// the same words, ignoring case and punctuation, or the same first and last
// name with a middle name or initial left in or out ("Robert J. Smith" for
// Robert Smith).
func signedAs(signature, name string) bool {
	words := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	}
	s, n := words(signature), words(name)
	if len(s) == 0 || len(n) == 0 {
		return false
	}
	if strings.Join(s, " ") == strings.Join(n, " ") {
		return true
	}
	return len(s) > 1 && len(n) > 1 && s[0] == n[0] && s[len(s)-1] == n[len(n)-1]
}

func (a *App) joinPage(w http.ResponseWriter, r *http.Request) {
	d := joinData{Token: a.formToken(), Plan: cleanPlan(r.URL.Query().Get("plan")), Today: a.signedDate(), Adults: adultRows(nil, nil), Riders: riderRows(nil, nil)}
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
			d.Adults = adultRows(r.PostForm["adult_name"], r.PostForm["adult_email"])
			d.Riders, d.Guardian = riderRows(r.PostForm["rider_name"], r.PostForm["rider_age"]), r.PostFormValue("guardian") != ""
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
	var adults []HouseholdAdult
	if problem == "" && details.Plan == "family" { // an Individual plan ignores the adults' boxes, hidden for it
		adults, problem = readHousehold(adultRows(r.PostForm["adult_name"], r.PostForm["adult_email"]), details.Email)
	}
	var riders []joinRider
	if problem == "" {
		riders, problem = joinRiders(r)
	}
	if problem != "" {
		fail(http.StatusBadRequest, joinProblems[problem])
		return
	}
	// Only count well-formed signups, so typos never lock anyone out. A
	// club night on one wifi can bring in a crowd, and the spam checks above
	// stop bots, so the cap is generous.
	if !a.limit.Allow("join:"+clientIP(r), 20, time.Hour) {
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
	if result != Joined {
		a.renewal(r, reply, m, details, riders, adults, year)
		return
	}
	a.mailer.Go(func() { a.mailer.NotifySignup(m) }) // their welcome email waits until an admin marks them paid
	signedFor, added := a.applyJoin(m, year, details.Signature, details.Drawing, clientIP(r), riders, adults)
	emailed := a.emailHousehold(m, added) // if their dues are paid already; otherwise once they are
	// The link to confirm their address. A few an hour per address, so
	// nobody can flood someone's inbox by typing it in.
	if a.limit.Allow("confirm:"+m.Email, 3, time.Hour) {
		a.mailer.Go(func() { a.sendConfirm(m) }) // a failure is logged by send
	}

	d := joinData{OK: true, Form: memberDetails{Email: m.Email}} // the email, for "Email me my membership page link"
	dues := a.cfg.Dues(m.Plan)
	d.Message = fmt.Sprintf("Thanks, %s! Two last steps.", m.FirstName())
	d.Detail = fmt.Sprintf("Pay your $%d %s dues for %d to finish joining.", dues, strings.ToLower(m.PlanName()), year)
	if link := a.cfg.PayLink(m.Plan); link != "" {
		d.PayURL, d.PayLabel = link, fmt.Sprintf("Pay $%d with PayPal", dues)
	} else {
		d.Detail += " We’ll email you how to pay."
	}
	if len(signedFor) > 0 {
		d.Detail += " You signed the waiver for " + andList(signedFor) + " too."
	}
	if len(emailed) > 0 {
		d.Detail += " We emailed " + andList(emailed) + " a link to sign their own waiver."
	} else if len(added) > 0 {
		var names []string
		for _, h := range added {
			names = append(names, h.FirstName())
		}
		d.Detail += " Once your dues are paid, we’ll email " + andList(names) + " a link to sign their own waiver."
	}
	// Club emails wait until they confirm the address from their inbox,
	// since anyone could type it. Paying doesn't wait.
	d.Detail += " Please also confirm your email: we’ve sent a link to " + m.Email +
		". Not there in a few minutes? Check spam, or press “Email me my membership page link” below."
	reply(http.StatusOK, d)
}

// renewal answers the join form for an address already on the list (see
// renewal.go): what was typed waits for the member to confirm it from their
// inbox, and the page says the same whoever the member is, paid or not, so
// typing someone's address shows nothing about them.
func (a *App) renewal(r *http.Request, reply func(int, joinData), m Member, details memberDetails,
	riders []joinRider, adults []HouseholdAdult, year int) {
	p := PendingJoin{Year: year, Phone: details.Phone, EmergencyName: details.EmergencyName, EmergencyPhone: details.EmergencyPhone,
		Level: details.Level, Terms: details.Terms, Signature: details.Signature, Drawing: details.Drawing, IP: clientIP(r), Adults: adults}
	for _, k := range riders {
		p.Riders = append(p.Riders, PendingRider{k.Name, k.Age})
	}
	m, err := a.store.SetPendingJoin(m.ID, p)
	if err != nil {
		log.Printf("join %s: renewal: %v", m.Email, err)
		reply(http.StatusInternalServerError, joinData{Message: "Something went wrong on our end. Try again in a minute."})
		return
	}
	if a.limit.Allow("confirm:"+m.Email, 3, time.Hour) {
		a.mailer.Go(func() { a.sendConfirm(m) }) // a failure is logged by send
	}
	reply(http.StatusOK, joinData{OK: true, Form: memberDetails{Email: m.Email},
		Message: "This email is already on our list.",
		Detail: "We’ve sent a link to " + m.Email + ". Open it and press Confirm it’s me to finish: that signs your " + strconv.Itoa(year) +
			" waiver and saves what you filled in. Your membership page, from the same link, shows your dues and how to pay them." +
			" Not there in a few minutes? Check spam, or press “Email me my membership page link” below."})
}

// applyJoin signs the waivers from a join form (theirs, and any riders under
// 18 they signed for as parent or guardian) and adds the other adults on a
// Family plan. It runs when a new member joins, and when a member confirms a
// renewal. It returns the riders signed for and the adults added.
func (a *App) applyJoin(m Member, year int, signature string, drawing []byte, ip string, riders []joinRider, adults []HouseholdAdult) ([]string, []HouseholdAdult) {
	// This year's waiver, unless they already signed one. The record keeps
	// the name on file and the name typed.
	if _, _, err := a.store.SignWaiver(Waiver{MemberID: m.ID, Year: year, Signature: signature, Drawing: drawing, Where: "Join form", IP: ip}, a.site.waiver); err != nil {
		log.Printf("join %s: waiver: %v", m.Email, err)
	}
	var signedFor []string
	for _, k := range riders {
		signed := Waiver{MemberID: m.ID, Year: year, Signature: signature, Drawing: drawing, Where: "Join form", IP: ip,
			Rider: k.Name, Minor: true, RiderAge: k.Age, GuardianPhone: m.Phone}
		if _, _, err := a.store.SignWaiver(signed, a.site.waiver.forMinor()); err != nil {
			log.Printf("join %s: minor waiver: %v", m.Email, err)
			continue
		}
		signedFor = append(signedFor, k.Name)
	}
	// The other adults on a Family plan sign their own waivers, with the link
	// emailed to them. A member who has adults listed already keeps them.
	var added []HouseholdAdult
	if m.Plan == "family" && len(adults) > 0 {
		var err error
		if added, err = a.store.AddHousehold(m.ID, adults, true); err != nil {
			log.Printf("join %s: household: %v", m.Email, err)
		}
	}
	return signedFor, added
}

// ---- Unsubscribe ----

type unsubData struct {
	Token, Email string
	State        string // ask, done, test (the link in a test email), invalid
}

// unsubscribePage asks before unsubscribing, because email scanners open
// links automatically and would otherwise unsubscribe people by accident.
func (a *App) unsubscribePage(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	m, ok := a.store.MemberByToken(t)
	d := unsubData{Token: t, Email: m.Email, State: "ask"}
	switch {
	case t == testToken:
		d.State = "test"
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
	// The token comes along so "Turn club emails back on" works from here.
	d := unsubData{Token: t, Email: m.Email, State: "done"}
	switch {
	case t == testToken:
		d.State = "test"
	case !ok:
		d.State = "invalid"
	}
	a.site.render(w, http.StatusOK, "unsubscribe", "/unsubscribe", "Unsubscribe — "+siteName, d)
}

// ---- Sitemap ----

// sitemap lists the public pages for search engines: the content pages, the
// calendar, the join form, every published newsletter, and the photo albums.
func (a *App) sitemap(w http.ResponseWriter, r *http.Request) {
	type loc struct {
		Loc string `xml:"loc"`
	}
	var urls []loc
	for _, p := range *a.site.pageList.Load() {
		urls = append(urls, loc{a.cfg.SiteURL + p.Path})
	}
	for _, path := range []string{"/calendar", "/routes", "/join", "/newsletter", "/gallery"} {
		urls = append(urls, loc{a.cfg.SiteURL + path})
	}
	for _, is := range a.store.Issues() {
		if is.IsPublished() {
			urls = append(urls, loc{a.cfg.SiteURL + is.Path()})
		}
	}
	for _, c := range a.store.AlbumCards(false) {
		urls = append(urls, loc{a.cfg.SiteURL + c.Href()})
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

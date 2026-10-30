package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const sessionCookie = "pv_admin"

type adminView struct {
	Title, Tab, Flash, Error, SiteURL string
	Me                                string // the logged-in admin's name
	Nav                               adminNav
	Attention                         *attentionView // the Needs attention page
	CSS                               template.CSS
	Year                              int
	// Members tab
	Q, Dues       string // search text; dues filter: "", "paid", "unpaid"
	Members       []Member
	Active, Unsub int
	Paid, Unpaid  int // active members paid / not paid for Year
	Allowance     emailAllowance
	// CanManage: the logged-in admin may delete members and remove admins
	// (the president, or whoever has the server's setup password).
	CanManage bool
	// CanNamePresident: the logged-in admin may choose who is president.
	CanNamePresident bool
	// Email tab
	Campaigns     []Campaign
	Draft         EmailDraft
	Welcome       WelcomeEmail
	TestTo        string
	Live, Sending bool
	// Newsletter tab
	Issues     []Issue
	Issue      Issue
	MonthLive  *Issue    // another issue already published for Issue's month
	IssueRides ridesView // what the issue's rides block shows right now
	Months     []monthOption
	NewMonth   int
	NewYear    int
	// Minutes tab
	Minutes []Minutes
	// Instagram tab
	Instagram     []string
	InstagramText string
	InstagramLink *InstagramLink
	InstaFeed     []InstaPost
	// Pages and Board tabs
	Pages    []pageRow
	Page     pageRow
	PageText string
	Board    []BoardMember
	// Home photos tab
	HomePhotos    []HomePhoto
	MaxHomePhotos int
	// Rides tab
	Series              []RideSeries
	Upcoming            []RideOcc
	Weekdays            []monthOption
	Held                []RideOcc // rides added on the website, waiting for approval
	StravaReady         bool
	StravaAdmins        []string
	StravaAdminsChecked *time.Time
	StravaLink          *StravaLink
	APIEnabled          bool
	SeriesForm          seriesForm
	History             *historyView // the ride history page
	// Calendar tab and ride pages
	Month    calMonth
	RideForm rideForm
	// Admins tab, login, and password pages
	Admins     []AdminUser
	Account    AdminUser // the logged-in admin, or whose password is being set
	SetupLogin bool      // no accounts yet, so the setup password logs in
	Email      string    // typed on the login form
	Token      string    // the password link's secret
	TestLink   string    // in test mode, the password link that would have been emailed
}

// ---- Sessions ----
// Admins log in with their own account (see admins.go), so the newsletter and
// the calendar can show who did what. A login cookie is a signed expiry time
// and the account (or, for the setup password, the name typed at login).
// Changing ADMIN_PASSWORD logs everyone out, since the signing key includes it.

func (a *App) sessionKey() []byte {
	k := sha256.Sum256([]byte("admin\x00" + a.store.Secret() + "\x00" + a.cfg.AdminPassword))
	return k[:]
}

// adminName returns the logged-in admin's name, or false if nobody is logged in.
func (a *App) adminName(r *http.Request) (string, bool) {
	u, ok := a.currentAdmin(r)
	return u.Name, ok
}

// sessionValue returns what the login cookie holds, if it's valid.
func (a *App) sessionValue(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	exp, rest, _ := strings.Cut(c.Value, ".")
	enc, sig, ok := strings.Cut(rest, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(sign(a.sessionKey(), exp+"."+enc))) {
		return "", false
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	name, err2 := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || err2 != nil || time.Now().Unix() >= n {
		return "", false
	}
	return string(name), true
}

func (a *App) loggedIn(r *http.Request) bool {
	_, ok := a.adminName(r)
	return ok
}

// setSession logs someone in: who is an account (accountSession) or, for
// the setup password, a name.
func (a *App) setSession(w http.ResponseWriter, who string, maxAge int) {
	exp := strconv.FormatInt(time.Now().Add(time.Duration(maxAge)*time.Second).Unix(), 10)
	enc := base64.RawURLEncoding.EncodeToString([]byte(who))
	value := exp + "." + enc + "." + sign(a.sessionKey(), exp+"."+enc)
	if maxAge < 0 {
		value = ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/admin", MaxAge: maxAge,
		HttpOnly: true, Secure: strings.HasPrefix(a.cfg.SiteURL, "https://"), SameSite: http.SameSiteLaxMode,
	})
}

// sameOrigin blocks form posts coming from other websites.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		o = r.Header.Get("Referer")
	}
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func (a *App) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.loggedIn(r) {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			} else {
				http.Error(w, "Your login expired. Log in again.", http.StatusUnauthorized)
			}
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		h(w, r)
	})
}

func (a *App) renderAdmin(w http.ResponseWriter, r *http.Request, status int, name string, v adminView) {
	v.CSS, v.SiteURL = a.site.adminCSS, a.cfg.SiteURL
	v.Me, _ = a.adminName(r)
	if v.Tab != "" {
		v.Nav = adminNavFor(v.Tab, len(a.attention()))
	}
	var buf bytes.Buffer
	if err := a.site.admin.ExecuteTemplate(&buf, name, v); err != nil {
		log.Printf("admin template %s: %v", name, err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.setSession(w, "", -1)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// ---- Flash messages (passed as short codes in the URL after an action) ----

var flashText = map[string]string{
	"added":             "Member added.",
	"exists":            "That email is already on the list.",
	"deleted":           "Member deleted.",
	"sending":           "Sending now. It keeps going in the background, so you can close this page.",
	"stopped":           "Sending stopped.",
	"resumed":           "Sending resumed.",
	"welcome":           "Welcome email saved.",
	"paid":              "Marked as paid.",
	"unpaid":            "Marked as not paid.",
	"loggedin":          "",
	"discarded":         "Draft discarded.",
	"drafted":           "An email about the newsletter is ready below. Check it, send yourself a test, then send it.",
	"copied":            "This is a new draft copied from the other one. Edit away: the original is unchanged.",
	"instagram":         "Saved. The home page shows these posts now.",
	"page-saved":        "Saved. The page is updated on the website.",
	"page-reset":        "Done. The page is back to the GitHub version.",
	"page-created":      "Page added, and it’s live at the address above. Replace the sample text with your own and save.",
	"page-deleted":      "Page deleted.",
	"board":             "Saved. The board page is updated.",
	"home-photos":       "Saved. The home page is updated.",
	"connected":         "Strava is connected, and the club’s events are on the calendar.",
	"connected-error":   "Strava is connected, but reading the club’s events didn’t work. See the error below.",
	"disconnected":      "Strava disconnected. Events already imported stay on the calendar.",
	"synced":            "The club’s Strava events are up to date.",
	"igconnected":       "Instagram is connected. The home page shows the club’s posts in the site’s own layout now.",
	"igconnected-error": "Instagram is connected, but reading the posts didn’t work. See the error below.",
	"igdisconnected":    "Instagram feed removed. The home page uses Instagram’s embed for picked posts again.",
	"igsynced":          "The club’s Instagram posts are up to date.",
	"series-added":      "Weekly ride added to the calendar.",
	"series-saved":      "Weekly ride saved. Its dates from today on are updated.",
	"ride-saved":        "Saved, and the club has been emailed about any change of leader, date, or time.",
	"ride-added":        "Ride added to the calendar. The club has been emailed.",
	"ride-deleted":      "Ride deleted.",
	"ride-approved":     "Approved. The ride is on the calendar now.",
	"cancelled":         "Ride cancelled. The club has been emailed.",
	"restored":          "Ride is back on the calendar.",
	"setup":             "You’re logged in with the setup password. Add yourself and the other admins below: each gets an email to choose their own password, and from then on everyone logs in with their email.",
	"invited":           "Added. They’ve been emailed a link to choose their password.",
	"link-sent":         "A new password link is on its way.",
	"admin-removed":     "Admin removed. They can’t log in anymore.",
	"president":         "Saved. The president is marked below, and is the one admin who can remove admins and delete members.",
	"password-set":      "Your password is set. Next time, log in with your email and this password.",
	"password-changed":  "Password changed. Any other devices you were logged in on are logged out.",
	"series-deleted":    "Weekly ride removed. Dates that already had a leader stay on the calendar.",
	"leader":            "Saved, and the club has been emailed about any leader change.",
	"minutes-added":     "Minutes uploaded. They aren’t public until you publish them.",
	"minutes-published": "Published. They’re on the Meeting Minutes page now.",
	"minutes-hidden":    "Unpublished. They’re hidden from the website.",
	"minutes-deleted":   "Minutes deleted.",
	"created":           "New draft started. Replace the text and photos, then preview and publish.",
	"saved":             "Saved. It isn’t public until you publish it.",
	"updated":           "Saved. The live newsletter is updated.",
	"published":         "Published. It’s live on the website now.",
	"unpublished":       "Unpublished. It’s hidden from the website.",
	"issue-deleted":     "Issue deleted.",
}

var errorText = map[string]string{
	"name":            "Enter a name.",
	"email":           "Enter a valid email address.",
	"phone":           "Enter a phone number with its area code, like 301-555-0123.",
	"emergency":       "Enter the emergency contact’s name.",
	"emergency-phone": "Enter the emergency contact’s phone number, with its area code.",
	"president-only":  "Only the president can do that.",
	"file":            "Choose a CSV file to import.",
	"csv":             "That file couldn’t be read as a CSV. Export it from your spreadsheet as “CSV” and try again.",
	"nocol":           "Couldn’t find an email column in that file.",
	"empty":           "Add a subject and a message first.",
	"welcome":         "The welcome email needs a subject and a message.",
	"save":            "Couldn’t save. Check the server log.",
	"month":           "Choose a month and year.",
	"series":          "A weekly ride needs a name, a day, and a start time.",
	"connect":         "Connecting to Strava didn’t work. Try again.",
	"sync":            "Reading the club’s events from Strava didn’t work. See the error below.",
	"nostrava":        "Add the Strava API settings to paxvelo.conf first (README.md, “Ride calendar”).",
	"igfeed":          "That isn’t a Behold feed link. Copy it from the feed in Behold; it starts with https://feeds.behold.so/.",
	"igconnect":       "Reading that feed didn’t work. Check the link in Behold and try again.",
	"igsync":          "Reading the Instagram posts didn’t work. See the error below.",
	"season":          "Pick both season dates, or neither.",
	"noride":          "That ride isn’t on the calendar anymore.",
	"page-title":      "Give the new page a title.",
	"page-address":    "The address can only use lowercase letters, numbers, and dashes, like group-ride-etiquette.",
	"page-taken":      "That address is already used. Pick a different title or address.",
	"admin-exists":    "There’s already an admin with that email.",
	"invite":          "Added, but the email didn’t send. Check the email settings, then press “Send a new link” next to their name.",
	"remove-self":     "You can’t remove yourself. Ask another admin to.",
	"current":         "Your current password isn’t right, so it wasn’t changed.",
	"short":           "Use at least 10 characters for your new password.",
	"mismatch":        "The two new passwords don’t match.",
	"date":            "Enter the meeting date.",
	"nofile":          "Choose the minutes file to upload.",
	"bigfile":         "That file is too big. Minutes can be up to 20 MB.",
	"bigphotos":       "Those photos are too big together. Add a few at a time.",
	"gone":            "That issue was deleted.",
	"draft":           "There’s already an unsent email here, so the newsletter announcement didn’t replace it. Send it, or press Discard draft, then try “Email members about it” again.",
}

func flashFrom(r *http.Request) (flash, errMsg string) {
	q := r.URL.Query()
	if code := q.Get("ok"); code == "imported" {
		n, _ := strconv.Atoi(q.Get("n"))
		d, _ := strconv.Atoi(q.Get("d"))
		b, _ := strconv.Atoi(q.Get("b"))
		flash = fmt.Sprintf("Imported %d new %s.", n, plural(n, "member", "members"))
		if d > 0 {
			flash += fmt.Sprintf(" %d already on the list.", d)
		}
		if b > 0 {
			flash += fmt.Sprintf(" %d %s skipped (no valid email).", b, plural(b, "row", "rows"))
		}
	} else if code == "home-photos" {
		added, _ := strconv.Atoi(q.Get("added"))
		removed, _ := strconv.Atoi(q.Get("removed"))
		flash = homePhotosSaved(added, removed)
	} else {
		flash = flashText[code]
	}
	return flash, errorText[q.Get("err")]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func back(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// ---- Members ----

func (a *App) membersPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	dues := r.URL.Query().Get("dues")
	v := adminView{Title: "Members", Tab: "members", Q: q, Dues: dues, Year: a.cfg.ThisYear(), CanManage: a.canManage(r)}
	v.Flash, v.Error = flashFrom(r)
	lq := strings.ToLower(q)
	for _, m := range a.store.Members() {
		paid := m.PaidFor(v.Year)
		if m.Active() {
			v.Active++
			if paid {
				v.Paid++
			} else {
				v.Unpaid++
			}
		} else {
			v.Unsub++
		}
		if lq != "" && !m.matches(lq) {
			continue
		}
		if (dues == "paid" && !paid) || (dues == "unpaid" && paid) {
			continue
		}
		v.Members = append(v.Members, m)
	}
	a.renderAdmin(w, r, http.StatusOK, "members", v)
}

// matches reports whether a member's name, email, or phone number contains
// the (lowercase) search text.
func (m Member) matches(lq string) bool {
	digits := phoneDigits(lq)
	return strings.Contains(strings.ToLower(m.Name), lq) || strings.Contains(m.Email, lq) ||
		(len(digits) >= 3 && strings.Contains(phoneDigits(m.Phone), digits))
}

// markPaid records (or undoes) this year's dues for a member, usually right
// after PayPal emails the club about a payment.
func (a *App) markPaid(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	paid := r.FormValue("paid") == "1"
	if err := a.store.SetPaid(id, a.cfg.ThisYear(), paid); err != nil {
		back(w, r, "/admin/members?err=save")
		return
	}
	to := "/admin/members?ok=unpaid"
	if paid {
		to = "/admin/members?ok=paid"
	}
	// Keep the admin's search and filter in place.
	if ret := r.FormValue("return"); ret != "" {
		if u, err := url.Parse(ret); err == nil {
			q := u.Query()
			q.Set("ok", strings.TrimPrefix(to, "/admin/members?ok="))
			to = "/admin/members?" + q.Encode()
		}
	}
	back(w, r, to)
}

func (a *App) addMember(w http.ResponseWriter, r *http.Request) {
	d, problem := joinDetails(r, false)
	if problem != "" {
		back(w, r, "/admin/members?err="+problem)
		return
	}
	year := a.cfg.ThisYear()
	m, res, err := a.store.Join(d, year)
	if err == nil && r.FormValue("paid") != "" {
		err = a.store.SetPaid(m.ID, year, true)
	}
	switch {
	case err != nil:
		back(w, r, "/admin/members?err=save")
	case res == AlreadyMember:
		back(w, r, "/admin/members?ok=exists")
	default:
		back(w, r, "/admin/members?ok=added")
	}
}

// deleteMember takes someone off the list for good. Only the president can.
func (a *App) deleteMember(w http.ResponseWriter, r *http.Request) {
	if !a.canManage(r) {
		back(w, r, "/admin/members?err=president-only")
		return
	}
	id, _ := strconv.Atoi(r.PathValue("id"))
	if err := a.store.DeleteMember(id); err != nil {
		back(w, r, "/admin/members?err=save")
		return
	}
	back(w, r, "/admin/members?ok=deleted")
}

func (a *App) exportCSV(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="paxvelo-members-`+time.Now().In(a.cfg.Location).Format("2006-01-02")+`.csv"`)
	cw := csv.NewWriter(w)
	year := a.cfg.ThisYear()
	cw.Write([]string{"Name", "Email", "Phone", "Emergency contact", "Emergency phone", "Plan", "Riding", "Rides", fmt.Sprintf("Paid %d", year), "Years paid", "Joined", "Status"})
	for _, m := range a.store.Members() {
		status := "Active"
		if !m.Active() {
			status = "Unsubscribed"
		}
		paid := "No"
		if m.PaidFor(year) {
			paid = "Yes"
		}
		years := make([]string, len(m.PaidYears))
		for i, y := range m.PaidYears {
			years[i] = strconv.Itoa(y)
		}
		cw.Write([]string{m.Name, m.Email, m.Phone, m.EmergencyName, m.EmergencyPhone, m.PlanName(), m.LevelName(), m.RideNames(), paid, strings.Join(years, " "),
			m.Joined.In(a.cfg.Location).Format("2006-01-02"), status})
	}
	cw.Flush()
}

func (a *App) importCSV(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	f, _, err := r.FormFile("file")
	if err != nil {
		back(w, r, "/admin/members?err=file")
		return
	}
	defer f.Close()
	rows, bad, err := parseImport(f)
	if err != nil {
		code := "csv"
		if errors.Is(err, errNoEmailColumn) {
			code = "nocol"
		}
		back(w, r, "/admin/members?err="+code)
		return
	}
	// Optionally bring over only people with a membership, now or in the past.
	who := r.FormValue("who")
	var list []Member
	for _, row := range rows {
		if (who == "current" && !row.membership) || (who == "members" && !row.membership && !row.paidBefore) {
			continue
		}
		list = append(list, row.Member)
	}
	added, dup, err := a.store.Import(list)
	if err != nil {
		back(w, r, "/admin/members?err=save")
		return
	}
	back(w, r, fmt.Sprintf("/admin/members?ok=imported&n=%d&d=%d&b=%d", added, dup, bad))
}

var errNoEmailColumn = errors.New("no email column")

// importRow is one person from an uploaded spreadsheet.
type importRow struct {
	Member
	membership bool // the file says they hold a membership now
	paidBefore bool // the file shows past dues payments
}

var yearRe = regexp.MustCompile(`\b(20\d\d)\b`)

// importDate understands Squarespace dates ("October 7, 2025 at 7:49:38 AM EDT")
// and plain ones ("2025-10-07").
func importDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " at "); i > 0 {
		s = s[:i]
	}
	for _, layout := range []string{"January 2, 2006", "2006-01-02", "1/2/2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Add(12 * time.Hour).UTC()
		}
	}
	return time.Time{}
}

// parseImport reads a member list exported from Squarespace, a spreadsheet,
// or this site's own export. It needs an email column and picks up whatever
// else it recognizes: names, phone numbers, emergency contacts, plan, years
// paid, join date, and opt-outs.
func parseImport(r io.Reader) (rows []importRow, bad int, err error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true
	records, err := cr.ReadAll()
	if err != nil {
		return nil, 0, err
	}
	if len(records) == 0 {
		return nil, 0, errNoEmailColumn
	}
	names := map[string]string{
		"email": "email", "email address": "email", "e-mail": "email", "email_address": "email",
		"name": "name", "full name": "name", "full_name": "name", "fullname": "name", "member name": "name",
		"first name": "first", "first_name": "first", "firstname": "first", "first": "first",
		"last name": "last", "last_name": "last", "lastname": "last", "last": "last", "surname": "last",
		"plan": "plan", "membership": "plan", "membership type": "plan", "member areas": "plan",
		"phone": "phone", "phone number": "phone", "mobile": "phone", "cell": "phone", "cell phone": "phone",
		"billing phone number": "phone2", "shipping phone number": "phone2",
		"emergency contact": "ename", "emergency contact name": "ename",
		"emergency phone": "ephone", "emergency contact phone": "ephone",
		"years paid": "years", "last order date": "lastorder",
		"joined": "joined", "member since": "joined", "created on": "created",
		"status": "status", "accepts email marketing": "consent",
	}
	col := map[string]int{}
	header := make([]string, len(records[0]))
	for i, h := range records[0] {
		header[i] = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		if key := names[header[i]]; key != "" {
			if _, taken := col[key]; !taken {
				col[key] = i
			}
		}
	}
	if _, ok := col["email"]; !ok {
		for i, h := range header {
			if strings.Contains(h, "email") {
				col["email"] = i
				break
			}
		}
	}
	start := 1
	if _, ok := col["email"]; !ok {
		// No header row: use whichever column holds an email address.
		start = 0
		for i, v := range records[0] {
			if strings.Contains(v, "@") {
				col["email"] = i
				break
			}
		}
		if _, ok := col["email"]; !ok {
			return nil, 0, errNoEmailColumn
		}
		for i := range records[0] {
			if i != col["email"] {
				col["name"] = i
				break
			}
		}
	}
	for _, rec := range records[start:] {
		get := func(key string) string {
			if i, ok := col[key]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		email, ok := cleanEmail(get("email"))
		if !ok {
			if strings.TrimSpace(strings.Join(rec, "")) != "" {
				bad++
			}
			continue
		}
		name := get("name")
		if name == "" {
			name = get("first") + " " + get("last")
		}
		row := importRow{Member: Member{
			Name: cleanName(name), Email: email,
			Phone:         cleanPhone(or(get("phone"), get("phone2"))),
			EmergencyName: cleanName(get("ename")), EmergencyPhone: cleanPhone(get("ephone")),
		}}
		plan := strings.ToLower(get("plan"))
		row.Plan = "individual"
		if strings.Contains(plan, "family") {
			row.Plan = "family"
		}
		row.membership = plan != ""
		seen := map[int]bool{}
		for _, y := range yearRe.FindAllString(get("years")+" "+get("lastorder"), -1) {
			n, _ := strconv.Atoi(y)
			if !seen[n] {
				seen[n] = true
				row.PaidYears = append(row.PaidYears, n)
			}
		}
		sort.Ints(row.PaidYears)
		row.paidBefore = len(row.PaidYears) > 0
		if row.Joined = importDate(get("joined")); row.Joined.IsZero() {
			row.Joined = importDate(get("created"))
		}
		if strings.EqualFold(get("status"), "unsubscribed") || strings.EqualFold(get("consent"), "false") {
			now := time.Now().UTC()
			row.Unsubscribed = &now
		}
		rows = append(rows, row)
	}
	return rows, bad, nil
}

// backup downloads all the club data, including every member's contact
// details and the admins' password hashes, so only the president can.
func (a *App) backup(w http.ResponseWriter, r *http.Request) {
	if !a.canManage(r) {
		back(w, r, "/admin/members?err=president-only")
		return
	}
	b, err := a.store.Snapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="paxvelo-backup-`+time.Now().In(a.cfg.Location).Format("2006-01-02")+`.json"`)
	w.Write(b)
}

// ---- Email ----

func (a *App) emailView(r *http.Request) adminView {
	v := adminView{
		Title: "Email", Tab: "email", Campaigns: a.store.Campaigns(), Draft: a.store.Draft(),
		Welcome: a.store.Welcome(), TestTo: a.cfg.AdminEmail, Live: a.mailer.live(), Year: a.cfg.ThisYear(),
		Allowance: emailAllowance{Sent: a.store.EmailsToday(), Limit: a.cfg.EmailLimit},
	}
	for _, m := range a.store.Members() {
		if !m.Active() {
			continue
		}
		v.Active++
		if m.PaidFor(v.Year) {
			v.Paid++
		} else {
			v.Unpaid++
		}
	}
	for _, c := range v.Campaigns {
		if c.Status == "sending" {
			v.Sending = true
		}
	}
	v.Flash, v.Error = flashFrom(r)
	return v
}

func (a *App) emailPage(w http.ResponseWriter, r *http.Request) {
	a.renderAdmin(w, r, http.StatusOK, "email", a.emailView(r))
}

func (a *App) emailStatus(w http.ResponseWriter, r *http.Request) {
	type row struct {
		ID       int    `json:"id"`
		Progress string `json:"progress"`
		Status   string `json:"status"`
		Error    string `json:"error"`
	}
	var out []row
	for _, c := range a.store.Campaigns() {
		out = append(out, row{c.ID, progress(c), statusLabel(c.Status), c.LastError})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func draftFrom(r *http.Request) EmailDraft {
	return EmailDraft{Subject: strings.TrimSpace(r.FormValue("subject")), Body: strings.TrimSpace(r.FormValue("body"))}
}

// emailPreview answers with the email as a member would see it, for the
// preview beside the message box. It fills in the {placeholders} for a
// sample member named Pat.
func (a *App) emailPreview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	d := draftFrom(r)
	pat := Member{Name: "Pat Rider", Plan: "individual", Level: "some"}
	page, err := a.site.renderEmail(a.mailer.personalize(d.Subject, pat), a.mailer.personalize(d.Body, pat), memberLinks{Mine: "#", Unsub: "#"}, "")
	if err != nil {
		http.Error(w, "The preview couldn’t be made.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, page)
}

func (a *App) sendTest(w http.ResponseWriter, r *http.Request) {
	d := draftFrom(r)
	a.store.SetDraft(d) // keep what was typed, so "Send to everyone" is one click after the test
	v := a.emailView(r)
	v.Draft = d
	to, ok := cleanEmail(r.FormValue("test_to"))
	switch {
	case d.Subject == "" || d.Body == "":
		v.Error = errorText["empty"]
	case !ok:
		v.Error = "Enter a valid email address to send the test to."
	default:
		v.TestTo = to
		if err := a.mailer.SendTest(to, d.Subject, d.Body); err != nil {
			v.Error = "The test email didn’t send: " + err.Error()
		} else if a.mailer.live() {
			v.Flash = "Test sent to " + to + ". Check that inbox, then send to everyone."
		} else {
			v.Flash = "Test mode: the email was written to the server log instead of being sent."
		}
	}
	a.renderAdmin(w, r, http.StatusOK, "email", v)
}

func (a *App) sendAll(w http.ResponseWriter, r *http.Request) {
	d := draftFrom(r)
	if d.Subject == "" || d.Body == "" {
		a.store.SetDraft(d)
		back(w, r, "/admin/email?err=empty")
		return
	}
	if _, err := a.store.StartCampaign(d.Subject, d.Body, r.FormValue("audience"), a.cfg.ThisYear()); err != nil {
		a.store.SetDraft(d)
		v := a.emailView(r)
		v.Draft, v.Error = d, err.Error()
		a.renderAdmin(w, r, http.StatusOK, "email", v)
		return
	}
	a.mailer.Wake()
	back(w, r, "/admin/email?ok=sending")
}

func (a *App) stopCampaign(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	a.store.StopCampaign(id)
	back(w, r, "/admin/email?ok=stopped")
}

func (a *App) resumeCampaign(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	a.store.ResumeCampaign(id)
	a.mailer.Wake()
	back(w, r, "/admin/email?ok=resumed")
}

// discardDraft empties the saved email draft.
func (a *App) discardDraft(w http.ResponseWriter, r *http.Request) {
	if err := a.store.SetDraft(EmailDraft{}); err != nil {
		back(w, r, "/admin/email?err=save")
		return
	}
	back(w, r, "/admin/email?ok=discarded")
}

func (a *App) saveWelcome(w http.ResponseWriter, r *http.Request) {
	we := WelcomeEmail{
		Enabled: r.FormValue("enabled") != "",
		Subject: strings.TrimSpace(r.FormValue("subject")),
		Body:    strings.TrimSpace(r.FormValue("body")),
		Tips:    map[string]string{},
	}
	for _, c := range riderLevels {
		// Advice left as the built-in text isn't saved, so it follows any
		// later change to the built-in advice.
		t := strings.TrimSpace(strings.ReplaceAll(r.FormValue("tips_"+c.Value), "\r\n", "\n"))
		if t != "" && t != levelAdvice[c.Value] {
			we.Tips[c.Value] = t
		}
	}
	if we.Subject == "" || we.Body == "" {
		back(w, r, "/admin/email?err=welcome#welcome")
		return
	}
	if err := a.store.SetWelcome(we); err != nil {
		back(w, r, "/admin/email?err=save#welcome")
		return
	}
	back(w, r, "/admin/email?ok=welcome#welcome")
}

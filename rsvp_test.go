package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// An invitation from start to finish: an admin makes it, the invitation
// email carries each member's own link, members answer (with guests and what
// they're bringing), and the admin page adds it up.
func TestRSVP(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	a.store.Import([]Member{
		{Name: "Pat Rider", Email: "pat@example.com", PaidYears: []int{year}, Joined: time.Now().AddDate(-1, 0, -1)},
		{Name: "Sam Spoke", Email: "sam@example.com", PaidYears: []int{year}, Joined: time.Now().AddDate(-1, 0, -1)},
		{Name: "Quinn Quiet", Email: "quinn@example.com", PaidYears: []int{year}, Joined: time.Now().AddDate(-1, 0, -1)},
	})
	pat, _ := a.store.MemberByEmail("pat@example.com")
	sam, _ := a.store.MemberByEmail("sam@example.com")

	day := time.Now().In(clubTZ).AddDate(0, 0, 20).Format("2006-01-02")
	ct, body := form(url.Values{"title": {"December Meeting"}, "date": {day}, "time": {"18:30"}, "place": {"12 Spoke Lane, Leonardtown"},
		"details": {"Park on the street."}, "ask_bringing": {"1"}, "show_guests": {"1"}})
	w := do(t, h, admin, "POST", "/admin/rsvp", ct, body)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "/admin/rsvp/1?ok=rsvp-saved") {
		t.Fatalf("making the invitation: %d %s", w.Code, w.Header().Get("Location"))
	}
	e, ok := a.store.RSVPEvent(1)
	if !ok || e.Title != "December Meeting" || !e.AskBringing || !e.IsOpen(time.Now()) || !strings.HasSuffix(e.When(), "at 6:30 pm") {
		t.Fatalf("the invitation: %+v (%s)", e, e.When())
	}

	// The invitation email: the button is each member's own link.
	if w := do(t, h, admin, "POST", "/admin/rsvp/1/email", "", nil); !strings.Contains(w.Header().Get("Location"), "ok=rsvp-drafted") {
		t.Fatalf("writing the email: %s", w.Header().Get("Location"))
	}
	d := a.store.Draft()
	if !strings.Contains(d.Body, "[[RSVP]({rsvp_1})]") || !strings.Contains(d.Body, "({rsvp_1_calendar})") || !strings.Contains(d.Body, "({rsvp_1_google})") || !strings.Contains(d.Body, "[Directions]({rsvp_1_directions})") || !strings.Contains(d.Body, "12 Spoke Lane") || d.Subject != "You’re invited: December Meeting" {
		t.Fatalf("the draft: %+v", d)
	}
	if got := a.mailer.personalize(d.Body, pat); !strings.Contains(got, a.cfg.SiteURL+"/rsvp/1?t="+pat.Token) {
		t.Errorf("Pat's copy should have Pat's link: %s", got)
	}
	// A second press doesn't overwrite the draft.
	if w := do(t, h, admin, "POST", "/admin/rsvp/1/email", "", nil); !strings.Contains(w.Header().Get("Location"), "err=rsvp-draft") {
		t.Errorf("a draft already there: %s", w.Header().Get("Location"))
	}
	// The email page offers the link as a piece.
	if page := do(t, h, admin, "GET", "/admin/email", "", nil).Body.String(); !strings.Contains(page, `value="rsvp_1"`) {
		t.Errorf("the email editor should offer the RSVP link")
	}

	// Without a member's link: no place, no form, not indexed.
	w = do(t, h, nil, "GET", "/rsvp/1", "", nil)
	if page := w.Body.String(); w.Code != 200 || strings.Contains(page, "Spoke Lane") || strings.Contains(page, "Send my RSVP") || w.Header().Get("X-Robots-Tag") != "noindex" || w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Errorf("a page without a link: %d %s", w.Code, page)
	}
	if w := do(t, h, nil, "GET", "/rsvp/1?t=nobody", "", nil); strings.Contains(w.Body.String(), "Spoke Lane") {
		t.Errorf("a made-up link shouldn't show the place")
	}
	// Pat's link: the place, and the form with their name filled in.
	page := do(t, h, nil, "GET", "/rsvp/1?t="+pat.Token, "", nil).Body.String()
	if !strings.Contains(page, "12 Spoke Lane") || !strings.Contains(page, "Send my RSVP") || !strings.Contains(page, `value="Pat Rider"`) || !strings.Contains(page, "What are you bringing?") {
		t.Errorf("Pat's page: %s", page)
	}

	// Pat's coming with two, bringing chili.
	ct, body = form(url.Values{"t": {pat.Token}, "name": {"Pat Rider"}, "answer": {"yes"}, "guests": {"2"}, "who": {"Alex and Jo"}, "bringing": {"Chili"}})
	if w := do(t, h, nil, "POST", "/rsvp/1", ct, body); w.Code != http.StatusSeeOther {
		t.Fatalf("Pat answering: %d %s", w.Code, w.Body)
	}
	// Sam can't come: guests and bringing are dropped.
	ct, body = form(url.Values{"t": {sam.Token}, "name": {"Sam"}, "answer": {"no"}, "guests": {"3"}, "bringing": {"Pie"}})
	do(t, h, nil, "POST", "/rsvp/1", ct, body)
	// No answer picked: the form comes back.
	ct, body = form(url.Values{"t": {sam.Token}, "name": {"Sam"}})
	if w := do(t, h, nil, "POST", "/rsvp/1", ct, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Pick whether you can come") {
		t.Errorf("no answer: %d", w.Code)
	}
	// A test email's link saves nothing.
	ct, body = form(url.Values{"t": {testToken}, "name": {"Test"}, "answer": {"yes"}})
	if w := do(t, h, nil, "POST", "/rsvp/1", ct, body); !strings.Contains(w.Body.String(), "nothing was saved") {
		t.Errorf("a test answer: %s", w.Body)
	}

	e, _ = a.store.RSVPEvent(1)
	c := e.Count()
	if c.Yes != 1 || c.No != 1 || c.Guests != 2 || c.Coming != 3 || len(e.Replies) != 2 {
		t.Fatalf("the count: %+v %+v", c, e.Replies)
	}
	if r, _ := e.ReplyFor(sam.ID); r.Guests != 0 || r.Bringing != "" || r.Name != "Sam" {
		t.Errorf("Sam's answer: %+v", r)
	}
	page = do(t, h, nil, "GET", "/rsvp/1?t="+pat.Token+"&saved=1", "", nil).Body.String()
	if !strings.Contains(page, "See you there") {
		t.Errorf("after answering: %s", page)
	}

	// Members see who's coming and what they're bringing (not who can't).
	page = do(t, h, nil, "GET", "/rsvp/1?t="+sam.Token, "", nil).Body.String()
	going := page[max(0, strings.Index(page, `id="going"`)):]
	if !strings.Contains(going, "Pat Rider") || !strings.Contains(going, "with Alex and Jo") || !strings.Contains(going, "bringing Chili") || strings.Contains(going, ">Sam<") {
		t.Errorf("who's coming: %s", going)
	}
	if page := do(t, h, nil, "GET", "/rsvp/1", "", nil).Body.String(); strings.Contains(page, "Pat Rider") {
		t.Error("without a member's link the guest list stays hidden")
	}

	// Directions: Apple Maps on an iPhone, Google Maps elsewhere, both to
	// the street address; without a member's link, back to the RSVP page.
	directions := func(path, ua string) string {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("User-Agent", ua)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Header().Get("Location")
	}
	if loc := directions("/rsvp/1/directions?t="+pat.Token, "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)"); loc != "https://maps.apple.com/?daddr=12+Spoke+Lane%2C+Leonardtown" {
		t.Errorf("directions on an iPhone: %s", loc)
	}
	if loc := directions("/rsvp/1/directions?t="+pat.Token, "Mozilla/5.0 (Linux; Android 14)"); !strings.HasPrefix(loc, "https://www.google.com/maps/dir/?api=1&destination=12+Spoke+Lane") {
		t.Errorf("directions on Android: %s", loc)
	}
	if loc := directions("/rsvp/1/directions", "iPhone"); loc != "/rsvp/1?t=" {
		t.Errorf("directions without a link: %s", loc)
	}
	if page := do(t, h, nil, "GET", "/rsvp/1?t="+pat.Token, "", nil).Body.String(); !strings.Contains(page, ">Directions</a>") || !strings.Contains(page, "Waze") {
		t.Error("the page should have the Directions button")
	}

	// Add to my calendar: the place only from a member's link.
	w = do(t, h, nil, "GET", "/rsvp/1/calendar.ics?t="+pat.Token, "", nil)
	ics := w.Body.String()
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/calendar") || !strings.Contains(ics, "SUMMARY:December Meeting") ||
		!strings.Contains(ics, "LOCATION:12 Spoke Lane\\, Leonardtown") || !strings.Contains(ics, "DTSTART:") {
		t.Errorf("the calendar file: %s", ics)
	}
	if ics := do(t, h, nil, "GET", "/rsvp/1/calendar.ics", "", nil).Body.String(); strings.Contains(ics, "Spoke Lane") {
		t.Error("a calendar file without a link shouldn't have the place")
	}
	if loc := do(t, h, nil, "GET", "/rsvp/1/google?t="+pat.Token, "", nil).Header().Get("Location"); !strings.HasPrefix(loc, "https://calendar.google.com/calendar/render?") ||
		!strings.Contains(loc, "location=12+Spoke+Lane") || !strings.Contains(loc, "dates=") {
		t.Errorf("Google Calendar: %s", loc)
	}

	// Pat changes their mind: still one answer.
	ct, body = form(url.Values{"t": {pat.Token}, "name": {"Pat Rider"}, "answer": {"maybe"}, "bringing": {"Cookies"}})
	do(t, h, nil, "POST", "/rsvp/1", ct, body)
	e, _ = a.store.RSVPEvent(1)
	if c := e.Count(); c.Maybe != 1 || c.Yes != 0 || len(e.Replies) != 2 {
		t.Errorf("after changing: %+v", c)
	}

	// The admin page: the answers, who's bringing what, who hasn't answered.
	page = do(t, h, admin, "GET", "/admin/rsvp/1", "", nil).Body.String()
	for _, want := range []string{"Pat Rider", "Cookies", "Can’t make it", "Quinn Quiet", "Who’s bringing what"} {
		if !strings.Contains(page, want) {
			t.Errorf("the admin page should show %q", want)
		}
	}
	if csv := do(t, h, admin, "GET", "/admin/rsvp/1/answers.csv", "", nil).Body.String(); !strings.Contains(csv, "Pat Rider,pat@example.com,Maybe,0,,Cookies") {
		t.Errorf("the spreadsheet: %s", csv)
	}

	// Removing answers: one row's, or every ticked one; Undo puts them back.
	ct, body = form(url.Values{"member": {strconv.Itoa(sam.ID)}})
	if w := do(t, h, admin, "POST", "/admin/rsvp/1/answers/remove", ct, body); !strings.HasSuffix(w.Header().Get("Location"), "#dropped") {
		t.Errorf("removing Sam's answer: %s", w.Header().Get("Location"))
	}
	if e, _ := a.store.RSVPEvent(1); len(e.Replies) != 1 {
		t.Errorf("after removing one: %+v", e.Replies)
	}
	if page := do(t, h, admin, "GET", "/admin/rsvp/1?undo=1", "", nil).Body.String(); !strings.Contains(page, "Removed Sam’s answer") {
		t.Error("the page should offer Undo")
	}
	if page := do(t, h, admin, "GET", "/admin/rsvp/1", "", nil).Body.String(); strings.Contains(page, "Removed Sam’s answer") {
		t.Error("opening the page later shouldn't bring the Undo back")
	}
	do(t, h, admin, "POST", "/admin/rsvp/1/answers/restore", "", nil)
	ct, body = form(url.Values{"member": {strconv.Itoa(sam.ID), strconv.Itoa(pat.ID)}})
	do(t, h, admin, "POST", "/admin/rsvp/1/answers/remove", ct, body)
	if e, _ := a.store.RSVPEvent(1); len(e.Replies) != 0 {
		t.Errorf("after removing the ticked ones: %+v", e.Replies)
	}
	if page := do(t, h, admin, "GET", "/admin/rsvp/1?undo=1", "", nil).Body.String(); !strings.Contains(page, "Removed 2 answers") {
		t.Error("Undo for two")
	}
	do(t, h, admin, "POST", "/admin/rsvp/1/answers/restore", "", nil)
	if e, _ := a.store.RSVPEvent(1); len(e.Replies) != 2 {
		t.Errorf("after Undo: %+v", e.Replies)
	}

	// An event can keep the guest list to admins.
	ev, _ := a.store.RSVPEvent(1)
	ev.HideGuests = true
	a.store.SaveRSVPEvent(ev)
	if page := do(t, h, nil, "GET", "/rsvp/1?t="+sam.Token, "", nil).Body.String(); strings.Contains(page, `id="going"`) {
		t.Error("a hidden guest list")
	}

	// Closed: answers stop.
	ct, body = form(url.Values{"closed": {"1"}})
	do(t, h, admin, "POST", "/admin/rsvp/1/close", ct, body)
	ct, body = form(url.Values{"t": {sam.Token}, "name": {"Sam"}, "answer": {"yes"}})
	if w := do(t, h, nil, "POST", "/rsvp/1", ct, body); w.Code != http.StatusConflict {
		t.Errorf("answering a closed RSVP: %d", w.Code)
	}

	// Remove, then Undo: the answers come back too.
	if w := do(t, h, admin, "POST", "/admin/rsvp/1/remove", "", nil); !strings.Contains(w.Header().Get("Location"), "undo=1") {
		t.Errorf("removing: %s", w.Header().Get("Location"))
	}
	if _, ok := a.store.RSVPEvent(1); ok {
		t.Fatal("removed invitation still there")
	}
	if page := do(t, h, admin, "GET", "/admin/rsvp?undo=1", "", nil).Body.String(); !strings.Contains(page, "/admin/rsvp/restore") {
		t.Error("right after removing, the list should offer Undo")
	}
	if page := do(t, h, admin, "GET", "/admin/rsvp", "", nil).Body.String(); strings.Contains(page, "/admin/rsvp/restore") {
		t.Error("the Undo notice stuck on the RSVP list")
	}
	if w := do(t, h, nil, "GET", "/rsvp/1?t="+pat.Token, "", nil); w.Code != http.StatusNotFound {
		t.Errorf("a removed invitation's page: %d", w.Code)
	}
	do(t, h, admin, "POST", "/admin/rsvp/restore", "", nil)
	if e, ok := a.store.RSVPEvent(1); !ok || len(e.Replies) != 2 {
		t.Errorf("after Undo: %+v", e)
	}
}

// Website admins run RSVPs too (they email members about events).
func TestRSVPForWebsiteAdmins(t *testing.T) {
	a, h, _ := testApp(t)
	web, _, _ := adminLevels(t, a)
	ct, body := form(url.Values{"title": {"Holiday Party"}})
	if w := do(t, h, web, "POST", "/admin/rsvp", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=rsvp-saved") {
		t.Fatalf("a website admin making an invitation: %s", w.Header().Get("Location"))
	}
	if w := do(t, h, web, "GET", "/admin/rsvp", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Holiday Party") {
		t.Errorf("a website admin's RSVPs page: %d", w.Code)
	}
}

func TestRSVPLinks(t *testing.T) {
	got := rsvpLinks("[[RSVP]({rsvp_3})] and {rsvp_12} but not {rsvp_x}; {rsvp_3_calendar} {rsvp_3_google}", "https://x.club", "abc")
	want := "[[RSVP](https://x.club/rsvp/3?t=abc)] and https://x.club/rsvp/12?t=abc but not {rsvp_x}; https://x.club/rsvp/3/calendar.ics?t=abc https://x.club/rsvp/3/google?t=abc"
	if got != want {
		t.Errorf("got %s", got)
	}
	e := RSVPEvent{Date: "2026-12-12", Closes: "2026-12-10"}
	at := func(s string) time.Time { d, _ := time.ParseInLocation("2006-01-02 15:04", s, clubTZ); return d }
	for _, c := range []struct {
		when string
		open bool
	}{{"2026-12-09 12:00", true}, {"2026-12-10 23:00", true}, {"2026-12-11 08:00", false}} {
		if e.IsOpen(at(c.when)) != c.open {
			t.Errorf("open on %s: want %v", c.when, c.open)
		}
	}
	for place, want := range map[string]string{
		"Karl’s house, 12 Spoke Lane, Leonardtown": "12 Spoke Lane, Leonardtown",
		"Leonardtown Square":                       "Leonardtown Square",
		"The Brewery, Leonardtown":                 "The Brewery, Leonardtown",
	} {
		if got := (RSVPEvent{Place: place}).MapAddress(); got != want {
			t.Errorf("MapAddress(%q) = %q", place, got)
		}
	}
	if csvSafe("=SUM(A1)") != "'=SUM(A1)" || csvSafe("Chili") != "Chili" {
		t.Error("csvSafe")
	}
}

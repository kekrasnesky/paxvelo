package main

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// An invitation tied to a calendar event: the calendar shows the head count
// and, on hover, who's coming (short names) and what they're bringing; the
// event's page takes RSVPs from a member's link, offers anyone else their
// link by email, and has Directions, Add to calendar and Share.
func TestRSVPOnCalendar(t *testing.T) {
	a, h, admin := testApp(t)
	var mu sync.Mutex
	var mails []string
	a.mailer.onSend = func(to, subject, body string) { mu.Lock(); mails = append(mails, to+"|"+subject+"|"+body); mu.Unlock() }

	year := a.cfg.ThisYear()
	a.store.Import([]Member{
		{Name: "Pat Rider", Email: "pat@example.com", PaidYears: []int{year}, Joined: time.Now().AddDate(-1, 0, -1)},
		{Name: "Sam Spoke", Email: "sam@example.com", PaidYears: []int{year}, Joined: time.Now().AddDate(-1, 0, -1)},
	})
	pat, _ := a.store.MemberByEmail("pat@example.com")
	sam, _ := a.store.MemberByEmail("sam@example.com")

	day := a.today().AddDate(0, 0, 10).Format("2006-01-02")
	ev, err := a.store.AddRide(CalRide{Date: day, Time: "18:00", Name: "PaxVelo Club Meeting", Place: "Leonardtown Library", Kind: "meeting"})
	if err != nil {
		t.Fatal(err)
	}
	a.store.AddRide(CalRide{Date: day, Time: "08:00", Name: "Morning Ride"}) // a ride that day isn't offered

	// The new-invitation form offers the event; one with only a day picks it.
	if page := do(t, h, admin, "GET", "/admin/rsvp", "", nil).Body.String(); !strings.Contains(page, `value="`+ev.Key+`"`) || strings.Contains(page, "Morning Ride") {
		t.Errorf("the form should offer the meeting, not the ride")
	}
	ct, body := form(url.Values{"title": {"December Meeting"}, "ride": {ev.Key}, "ask_bringing": {"1"}, "show_guests": {"1"}, "remind": {"1"}})
	do(t, h, admin, "POST", "/admin/rsvp", ct, body)
	e, _ := a.store.RSVPEvent(1)
	if e.Ride != ev.Key || e.Date != day || e.Time != "18:00" {
		t.Fatalf("tied to the calendar: %+v", e)
	}
	if page := do(t, h, admin, "GET", "/admin/rsvp/1", "", nil).Body.String(); !strings.Contains(page, `/calendar/ride/`+ev.Key+`#rsvp`) {
		t.Error("the admin page should link to the event")
	}

	a.store.AnswerRSVP(1, pat, RSVPReply{Name: "Pat Rider", Answer: rsvpYes, Guests: 2, Who: "Alex and Jo", Bringing: "Chili"}, time.Now())
	a.store.AnswerRSVP(1, sam, RSVPReply{Name: "Sam Spoke", Answer: rsvpMaybe}, time.Now())

	// The month: the count on the event; the hover card lists who and what.
	cal := do(t, h, nil, "GET", "/calendar?m="+day[:7], "", nil).Body.String()
	for _, want := range []string{"3 coming · 1 maybe", "Pat R.", "+2", "Chili", "Sam S.", "View event or RSVP"} {
		if !strings.Contains(cal, want) {
			t.Errorf("the calendar should show %q", want)
		}
	}
	if strings.Contains(cal, "Pat Rider") || strings.Contains(cal, "Alex and Jo") {
		t.Error("the public calendar shows short names, not who's coming with them")
	}

	// The event page for anyone: who's coming, the buttons, a link by email.
	page := do(t, h, nil, "GET", "/calendar/ride/"+ev.Key, "", nil).Body.String()
	for _, want := range []string{`id="rsvp"`, "Pat R.", "bringing Chili", ">Directions</a>", "Add to calendar", "ride-share", `action="/rsvp/1/link"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the event page should have %q", want)
		}
	}
	if strings.Contains(page, "Send my RSVP") {
		t.Error("without a member's link there's no answer form")
	}

	// Asking for the link: the same answer for anyone, an email for a member.
	ct, body = form(url.Values{"email": {"pat@example.com"}, "back": {ev.Key}})
	w := do(t, h, nil, "POST", "/rsvp/1/link", ct, body)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "/calendar/ride/"+ev.Key+"?ok=rsvplink") {
		t.Fatalf("asking for the link: %d %s", w.Code, w.Header().Get("Location"))
	}
	ct, body = form(url.Values{"email": {"nobody@example.com"}, "back": {ev.Key}})
	if loc := do(t, h, nil, "POST", "/rsvp/1/link", ct, body).Header().Get("Location"); !strings.Contains(loc, "ok=rsvplink") {
		t.Errorf("an address not on the list gets the same answer: %s", loc)
	}
	mu.Lock()
	got := mails
	mu.Unlock()
	if len(got) != 1 || !strings.HasPrefix(got[0], "pat@example.com|Your RSVP link: December Meeting|") || !strings.Contains(got[0], "/calendar/ride/"+ev.Key+"?t="+pat.Token) {
		t.Fatalf("the link email: %q", got)
	}

	// From Pat's link: the form, full names, and answering comes back here.
	page = do(t, h, nil, "GET", "/calendar/ride/"+ev.Key+"?t="+pat.Token, "", nil).Body.String()
	if !strings.Contains(page, "Save my answer") || !strings.Contains(page, "Pat Rider") || !strings.Contains(page, "with Alex and Jo") || !strings.Contains(page, `name="back" value="`+ev.Key+`"`) {
		t.Errorf("Pat's event page: %s", page)
	}
	ct, body = form(url.Values{"t": {pat.Token}, "back": {ev.Key}, "name": {"Pat Rider"}, "answer": {"yes"}, "bringing": {"Cornbread"}})
	if loc := do(t, h, nil, "POST", "/rsvp/1", ct, body).Header().Get("Location"); !strings.HasPrefix(loc, "/calendar/ride/"+ev.Key+"?t="+pat.Token+"&saved=1") {
		t.Errorf("after answering on the calendar: %s", loc)
	}
	// The RSVP page links to the event.
	if page := do(t, h, nil, "GET", "/rsvp/1?t="+pat.Token, "", nil).Body.String(); !strings.Contains(page, "On the club calendar") {
		t.Error("the RSVP page should link to the calendar")
	}

	// Guest list kept to admins: only the counts.
	e, _ = a.store.RSVPEvent(1)
	e.HideGuests = true
	a.store.SaveRSVPEvent(e)
	if cal := do(t, h, nil, "GET", "/calendar?m="+day[:7], "", nil).Body.String(); strings.Contains(cal, "Pat R.") || !strings.Contains(cal, "coming") {
		t.Error("a hidden guest list shows counts only")
	}
}

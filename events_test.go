package main

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGuessKind(t *testing.T) {
	for name, want := range map[string]string{
		"October Membership Meeting (10/23/2026)": "meeting",
		"Holiday Party at Ruddy Duck":             "social",
		"Flat-tire clinic":                        "clinic",
		"Saturday Morning Ride":                   "",
		"Tuesday Night Ride":                      "",
	} {
		if got := guessKind(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}

// Members can add a party or a meeting as well as a ride: it has no pace
// groups, and whoever adds it hosts it.
func TestAddEventOnCalendar(t *testing.T) {
	a, h, sent := rideApp(t)
	if page := do(t, h, nil, "GET", "/calendar/new", "", nil).Body.String(); !strings.Contains(page, `name="kind" value="social"`) || !strings.Contains(page, "Social or party") {
		t.Error("Add to the calendar should ask what it is")
	}
	fri := nextWeekday(a, time.Friday)
	ct, body := form(url.Values{"date": {fri}, "time": {"18:30"}, "name": {"Holiday Party"}, "kind": {"social"}, "pace": {"2"},
		"leader_name": {"Pat Rider"}, "lead": {"on"}, "ft": {oldToken(a)}})
	if w := do(t, h, nil, "POST", "/calendar/new", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=held") {
		t.Fatalf("adding an event: %s", w.Header().Get("Location"))
	}
	held := a.store.HeldRides()
	if len(held) != 1 || held[0].Kind != "social" || len(held[0].Groups) != 0 || held[0].leaderFor("") == nil {
		t.Fatalf("event %+v", held)
	}
	if len(*sent) != 1 || !strings.HasPrefix((*sent)[0].subject, "Event waiting for approval") {
		t.Errorf("notice %+v", *sent)
	}
}

// An event can have its own block in the newsletter: it shows its day, time
// and place from the calendar, with a link to save it, and isn't listed again
// with the rides.
func TestEventInNewsletter(t *testing.T) {
	a, h, _ := rideApp(t)
	rec := httptest.NewRecorder()
	a.setSession(rec, "Board Member", 3600)
	admin := rec.Result().Cookies()[0]
	thu := nextWeekday(a, time.Thursday)
	ev, err := a.store.AddRide(CalRide{Date: thu, Time: "18:00", Name: "November Membership Meeting", Place: "Leonardtown Library", Kind: "meeting"})
	if err != nil {
		t.Fatal(err)
	}
	now := a.today()
	is, _ := a.store.SaveIssue(Issue{Month: int(now.Month()), Year: now.Year(), Blocks: []Block{{ID: "aaaa", Kind: "rides"}, {ID: "bbbb", Kind: "share"}}})

	// From the event's admin page.
	page := do(t, h, admin, "GET", "/admin/rides/"+ev.Key, "", nil).Body.String()
	if !strings.Contains(page, `action="/admin/rides/`+ev.Key+`/newsletter"`) {
		t.Fatal("the event's page should offer Add to newsletter")
	}
	ct, body := form(url.Values{"issue": {strconv.Itoa(is.ID)}})
	if to := do(t, h, admin, "POST", "/admin/rides/"+ev.Key+"/newsletter", ct, body).Header().Get("Location"); to != "/admin/newsletter/"+strconv.Itoa(is.ID)+"?ok=event-added" {
		t.Errorf("after adding: %s", to)
	}
	do(t, h, admin, "POST", "/admin/rides/"+ev.Key+"/newsletter", ct, body) // twice is still once
	is, _ = a.store.Issue(is.ID)
	if len(is.Blocks) != 3 || is.Blocks[1].Kind != "event" || is.Blocks[1].Event != ev.Key {
		t.Fatalf("the event should go after the rides, once: %+v", is.Blocks)
	}

	// The editor offers the event, and saving keeps it with a few words.
	edit := do(t, h, admin, "GET", "/admin/newsletter/"+strconv.Itoa(is.ID), "", nil).Body.String()
	if !strings.Contains(edit, `<option value="`+ev.Key+`" selected>`) || !strings.Contains(edit, "Club meeting: November Membership Meeting") {
		t.Error("the Event block should list the event, picked")
	}
	p := "b." + is.Blocks[1].ID + "."
	ct, body = form(url.Values{"blocks_form": {"1"}, "month": {strconv.Itoa(is.Month)}, "year": {strconv.Itoa(is.Year)},
		"block": {"aaaa:rides", is.Blocks[1].ID + ":event", "bbbb:share"}, p + "event": {ev.Key}, p + "body": {"Elections for next year’s board."}})
	do(t, h, admin, "POST", "/admin/newsletter/"+strconv.Itoa(is.ID), ct, body)
	is, _ = a.store.Issue(is.ID)
	if b := is.Blocks[1]; b.Event != ev.Key || b.Body != "Elections for next year’s board." {
		t.Fatalf("saved %+v", b)
	}

	// What readers see.
	v, err := a.issueView(is, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range v.Parts[0].Rides.Rides {
		if r.Key == ev.Key {
			t.Error("an event with its own block shouldn't be listed with the rides too")
		}
	}
	html, _ := a.site.renderNews(v, is.Path(), "x")
	for _, want := range []string{`class="nl-event"`, "Club meeting", "November Membership Meeting", "Leonardtown Library", "6:00 PM",
		`href="/calendar/ride/` + ev.Key + `/calendar.ics"`, "Add to my calendar", "Elections for next year’s board."} {
		if !strings.Contains(string(html), want) {
			t.Errorf("the issue should show %s", want)
		}
	}
	// The email about the issue lists it.
	do(t, h, admin, "POST", "/admin/newsletter/"+strconv.Itoa(is.ID)+"/email", "", nil)
	if d := a.store.Draft(); !strings.Contains(d.Body, "Coming up:") || !strings.Contains(d.Body, "November Membership Meeting") || !strings.Contains(d.Body, "/calendar/ride/"+ev.Key) {
		t.Errorf("email draft: %s", d.Body)
	}
	// Cancelling the event shows in the newsletter.
	a.store.UpdateRide(ev.Key, func(c *CalRide) error { c.Cancelled = true; return nil })
	v, _ = a.issueView(is, nil)
	html, _ = a.site.renderNews(v, is.Path(), "x")
	if !strings.Contains(string(html), "nl-event off") || strings.Contains(string(html), "Add to my calendar") {
		t.Error("a cancelled event should show as cancelled, without the button")
	}
}

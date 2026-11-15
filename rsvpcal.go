package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RSVPs on the club calendar: an invitation can be tied to a calendar event
// (RSVPEvent.Ride, picked as "On the calendar" on Email → RSVPs). The
// calendar then shows its answers like a ride's leaders: the head count on
// the event, and who's coming and what they're bringing when you hover it.
// The event's page gets the RSVP: who's coming, Directions / Add to
// calendar / Share, and the answer form for a member who came from their own
// link (?t=). Anyone else can have their link emailed to them.
//
// On the public calendar names are short ("Pat R."), and an invitation that
// keeps its guest list to admins shows only the counts.

// rsvpOnCal is an invitation as the calendar shows it on its event.
type rsvpOnCal struct {
	ID     int
	Open   bool
	Hidden bool // the guest list is for admins: counts only
	Count  RSVPCount
	Going  []calGuest // the first few, yes before maybe
	More   int        // how many more aren't listed
}

// calGuest is one answer as the calendar shows it.
type calGuest struct {
	Name     string
	Guests   int
	Maybe    bool
	Bringing string
}

// maxCalGuests is how many names the calendar's hover card lists.
const maxCalGuests = 8

func rsvpCalOf(e RSVPEvent, now time.Time) *rsvpOnCal {
	c := &rsvpOnCal{ID: e.ID, Open: e.IsOpen(now), Hidden: e.HideGuests, Count: e.Count()}
	if e.HideGuests {
		return c
	}
	for _, r := range e.Going() {
		if len(c.Going) == maxCalGuests {
			c.More++
			continue
		}
		g := calGuest{Name: shortName(r.Name), Guests: r.Guests, Maybe: r.Answer == rsvpMaybe}
		if e.AskBringing {
			g.Bringing = r.Bringing
		}
		c.Going = append(c.Going, g)
	}
	return c
}

// rsvpForRide is the invitation tied to a calendar event, if there is one
// (the newest, should two be tied to it).
func (a *App) rsvpForRide(key string) (RSVPEvent, bool) {
	if key == "" {
		return RSVPEvent{}, false
	}
	for _, e := range a.store.RSVPEvents() {
		if e.Ride == key {
			return e, true
		}
	}
	return RSVPEvent{}, false
}

// withRSVPs marks the calendar's events that have an invitation.
func (a *App) withRSVPs(rides []RideOcc) {
	byRide := map[string]RSVPEvent{}
	for _, e := range a.store.RSVPEvents() {
		if _, taken := byRide[e.Ride]; e.Ride != "" && !taken {
			byRide[e.Ride] = e
		}
	}
	if len(byRide) == 0 {
		return
	}
	now := time.Now()
	for i := range rides {
		if e, ok := byRide[rides[i].Key]; ok {
			rides[i].RSVP = rsvpCalOf(e, now)
		}
	}
}

func (a *App) withRSVPsMonth(m *calMonth) {
	for _, w := range m.Weeks {
		for i := range w {
			a.withRSVPs(w[i].Rides)
		}
	}
	for i := range m.Days {
		a.withRSVPs(m.Days[i].Rides)
	}
}

// calRSVP is the RSVP for an event's page: the member's own when they came
// from their link (?t=), else the public view.
func (a *App) calRSVP(r *http.Request, o RideOcc) *rsvpView {
	e, ok := a.rsvpForRide(o.Key)
	if !ok {
		return nil
	}
	t := r.URL.Query().Get("t")
	v, ok := a.rsvpView(e.ID, t)
	if !ok {
		return nil
	}
	v.Apple = appleDevice(r)
	v.Saved = r.URL.Query().Get("saved") != ""
	v.Back = o.Key
	if t == "" {
		v.Invalid = false // no link at all: the page offers to email it
	}
	return &v
}

// calendarRSVPURL is where a member's RSVP link goes for an invitation:
// its event on the club calendar when it has one, else its RSVP page.
func (a *App) calendarRSVPURL(e RSVPEvent, token string) string {
	if e.Ride != "" {
		if _, ok := a.store.Ride(e.Ride); ok {
			return a.cfg.SiteURL + "/calendar/ride/" + e.Ride + "?t=" + url.QueryEscape(token) + "#rsvp"
		}
	}
	return a.cfg.SiteURL + "/rsvp/" + strconv.Itoa(e.ID) + "?t=" + url.QueryEscape(token)
}

// rsvpSendLink emails a member their own RSVP link. It answers the same
// whether or not the address is on the list, so it can't be used to find out
// who's a member.
func (a *App) rsvpSendLink(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	id, _ := strconv.Atoi(r.PathValue("id"))
	e, ok := a.store.RSVPEvent(id)
	if !ok {
		a.site.notFound(w, r)
		return
	}
	back := "/rsvp/" + strconv.Itoa(id) + "?"
	if k := r.PostFormValue("back"); k != "" && k == e.Ride {
		back = "/calendar/ride/" + k + "?"
	}
	email, ok := cleanEmail(strings.TrimSpace(r.PostFormValue("email")))
	switch {
	case !ok:
		http.Redirect(w, r, back+"err=rsvpemail#rsvp", http.StatusSeeOther)
		return
	case !a.limit.Allow("rsvplink:"+clientIP(r), 5, time.Hour) || !a.limit.Allow("rsvplink:"+email, 3, time.Hour):
		http.Redirect(w, r, back+"err=rsvplimit#rsvp", http.StatusSeeOther)
		return
	}
	if m, found := a.store.MemberByEmail(email); found && m.Active() {
		when := ""
		if s := e.When(); s != "" {
			when = " on " + s
		}
		body := fmt.Sprintf("Hi %s,\n\nHere's your own link to RSVP for **%s**%s. Say whether you can come, who's coming with you and, if you like, what you're bringing:\n\n[[RSVP](%s)]\n\nThe link is just for you: you can change your answer any time.",
			or(m.FirstName(), "there"), e.Title, when, a.calendarRSVPURL(e, m.Token))
		a.mailer.SendMine(mailRSVPLink, m, "Your RSVP link: "+e.Title, body) // a failure is in the sent log
	}
	http.Redirect(w, r, back+"ok=rsvplink#rsvp", http.StatusSeeOther)
}

// calEvents are the calendar's events (not rides) an invitation can be tied
// to: from a month back to half a year ahead.
func (a *App) calEvents() []RideOcc {
	today := a.today()
	var out []RideOcc
	for _, o := range a.store.Rides(today.AddDate(0, -1, 0), today.AddDate(0, 6, 0)) {
		if !o.IsRide() && !o.Cancelled {
			out = append(out, o)
		}
	}
	return out
}

// guessRide is the calendar event an invitation without one most likely
// belongs to: the only event on its day.
func (a *App) guessRide(e RSVPEvent) string {
	if e.Ride != "" || e.Date == "" {
		return e.Ride
	}
	key := ""
	for _, o := range a.calEvents() {
		if o.Date == e.Date {
			if key != "" {
				return ""
			}
			key = o.Key
		}
	}
	return key
}

package main

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The member's RSVP page (rsvp.go): /rsvp/<id>?t=<their token>, from the
// button in the invitation. It isn't linked anywhere or indexed, and without
// a member's link it shows only the event's name and day, never the place.

type rsvpView struct {
	Event   RSVPEvent
	Open    bool
	Past    bool
	Token   string
	Member  *Member
	Reply   *RSVPReply // their answer so far
	Test    bool       // the link in a test email
	Invalid bool       // no member's link, or one that isn't anyone's any more
	Saved   bool
	Message string
	Answers []choice
	Apple   bool // an iPhone, iPad or Mac: Apple Maps and the calendar file first
	// The form as sent, when it comes back with a message.
	Form RSVPReply
}

// Value is what the form shows: what they just sent, else their answer,
// else their name from the member list.
func (v rsvpView) Value() RSVPReply {
	if v.Form.Answer != "" || v.Form.Name != "" {
		return v.Form
	}
	if v.Reply != nil {
		return *v.Reply
	}
	if v.Member != nil {
		return RSVPReply{Name: v.Member.Name, Answer: ""}
	}
	return RSVPReply{}
}

// ShowGuests says the page lists who's coming: from a member's own link,
// unless the event keeps it to admins.
func (v rsvpView) ShowGuests() bool {
	return (v.Member != nil || v.Test) && !v.Event.HideGuests
}

// CalendarLink is the member's own "Add to my calendar" address for kind
// "calendar.ics" or "google", or "" when the event has no date or is over.
func (v rsvpView) CalendarLink(kind string) string {
	if (v.Member == nil && !v.Test) || v.Past || v.Event.Day().IsZero() {
		return ""
	}
	return "/rsvp/" + strconv.Itoa(v.Event.ID) + "/" + kind + "?t=" + url.QueryEscape(v.Token)
}

// rsvpLink is one "Also:" link under the page's buttons.
type rsvpLink struct {
	Label, URL string
	NewTab     bool
}

// Directions is the Directions button: Apple Maps on Apple devices, else
// Google Maps; "" without a place (or without a member's link).
func (v rsvpView) Directions() string {
	if v.Event.Place == "" {
		return ""
	}
	if v.Apple {
		return mapsLink("apple", v.Event.MapAddress())
	}
	return mapsLink("google", v.Event.MapAddress())
}

// AddToCalendar is the Add to calendar button: the calendar file on Apple
// devices, else Google Calendar.
func (v rsvpView) AddToCalendar() string {
	if v.Apple {
		return v.CalendarLink("calendar.ics")
	}
	return v.CalendarLink("google")
}

// OtherApps are the links for people who use other apps than the buttons.
func (v rsvpView) OtherApps() []rsvpLink {
	var out []rsvpLink
	if v.Directions() != "" {
		addr := v.Event.MapAddress()
		if v.Apple {
			out = append(out, rsvpLink{"Google Maps", mapsLink("google", addr), true})
		} else {
			out = append(out, rsvpLink{"Apple Maps", mapsLink("apple", addr), true})
		}
		out = append(out, rsvpLink{"Waze", mapsLink("waze", addr), true})
	}
	if v.AddToCalendar() != "" {
		if v.Apple {
			out = append(out, rsvpLink{"Google Calendar", v.CalendarLink("google"), true})
		} else {
			out = append(out, rsvpLink{"Apple or Outlook calendar", v.CalendarLink("calendar.ics"), false})
		}
	}
	return out
}

// appleDevice says the visitor is on an iPhone, iPad (which says Macintosh)
// or Mac, where Apple Maps and Calendar are the built-in apps.
func appleDevice(r *http.Request) bool {
	ua := r.UserAgent()
	return strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad") || strings.Contains(ua, "Macintosh")
}

// GuestChoices are the numbers the "how many with you" box offers.
func (v rsvpView) GuestChoices() []int {
	out := make([]int, maxRSVPGuests)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func (a *App) rsvpView(id int, token string) (rsvpView, bool) {
	e, ok := a.store.RSVPEvent(id)
	if !ok {
		return rsvpView{}, false
	}
	now := time.Now()
	v := rsvpView{Event: e, Open: e.IsOpen(now), Past: e.Past(now), Token: token, Answers: rsvpAnswers}
	switch {
	case token == testToken:
		v.Test = true
	case token == "":
		v.Invalid = true
	default:
		m, ok := a.store.MemberByToken(token)
		if !ok {
			v.Invalid = true
			break
		}
		v.Member = &m
		if r, ok := e.ReplyFor(m.ID); ok {
			v.Reply = &r
		}
	}
	if v.Member == nil && !v.Test { // the place is for members' eyes
		v.Event.Place, v.Event.Details = "", ""
	}
	return v, true
}

func (a *App) renderRSVP(w http.ResponseWriter, status int, v rsvpView) {
	w.Header().Set("X-Robots-Tag", "noindex")
	// The link is theirs: don't hand it to other sites. (no-referrer would
	// make the form's own post come with Origin: null, which sameOrigin blocks.)
	w.Header().Set("Referrer-Policy", "same-origin")
	a.site.render(w, status, "rsvp", "", v.Event.Title+" — RSVP", v)
}

func (a *App) rsvpPage(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	v, ok := a.rsvpView(id, r.URL.Query().Get("t"))
	if !ok {
		a.site.notFound(w, r)
		return
	}
	v.Saved = r.URL.Query().Get("saved") != ""
	v.Apple = appleDevice(r)
	a.renderRSVP(w, http.StatusOK, v)
}

// rsvpAnswer saves a member's answer.
func (a *App) rsvpAnswer(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	r.ParseForm()
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	id, _ := strconv.Atoi(r.PathValue("id"))
	t := r.PostFormValue("t")
	v, ok := a.rsvpView(id, t)
	if !ok {
		a.site.notFound(w, r)
		return
	}
	reply := RSVPReply{
		Name:     cleanLine(r.PostFormValue("name"), 100),
		Answer:   r.PostFormValue("answer"),
		Who:      cleanLine(r.PostFormValue("who"), 200),
		Bringing: cleanLine(r.PostFormValue("bringing"), 200),
		Note:     cleanLine(r.PostFormValue("note"), 300),
	}
	if n, err := strconv.Atoi(r.PostFormValue("guests")); err == nil && n > 0 {
		reply.Guests = min(n, maxRSVPGuests)
	}
	v.Form = reply
	v.Apple = appleDevice(r)
	if v.Test {
		v.Message = "That was a test email, so nothing was saved. In the real email, each member’s link saves their own answer."
		a.renderRSVP(w, http.StatusOK, v)
		return
	}
	if v.Member == nil {
		a.renderRSVP(w, http.StatusBadRequest, v)
		return
	}
	if reply.Answer != rsvpYes && reply.Answer != rsvpMaybe && reply.Answer != rsvpNo {
		v.Message = "Pick whether you can come."
		a.renderRSVP(w, http.StatusBadRequest, v)
		return
	}
	if reply.Name == "" {
		v.Message = "Add your name."
		a.renderRSVP(w, http.StatusBadRequest, v)
		return
	}
	switch err := a.store.AnswerRSVP(id, *v.Member, reply, time.Now()); {
	case errors.Is(err, errRSVPClosed):
		v.Open = false
		v.Message = "RSVPs just closed, so nothing changed. Email the club if your plans changed."
		a.renderRSVP(w, http.StatusConflict, v)
		return
	case err != nil:
		log.Printf("rsvp %d: %v", id, err)
		v.Message = "Something went wrong on our end. Try again in a minute."
		a.renderRSVP(w, http.StatusInternalServerError, v)
		return
	}
	http.Redirect(w, r, "/rsvp/"+strconv.Itoa(id)+"?t="+url.QueryEscape(t)+"&saved=1#answer", http.StatusSeeOther)
}

// rsvpLinks fills in each member's own links in an email: {rsvp_<id>} to the
// event's RSVP page, {rsvp_<id>_calendar} to its calendar file (Apple,
// Outlook), {rsvp_<id>_google} to Google Calendar and {rsvp_<id>_directions}
// to directions in their phone's maps app.
func rsvpLinks(s, siteURL, token string) string {
	if !strings.Contains(s, "{rsvp_") {
		return s
	}
	return rsvpPiece.ReplaceAllStringFunc(s, func(p string) string {
		m := rsvpPiece.FindStringSubmatch(p)
		path := "/rsvp/" + m[1]
		switch m[2] {
		case "_calendar":
			path += "/calendar.ics"
		case "_google":
			path += "/google"
		case "_directions":
			path += "/directions"
		}
		return siteURL + path + "?t=" + url.QueryEscape(token)
	})
}

var rsvpPiece = regexp.MustCompile(`\{rsvp_(\d+)(_calendar|_google|_directions)?\}`)

// rsvpCalendarEvent is the event as a calendar app gets it, from a member's
// link (the place only then); it answers 404 for one without a date.
func (a *App) rsvpCalendarEvent(w http.ResponseWriter, r *http.Request) (v rsvpView, start, end time.Time, allDay, ok bool) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	v, found := a.rsvpView(id, r.URL.Query().Get("t"))
	if found {
		start, end, allDay, ok = v.Event.CalendarTimes()
	}
	if !ok {
		a.site.notFound(w, r)
	}
	return
}

// rsvpDescription is the calendar entry's notes: the details and the
// member's own page, to change their answer.
func (a *App) rsvpDescription(v rsvpView) string {
	link := a.cfg.SiteURL + "/rsvp/" + strconv.Itoa(v.Event.ID)
	if v.Member != nil {
		link += "?t=" + url.QueryEscape(v.Token)
	}
	if d := strings.TrimSpace(v.Event.Details); d != "" {
		return d + "\n\nRSVP: " + link
	}
	return "RSVP: " + link
}

// rsvpICS is the event as a calendar file: on a phone it opens straight in
// the calendar app (Apple, Outlook and others).
func (a *App) rsvpICS(w http.ResponseWriter, r *http.Request) {
	v, start, end, allDay, ok := a.rsvpCalendarEvent(w, r)
	if !ok {
		return
	}
	host := strings.TrimPrefix(strings.TrimPrefix(a.cfg.SiteURL, "https://"), "http://")
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//PaxVelo//RSVP//EN", "METHOD:PUBLISH", "BEGIN:VEVENT",
		"UID:rsvp-" + strconv.Itoa(v.Event.ID) + "@" + host, "DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z")}
	if allDay {
		lines = append(lines, "DTSTART;VALUE=DATE:"+start.Format("20060102"), "DTEND;VALUE=DATE:"+end.Format("20060102"))
	} else {
		lines = append(lines, "DTSTART:"+start.UTC().Format("20060102T150405Z"), "DTEND:"+end.UTC().Format("20060102T150405Z"))
	}
	lines = append(lines, "SUMMARY:"+icsText(v.Event.Title))
	if p := v.Event.Place; p != "" {
		lines = append(lines, "LOCATION:"+icsText(p))
	}
	lines = append(lines, "DESCRIPTION:"+icsText(a.rsvpDescription(v)), "END:VEVENT", "END:VCALENDAR")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(icsFold(l))
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="paxvelo-event.ics"`)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Write([]byte(b.String()))
}

// rsvpDirections sends a member to directions in their phone's maps app
// (the email's Directions link). Without a member's link there's no place,
// so it goes to the RSVP page.
func (a *App) rsvpDirections(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	t := r.URL.Query().Get("t")
	v, ok := a.rsvpView(id, t)
	if !ok {
		a.site.notFound(w, r)
		return
	}
	v.Apple = appleDevice(r)
	to := v.Directions()
	if to == "" {
		to = "/rsvp/" + strconv.Itoa(id) + "?t=" + url.QueryEscape(t)
	}
	w.Header().Set("Referrer-Policy", "no-referrer") // the link is theirs
	w.Header().Set("X-Robots-Tag", "noindex")
	http.Redirect(w, r, to, http.StatusFound)
}

// rsvpGoogle sends the member to Google Calendar's "add an event" page,
// filled in.
func (a *App) rsvpGoogle(w http.ResponseWriter, r *http.Request) {
	v, start, end, allDay, ok := a.rsvpCalendarEvent(w, r)
	if !ok {
		return
	}
	q := url.Values{"action": {"TEMPLATE"}, "text": {v.Event.Title}, "details": {a.rsvpDescription(v)}}
	if allDay {
		q.Set("dates", start.Format("20060102")+"/"+end.Format("20060102"))
	} else {
		q.Set("dates", start.UTC().Format("20060102T150405Z")+"/"+end.UTC().Format("20060102T150405Z"))
	}
	if p := v.Event.Place; p != "" {
		q.Set("location", p)
	}
	w.Header().Set("Referrer-Policy", "no-referrer") // the link is theirs
	w.Header().Set("X-Robots-Tag", "noindex")
	http.Redirect(w, r, "https://calendar.google.com/calendar/render?"+q.Encode(), http.StatusFound)
}

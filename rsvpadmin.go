package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Email → RSVPs (every admin): the invitations, and for each one who's
// coming, who they're bringing and what, as the answers come in (rsvp.go).

type rsvpAdminView struct {
	Events     []RSVPEvent // the list
	Event      *RSVPEvent  // the one open
	Count      RSVPCount
	Waiting    []Member // current members who haven't answered
	Open       bool
	Removed    *RSVPEvent
	Dropped    *RSVPDropped // answers just removed from this event, for Undo
	ReminderOn bool         // the day-before reminder is switched on
	Form       rsvpFormData // the new-invitation or edit form
	OnCal      *RideOcc     // the calendar event it's tied to
	Now        time.Time
}

const rsvpAdminPath = "/admin/rsvp"

// rsvpFormData is the invitation form: the invitation, and the calendar's
// events it can be tied to (Ride preselected: its own, or the only event on
// its day).
type rsvpFormData struct {
	E      RSVPEvent
	Events []RideOcc
	Ride   string
}

func (a *App) rsvpForm(e RSVPEvent) rsvpFormData {
	return rsvpFormData{E: e, Events: a.calEvents(), Ride: a.guessRide(e)}
}

func (a *App) rsvpAdmin(w http.ResponseWriter, r *http.Request) {
	v := rsvpAdminView{Events: a.store.RSVPEvents(), Removed: a.store.RemovedRSVP(), Now: time.Now(), Form: a.rsvpForm(RSVPEvent{AskBringing: true})}
	flash, errMsg := flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "rsvps", adminView{Title: "RSVPs", Tab: "rsvp", Flash: flash, Error: errMsg, RSVP: &v})
}

func (a *App) rsvpAdminEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	e, ok := a.store.RSVPEvent(id)
	if !ok {
		back(w, r, rsvpAdminPath+"?err=rsvp-gone")
		return
	}
	v := rsvpAdminView{Event: &e, Count: e.Count(), Open: e.IsOpen(time.Now()), Dropped: a.store.DroppedRSVP(id), ReminderOn: a.store.RSVPReminderOn(), Now: time.Now(), Form: a.rsvpForm(e)}
	if o, ok := a.store.Ride(e.Ride); ok && e.Ride != "" {
		v.OnCal = &o
	}
	answered := map[int]bool{}
	for _, rep := range e.Replies {
		answered[rep.Member] = true
	}
	for _, m := range a.store.Members() {
		if m.Paid() && m.Active() && !answered[m.ID] {
			v.Waiting = append(v.Waiting, m)
		}
	}
	slices.SortFunc(v.Waiting, func(x, y Member) int { return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) })
	flash, errMsg := flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "rsvp", adminView{Title: e.Title + " — RSVPs", Tab: "rsvp", Flash: flash, Error: errMsg, RSVP: &v})
}

// saveRSVPEvent adds an invitation (/admin/rsvp) or changes one (/admin/rsvp/<id>).
func (a *App) saveRSVPEvent(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id, _ := strconv.Atoi(r.PathValue("id"))
	here, closed := rsvpAdminPath, false
	if id != 0 {
		here += "/" + strconv.Itoa(id)
		old, ok := a.store.RSVPEvent(id)
		if !ok {
			back(w, r, rsvpAdminPath+"?err=rsvp-gone")
			return
		}
		closed = old.Closed
	}
	e := RSVPEvent{
		Closed:      closed,
		ID:          id,
		Title:       cleanLine(r.FormValue("title"), 100),
		Date:        strings.TrimSpace(r.FormValue("date")),
		Time:        strings.TrimSpace(r.FormValue("time")),
		Place:       cleanLine(r.FormValue("place"), 200),
		Details:     strings.TrimSpace(strings.ReplaceAll(r.FormValue("details"), "\r\n", "\n")),
		AskBringing: r.FormValue("ask_bringing") != "",
		HideGuests:  r.FormValue("show_guests") == "",
		NoReminder:  r.FormValue("remind") == "",
		Closes:      strings.TrimSpace(r.FormValue("closes")),
	}
	if k := r.FormValue("ride"); k != "" {
		if o, ok := a.store.Ride(k); ok {
			e.Ride = k
			if e.Date == "" { // the calendar has the day
				e.Date, e.Time = o.Date, or(e.Time, o.Time)
			}
		}
	}
	if len(e.Details) > 1000 {
		e.Details = e.Details[:1000]
	}
	if e.Title == "" {
		back(w, r, here+"?err=rsvp-title")
		return
	}
	for _, d := range []string{e.Date, e.Closes} {
		if _, err := time.Parse("2006-01-02", d); d != "" && err != nil {
			back(w, r, here+"?err=rsvp-date")
			return
		}
	}
	if _, err := time.Parse("15:04", e.Time); e.Time != "" && err != nil {
		e.Time = ""
	}
	saved, err := a.store.SaveRSVPEvent(e)
	if err != nil {
		back(w, r, here+"?err=save")
		return
	}
	back(w, r, rsvpAdminPath+"/"+strconv.Itoa(saved)+"?ok=rsvp-saved")
}

func (a *App) closeRSVP(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	r.ParseForm()
	closed := r.FormValue("closed") == "1"
	if err := a.store.SetRSVPClosed(id, closed); err != nil {
		back(w, r, rsvpAdminPath+"?err=rsvp-gone")
		return
	}
	code := "rsvp-closed"
	if !closed {
		code = "rsvp-opened"
	}
	back(w, r, rsvpAdminPath+"/"+strconv.Itoa(id)+"?ok="+code)
}

func (a *App) removeRSVP(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if err := a.store.RemoveRSVPEvent(id); err != nil {
		back(w, r, rsvpAdminPath+"?err=rsvp-gone")
		return
	}
	back(w, r, rsvpAdminPath+"#removed")
}

func (a *App) restoreRSVP(w http.ResponseWriter, r *http.Request) {
	id, err := a.store.RestoreRSVPEvent()
	if err != nil || id == 0 {
		back(w, r, rsvpAdminPath)
		return
	}
	back(w, r, rsvpAdminPath+"/"+strconv.Itoa(id)+"?ok=rsvp-restored")
}

// dropRSVPReplies takes answers off: a row's Remove, or every ticked one.
// Undo puts them back.
func (a *App) dropRSVPReplies(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	r.ParseForm()
	var members []int
	for _, m := range r.Form["member"] {
		if n, err := strconv.Atoi(m); err == nil {
			members = append(members, n)
		}
	}
	here := rsvpAdminPath + "/" + strconv.Itoa(id)
	n, err := a.store.DropRSVPReplies(id, members)
	switch {
	case err != nil:
		back(w, r, rsvpAdminPath+"?err=rsvp-gone")
	case n == 0:
		back(w, r, here+"#answers")
	default:
		back(w, r, here+"#dropped")
	}
}

func (a *App) restoreRSVPReplies(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if err := a.store.RestoreRSVPReplies(id); err != nil {
		back(w, r, rsvpAdminPath+"?err=rsvp-gone")
		return
	}
	back(w, r, rsvpAdminPath+"/"+strconv.Itoa(id)+"?ok=rsvp-restored#answers")
}

// rsvpEmail writes the invitation into the Email members draft, with each
// member's own RSVP link as the button.
func (a *App) rsvpEmail(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	e, ok := a.store.RSVPEvent(id)
	if !ok {
		back(w, r, rsvpAdminPath+"?err=rsvp-gone")
		return
	}
	if d := a.store.Draft(); d.Subject != "" || d.Body != "" {
		back(w, r, "/admin/email?err=rsvp-draft")
		return
	}
	if err := a.store.SetDraft(rsvpInvitation(e)); err != nil {
		back(w, r, "/admin/email?err=save")
		return
	}
	back(w, r, "/admin/email?ok=rsvp-drafted")
}

// rsvpInvitation is the invitation email, for the admin to change as they like.
func rsvpInvitation(e RSVPEvent) EmailDraft {
	var b strings.Builder
	b.WriteString("Hi {first_name},\n\n")
	b.WriteString("You’re invited to the **" + e.Title + "**")
	if w := e.When(); w != "" {
		b.WriteString(", " + w)
	}
	if e.Place != "" {
		b.WriteString(", at " + e.Place)
	}
	b.WriteString(".\n\n")
	if e.Details != "" {
		for _, p := range strings.Split(e.Details, "\n\n") {
			if p = strings.TrimSpace(p); p != "" {
				b.WriteString(p + "\n\n")
			}
		}
	}
	b.WriteString("Can you make it? Tap below to let us know")
	if e.AskBringing {
		b.WriteString(", who’s coming with you and what you’d like to bring")
	} else {
		b.WriteString(" and who’s coming with you")
	}
	b.WriteString(". The link is just for you, and you can change your answer any time")
	if l := e.ClosesLabel(); l != "" {
		b.WriteString(" until " + l)
	}
	b.WriteString(".\n\n")
	b.WriteString("[[RSVP](" + "{" + e.Piece() + "}" + ")]\n\n")
	if e.Place != "" {
		b.WriteString("[Directions]({" + e.Piece() + "_directions}) in your maps app\n\n")
	}
	if e.Date != "" {
		b.WriteString("Add it to your calendar: [Apple or Outlook]({" + e.Piece() + "_calendar}) · [Google]({" + e.Piece() + "_google})\n\n")
	}
	b.WriteString("See you there!\nPaxVelo")
	return EmailDraft{Subject: "You’re invited: " + e.Title, Body: b.String()}
}

// rsvpCSV is an event's answers as a spreadsheet.
func (a *App) rsvpCSV(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	e, ok := a.store.RSVPEvent(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	cw.Write([]string{"Name", "Email", "Answer", "Guests", "Who", "Bringing", "Note", "Answered"})
	for _, rep := range e.ByAnswer() {
		cw.Write([]string{csvSafe(rep.Name), rep.Email, rep.AnswerLabel(), strconv.Itoa(rep.Guests), csvSafe(rep.Who), csvSafe(rep.Bringing), csvSafe(rep.Note),
			rep.Updated.In(clubTZ).Format("2006-01-02 15:04")})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		log.Printf("rsvp csv: %v", err)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="rsvp-%d.csv"`, id))
	w.Write(buf.Bytes())
}

// csvSafe keeps what a member typed from being read as a formula by a
// spreadsheet.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
		return "'" + s
	}
	return s
}

// rsvpPieces are the open invitations' links, for the email editor's
// "Insert for each member" list.
func (a *App) rsvpPieces() []autoPiece {
	var out []autoPiece
	now := time.Now()
	for _, e := range a.store.RSVPEvents() {
		if e.IsOpen(now) {
			out = append(out, autoPiece{e.Piece(), "RSVP: " + e.Title, "Their own link to answer " + e.Title})
			if e.Date != "" {
				out = append(out, autoPiece{e.Piece() + "_calendar", "Calendar (Apple, Outlook): " + e.Title, "Adds " + e.Title + " to their calendar app"},
					autoPiece{e.Piece() + "_google", "Google Calendar: " + e.Title, "Adds " + e.Title + " to their Google Calendar"})
			}
			if e.Place != "" {
				out = append(out, autoPiece{e.Piece() + "_directions", "Directions: " + e.Title, "Directions to " + e.Title + " in their maps app"})
			}
		}
	}
	return out
}

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

// rsvpLinks fills in each {rsvp_<id>} in an email with the member's own
// link to that event's RSVP page.
func rsvpLinks(s, siteURL, token string) string {
	if !strings.Contains(s, "{rsvp_") {
		return s
	}
	return rsvpPiece.ReplaceAllStringFunc(s, func(p string) string {
		return siteURL + "/rsvp/" + p[len("{rsvp_"):len(p)-1] + "?t=" + url.QueryEscape(token)
	})
}

var rsvpPiece = regexp.MustCompile(`\{rsvp_\d+\}`)

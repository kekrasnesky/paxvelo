package main

import (
	"log"
	"net/http"
	"time"
)

// Getting the newsletter without joining: name and email on the newsletter
// page. A new address goes on the list as email list only (no plan, no
// dues, no waiver) and gets club emails once it's confirmed from the inbox
// (sendConfirm). Someone who unsubscribed earlier is sent the same Confirm
// button to come back; an address already getting club emails gets nothing.
// The page says the same for every address, so it shows nothing about who
// is on the list.

func (a *App) newsSignup(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	v := newsView{Empty: true, Token: a.formToken(), SignupName: cleanName(r.FormValue("name"))}
	email, okEmail := cleanEmail(r.FormValue("email"))
	v.SignupEmail = email
	if !okEmail {
		v.SignupEmail = r.FormValue("email")
	}
	switch {
	case r.FormValue("website") != "": // the honeypot: looks done, does nothing
		v.Signup = "sent"
	case !a.validFormToken(r.FormValue("ft")):
		v.SignupError = "This page expired. Try again."
	case v.SignupName == "":
		v.SignupError = "Enter your name."
	case !okEmail:
		v.SignupError = "That doesn’t look like an email address."
	case !a.limit.Allow("news-signup:"+clientIP(r), 5, time.Hour) || !a.limit.Allow("news-signup:"+email, 3, time.Hour):
		v.SignupError = "Too many tries. Try again in an hour."
	default:
		m, send, err := a.store.SubscribeNews(v.SignupName, email)
		if err != nil {
			log.Printf("newsletter signup: %v", err)
			v.SignupError = "Something went wrong on our end. Try again in a minute."
			break
		}
		if send {
			a.mailer.Go(func() { a.sendConfirm(m) }) // a failure is in the sent log
		}
		v.Signup = "sent"
	}
	status := http.StatusOK
	if v.SignupError != "" {
		status = http.StatusBadRequest
	}
	b, err := a.site.renderNews(v, "/newsletter", "Newsletter — "+siteName)
	if err != nil {
		log.Printf("newsletter signup page: %v", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(b)
}

// SubscribeNews puts an address on the email list for the newsletter, and
// reports whether to send it the Confirm button: for a new address, or one
// that unsubscribed (it stays off until confirmed).
func (s *Store) SubscribeNews(name, email string) (Member, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.byEmail(email); m != nil {
		if m.Active() {
			return *m, false, nil
		}
		m.Unconfirmed = true
		return *m, true, s.save()
	}
	m := s.newMember(name, email, "")
	m.Unconfirmed = !s.d.ConfirmOff
	return *m, m.Unconfirmed, s.save()
}

// newsOnly reports whether they signed up for the newsletter alone: on the
// list with no plan, never having filled in the join form.
func (m Member) newsOnly() bool { return m.Plan == "" && m.TermsAgreed == nil && m.PendingJoin == nil }

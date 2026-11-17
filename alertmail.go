package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Ride alerts by email: the same cancellations and put-backs the phones get
// (push.go), for anyone who'd rather have an email, or whose phone can't do
// notifications (an iPhone without the site on its home screen). The email
// box is in the Ride alerts panel. Anyone can type any address, so nothing
// is sent to it until its owner presses Confirm in the email we send (a
// GET only shows the button: email scanners open links). Every alert email
// has a Stop link, and Gmail's one-click Unsubscribe works too. Nothing
// ties an address to a member.

// AlertEmail is one address that asked for ride alerts.
type AlertEmail struct {
	Email     string    `json:"email"`
	Token     string    `json:"token"` // the secret in its Confirm and Stop links
	Confirmed bool      `json:"confirmed,omitempty"`
	Added     time.Time `json:"added"`
}

const (
	maxAlertEmails  = 3000
	alertEmailWaits = 7 * 24 * time.Hour // an address not confirmed by then is forgotten
)

var errTooManyAlertEmails = errors.New("too many addresses have ride alerts on")

// AddAlertEmail keeps an address that asked for ride alerts and returns it:
// a new one unconfirmed with a fresh token, one already kept as it is (an
// unconfirmed one waits another week).
func (s *Store) AddAlertEmail(email string, now time.Time) (AlertEmail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.AlertEmails = slices.DeleteFunc(s.d.AlertEmails, func(x AlertEmail) bool {
		return !x.Confirmed && now.Sub(x.Added) > alertEmailWaits
	})
	if i := slices.IndexFunc(s.d.AlertEmails, func(x AlertEmail) bool { return x.Email == email }); i >= 0 {
		x := &s.d.AlertEmails[i]
		if !x.Confirmed {
			x.Added = now.UTC()
		}
		return *x, s.save()
	}
	if len(s.d.AlertEmails) >= maxAlertEmails {
		return AlertEmail{}, errTooManyAlertEmails
	}
	x := AlertEmail{Email: email, Token: randHex(16), Added: now.UTC()}
	s.d.AlertEmails = append(s.d.AlertEmails, x)
	return x, s.save()
}

// AlertEmailByToken finds an address by the secret in its links.
func (s *Store) AlertEmailByToken(token string) (AlertEmail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		return AlertEmail{}, false
	}
	for _, x := range s.d.AlertEmails {
		if x.Token == token {
			return x, true
		}
	}
	return AlertEmail{}, false
}

// ConfirmAlertEmail turns alerts on for the address with that token.
func (s *Store) ConfirmAlertEmail(token string) (AlertEmail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.d.AlertEmails, func(x AlertEmail) bool { return token != "" && x.Token == token })
	if i < 0 {
		return AlertEmail{}, false
	}
	if !s.d.AlertEmails[i].Confirmed {
		s.d.AlertEmails[i].Confirmed = true
		if err := s.save(); err != nil {
			log.Printf("saving ride alert emails: %v", err)
		}
	}
	return s.d.AlertEmails[i], true
}

// RemoveAlertEmail forgets the address with that token.
func (s *Store) RemoveAlertEmail(token string) (AlertEmail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.d.AlertEmails, func(x AlertEmail) bool { return token != "" && x.Token == token })
	if i < 0 {
		return AlertEmail{}, false
	}
	x := s.d.AlertEmails[i]
	s.d.AlertEmails = slices.Delete(s.d.AlertEmails, i, i+1)
	if err := s.save(); err != nil {
		log.Printf("saving ride alert emails: %v", err)
	}
	return x, true
}

// AlertEmails are the confirmed addresses.
func (s *Store) AlertEmails() []AlertEmail {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AlertEmail
	for _, x := range s.d.AlertEmails {
		if x.Confirmed {
			out = append(out, x)
		}
	}
	return out
}

// ---- The panel's email box (site.js) ----

// alertEmailJSON takes the address from the Ride alerts panel and emails it a
// Confirm button. The answer is the same for any address.
func (a *App) alertEmailJSON(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	var in struct {
		Email   string `json:"email"`
		Website string `json:"website"` // the honeypot: people leave it empty
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	email, ok := cleanEmail(in.Email)
	if !ok {
		http.Error(w, "That doesn’t look like an email address.", http.StatusBadRequest)
		return
	}
	if !a.limit.Allow("alertmail:"+clientIP(r), 5, time.Hour) || !a.limit.Allow("alertmail:"+email, 3, time.Hour) {
		http.Error(w, "Too many tries. Wait a bit.", http.StatusTooManyRequests)
		return
	}
	if in.Website == "" {
		x, err := a.store.AddAlertEmail(email, time.Now())
		switch {
		case errors.Is(err, errTooManyAlertEmails):
			http.Error(w, "Ride alert emails are full right now.", http.StatusServiceUnavailable)
			return
		case err != nil:
			http.Error(w, "Couldn’t save that.", http.StatusInternalServerError)
			return
		case !x.Confirmed:
			body := fmt.Sprintf("Press the button to get an email when a PaxVelo ride is cancelled or put back on.\n\n[[Confirm](%s)]\n\nIf you didn't ask for this, no action is needed: nothing will come unless you confirm.",
				a.cfg.SiteURL+"/alerts/email?t="+x.Token)
			a.mailer.Go(func() { a.mailer.SendDirect(mailAlertConfirm, "", x.Email, "Confirm ride alerts", body) }) // a failure is in the sent log
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"ok": "Check your email to confirm."})
}

// ---- Confirm and Stop pages ----

type alertMailData struct {
	Token, Email string
	State        string // confirm (ask), on, stop (ask), off, invalid
}

func (a *App) renderAlertMail(w http.ResponseWriter, d alertMailData) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	a.site.render(w, http.StatusOK, "alerts", "/alerts/email", "Ride alerts — "+siteName, d)
}

// alertMailPage shows the Confirm button (?t=) or the Stop button
// (?t=&stop=1). Opening the link changes nothing.
func (a *App) alertMailPage(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	x, ok := a.store.AlertEmailByToken(t)
	d := alertMailData{Token: t, Email: x.Email, State: "confirm"}
	switch {
	case !ok && r.URL.Query().Get("stop") != "":
		d.State = "off" // already stopped
	case !ok:
		d.State = "invalid"
	case r.URL.Query().Get("stop") != "":
		d.State = "stop"
	case x.Confirmed:
		d.State = "on"
	}
	a.renderAlertMail(w, d)
}

// alertMailPost presses Confirm or Stop. Gmail's one-click Unsubscribe
// posts here too, with the token and stop=1 in the address.
func (a *App) alertMailPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	r.ParseForm()
	t := or(r.URL.Query().Get("t"), r.PostFormValue("t"))
	stop := r.URL.Query().Get("stop") != "" || r.PostFormValue("what") == "stop"
	var d alertMailData
	if stop {
		x, _ := a.store.RemoveAlertEmail(t)
		d = alertMailData{Email: x.Email, State: "off"}
	} else if x, ok := a.store.ConfirmAlertEmail(t); ok {
		d = alertMailData{Token: t, Email: x.Email, State: "on"}
	} else {
		d.State = "invalid"
	}
	a.renderAlertMail(w, d)
}

// ---- Sending (ridestatus.go) ----

// mailAlerts emails the confirmed addresses about a cancellation or a ride
// put back on, like the phones (alertRide): a ride today or later, not one
// waiting for approval. Riders who said "I'm in" with the same address get
// their own email (mailRiders), so not this one too.
func (a *App) mailAlerts(o RideOcc, status string, wasCancelled bool) {
	if o.Held || o.Date < a.today().Format("2006-01-02") {
		return
	}
	var subject, body string
	switch {
	case status == statusCancel && !wasCancelled:
		subject, body = "Cancelled: "+o.Name+", "+shortDate(o.Date), fmt.Sprintf("**%s** on %s is cancelled.", o.Name, rideWhen(o))
		if o.StatusNote != "" {
			body += "\n\n" + o.StatusNote
		}
		body += fmt.Sprintf("\n\n[Find another ride](%s/calendar)", a.cfg.SiteURL)
	case status != statusCancel && wasCancelled:
		subject, body = "Back on: "+o.Name+", "+shortDate(o.Date), fmt.Sprintf("**%s** on %s is back on.\n\n[Ride details](%s)", o.Name, rideWhen(o), a.rideLink(o.Key))
	default:
		return
	}
	riders := map[string]bool{}
	for _, rd := range o.Riders {
		riders[strings.ToLower(rd.Email)] = true
	}
	var to []AlertEmail
	for _, x := range a.store.AlertEmails() {
		if !riders[x.Email] {
			to = append(to, x)
		}
	}
	// One at a time with the bulk sends' pause (SEND_DELAY), in the background.
	a.mailer.Go(func() {
		for i, x := range to {
			if i > 0 && a.mailer.live() {
				time.Sleep(a.cfg.SendDelay)
			}
			stop := a.cfg.SiteURL + "/alerts/email?t=" + url.QueryEscape(x.Token) + "&stop=1"
			a.mailer.SendAlert(mailAlert, x.Email, subject, body, stop) // a failure is in the sent log
		}
	})
}

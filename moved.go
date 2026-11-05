package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The site-move email: for members who paid on the old Squarespace site, a
// courtesy note that the club has a new website, that their dues are paid
// (through DuesExpire), and a link to check their details and sign the
// waiver. Admins send it from the member list or a member's page; it marks
// them paid for this year if they weren't, instead of the new-member welcome.

const defaultMovedSubject = "PaxVelo has a new website"

const defaultMovedBody = `Hi {first_name},

PaxVelo has moved to a new website, and your membership came with it. This is a courtesy email to let you know your dues are paid through {dues_expire}. There's nothing to pay.

Please take a minute to check your details, add an emergency contact, and sign this year's waiver:

[[Update my membership]({my_link})]

The link is just for you, so keep this email. You can also reach your page any time from My Membership on {site}.

See you on the road!
PaxVelo`

// MovedEmail is the site-move email as saved, or the built-in one.
func (s *Store) MovedEmail() EmailDraft {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.d.Moved
	if e.Subject == "" {
		e.Subject = defaultMovedSubject
	}
	if e.Body == "" {
		e.Body = defaultMovedBody
	}
	return e
}

func (s *Store) SetMovedEmail(e EmailDraft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Body == defaultMovedBody {
		e.Body = "" // keeps following the built-in text
	}
	if e.Subject == defaultMovedSubject {
		e.Subject = ""
	}
	s.d.Moved = e
	return s.save()
}

// MarkMovedSent records that the site-move email went to them.
func (s *Store) MarkMovedSent(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(id)
	if m == nil {
		return nil
	}
	now := time.Now().UTC()
	m.MovedSent = &now
	return s.save()
}

// SendMoved sends a member the site-move email.
func (m *Mailer) SendMoved(mem Member) error {
	e := m.store.MovedEmail()
	return m.sendTo(mailMoved, 0, "", mem, e.Subject, e.Body, nil)
}

// moveMember marks a member paid for this year (if they weren't) and sends
// them the site-move email instead of the welcome email.
func (a *App) moveMember(id int) error {
	year := a.cfg.ThisYear()
	m, ok := a.store.MemberByID(id)
	if !ok {
		return nil
	}
	if _, err := a.store.Pay(id, false); err != nil { // does nothing if they're paid up
		return err
	}
	if err := a.store.MarkMovedSent(id); err != nil {
		return err
	}
	if m.WelcomePending {
		a.store.SkipWelcome(id) // they're an existing member: no welcome as well
	}
	m, _ = a.store.MemberByID(id)
	a.mailer.Go(func() { a.mailer.SendMoved(m) })
	a.emailHousehold(a.store.UnsignedHousehold(id, year))
	return nil
}

// SkipWelcome clears a pending welcome email.
func (s *Store) SkipWelcome(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.byID(id); m != nil && m.WelcomePending {
		m.WelcomePending = false
		return s.save()
	}
	return nil
}

// movedMany is "Mark paid & send site-move email" on the ticked rows of the
// member list, or the button on one member's page.
func (a *App) movedMany(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	n := 0
	for _, s := range r.Form["id"] {
		id, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		if err := a.moveMember(id); err != nil {
			back(w, r, "/admin/members?err=save")
			return
		}
		n++
	}
	ret := r.FormValue("return")
	if u, err := url.Parse(ret); err == nil && (strings.HasPrefix(u.Path, "/admin/members") || u.Path == "/admin/email/auto") {
		q := u.Query()
		q.Set("ok", "moved")
		q.Set("n", strconv.Itoa(n))
		to := u.Path + "?" + q.Encode()
		if u.Path == "/admin/email/auto" { // sent from Email → Automations: open its row again
			to += "#a-moved"
		}
		back(w, r, to)
		return
	}
	back(w, r, "/admin/members?ok=moved&n="+strconv.Itoa(n))
}

// saveMoved saves the site-move email from the Email page.
func (a *App) saveMoved(w http.ResponseWriter, r *http.Request) {
	e := EmailDraft{Subject: strings.TrimSpace(r.FormValue("subject")), Body: strings.TrimSpace(strings.ReplaceAll(r.FormValue("body"), "\r\n", "\n"))}
	if e.Subject == "" || e.Body == "" {
		back(w, r, "/admin/email/auto/moved?err=welcome")
		return
	}
	if err := a.store.SetMovedEmail(e); err != nil {
		back(w, r, "/admin/email/auto/moved?err=save")
		return
	}
	back(w, r, "/admin/email/auto?ok=moved-saved#a-moved")
}

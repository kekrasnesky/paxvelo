package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Automated emails (Email → Automations): every email the site sends by
// itself, in one list. Three are built in and always there: the email check a
// new joiner gets, the welcome email on their first Mark paid, and the
// site-move email admins send from Members. Admins add their own too: an
// email that goes a number of days before or after something happens to a
// member (they join, they're marked paid, their dues run out…), each member
// once for each time it happens.
//
// A new automation only emails members who reach that point after it was
// made, never everyone it would have matched before, and nothing is sent
// overnight: the check runs every 15 minutes from 9 am, club time.

type Automation struct {
	ID      int       `json:"id"`
	Name    string    `json:"name"`
	Trigger string    `json:"trigger"` // an autoTriggers key
	Days    int       `json:"days"`    // how many days before or after
	Subject string    `json:"subject"`
	Body    string    `json:"body"`
	Paused  bool      `json:"paused,omitempty"`
	Created time.Time `json:"created"`
	// Sent is, for each member it went to, the day it went for (their join
	// day, the day they paid…), so it goes once each time that happens.
	Sent  map[int]string `json:"sent,omitempty"`
	Count int            `json:"count,omitempty"` // emails sent
	Last  *time.Time     `json:"last,omitempty"`  // the latest one
}

// autoTrigger is something that happens to a member that an automation can
// follow. Event says it in words; If, a condition checked on the day it's due.
type autoTrigger struct {
	Key, Event, If string
	Before         bool // the email goes before the day (dues running out), not after
	Help           string
}

var autoTriggers = []autoTrigger{
	{"joined", "someone joins on the website", "", false, "Everyone who fills in the join form, paid or not."},
	{"unpaid", "someone joins on the website", "if they haven’t paid yet", false, "A nudge to pay: it’s skipped once they’re marked paid."},
	{"paid", "a member is marked paid", "", false, "Each time dues are marked paid, joining or renewing."},
	{"waiver", "a member is marked paid", "if they haven’t signed this year’s waiver", false, "A reminder to sign: it’s skipped once they have (a first-ride waiver this year counts). Ends with a Sign button unless the words have the waiver link."},
	{"expiring", "a member’s dues run out", "", true, "A renewal reminder: it’s skipped if they renew first."},
	{"expired", "a member’s dues ran out", "if they haven’t renewed", false, "For members whose dues lapsed."},
}

func triggerFor(key string) (autoTrigger, bool) {
	for _, t := range autoTriggers {
		if t.Key == key {
			return t, true
		}
	}
	return autoTrigger{}, false
}

// Label is the trigger as the picker shows it.
func (t autoTrigger) Label() string {
	if t.If != "" {
		return t.Event + ", " + t.If
	}
	return t.Event
}

// When says when an automation goes, like "3 days after someone joins on the
// website, if they haven’t paid yet".
func (a Automation) When() string {
	t, ok := triggerFor(a.Trigger)
	if !ok {
		return "Never: pick when it goes"
	}
	var s string
	switch {
	case a.Days == 0:
		s = "The day " + t.Event
	case t.Before:
		s = plural(a.Days, "1 day", strconv.Itoa(a.Days)+" days") + " before " + t.Event
	default:
		s = plural(a.Days, "1 day", strconv.Itoa(a.Days)+" days") + " after " + t.Event
	}
	if t.If != "" {
		s += ", " + t.If
	}
	return s
}

// day is the start of a time's calendar day in the club's time zone.
func clubDay(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// lastPaidOn is when an admin last marked them paid, or zero.
func (m Member) lastPaidOn() time.Time {
	var last time.Time
	for _, t := range m.PaidOn {
		if t.After(last) {
			last = t
		}
	}
	return last
}

// autoDue is when an automation is due for a member, and the day it's
// following (sent once for each), or ok false when it isn't for them.
func (a *App) autoDue(au Automation, m Member) (due time.Time, occ string, ok bool) {
	loc := a.cfg.Location
	var base time.Time
	switch au.Trigger {
	case "joined", "unpaid":
		if m.TermsAgreed == nil || (au.Trigger == "unpaid" && m.Paid()) { // the join form sets TermsAgreed
			return
		}
		base = m.Joined
	case "paid", "waiver":
		base = m.lastPaidOn()
		if au.Trigger == "waiver" && !base.IsZero() {
			if a.store.HasWaiver(m.ID, a.cfg.ThisYear()) {
				return
			}
		}
	case "expiring":
		if !m.Paid() {
			return
		}
		base = m.DuesExpire()
	case "expired":
		if m.Paid() {
			return
		}
		base = m.DuesExpire()
	default:
		return
	}
	if base.IsZero() {
		return
	}
	b := clubDay(base, loc)
	days := au.Days
	if t, _ := triggerFor(au.Trigger); t.Before {
		days = -days
	}
	return b.AddDate(0, 0, days), b.Format("2006-01-02"), true
}

// autoCatchUp is how far back a missed email still goes (the server was
// down, or the automation was paused for a few days).
const autoCatchUp = 7

// runAutomations sends what's due now. From 9 am, club time.
func (a *App) runAutomations(now time.Time) {
	local := now.In(a.cfg.Location)
	if local.Hour() < 9 {
		return
	}
	today := clubDay(local, a.cfg.Location)
	members := a.store.Members()
	for _, au := range a.store.Automations() {
		if au.Paused || au.Subject == "" || au.Body == "" {
			continue
		}
		start := clubDay(au.Created, a.cfg.Location)
		for _, m := range members {
			if !m.Emailable() {
				continue
			}
			due, occ, ok := a.autoDue(au, m)
			if !ok || due.After(today) || due.Before(start) || due.Before(today.AddDate(0, 0, -autoCatchUp)) || au.Sent[m.ID] == occ {
				continue
			}
			// Marked first, so it never goes twice, even if the server stops mid-send.
			if !a.store.MarkAutoSent(au.ID, m.ID, occ) {
				continue
			}
			a.mailer.sendTo(mailAuto, 0, "", m, au.Subject, autoBody(au), nil) // a failure is in the sent log
		}
	}
}

// waiverButton ends a waiver reminder that has no link to the waiver, so
// one tap opens the right form (not the first-ride one).
const waiverButton = "[[Sign my {year} waiver]({waiver_link})]"

// autoBody is what an automation sends: its words, and for a waiver
// reminder without {waiver_link}, the Sign button at the end.
func autoBody(au Automation) string {
	if au.Trigger == "waiver" && !strings.Contains(au.Body, "{waiver_link}") {
		return strings.TrimRight(au.Body, "\n") + "\n\n" + waiverButton
	}
	return au.Body
}

func (a *App) automationLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		a.runAutomations(time.Now())
		a.runRequestReminders(time.Now()) // requestreminders.go
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

// ---- Stored automations ----

func (s *Store) Automations() []Automation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Automation, 0, len(s.d.Automations))
	for _, au := range s.d.Automations {
		c := *au
		c.Sent = nil // a copy, so the runner reads it while sends change the original
		if au.Sent != nil {
			c.Sent = make(map[int]string, len(au.Sent))
			for k, v := range au.Sent {
				c.Sent[k] = v
			}
		}
		out = append(out, c)
	}
	return out
}

func (s *Store) automation(id int) *Automation {
	for _, au := range s.d.Automations {
		if au.ID == id {
			return au
		}
	}
	return nil
}

func (s *Store) Automation(id int) (Automation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if au := s.automation(id); au != nil {
		return *au, true
	}
	return Automation{}, false
}

// SaveAutomation adds an automation (ID 0) or changes one's name, timing and
// words, keeping what it has sent.
func (s *Store) SaveAutomation(in Automation) (Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.ID == 0 {
		s.d.NextAuto++
		in.ID, in.Created, in.Sent, in.Count, in.Last = s.d.NextAuto, time.Now().UTC(), nil, 0, nil
		s.d.Automations = append(s.d.Automations, &in)
		return in, s.save()
	}
	au := s.automation(in.ID)
	if au == nil {
		return Automation{}, errors.New("that automated email is gone")
	}
	au.Name, au.Trigger, au.Days, au.Subject, au.Body = in.Name, in.Trigger, in.Days, in.Subject, in.Body
	return *au, s.save()
}

func (s *Store) PauseAutomation(id int, paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	au := s.automation(id)
	if au == nil {
		return nil
	}
	au.Paused = paused
	return s.save()
}

// RemoveAutomation takes one off the list and hands it back, for Undo.
func (s *Store) RemoveAutomation(id int) (Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.d.Automations, func(au *Automation) bool { return au.ID == id })
	if i < 0 {
		return Automation{}, nil
	}
	gone := *s.d.Automations[i]
	s.d.Automations = slices.Delete(s.d.Automations, i, i+1)
	s.d.RemovedAuto = &gone
	return gone, s.save()
}

// RestoreAutomation puts back the one removed last (Undo).
func (s *Store) RestoreAutomation(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.d.RemovedAuto
	if g == nil || g.ID != id || s.automation(id) != nil {
		return false
	}
	s.d.Automations = append(s.d.Automations, g)
	slices.SortFunc(s.d.Automations, func(x, y *Automation) int { return x.ID - y.ID })
	s.d.RemovedAuto = nil
	s.save()
	return true
}

// The built-in automations (confirm, welcome, moved) can be deleted too. A
// deleted one leaves the list and stops: it is switched off (confirm: new
// joiners start confirmed; welcome: paused) or, for the site-move email,
// its buttons go. The president can put it back from the list's foot.
var builtinKeys = []string{"confirm", "welcome", "moved"}

func isBuiltinKey(k string) bool { return slices.Contains(builtinKeys, k) }

// BuiltinRemoved reports whether a built-in automation was deleted.
func (s *Store) BuiltinRemoved(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.d.RemovedBuiltin, key)
}

// RemoveBuiltin deletes a built-in automation (see above).
func (s *Store) RemoveBuiltin(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !isBuiltinKey(key) || slices.Contains(s.d.RemovedBuiltin, key) {
		return nil
	}
	s.d.RemovedBuiltin = append(s.d.RemovedBuiltin, key)
	switch key {
	case "confirm":
		s.d.ConfirmOff = true
	case "welcome":
		s.d.Welcome.Enabled = false
	}
	return s.save()
}

// RestoreBuiltin puts a deleted built-in automation back, switched on.
func (s *Store) RestoreBuiltin(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.Index(s.d.RemovedBuiltin, key)
	if i < 0 {
		return nil
	}
	s.d.RemovedBuiltin = slices.Delete(s.d.RemovedBuiltin, i, i+1)
	switch key {
	case "confirm":
		s.d.ConfirmOff = false
	case "welcome":
		s.d.Welcome.Enabled = true
	}
	return s.save()
}

// MarkAutoSent records that an automation is going to a member for a day,
// and reports false if it already went (or the automation is gone or paused).
func (s *Store) MarkAutoSent(id, member int, occ string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	au := s.automation(id)
	if au == nil || au.Paused || au.Sent[member] == occ {
		return false
	}
	if au.Sent == nil {
		au.Sent = map[int]string{}
	}
	now := time.Now().UTC()
	au.Sent[member], au.Count, au.Last = occ, au.Count+1, &now
	s.save()
	return true
}

// ---- The confirm-your-email email, as admins edit it ----

const defaultConfirmSubject = "Confirm your email for PaxVelo"

// defaultConfirmBody is the email a new joiner gets. Three club pieces fill
// in for each joiner: the Confirm button, a line with their PayPal link while
// they haven't paid, and the rides coming up that suit them.
const defaultConfirmBody = `Hi {first_name},

Thanks for joining PaxVelo! Press the button to confirm this is your email, so club news and ride updates reach you.

{confirm_button}

{pay_line}

{rides_for_you}

Every club ride is on the calendar: {site}/calendar

Before your first ride, take a couple of minutes to read Bike Safety 101: {site}/bike-safety

The button also opens your membership page, where you can check your dues and update your details any time. Keep this email: the link is just for you.

If you didn’t sign up for PaxVelo, no action is needed: this address won’t get club emails unless it’s confirmed.`

func (s *Store) ConfirmText() EmailDraft {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.d.Confirm
	if e.Subject == "" {
		e.Subject = defaultConfirmSubject
	}
	if e.Body == "" {
		e.Body = defaultConfirmBody
	}
	return e
}

// ConfirmOn reports whether new joiners get the confirm email.
func (s *Store) ConfirmOn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.d.ConfirmOff
}

func (s *Store) SetConfirmOn(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.ConfirmOff = !on
	return s.save()
}

func (s *Store) SetConfirmText(e EmailDraft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Body == defaultConfirmBody {
		e.Body = "" // keeps following the built-in text
	}
	if e.Subject == defaultConfirmSubject {
		e.Subject = ""
	}
	s.d.Confirm = e
	return s.save()
}

// ---- Admin: Email → Automations ----

// autoRow is one email on the Automations list.
type autoRow struct {
	Key, Name, When, Who, Subject, About  string
	Paused                                bool
	CanPause, CanEdit, CanRemove, CanSend bool
	Manual                                bool         // sent by hand, not by itself
	Fixed                                 bool         // the site writes it: can't be changed
	Movers                                []moveChoice // who it can go to, for one sent by hand
	Sent                                  int
	Last                                  time.Time
}

// autoForm is the editor for one automated email.
type autoForm struct {
	Key, Name, Trigger, Subject, Body, About string
	Days                                     int
	New, Custom, Paused, Welcome             bool
	Pieces                                   []autoPiece // what can be put in its words
}

// autoPiece is a {placeholder} the email editor offers.
type autoPiece struct{ Key, Label, Help string }

var (
	piecesMember = []autoPiece{
		{"first_name", "First name", "Each member’s first name"},
		{"pay_link", "Pay link", "Their own PayPal link for their plan"},
		{"dues", "Dues amount", "Like $35"},
		{"dues_expire", "Dues run out", "The day their dues run out"},
		{"my_link", "My membership link", "Their own membership page"},
		{"waiver_link", "Waiver link", "Their membership page, opened at the waiver to sign"},
		{"kit_link", "Kit order link", "Their own link to reserve club kit"},
		{"year", "Year", "This year"},
		{"site", "Website address", ""},
		{"strava", "Club on Strava", ""},
	}
	piecesWelcome = append([]autoPiece{{"ride_tips", "Where to start riding", "The advice for their answer on the join form, with rides that suit them"}}, piecesMember...)
	piecesConfirm = append([]autoPiece{
		{"confirm_button", "Confirm button", "The button that confirms their email (it opens their membership page)"},
		{"pay_line", "Pay line", "A line with their PayPal link, while they haven’t paid"},
		{"rides_for_you", "Rides for them", "The next rides that suit their answer on the join form"}}, piecesMember...)
)

// builtinAutos are the emails the site sends by itself that admins can't
// change, listed so the whole picture is in one place.
var builtinAutos = []autoRow{
	{Key: "signup-alert", Name: "New member alert", When: "Right away when someone joins on the website", Who: "Every admin", About: "Their plan, email, phone and riding answer, and a link to find them on Members so the treasurer can press Mark paid."},
	{Key: "my-link", Name: "Membership link", When: "When someone asks for it on the website", Who: "The member who asked", About: "A link to their own membership page. Someone who hasn’t confirmed their email gets the Confirm your email email instead."},
	{Key: "family-waiver", Name: "Family waiver link", When: "When a Family plan is marked paid", Who: "Each other adult on the plan", About: "A private link to sign their own waiver."},
	{Key: "ride-notice", Name: "Ride notices", When: "When a ride is added, changed or needs a leader", Who: "The club’s rides inbox", About: "So whoever watches the rides inbox knows what changed on the calendar."},
	{Key: "request-ask", Name: "Spending request", When: "Right away when someone sends a spending request", Who: "The treasurer, and the president when their OK is needed", About: "The receipt, amount and what for, with Approve and Reject buttons that work without logging in."},
	{Key: "request-nudge", Name: "Request reminder", When: "3 days after a spending request is sent, if still waiting", Who: "Whoever hasn’t answered", About: "Once, with the same Approve and Reject buttons."},
	{Key: "request-paylist", Name: "To pay back", When: "Mondays, when approved requests aren’t paid back yet", Who: "The treasurer", About: "Who to pay, how much and how they want it."},
	{Key: "request-receipt", Name: "Receipt reminder", When: "10 days after a purchase is approved, if no receipt yet", Who: "Whoever asked to buy it", About: "Once, with a link to add the receipt."},
	{Key: "request-paid", Name: "Reimbursement receipt", When: "When the treasurer presses Paid on a request", Who: "PaxVeloClub@gmail.com only", About: "Who was paid, how much, the PayPal address, who approved it and what it was for."},
	{Key: "password", Name: "Admin password link", When: "When an admin is added or forgets their password", Who: "That admin", About: "A one-time link to choose a password."},
}

// autoView is the Automations page.
type autoView struct {
	Autos, Manual, Builtin []autoRow
	Form                   *autoForm
	Triggers               []autoTrigger
	Removed                string    // an automation just removed (its number, or a built-in's key), for Undo
	Gone                   []autoRow // built-in ones deleted earlier, for the president to put back
}

// moveChoice is a member in the site-move email's Send picker.
type moveChoice struct {
	ID          int
	Name, Email string
	Paid, Sent  bool
}

// moveChoices lists members on the email list for the site-move email:
// those it hasn't gone to first, then by name.
func (a *App) moveChoices() []moveChoice {
	var out []moveChoice
	for _, m := range a.store.Members() {
		if !m.Active() {
			continue
		}
		out = append(out, moveChoice{ID: m.ID, Name: m.Name, Email: m.Email, Paid: m.Paid(), Sent: m.MovedSent != nil})
	}
	slices.SortFunc(out, func(x, y moveChoice) int {
		if x.Sent != y.Sent {
			if x.Sent {
				return 1
			}
			return -1
		}
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	return out
}

// sentCounts counts the sent log's emails of each kind, with the latest.
func (a *App) sentCounts() (map[string]int, map[string]time.Time) {
	n, last := map[string]int{}, map[string]time.Time{}
	for _, e := range a.mailer.log.All() {
		if e.Failed() {
			continue
		}
		n[e.Kind]++
		if e.Time.After(last[e.Kind]) {
			last[e.Kind] = e.Time
		}
	}
	return n, last
}

// autoRows lists the automations; canRemove is whether this admin may
// remove the ones admins made (the board).
func (a *App) autoRows(canRemove bool) (autos, manual []autoRow) {
	n, last := a.sentCounts()
	w, c, mv := a.store.Welcome(), a.store.ConfirmText(), a.store.MovedEmail()
	autos = []autoRow{
		{Key: "confirm", Name: "Confirm your email", When: "Right away when someone joins on the website", Who: "The new member",
			Subject: c.Subject, Paused: !a.store.ConfirmOn(), CanPause: true, CanEdit: true, Sent: n[mailConfirm], Last: last[mailConfirm],
			About: "Club emails reach a new member once they press its button. Members coming back after unsubscribing, or renewing on the join form, get it with a first paragraph written for them. It can’t be paused: new members need it."},
		{Key: "welcome", Name: "Welcome email", When: "The first time someone who joined on the website is marked paid", Who: "The new member",
			Subject: w.Subject, Paused: !w.Enabled, CanPause: true, CanEdit: true, Sent: n[mailWelcome], Last: last[mailWelcome],
			About: "With where to start riding, from their answer on the join form."},
	}
	for _, au := range a.store.Automations() {
		r := autoRow{Key: strconv.Itoa(au.ID), Name: au.Name, When: au.When(), Who: "Members who reach that point, unless they unsubscribed",
			Subject: au.Subject, Paused: au.Paused, CanPause: true, CanEdit: true, CanRemove: canRemove, Sent: au.Count}
		if t, ok := triggerFor(au.Trigger); ok {
			r.About = t.Help
		}
		if au.Last != nil {
			r.Last = *au.Last
		}
		autos = append(autos, r)
	}
	manual = []autoRow{{Key: "moved", Name: "Site-move email", When: "Sent by hand", Who: "Members who paid on the old site",
		Subject: mv.Subject, CanEdit: true, CanSend: true, Manual: true, Sent: n[mailMoved], Last: last[mailMoved],
		About: "Tick members and press Mark paid & send site-move email, or use the button on a member’s page. It goes instead of the welcome email."}}
	for _, list := range []*[]autoRow{&autos, &manual} { // built-in ones: the president can delete them
		kept := (*list)[:0]
		for _, r := range *list {
			if isBuiltinKey(r.Key) {
				if a.store.BuiltinRemoved(r.Key) {
					continue
				}
				r.CanRemove = canRemove
			}
			kept = append(kept, r)
		}
		*list = kept
	}
	return
}

func (a *App) autoPage(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Automated emails", Tab: "auto", TestTo: a.cfg.AdminEmail, Live: a.mailer.live()}
	av := &autoView{Builtin: slices.Clone(builtinAutos), Triggers: autoTriggers}
	for i := range av.Builtin {
		av.Builtin[i].Fixed = true
	}
	av.Autos, av.Manual = a.autoRows(a.isBoard(r))
	if k := r.URL.Query().Get("removed"); isBuiltinKey(k) {
		av.Removed = k
	} else if id, _ := strconv.Atoi(k); id > 0 {
		av.Removed = strconv.Itoa(id)
	}
	if a.isBoard(r) {
		names := map[string]string{"confirm": "Confirm your email", "welcome": "Welcome email", "moved": "Site-move email"}
		for _, k := range builtinKeys {
			if a.store.BuiltinRemoved(k) {
				av.Gone = append(av.Gone, autoRow{Key: k, Name: names[k]})
			}
		}
	}
	for i := range av.Manual {
		if av.Manual[i].CanSend {
			av.Manual[i].Movers = a.moveChoices()
		}
	}
	v.Auto = av
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "auto", v)
}

// autoFormFor is the editor for an automation by its key: "confirm",
// "welcome", "moved", "new" or an automation's number.
func (a *App) autoFormFor(key string) (*autoForm, bool) {
	switch key {
	case "confirm":
		e := a.store.ConfirmText()
		return &autoForm{Key: key, Name: "Confirm your email", Subject: e.Subject, Body: e.Body, Pieces: piecesConfirm,
			About: "Goes right away when someone joins on the website. Keep the Confirm button in it: club emails reach them once they press it."}, true
	case "welcome":
		we := a.store.Welcome()
		return &autoForm{Key: key, Name: "Welcome email", Subject: we.Subject, Body: we.Body, Paused: !we.Enabled, Welcome: true, Pieces: piecesWelcome,
			About: "Goes the first time someone who joined on the website is marked paid."}, true
	case "moved":
		e := a.store.MovedEmail()
		return &autoForm{Key: key, Name: "Site-move email", Subject: e.Subject, Body: e.Body, Pieces: piecesMember,
			About: "Sent by hand from Members to people who paid on the old site."}, true
	case "new":
		return &autoForm{Key: key, New: true, Custom: true, Trigger: "unpaid", Days: 3, Pieces: piecesMember,
			Body: "Hi {first_name},\n\nWrite your message here.\n\nSee you on the road!\nPaxVelo"}, true
	}
	id, _ := strconv.Atoi(key)
	au, ok := a.store.Automation(id)
	if !ok {
		return nil, false
	}
	return &autoForm{Key: key, Custom: true, Name: au.Name, Trigger: au.Trigger, Days: au.Days, Subject: au.Subject, Body: au.Body, Paused: au.Paused, Pieces: piecesMember}, true
}

func (a *App) autoEditPage(w http.ResponseWriter, r *http.Request) {
	f, ok := a.autoFormFor(r.PathValue("key"))
	if !ok {
		http.Redirect(w, r, "/admin/email/auto", http.StatusSeeOther)
		return
	}
	a.renderAutoEdit(w, r, f, "")
}

func (a *App) renderAutoEdit(w http.ResponseWriter, r *http.Request, f *autoForm, problem string) {
	title := f.Name
	if f.New {
		title = "New automated email"
	}
	v := adminView{Title: title, Tab: "auto", TestTo: a.cfg.AdminEmail, Live: a.mailer.live(), Links: a.editorLinks(), Welcome: a.store.Welcome()}
	v.Auto = &autoView{Form: f, Triggers: autoTriggers}
	v.Flash, v.Error = flashFrom(r)
	if problem != "" {
		v.Error = problem
	}
	a.renderAdmin(w, r, http.StatusOK, "auto-edit", v)
}

func (a *App) autoSave(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	f, ok := a.autoFormFor(key)
	if !ok {
		http.Redirect(w, r, "/admin/email/auto", http.StatusSeeOther)
		return
	}
	f.Subject = strings.TrimSpace(r.FormValue("subject"))
	f.Body = strings.TrimSpace(strings.ReplaceAll(r.FormValue("body"), "\r\n", "\n"))
	if f.Custom {
		f.Name = strings.TrimSpace(r.FormValue("name"))
		f.Trigger = r.FormValue("trigger")
		f.Days, _ = strconv.Atoi(r.FormValue("days"))
	}
	var problem string
	switch {
	case f.Custom && f.Name == "":
		problem = "Give it a name, so you can find it on the list."
	case f.Custom && func() bool { _, ok := triggerFor(f.Trigger); return !ok }():
		problem = "Pick when it goes."
	case f.Days < 0 || f.Days > 365:
		problem = "The number of days needs to be from 0 to 365."
	case f.Subject == "" || f.Body == "":
		problem = "It needs a subject and a message."
	}
	if problem != "" {
		a.renderAutoEdit(w, r, f, problem)
		return
	}
	var err error
	switch key {
	case "confirm":
		err = a.store.SetConfirmText(EmailDraft{Subject: f.Subject, Body: f.Body})
	case "moved":
		err = a.store.SetMovedEmail(EmailDraft{Subject: f.Subject, Body: f.Body})
	case "welcome":
		we := a.store.Welcome()
		we.Subject, we.Body = f.Subject, f.Body
		if r.Form.Has("tips") {
			we.Tips = map[string]string{}
			for _, c := range riderLevels {
				// Advice left as the built-in text isn't saved, so it follows
				// any later change to the built-in advice.
				t := strings.TrimSpace(strings.ReplaceAll(r.FormValue("tips_"+c.Value), "\r\n", "\n"))
				if t != "" && t != levelAdvice[c.Value] {
					we.Tips[c.Value] = t
				}
			}
		}
		err = a.store.SetWelcome(we)
	default:
		id, _ := strconv.Atoi(key)
		var au Automation
		au, err = a.store.SaveAutomation(Automation{ID: id, Name: f.Name, Trigger: f.Trigger, Days: f.Days, Subject: f.Subject, Body: f.Body})
		key = strconv.Itoa(au.ID)
	}
	if err != nil {
		a.renderAutoEdit(w, r, f, "It didn’t save: "+err.Error())
		return
	}
	back(w, r, "/admin/email/auto?ok=auto-saved#a-"+key)
}

// autoPause pauses or resumes one: the welcome email or an admin's own.
func (a *App) autoPause(w http.ResponseWriter, r *http.Request) {
	key, paused := r.PathValue("key"), r.FormValue("paused") == "1"
	switch key {
	case "welcome":
		we := a.store.Welcome()
		we.Enabled = !paused
		a.store.SetWelcome(we)
	case "confirm":
		a.store.SetConfirmOn(!paused)
	default:
		id, _ := strconv.Atoi(key)
		a.store.PauseAutomation(id, paused)
	}
	if r.Header.Get("Accept") == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"paused": paused})
		return
	}
	ok := "auto-resumed"
	if paused {
		ok = "auto-paused"
	}
	back(w, r, "/admin/email/auto?ok="+ok+"#a-"+key)
}

func (a *App) autoRemove(w http.ResponseWriter, r *http.Request) {
	if key := r.PathValue("key"); isBuiltinKey(key) {
		if err := a.store.RemoveBuiltin(key); err != nil {
			back(w, r, "/admin/email/auto?err=save")
			return
		}
		back(w, r, "/admin/email/auto?ok=auto-removed&removed="+key)
		return
	}
	id, _ := strconv.Atoi(r.PathValue("key"))
	if _, err := a.store.RemoveAutomation(id); err != nil {
		back(w, r, "/admin/email/auto?err=save")
		return
	}
	back(w, r, "/admin/email/auto?ok=auto-removed&removed="+strconv.Itoa(id))
}

func (a *App) autoRestore(w http.ResponseWriter, r *http.Request) {
	if key := r.PathValue("key"); isBuiltinKey(key) {
		a.store.RestoreBuiltin(key)
		back(w, r, "/admin/email/auto?ok=auto-restored#a-"+key)
		return
	}
	id, _ := strconv.Atoi(r.PathValue("key"))
	a.store.RestoreAutomation(id)
	back(w, r, "/admin/email/auto?ok=auto-restored#a-"+strconv.Itoa(id))
}

// samplePat is the member previews are drawn for.
func (a *App) samplePat() Member {
	y := a.cfg.ThisYear()
	now := time.Now()
	return Member{Name: "Pat Rider", Plan: "individual", Level: "some", PaidYears: []int{y}, Token: testToken,
		Joined: time.Date(y, now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)}
}

// fillConfirm fills in the confirm email's club pieces for a member.
func (a *App) fillConfirm(body string, m Member) string {
	button := "[[Confirm my email](" + a.cfg.membershipURL(m.Token) + "&confirm=1#confirm)]"
	pay := ""
	if p := a.cfg.PayLink(m.Plan); p != "" && !m.Paid() {
		pay = "Haven’t paid yet? [Pay your $" + strconv.Itoa(a.cfg.Dues(m.Plan)) + " " + strings.ToLower(m.PlanName()) + " dues for " + strconv.Itoa(a.cfg.ThisYear()) + "](" + p + ")."
	}
	rides := rideList(a.ridesFor(m.Level, 3), a.cfg.SiteURL)
	return strings.NewReplacer("{confirm_button}", button, "{pay_line}", pay, "{rides_for_you}", rides).Replace(body)
}

// autoPreview draws an email for the sample member Pat: the confirm email's
// club pieces filled in too. GET draws one as saved, for the list.
func (a *App) autoPreview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	key := r.PathValue("key")
	var d EmailDraft
	var trigger string
	if r.Method == http.MethodPost {
		d, trigger = draftFrom(r), r.FormValue("trigger")
	} else if f, ok := a.autoFormFor(key); ok {
		d, trigger = EmailDraft{Subject: f.Subject, Body: f.Body}, f.Trigger
	}
	d.Body = autoBody(Automation{Trigger: trigger, Body: d.Body})
	pat := a.samplePat()
	if key == "confirm" {
		pat.PaidYears = nil
		d.Body = a.fillConfirm(d.Body, pat)
	}
	page, err := a.site.renderEmail(a.mailer.personalize(d.Subject, pat), a.mailer.personalize(d.Body, pat), memberLinks{Mine: "#", Unsub: "#"}, "")
	if err != nil {
		http.Error(w, "The preview couldn’t be made.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, page)
}

// autoTest sends a test of what's in the editor, and answers in JSON.
func (a *App) autoTest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	d := draftFrom(r)
	w.Header().Set("Content-Type", "application/json")
	say := func(ok bool, msg string) { json.NewEncoder(w).Encode(map[string]any{"ok": ok, "message": msg}) }
	to, ok := cleanEmail(r.FormValue("test_to"))
	switch {
	case d.Subject == "" || d.Body == "":
		say(false, "It needs a subject and a message first.")
		return
	case !ok:
		say(false, "Enter a valid email address to send the test to.")
		return
	}
	if r.PathValue("key") == "confirm" {
		d.Body = a.fillConfirm(d.Body, Member{Token: testToken, Plan: "individual", Level: "some"})
	}
	d.Body = autoBody(Automation{Trigger: r.FormValue("trigger"), Body: d.Body})
	if err := a.mailer.SendTest(to, d.Subject, d.Body); err != nil {
		say(false, "The test didn’t send: "+err.Error())
		return
	}
	if !a.mailer.live() {
		say(true, "Test mode: the email was written to the server log instead of being sent.")
		return
	}
	say(true, "Test sent to "+to+".")
}

// mailPieces are the pieces each kind of email editor offers, for templates.
var mailPieces = map[string][]autoPiece{"member": piecesMember, "welcome": piecesWelcome, "confirm": piecesConfirm}

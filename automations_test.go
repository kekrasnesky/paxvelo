package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// An admin's automated email goes to members who reach its day after it
// was made, once, from 9 am; not before 9, not to anyone who got there
// before it existed, and not while it's paused.
func TestAutomationRuns(t *testing.T) {
	a, h, admin := testApp(t)
	var mu sync.Mutex
	var got []string
	a.mailer.onSend = func(to, subject, body string) { mu.Lock(); got = append(got, to+"|"+subject+"|"+body); mu.Unlock() }
	sent := func() []string { mu.Lock(); defer mu.Unlock(); out := got; got = nil; return out }

	// Made from the editor: 2 days after joining, if they haven't paid.
	ct, body := form(url.Values{"name": {"Pay nudge"}, "trigger": {"unpaid"}, "days": {"2"}, "subject": {"Still want to ride, {first_name}?"}, "body": {"Hi {first_name},\n\n[[Pay now]({pay_link})]"}})
	if loc := do(t, h, admin, "POST", "/admin/email/auto/new/save", ct, body).Header().Get("Location"); !strings.HasPrefix(loc, "/admin/email/auto?ok=auto-saved#a-") {
		t.Fatalf("after saving: %q", loc)
	}
	autos := a.store.Automations()
	if len(autos) != 1 || autos[0].When() != "2 days after someone joins on the website, if they haven’t paid yet" {
		t.Fatalf("saved: %+v", autos)
	}
	au := autos[0]

	loc := a.cfg.Location
	now := time.Now().In(loc)
	nine := time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, loc)
	join := func(name, email string, daysAgo int) Member {
		m, _, err := a.store.Join(memberDetails{Name: name, Email: email, Plan: "individual", Terms: true}, a.cfg.ThisYear())
		if err != nil {
			t.Fatal(err)
		}
		a.store.mu.Lock()
		p := a.store.byID(m.ID)
		p.Joined = nine.AddDate(0, 0, -daysAgo)
		p.Unconfirmed = false
		*m.TermsAgreed = p.Joined
		m = *p
		a.store.mu.Unlock()
		return m
	}
	due := join("Dana Due", "dana@example.com", 2)
	join("Early Ed", "ed@example.com", 1) // due tomorrow
	paid := join("Paula Paid", "paula@example.com", 2)
	a.store.Pay(paid.ID, false)
	// Made before the automation was: its day (2 days after) was before today.
	old := join("Olive Old", "olive@example.com", 5)
	a.store.mu.Lock()
	a.store.automation(au.ID).Created = nine.AddDate(0, 0, -1)
	a.store.mu.Unlock()

	a.runAutomations(nine.Add(-time.Hour)) // 8:30 am: nothing yet
	if s := sent(); len(s) != 0 {
		t.Fatalf("sent before 9: %q", s)
	}
	a.runAutomations(nine)
	s := sent()
	if len(s) != 1 || !strings.HasPrefix(s[0], "dana@example.com|Still want to ride, Dana?|Hi Dana,") || !strings.Contains(s[0], "paypal") && !strings.Contains(s[0], "/join") {
		t.Fatalf("sent: %q", s)
	}
	_ = old
	a.runAutomations(nine.Add(time.Hour)) // again: once only
	if s := sent(); len(s) != 0 {
		t.Fatalf("sent twice: %q", s)
	}
	if au, _ = a.store.Automation(au.ID); au.Count != 1 || au.Sent[due.ID] == "" {
		t.Errorf("counted: %+v", au)
	}

	// Paused, nothing goes; the list says so.
	ct, body = form(url.Values{"paused": {"1"}})
	do(t, h, admin, "POST", "/admin/email/auto/"+strconv.Itoa(au.ID)+"/pause", ct, body)
	join("Pat Paused", "pp@example.com", 2)
	a.runAutomations(nine.Add(2 * time.Hour))
	if s := sent(); len(s) != 0 {
		t.Fatalf("sent while paused: %q", s)
	}
	page := do(t, h, admin, "GET", "/admin/email/auto", "", nil).Body.String()
	for _, want := range []string{`class="auto-row is-off" id="a-` + strconv.Itoa(au.ID) + `"`, `aria-checked="false" aria-label="Pay nudge"`, "Confirm your email", "Welcome email", `class="auto-row is-manual" id="a-moved"`, "New member alert"} {
		if !strings.Contains(page, want) {
			t.Errorf("the Automations list is missing %q", want)
		}
	}
	// Resumed within the week, the member who got there meanwhile gets it.
	ct, body = form(url.Values{"paused": {"0"}})
	do(t, h, admin, "POST", "/admin/email/auto/"+strconv.Itoa(au.ID)+"/pause", ct, body)
	a.runAutomations(nine.Add(3 * time.Hour))
	if s := sent(); len(s) != 1 || !strings.HasPrefix(s[0], "pp@example.com|") {
		t.Fatalf("after resuming: %q", s)
	}

	// Removed, and put back with Undo.
	if loc := do(t, h, admin, "POST", "/admin/email/auto/"+strconv.Itoa(au.ID)+"/remove", "", nil).Header().Get("Location"); !strings.Contains(loc, "removed="+strconv.Itoa(au.ID)) {
		t.Fatalf("after removing: %q", loc)
	}
	if len(a.store.Automations()) != 0 {
		t.Fatal("not removed")
	}
	do(t, h, admin, "POST", "/admin/email/auto/"+strconv.Itoa(au.ID)+"/restore", "", nil)
	if back := a.store.Automations(); len(back) != 1 || back[0].Count != 2 {
		t.Fatalf("after Undo: %+v", back)
	}
}

// Each kind of day an automation can follow.
func TestAutomationTriggers(t *testing.T) {
	a, _, _ := testApp(t)
	loc := a.cfg.Location
	today := clubDay(time.Now(), loc)
	agreed := today
	m := Member{ID: 1, Joined: today.AddDate(-1, 0, 10), TermsAgreed: &agreed}
	m.PaidYears = []int{m.Joined.In(loc).Year()} // paid for the year that ends 10 days from now
	m.PaidOn = map[int]time.Time{m.PaidYears[0]: today.AddDate(0, 0, -3)}
	for _, c := range []struct {
		trigger string
		days    int
		due     time.Time
		ok      bool
	}{
		{"expiring", 10, today, true},
		{"expired", 5, time.Time{}, false}, // still paid
		{"paid", 3, today, true},
		{"waiver", 3, today, true},
		{"unpaid", 1, time.Time{}, false},
	} {
		due, _, ok := a.autoDue(Automation{Trigger: c.trigger, Days: c.days}, m)
		if ok != c.ok || (ok && !due.Equal(c.due)) {
			t.Errorf("%s: due %v, %v", c.trigger, due, ok)
		}
	}
	if w := (Automation{Trigger: "expiring", Days: 0}).When(); w != "The day a member’s dues run out" {
		t.Errorf("when: %q", w)
	}
}

// The confirm email as admins write it is what new joiners get, with its
// Confirm button. Every piece can be taken out, the button too: the email
// then goes as written (its My membership link still leads to Confirm).
func TestConfirmEmailEditable(t *testing.T) {
	a, h, admin := testApp(t)
	page := do(t, h, admin, "GET", "/admin/email/auto/confirm", "", nil).Body.String()
	for _, want := range []string{`class="card mail-form auto-form"`, "{confirm_button}", `value="confirm_button"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the editor is missing %q", want)
		}
	}
	var got string
	a.mailer.onSend = func(to, subject, body string) { got = subject + "|" + body }
	ct, body := form(url.Values{"subject": {"Confirm"}, "body": {"Hi {first_name}, just this."}})
	if loc := do(t, h, admin, "POST", "/admin/email/auto/confirm/save", ct, body).Header().Get("Location"); !strings.Contains(loc, "ok=auto-saved") {
		t.Fatalf("saving without the button: %q", loc)
	}
	jo, _, _ := a.store.Join(memberDetails{Name: "Jo Doe", Email: "jo@example.com", Plan: "individual"}, a.cfg.ThisYear())
	a.sendConfirm(jo)
	if got != "Confirm|Hi Jo, just this." {
		t.Errorf("without the button: %q", got)
	}
	ct, body = form(url.Values{"subject": {"One click, {first_name}"}, "body": {"Hi {first_name}, welcome aboard.\n\n{confirm_button}\n\n{pay_line}"}})
	do(t, h, admin, "POST", "/admin/email/auto/confirm/save", ct, body)
	m, _, _ := a.store.Join(memberDetails{Name: "Kim Lee", Email: "kim@example.com", Plan: "individual"}, a.cfg.ThisYear())
	a.sendConfirm(m)
	if !strings.HasPrefix(got, "One click, Kim|Hi Kim, welcome aboard.") || !strings.Contains(got, "[[Confirm my email](") {
		t.Errorf("sent: %q", got)
	}
	// Back to the built-in text when it's saved unchanged.
	ct, body = form(url.Values{"subject": {defaultConfirmSubject}, "body": {defaultConfirmBody}})
	do(t, h, admin, "POST", "/admin/email/auto/confirm/save", ct, body)
	if a.store.d.Confirm != (EmailDraft{}) {
		t.Errorf("kept %+v", a.store.d.Confirm)
	}
	// Its preview fills in the button for Pat.
	if p := do(t, h, admin, "GET", "/admin/email/auto/confirm/preview", "", nil).Body.String(); !strings.Contains(p, "Confirm my email") {
		t.Error("the preview should show the Confirm button")
	}
}

// The site-move email's row has Send: a list of members to tick, and
// sending comes back to the row.
func TestSiteMoveSendFromAutomations(t *testing.T) {
	a, h, admin := testApp(t)
	m, _, _ := a.store.Join(memberDetails{Name: "Ollie Old", Email: "ollie@example.com", Plan: "individual"}, a.cfg.ThisYear())
	page := do(t, h, admin, "GET", "/admin/email/auto", "", nil).Body.String()
	for _, want := range []string{`class="auto-send"`, `class="auto-pick"`, `name="id" value="` + strconv.Itoa(m.ID) + `"`, "Ollie Old", "Not paid"} {
		if !strings.Contains(page, want) {
			t.Errorf("the Automations page is missing %q", want)
		}
	}
	ct, body := form(url.Values{"id": {strconv.Itoa(m.ID)}, "return": {"/admin/email/auto"}})
	if loc := do(t, h, admin, "POST", "/admin/members/moved", ct, body).Header().Get("Location"); loc != "/admin/email/auto?n=1&ok=moved#a-moved" {
		t.Errorf("after sending: %q", loc)
	}
	if m, _ = a.store.MemberByID(m.ID); m.MovedSent == nil || !m.Paid() {
		t.Errorf("sent and paid: %+v", m)
	}
	if page := do(t, h, admin, "GET", "/admin/email/auto", "", nil).Body.String(); !strings.Contains(page, `value="`+strconv.Itoa(m.ID)+`" data-sent`) {
		t.Error("the list should show it went")
	}
}

// The confirm email has a switch too. Off, a new joiner isn't asked to
// confirm: club emails reach them right away, and no confirm email goes.
func TestConfirmEmailSwitch(t *testing.T) {
	a, h, admin := testApp(t)
	ct, body := form(url.Values{"paused": {"1"}})
	do(t, h, admin, "POST", "/admin/email/auto/confirm/pause", ct, body)
	if a.store.ConfirmOn() {
		t.Fatal("the switch should turn it off")
	}
	if page := do(t, h, admin, "GET", "/admin/email/auto", "", nil).Body.String(); !strings.Contains(page, `class="auto-row is-off" id="a-confirm"`) {
		t.Error("the list should show it off")
	}
	m, _, _ := a.store.JoinPublic(memberDetails{Name: "Jo Doe", Email: "jo@example.com", Plan: "individual", Terms: true}, a.cfg.ThisYear())
	if m.Unconfirmed || !m.Emailable() {
		t.Errorf("with the confirm email off, a new joiner should get club emails: %+v", m)
	}
	ct, body = form(url.Values{"paused": {"0"}})
	do(t, h, admin, "POST", "/admin/email/auto/confirm/pause", ct, body)
	if m, _, _ := a.store.JoinPublic(memberDetails{Name: "Kim Lee", Email: "kim@example.com", Plan: "individual", Terms: true}, a.cfg.ThisYear()); !m.Unconfirmed {
		t.Error("back on, new joiners confirm first")
	}
}

// Automations are the president's: other admins don't see the screen in the
// menu, can't open it, and can't change, pause or remove an automation.
func TestAutomationsPresidentOnly(t *testing.T) {
	a, h, _ := testApp(t)
	pat, _ := a.store.AddAdmin("Pat", "pat@example.com", "")
	sam, _ := a.store.AddAdmin("Sam", "sam@example.com", "")
	a.store.SetPresident(pat.ID)
	as := func(u AdminUser) *http.Cookie {
		u, _ = a.store.Admin(u.ID)
		rec := httptest.NewRecorder()
		a.setSession(rec, accountSession(u), 3600)
		return rec.Result().Cookies()[0]
	}
	au, _ := a.store.SaveAutomation(Automation{Name: "Dues reminder", Trigger: "expiring", Days: 14, Subject: "Dues", Body: "Hi"})
	id := au.ID

	if page := do(t, h, as(sam), "GET", "/admin/email", "", nil).Body.String(); strings.Contains(page, `href="/admin/email/auto"`) {
		t.Error("a non-president shouldn't see Automations in the menu")
	}
	if loc := do(t, h, as(sam), "GET", "/admin/email/auto", "", nil).Header().Get("Location"); loc != "/admin/email?err=president-only" {
		t.Errorf("a non-president opening Automations went to %q", loc)
	}
	key := strconv.Itoa(id)
	ct, body := form(url.Values{"paused": {"1"}})
	do(t, h, as(sam), "POST", "/admin/email/auto/"+key+"/pause", ct, body)
	do(t, h, as(sam), "POST", "/admin/email/auto/"+key+"/remove", "", nil)
	ct, body = form(url.Values{"enabled": {"on"}, "subject": {"Hacked"}, "body": {"Hacked"}})
	do(t, h, as(sam), "POST", "/admin/welcome", ct, body)
	if au, ok := a.store.Automation(id); !ok || au.Paused || a.store.Welcome().Subject == "Hacked" {
		t.Fatal("a non-president changed an automation")
	}

	if page := do(t, h, as(pat), "GET", "/admin/email/auto", "", nil).Body.String(); !strings.Contains(page, "Dues reminder") || !strings.Contains(page, `href="/admin/email/auto"`) {
		t.Error("the president should see Automations")
	}
	ct, body = form(url.Values{"paused": {"1"}})
	do(t, h, as(pat), "POST", "/admin/email/auto/"+key+"/pause", ct, body)
	if au, _ := a.store.Automation(id); !au.Paused {
		t.Error("the president should be able to pause an automation")
	}
}

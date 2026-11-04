package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// mailbox collects the emails the app sends, by address.
type mailbox struct {
	mu   sync.Mutex
	sent map[string][]sent
}

type sent struct{ subject, body string }

func newMailbox(a *App) *mailbox {
	b := &mailbox{sent: map[string][]sent{}}
	a.mailer.onSend = func(to, subject, body string) {
		b.mu.Lock()
		b.sent[to] = append(b.sent[to], sent{subject, body})
		b.mu.Unlock()
	}
	return b
}

// to waits for the background senders, then lists what went to an address.
func (b *mailbox) to(a *App, addr string) []sent {
	a.mailer.bg.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sent[addr]
}

// joinJSON sends the join form as the page's script does and returns its answer.
func joinJSON(t *testing.T, a *App, h http.Handler, vals url.Values) map[string]any {
	t.Helper()
	ct, body := joinForm(a, vals)
	r := httptest.NewRequest("POST", "/join", body)
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("join answer %d: %s", w.Code, w.Body)
	}
	return got
}

// Someone typing a member's email into the join form, with their own name,
// changes nothing and learns nothing: no waiver signed in the member's name,
// no riders or adults added, no details filled in. The member gets an email,
// and only their Confirm counts it.
func TestJoinWithSomeoneElsesEmail(t *testing.T) {
	a, h, admin := testApp(t)
	a.cfg.PayPalFamily = "https://paypal.example/family"
	box := newMailbox(a)
	year := a.cfg.ThisYear()
	bob, _, _ := a.store.Join(memberDetails{Name: "Bob Member", Email: "bob@example.com", Plan: "family"}, year)

	got := joinJSON(t, a, h, url.Values{"plan": {"family"}, "first": {"Mallory"}, "last": {"Stranger"}, "email": {"bob@example.com"},
		"phone": {"301-555-9999"}, "level": {"racer"}, "emergency_name": {"Evil Person"}, "emergency_phone": {"301-555-8888"},
		"rider_name": {"Kid Stranger"}, "rider_age": {"12"}, "guardian": {"yes"},
		"adult_name": {"Spam Target"}, "adult_email": {"target@example.com"}})
	answer := got["message"].(string) + " " + got["detail"].(string)
	if got["ok"] != true || !strings.Contains(answer, "This email is already on our list.") || got["pay_url"] != "" {
		t.Fatalf("answer %v", got)
	}
	for _, secret := range []string{"Bob", "paid", "unsubscribed"} {
		if strings.Contains(answer, secret) {
			t.Errorf("the answer gives away %q: %s", secret, answer)
		}
	}

	m, _ := a.store.MemberByEmail("bob@example.com")
	if len(a.store.Waivers(year)) != 0 || len(m.Household) != 0 || m.Phone != "" || m.Level != "" || m.EmergencyName != "" || m.TermsAgreed != nil {
		t.Fatalf("nothing should change before the member confirms: %+v, waivers %+v", m, a.store.Waivers(year))
	}
	if !m.RenewalWaiting() {
		t.Fatal("the form should wait for the member")
	}
	mail := box.to(a, "bob@example.com")
	if len(mail) != 1 || mail[0].subject != "Finish renewing your PaxVelo membership" ||
		!strings.Contains(mail[0].body, "[[Confirm it’s me]("+a.cfg.membershipURL(bob.Token)+"#confirm)]") ||
		!strings.Contains(mail[0].body, "Didn’t fill in the form? Ignore this email, and nothing changes.") {
		t.Fatalf("the member should get the email to confirm it: %+v", mail)
	}
	if len(box.to(a, "target@example.com")) != 0 {
		t.Error("nobody else gets anything")
	}
	if page := do(t, h, admin, "GET", "/admin/members", "", nil).Body.String(); !strings.Contains(page, "Renewal not confirmed") {
		t.Error("Members should show the renewal is waiting")
	}

	// Marking the dues paid emails no adults, since none were added.
	ct, body := form(url.Values{"paid": {"1"}})
	do(t, h, admin, "POST", "/admin/members/"+strconv.Itoa(m.ID)+"/paid", ct, body)
	if len(box.to(a, "target@example.com")) != 0 {
		t.Error("an adult typed by a stranger must not be emailed")
	}

	// Left alone, it lapses: an old one does nothing when confirmed.
	a.store.mu.Lock()
	a.store.byID(m.ID).PendingJoin.At = time.Now().Add(-(pendingJoinDays + 1) * 24 * time.Hour)
	a.store.mu.Unlock()
	if m, _ := a.store.MemberByEmail("bob@example.com"); m.RenewalWaiting() {
		t.Error("an old renewal shouldn't wait any more")
	}
	ct, body = form(url.Values{"t": {bob.Token}})
	do(t, h, nil, "POST", "/membership/confirm", ct, body)
	if m, _ := a.store.MemberByEmail("bob@example.com"); len(a.store.Waivers(year)) != 0 || m.Phone != "" || m.PendingJoin != nil {
		t.Errorf("an old renewal must not be applied: %+v", m)
	}
}

// A member renewing on the join form: the same answer as anyone, then their
// Confirm signs the waivers they filled in, adds the adults, and fills in
// what's missing.
func TestRenewOnJoinForm(t *testing.T) {
	a, h, _ := testApp(t)
	box := newMailbox(a)
	year := a.cfg.ThisYear()
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "family", Phone: "301-555-0123"}, year)
	paid, _, _ := a.store.Join(memberDetails{Name: "Lee Paid", Email: "lee@example.com", Plan: "individual"}, year)
	a.store.SetPaid(paid.ID, year, true)

	// Paid or not, the answer is the same but for the address.
	renew := func(first, last, email string, extra url.Values) map[string]any {
		v := url.Values{"plan": {"family"}, "first": {first}, "last": {last}, "email": {email}, "phone": {"240-555-0100"},
			"level": {"some"}, "emergency_name": {"Sam Rider"}, "emergency_phone": {"240-555-0199"}}
		for k, vs := range extra {
			v[k] = vs
		}
		return joinJSON(t, a, h, v)
	}
	lee := renew("Lee", "Paid", "lee@example.com", nil)
	got := renew("Pat", "Rider", "pat@example.com", url.Values{"rider_name": {"Sam Rider"}, "rider_age": {"9"}, "guardian": {"yes"},
		"adult_name": {"Jo Rider"}, "adult_email": {"jo@example.com"}})
	if strings.ReplaceAll(lee["detail"].(string), "lee@", "pat@") != got["detail"] || lee["message"] != got["message"] || lee["pay_url"] != got["pay_url"] {
		t.Errorf("a paid and an unpaid member should get the same answer:\n%v\n%v", lee, got)
	}

	page := do(t, h, nil, "GET", "/membership?t="+pat.Token, "", nil).Body.String()
	if !strings.Contains(page, "Finish renewing: confirm it’s you.") || !strings.Contains(page, "Confirm it’s me") {
		t.Fatal("their page should ask them to confirm the renewal")
	}
	// "Email me my link" sends the email with the Confirm button.
	ct, body := form(url.Values{"email": {"pat@example.com"}})
	do(t, h, nil, "POST", "/membership/link", ct, body)
	if mail := box.to(a, "pat@example.com"); len(mail) != 2 || mail[1].subject != "Finish renewing your PaxVelo membership" {
		t.Errorf("their link should come with the Confirm button: %+v", mail)
	}

	ct, body = form(url.Values{"t": {pat.Token}})
	page = followed(t, h, do(t, h, nil, "POST", "/membership/confirm", ct, body))
	if !strings.Contains(page, "you’re renewed. Your waiver is signed") || strings.Contains(page, "Finish renewing") {
		t.Error("their page should say it's done")
	}
	m, _ := a.store.MemberByEmail("pat@example.com")
	if w, ok := a.store.WaiverFor(m.ID, year); !ok || w.Signature != "Pat Rider" {
		t.Errorf("their waiver should be signed: %+v", w)
	}
	if minors := a.store.MinorWaivers(m.ID, year); len(minors) != 1 || minors[0].Rider != "Sam Rider" || minors[0].RiderAge != 9 {
		t.Errorf("the rider they signed for: %+v", minors)
	}
	if len(m.Household) != 1 || m.Household[0].Email != "jo@example.com" {
		t.Errorf("the adult they listed: %+v", m.Household)
	}
	if m.Phone != "301-555-0123" || m.EmergencyName != "Sam Rider" || m.Level != "some" || m.TermsAgreed == nil || m.PendingJoin != nil {
		t.Errorf("only what's missing is filled in: %+v", m)
	}
	// Pressing it again (or reloading) does nothing more.
	ct, body = form(url.Values{"t": {pat.Token}})
	do(t, h, nil, "POST", "/membership/confirm", ct, body)
	if n := len(a.store.Waivers(year)); n != 2 {
		t.Errorf("%d waivers after confirming twice", n)
	}
}

// Turning club emails back on from your own page confirms the address too,
// so it's true when the page says emails are on.
func TestResubscribeConfirms(t *testing.T) {
	a, h, _ := testApp(t)
	kim, _, _ := a.store.Join(memberDetails{Name: "Kim Rider", Email: "kim@example.com", Plan: "individual"}, a.cfg.ThisYear())
	a.store.Unsubscribe(kim.Token)
	a.store.JoinPublic(memberDetails{Name: "Kim Rider", Email: "kim@example.com", Plan: "individual"}, a.cfg.ThisYear()) // rejoins: unconfirmed
	ct, body := form(url.Values{"t": {kim.Token}})
	w := do(t, h, nil, "POST", "/membership/emails", ct, body)
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/membership?t="+kim.Token) {
		t.Errorf("it should go back to their page: %q", loc)
	}
	page := followed(t, h, w)
	got, _ := a.store.MemberByToken(kim.Token)
	if !got.Emailable() || !strings.Contains(page, "Club emails are back on.") {
		t.Errorf("emails should really be on: %+v", got)
	}
	if _, err := a.store.StartCampaign("News", "Hi", "all", a.cfg.ThisYear()); err != nil {
		t.Errorf("Kim should be on the next email: %v", err)
	}
	// The page says so only when it's true.
	a.store.Unsubscribe(kim.Token)
	if page := do(t, h, nil, "GET", "/membership?t="+kim.Token+"&emails=on", "", nil).Body.String(); strings.Contains(page, "Club emails are back on.") {
		t.Error("an old link mustn't say emails are on")
	}
}

// The welcome email goes once a website joiner is both paid and confirmed,
// in either order.
func TestWelcomeWaitsForConfirm(t *testing.T) {
	a, h, admin := testApp(t)
	box := newMailbox(a)
	day := a.today().AddDate(0, 0, 3).Format("2006-01-02")
	a.store.AddRide(CalRide{Date: day, Time: "18:00", Name: "Wednesday Shop Ride", Place: "Old Line Cycles"})
	m, _, _ := a.store.JoinPublic(memberDetails{Name: "Jo Rider", Email: "jo@example.com", Plan: "individual", Level: "new"}, a.cfg.ThisYear())

	// Confirmed first, then paid.
	ct, body := form(url.Values{"t": {m.Token}})
	do(t, h, nil, "POST", "/membership/confirm", ct, body)
	if mail := box.to(a, "jo@example.com"); len(mail) != 0 {
		t.Fatalf("no welcome before they've paid: %+v", mail)
	}
	ct, body = form(url.Values{"paid": {"1"}})
	if loc := do(t, h, admin, "POST", "/admin/members/"+strconv.Itoa(m.ID)+"/paid", ct, body).Header().Get("Location"); !strings.Contains(loc, "ok=paid-welcome") {
		t.Errorf("paid and confirmed: the welcome goes: %s", loc)
	}
	mail := box.to(a, "jo@example.com")
	if len(mail) != 1 || !strings.Contains(mail[0].subject, "Welcome") {
		t.Fatalf("welcome: %+v", mail)
	}
	// Its ride advice names rides from the calendar, not a fixed time.
	if b := mail[0].body; !strings.Contains(b, "Rides coming up that suit you:") || !strings.Contains(b, "[Wednesday Shop Ride](") ||
		!strings.Contains(b, "from Old Line Cycles") || strings.Contains(b, "Wildewood") {
		t.Errorf("the welcome should list the rides coming up:\n%s", b)
	}
}

// Emails about a member's own membership, like the one to confirm their
// email, have their My membership link at the foot, but no unsubscribe.
func TestConfirmEmailFooter(t *testing.T) {
	a, _, _ := testApp(t)
	page, _ := a.site.renderEmail("Confirm", "Hi", memberLinks{Mine: "https://example.com/membership?t=abc"}, a.cfg.SiteURL)
	if !strings.Contains(page, `href="https://example.com/membership?t=abc"`) || !strings.Contains(page, "My membership") || strings.Contains(page, "Unsubscribe") {
		t.Error("the footer should have My membership and no Unsubscribe")
	}
}

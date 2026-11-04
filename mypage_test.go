package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A member's link opens their page: dues, contact details and riding
// answers, which they can change. Anyone else's guess at a link gets nothing.
func TestMembershipPage(t *testing.T) {
	a, h, _ := testApp(t)
	a.cfg.PayPalIndividual = "https://paypal.example/one"
	m, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual",
		Phone: "301-555-0177", EmergencyName: "Sam Rider", EmergencyPhone: "240-555-0199", Level: "some"}, a.cfg.ThisYear())

	page := do(t, h, nil, "GET", "/membership?t="+m.Token, "", nil).Body.String()
	for _, want := range []string{"Hi Pat", "aren’t paid yet", "https://paypal.example/one", "phone ending in 0177", "Sam R., phone ending in 0199", `value="some" required checked`} {
		if !strings.Contains(page, want) {
			t.Errorf("membership page should show %q", want)
		}
	}
	// The link travels in emails that get forwarded, so the page shows only part.
	for _, secret := range []string{"301-555-0177", "Sam Rider", "240-555-0199"} {
		if strings.Contains(page, secret) {
			t.Errorf("membership page shows %q in full", secret)
		}
	}
	if page := do(t, h, nil, "GET", "/membership?t=guess", "", nil).Body.String(); strings.Contains(page, "Sam R") || !strings.Contains(page, "isn’t valid") {
		t.Error("a wrong link must not show anyone's details")
	}

	// Saving with the boxes blank keeps what's on file.
	ct, body := form(url.Values{"t": {m.Token}, "level": {"experienced"}})
	if w := do(t, h, nil, "POST", "/membership", ct, body); !strings.Contains(w.Body.String(), "Saved.") {
		t.Errorf("saving just the riding answer should work: %d", w.Code)
	}
	if got, _ := a.store.MemberByToken(m.Token); got.Phone != "301-555-0177" || got.EmergencyName != "Sam Rider" || got.EmergencyPhone != "240-555-0199" || got.Level != "experienced" {
		t.Errorf("blank boxes should keep what's on file: %+v", got)
	}
	// A new emergency contact needs both a name and a phone.
	ct, body = form(url.Values{"t": {m.Token}, "emergency_name": {"Lee Rider"}, "level": {"racer"}})
	if w := do(t, h, nil, "POST", "/membership", ct, body); w.Code != http.StatusBadRequest {
		t.Errorf("a new contact without a phone should be refused: %d", w.Code)
	}

	// A bad phone is refused and changes nothing.
	ct, body = form(url.Values{"t": {m.Token}, "phone": {"555"}, "emergency_name": {"Lee"}, "emergency_phone": {"240-555-0100"}, "level": {"racer"}})
	if w := do(t, h, nil, "POST", "/membership", ct, body); w.Code != http.StatusBadRequest {
		t.Errorf("a bad phone should be refused: %d", w.Code)
	}
	ct, body = form(url.Values{"t": {m.Token}, "phone": {"3015550000"}, "emergency_name": {"Lee Rider"}, "emergency_phone": {"240 555 0100"}, "level": {"racer"}})
	if w := do(t, h, nil, "POST", "/membership", ct, body); !strings.Contains(w.Body.String(), "Saved.") {
		t.Errorf("saving should say so: %d", w.Code)
	}
	got, _ := a.store.MemberByToken(m.Token)
	if got.Phone != "301-555-0000" || got.EmergencyName != "Lee Rider" || got.EmergencyPhone != "240-555-0100" || got.Level != "racer" {
		t.Errorf("details not saved: %+v", got)
	}

	a.store.SetPaid(m.ID, a.cfg.ThisYear(), true)
	if page := do(t, h, nil, "GET", "/membership?t="+m.Token, "", nil).Body.String(); !strings.Contains(page, "dues: paid ✓") || strings.Contains(page, "paypal.example") {
		t.Error("a paid member should see it, and no PayPal button")
	}

	// They can turn club emails back on themselves.
	a.store.Unsubscribe(m.Token)
	ct, body = form(url.Values{"t": {m.Token}})
	do(t, h, nil, "POST", "/membership/emails", ct, body)
	if got, _ := a.store.MemberByToken(m.Token); !got.Active() {
		t.Error("the member's own link should turn emails back on")
	}
}

// followed is the page a form's redirect goes to.
func followed(t *testing.T, h http.Handler, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected a redirect, got %d", w.Code)
	}
	loc, _, _ := strings.Cut(w.Header().Get("Location"), "#")
	return do(t, h, nil, "GET", loc, "", nil).Body.String()
}

// "Send me my link" answers the same for members and strangers.
func TestSendMembershipLink(t *testing.T) {
	a, h, _ := testApp(t)
	a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com"}, a.cfg.ThisYear())
	for _, email := range []string{"pat@example.com", "nobody@example.com"} {
		ct, body := form(url.Values{"email": {email}})
		if page := do(t, h, nil, "POST", "/membership/link", ct, body).Body.String(); !strings.Contains(page, "your link is on its way") {
			t.Errorf("%s: should say the link is on its way", email)
		}
	}
}

// Every member email links to their membership page, and {my_link} works in the body.
func TestMemberEmailsLinkToMembership(t *testing.T) {
	a, _, _ := testApp(t)
	m := Member{Name: "Pat", Email: "pat@example.com", Token: "abc"}
	if got := a.mailer.personalize("{my_link}", m); got != a.cfg.SiteURL+"/membership?t=abc" {
		t.Errorf("{my_link} = %q", got)
	}
	page, _ := a.site.renderEmail("News", "Hi", memberLinks{Mine: "https://example.com/membership?t=abc", Unsub: "https://example.com/u"}, a.cfg.SiteURL)
	if !strings.Contains(page, `href="https://example.com/membership?t=abc"`) || !strings.Contains(page, "My membership") {
		t.Error("a member email should link to their membership page")
	}
}

// Unsubscribing can be undone from the same page, a bad link offers to send
// the member their own link, and the links in a test email say it was a test.
func TestUnsubscribePages(t *testing.T) {
	a, h, _ := testApp(t)
	m, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com"}, a.cfg.ThisYear())

	ct, body := form(url.Values{"t": {m.Token}})
	page := do(t, h, nil, "POST", "/unsubscribe", ct, body).Body.String()
	if got, _ := a.store.MemberByToken(m.Token); got.Active() || !strings.Contains(page, "You’re unsubscribed") {
		t.Fatalf("should be unsubscribed: %+v", got)
	}
	if !strings.Contains(page, `action="/membership/emails#emails"`) || !strings.Contains(page, `value="`+m.Token+`"`) || strings.Contains(page, "we’ll turn them back on") {
		t.Error("the done page should let them turn emails back on themselves")
	}
	if page := do(t, h, nil, "GET", "/unsubscribe?t="+m.Token, "", nil).Body.String(); !strings.Contains(page, "Turn club emails back on") {
		t.Error("opening the link again should offer the same")
	}
	ct, body = form(url.Values{"t": {m.Token}})
	page = followed(t, h, do(t, h, nil, "POST", "/membership/emails", ct, body))
	if got, _ := a.store.MemberByToken(m.Token); !got.Active() || !strings.Contains(page, "Club emails are back on.") {
		t.Error("the button should turn emails back on, and say so")
	}

	if page := do(t, h, nil, "GET", "/unsubscribe?t=guess", "", nil).Body.String(); !strings.Contains(page, "isn’t valid") || !strings.Contains(page, `action="/membership/link"`) {
		t.Error("a bad link should offer to send their membership link")
	}

	for _, path := range []string{"/unsubscribe?t=" + testToken, "/membership?t=" + testToken} {
		if page := do(t, h, nil, "GET", path, "", nil).Body.String(); !strings.Contains(page, "That was a test email") {
			t.Errorf("%s: should say it came from a test email", path)
		}
	}
	ct, body = form(url.Values{"List-Unsubscribe": {"One-Click"}})
	if page := do(t, h, nil, "POST", "/unsubscribe?t="+testToken, ct, body).Body.String(); !strings.Contains(page, "That was a test email") {
		t.Error("Gmail's Unsubscribe button on a test email should say so too")
	}
}

// Someone who joins on the website gets an email to confirm their address,
// with the rides that suit them, and no club emails until they press Confirm
// on their page. Someone back after unsubscribing gets the same, and
// confirming turns club emails back on. Paying never waits for it.
func TestConfirmEmail(t *testing.T) {
	a, h, admin := testApp(t)
	a.cfg.PayPalIndividual = "https://paypal.example/one"
	type sent struct{ to, subject, body string }
	mail := make(chan sent, 10)
	a.mailer.onSend = func(to, subject, body string) { mail <- sent{to, subject, body} }
	confirmFor := func(to string) sent {
		t.Helper()
		for {
			select {
			case s := <-mail:
				if s.to == to && s.subject == "Confirm your email for PaxVelo" {
					return s
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("no confirm email to %s", to)
			}
		}
	}
	day := a.today().AddDate(0, 0, 2).Format("2006-01-02")
	a.store.AddRide(CalRide{Date: day, Time: "18:00", Name: "Wednesday Shop Ride", Place: "Wildewood"})
	a.store.AddRide(CalRide{Date: day, Time: "17:30", Name: "Tuesday Night Ride"})
	a.store.AddRide(CalRide{Date: day, Time: "19:00", Name: "Zwift social spin"}) // open to all, but not a first ride

	var body io.Reader
	ct, body := joinForm(a, url.Values{"plan": {"individual"}, "first": {"Jo"}, "last": {"Rider"}, "email": {"jo@example.com"},
		"phone": {"301-555-0123"}, "emergency_name": {"Sam Rider"}, "emergency_phone": {"240-555-0199"}, "level": {"new"}})
	page := do(t, h, nil, "POST", "/join", ct, body).Body.String()
	if !strings.Contains(page, "paypal.example/one") || !strings.Contains(page, "confirm your email: we’ve sent a link to jo@example.com") {
		t.Errorf("the join page should show the pay link and explain the email: %s", page)
	}
	c := confirmFor("jo@example.com")
	m := a.store.Members()[0]
	for _, want := range []string{"[[Confirm my email](" + a.cfg.membershipURL(m.Token) + "&confirm=1#confirm)]", "If you didn’t sign up for PaxVelo, no action is needed", "paypal.example/one", "Wednesday Shop Ride", "Wildewood"} {
		if !strings.Contains(c.body, want) {
			t.Errorf("confirm email missing %q:\n%s", want, c.body)
		}
	}
	if strings.Contains(c.body, "Tuesday Night Ride") || strings.Contains(c.body, "Zwift") {
		t.Error("a new rider should be sent to a social ride on the road, not the fast one or a virtual one")
	}
	if !m.Unconfirmed || m.Emailable() {
		t.Fatalf("a new joiner waits for their confirmation: %+v", m)
	}
	if _, err := a.store.StartCampaign("News", "Hi", "all", a.cfg.ThisYear()); err == nil {
		t.Error("club emails should skip an unconfirmed address")
	}
	if page := do(t, h, admin, "GET", "/admin/members", "", nil).Body.String(); !strings.Contains(page, "Email not confirmed") {
		t.Error("Members should show the email isn't confirmed")
	}

	// Their page asks them to confirm; the button does it.
	if page := do(t, h, nil, "GET", "/membership?t="+m.Token, "", nil).Body.String(); !strings.Contains(page, `action="/membership/confirm"`) {
		t.Error("their page should have the Confirm button")
	}
	ct, body = form(url.Values{"t": {m.Token}})
	page = followed(t, h, do(t, h, nil, "POST", "/membership/confirm", ct, body))
	if got, _ := a.store.MemberByToken(m.Token); got.Unconfirmed || !got.Emailable() || !strings.Contains(page, "your email is confirmed") || strings.Contains(page, `action="/membership/confirm"`) {
		t.Errorf("confirming should take: %+v", got)
	}
	if _, err := a.store.StartCampaign("News", "Hi", "all", a.cfg.ThisYear()); err != nil {
		t.Errorf("a confirmed member gets club emails: %v", err)
	}

	// Back after unsubscribing: the same, and confirming turns emails on.
	kim, _, _ := a.store.Join(memberDetails{Name: "Kim Rider", Email: "kim@example.com", Plan: "individual"}, a.cfg.ThisYear())
	a.store.Unsubscribe(kim.Token)
	ct, body = joinForm(a, url.Values{"plan": {"individual"}, "name": {"Kim Rider"}, "email": {"kim@example.com"}, "phone": {"301-555-0124"},
		"level": {"racer"}, "emergency_name": {"Lee Rider"}, "emergency_phone": {"240-555-0100"}})
	if page := do(t, h, nil, "POST", "/join", ct, body).Body.String(); !strings.Contains(page, "This email is already on our list.") {
		t.Errorf("the join page should explain: %s", page)
	}
	if c := confirmFor("kim@example.com"); !strings.Contains(c.body, "Welcome back") {
		t.Errorf("a returning member's email should say so:\n%s", c.body)
	}
	if got, _ := a.store.MemberByToken(kim.Token); got.Active() || !got.Unconfirmed {
		t.Fatalf("club emails stay off until they confirm: %+v", got)
	}
	ct, body = form(url.Values{"t": {kim.Token}})
	do(t, h, nil, "POST", "/membership/confirm", ct, body)
	if got, _ := a.store.MemberByToken(kim.Token); !got.Emailable() {
		t.Errorf("confirming should turn club emails back on: %+v", got)
	}

	// "Send me my link" for someone still unconfirmed sends the confirm email.
	a.store.Join(memberDetails{Name: "Lou Rider", Email: "lou@example.com"}, a.cfg.ThisYear())
	lou, _ := a.store.MemberByEmail("lou@example.com")
	a.store.Unsubscribe(lou.Token)
	ct, body = joinForm(a, url.Values{"plan": {"individual"}, "name": {"Lou Rider"}, "email": {"lou@example.com"}, "phone": {"301-555-0125"},
		"level": {"some"}, "emergency_name": {"Lee Rider"}, "emergency_phone": {"240-555-0100"}})
	do(t, h, nil, "POST", "/join", ct, body)
	confirmFor("lou@example.com")
	ct, body = form(url.Values{"email": {"lou@example.com"}})
	do(t, h, nil, "POST", "/membership/link", ct, body)
	confirmFor("lou@example.com")

	// An admin adding the address vouches for it.
	a.store.Join(memberDetails{Name: "Lou Rider", Email: "lou@example.com"}, a.cfg.ThisYear())
	if got, _ := a.store.MemberByEmail("lou@example.com"); got.Unconfirmed {
		t.Error("an admin adding a member should confirm them")
	}
}

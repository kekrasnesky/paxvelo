package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A member's link opens their page: dues, contact details and riding
// answers, which they can change. Anyone else's guess at a link gets nothing.
func TestMembershipPage(t *testing.T) {
	a, h, _ := testApp(t)
	a.cfg.PayPalIndividual = "https://paypal.example/one"
	m, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual",
		Phone: "301-555-0123", EmergencyName: "Sam Rider", EmergencyPhone: "240-555-0199", Level: "some"}, a.cfg.ThisYear())

	page := do(t, h, nil, "GET", "/membership?t="+m.Token, "", nil).Body.String()
	for _, want := range []string{"Hi Pat", "aren’t paid yet", "https://paypal.example/one", "Sam Rider", `value="some" required checked`} {
		if !strings.Contains(page, want) {
			t.Errorf("membership page should show %q", want)
		}
	}
	if page := do(t, h, nil, "GET", "/membership?t=guess", "", nil).Body.String(); strings.Contains(page, "Sam Rider") || !strings.Contains(page, "isn’t valid") {
		t.Error("a wrong link must not show anyone's details")
	}

	// A bad phone is refused and changes nothing.
	ct, body := form(url.Values{"t": {m.Token}, "phone": {"555"}, "emergency_name": {"Lee"}, "emergency_phone": {"240-555-0100"}, "level": {"racer"}})
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

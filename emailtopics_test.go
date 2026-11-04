package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A member can turn off the newsletter or calendar event emails and still
// get club notices; unsubscribing stops everything.
func TestEmailTopics(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual"}, year)
	a.store.Join(memberDetails{Name: "Lee Rider", Email: "lee@example.com", Plan: "individual"}, year)

	page := do(t, h, nil, "GET", "/membership?t="+pat.Token, "", nil).Body.String()
	for _, want := range []string{`name="newsletter" value="yes" checked`, `name="events" value="yes" checked`, "Unsubscribe from everything"} {
		if !strings.Contains(page, want) {
			t.Errorf("their page should have %q", want)
		}
	}
	// Turn off events, keep the newsletter.
	ct, body := form(url.Values{"t": {pat.Token}, "do": {"topics"}, "newsletter": {"yes"}})
	page = followed(t, h, do(t, h, nil, "POST", "/membership/emails", ct, body))
	if !strings.Contains(page, "Saved.") || !strings.Contains(page, `name="newsletter" value="yes" checked`) || strings.Contains(page, `name="events" value="yes" checked`) {
		t.Error("the switches should save")
	}
	recipients := func(c Campaign, err error) int {
		t.Helper()
		if err != nil {
			return 0
		}
		return c.Total
	}
	if n := recipients(a.store.StartCampaignAbout(topicEvents, "Saturday ride", "Hi", "all", year)); n != 1 {
		t.Errorf("a ride email should skip Pat: %d", n)
	}
	if n := recipients(a.store.StartIssueCampaign(1, "November", "Hi", "all", year)); n != 2 {
		t.Errorf("the newsletter still goes to Pat: %d", n)
	}
	if n := recipients(a.store.StartCampaign("Dues", "Hi", "all", year)); n != 2 {
		t.Errorf("club notices go to everyone: %d", n)
	}
	if page := do(t, h, admin, "GET", "/admin/email", "", nil).Body.String(); !strings.Contains(page, `data-all="Everyone (2)" data-events="Everyone (1)"`) {
		t.Error("the Email page should count who a rides and events email reaches")
	}

	// The Unsubscribe link in a newsletter offers to stop just the newsletter.
	page = do(t, h, nil, "GET", "/unsubscribe?t="+pat.Token+"&from=newsletter", "", nil).Body.String()
	if !strings.Contains(page, `class="btn" type="submit" name="what" value="newsletter"`) || !strings.Contains(page, `value="all">Everything`) {
		t.Error("the unsubscribe page should offer the newsletter first, and everything")
	}
	ct, body = form(url.Values{"t": {pat.Token}, "what": {"newsletter"}})
	page = do(t, h, nil, "POST", "/unsubscribe", ct, body).Body.String()
	got, _ := a.store.MemberByToken(pat.Token)
	if !got.NoNewsletter || !got.Active() || !strings.Contains(page, "You won’t get the newsletter any more") {
		t.Errorf("stopping the newsletter keeps other emails: %+v", got)
	}
	// Gmail's one-click button stops the kind of email it came in.
	lee, _ := a.store.MemberByEmail("lee@example.com")
	ct, body = form(url.Values{"List-Unsubscribe": {"One-Click"}})
	if w := do(t, h, nil, "POST", "/unsubscribe?t="+lee.Token+"&from=events", ct, body); w.Code != http.StatusOK {
		t.Fatalf("one-click: %d", w.Code)
	}
	if got, _ := a.store.MemberByToken(lee.Token); !got.NoEvents || !got.Active() {
		t.Errorf("one-click from an event email stops event emails only: %+v", got)
	}
	// And from an email of no kind, everything, as before.
	ct, body = form(url.Values{"List-Unsubscribe": {"One-Click"}})
	do(t, h, nil, "POST", "/unsubscribe?t="+lee.Token, ct, body)
	if got, _ := a.store.MemberByToken(lee.Token); got.Active() {
		t.Error("one-click from a club notice unsubscribes")
	}
	// Unsubscribe from everything, from their page.
	ct, body = form(url.Values{"t": {pat.Token}, "what": {"all"}})
	do(t, h, nil, "POST", "/unsubscribe", ct, body)
	if got, _ := a.store.MemberByToken(pat.Token); got.Active() {
		t.Error("Unsubscribe from everything should unsubscribe")
	}
}

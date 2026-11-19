package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Name and email on the newsletter page put a new address on the email list
// once it's confirmed; the page answers the same for an address already on
// it, and only new or unsubscribed addresses get an email.
func TestNewsletterSignup(t *testing.T) {
	a, h, _ := testApp(t)
	box := newMailbox(a)
	a.refreshNewsletter()
	if page := do(t, h, nil, "GET", "/newsletter", "", nil).Body.String(); !strings.Contains(page, `action="/newsletter/subscribe"`) || !strings.Contains(page, `name="ft" value="`) {
		t.Fatal("the newsletter page should have the signup form")
	}
	ts := strconv.FormatInt(time.Now().Add(-10*time.Second).Unix(), 10)
	ft := ts + "." + sign(a.formKey(), ts)[:24]
	signup := func(name, email string, extra url.Values) (int, string) {
		v := url.Values{"name": {name}, "email": {email}, "ft": {ft}}
		for k, vs := range extra {
			v[k] = vs
		}
		ct, body := form(v)
		w := do(t, h, nil, "POST", "/newsletter/subscribe", ct, body)
		return w.Code, w.Body.String()
	}

	code, page := signup("Nell Reader", "nell@example.com", nil)
	if code != http.StatusOK || !strings.Contains(page, "Check your email") {
		t.Fatalf("after signing up: %d", code)
	}
	m, ok := a.store.MemberByEmail("nell@example.com")
	if !ok || m.Plan != "" || !m.Unconfirmed || m.Emailable() || m.WelcomePending || !m.newsOnly() {
		t.Fatalf("on the list, waiting for Confirm: %+v", m)
	}
	mail := box.to(a, "nell@example.com")
	if len(mail) != 1 || mail[0].subject != "Confirm your PaxVelo newsletter" || strings.Contains(mail[0].body, "Haven’t paid") || !strings.Contains(mail[0].body, "confirm=1") {
		t.Fatalf("the confirm email: %+v", mail)
	}
	if page := do(t, h, nil, "GET", "/membership?t="+m.Token, "", nil).Body.String(); !strings.Contains(page, `href="/join"`) || strings.Contains(page, "Not paid") || strings.Contains(page, `id="waiver"`) {
		t.Error("an email-list-only page offers Join, not dues or a waiver")
	}
	ct, body := form(url.Values{"t": {m.Token}})
	do(t, h, nil, "POST", "/membership/confirm", ct, body)
	if m, _ = a.store.MemberByEmail("nell@example.com"); !m.Emailable() || !m.Wants(topicNewsletter) {
		t.Errorf("confirmed, the newsletter comes: %+v", m)
	}

	// Already on the list: the same page, no email, nothing changed.
	_, again := signup("Somebody", "nell@example.com", nil)
	if !strings.Contains(again, "Check your email") || len(box.to(a, "nell@example.com")) != 1 {
		t.Error("an address already getting emails gets the same page and no email")
	}
	if m, _ = a.store.MemberByEmail("nell@example.com"); m.Name != "Nell Reader" {
		t.Errorf("nothing changes for an address on the list: %+v", m)
	}

	// The honeypot looks done and does nothing; an old page asks to try again.
	signup("Bot", "bot@example.com", url.Values{"website": {"x"}})
	if _, ok := a.store.MemberByEmail("bot@example.com"); ok {
		t.Error("the honeypot should add nobody")
	}
	if code, page := signup("Late", "late@example.com", url.Values{"ft": {"1.bad"}}); code != http.StatusBadRequest || !strings.Contains(page, "This page expired") || !strings.Contains(page, `value="late@example.com"`) {
		t.Errorf("a bad form token: %d", code)
	}
	if code, _ := signup("", "noname@example.com", nil); code != http.StatusBadRequest {
		t.Error("a name is needed")
	}
}

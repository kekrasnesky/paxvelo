package main

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The other adults on a Family plan each get a link, by email, to sign their
// own waiver. They're listed on the join form or the member's page, and the
// admin's record counts them until they sign.
func TestHouseholdAdults(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	var mu sync.Mutex
	sent := map[string]string{} // to: body
	a.mailer.onSend = func(to, subject, body string) {
		mu.Lock()
		defer mu.Unlock()
		sent[to] = body
	}
	link := func(to string) string { // waits for the email the join form sends in the background
		for range 100 {
			mu.Lock()
			body, ok := sent[to]
			mu.Unlock()
			if ok {
				_, rest, _ := strings.Cut(body, "/membership?t=")
				tok, _, _ := strings.Cut(rest, ")")
				return "/membership?t=" + tok
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no email to %s", to)
		return ""
	}
	join := func(plan string, extra url.Values) (int, string) {
		v := url.Values{"plan": {plan}, "first": {"Pat"}, "last": {"Rider"}, "email": {"pat@example.com"},
			"phone": {"301-555-0123"}, "emergency_name": {"Lee Rider"}, "emergency_phone": {"240-555-0199"}, "level": {"some"}}
		for k, vs := range extra {
			v[k] = vs
		}
		ct, body := joinForm(a, v)
		w := do(t, h, nil, "POST", "/join", ct, body)
		return w.Code, w.Body.String()
	}

	if page := do(t, h, nil, "GET", "/join", "", nil).Body.String(); strings.Count(page, `name="adult_email"`) != maxHousehold {
		t.Error("the join form should have room for the other adults")
	}
	for problem, adults := range map[string]url.Values{
		"adult-name":  {"adult_name": {"Jo"}, "adult_email": {"jo@example.com"}},
		"adult-email": {"adult_name": {"Jo Rider"}, "adult_email": {"jo@"}},
		"adult-self":  {"adult_name": {"Jo Rider"}, "adult_email": {"PAT@example.com"}},
	} {
		if code, body := join("family", adults); code != http.StatusBadRequest || !strings.Contains(body, template.HTMLEscapeString(joinProblems[problem])) || !strings.Contains(body, `value="Jo Rider"`) && problem != "adult-name" {
			t.Errorf("%s: should be refused, keeping what was typed: %d", problem, code)
		}
	}

	code, body := join("family", url.Values{"adult_name": {"Jo Rider", "", "Max Rider"}, "adult_email": {"jo@example.com", "", "Max@Example.com"}})
	if code != http.StatusOK || !strings.Contains(body, "Once your dues are paid, we’ll email Jo and Max a link to sign their own waiver.") {
		t.Fatalf("joining with adults: %d", code)
	}
	m, _ := a.store.MemberByEmail("pat@example.com")
	if len(m.Household) != 2 || m.Household[1].Email != "max@example.com" || m.Household[0].Token == "" {
		t.Fatalf("household %+v", m.Household)
	}

	// Nothing goes to them until an admin marks Pat's dues paid: not from
	// the join form, "Send me my link", or Pat's page.
	ct, fbody := form(url.Values{"email": {"jo@example.com"}})
	do(t, h, nil, "POST", "/membership/link", ct, fbody)
	ct, fbody = form(url.Values{"t": {m.Token}, "do": {"send"}, "email": {"jo@example.com"}})
	if w := do(t, h, nil, "POST", "/membership/household", ct, fbody); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "once your "+strconv.Itoa(year)+" dues are paid") {
		t.Errorf("sending a link before the dues are paid: %d", w.Code)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	_, early := sent["jo@example.com"]
	mu.Unlock()
	if early {
		t.Fatal("an adult's link waits until the dues are paid")
	}
	if unsigned := do(t, h, admin, "GET", "/admin/waivers?show=unsigned", "", nil).Body.String(); !strings.Contains(unsigned, "Their link is emailed once the dues are marked paid.") {
		t.Error("Not signed should say their link is waiting for the dues")
	}
	if needsWaivers(a) {
		t.Error("an unpaid membership doesn't need attention yet")
	}
	ct, fbody = form(url.Values{"paid": {"1"}})
	if w := do(t, h, admin, "POST", "/admin/members/"+strconv.Itoa(m.ID)+"/paid", ct, fbody); !strings.Contains(w.Header().Get("Location"), "-links") {
		t.Errorf("marking paid should email the adults: %s", w.Header().Get("Location"))
	}
	if !needsWaivers(a) {
		t.Error("Jo and Max ride on paid dues without a waiver")
	}
	jo := link("jo@example.com")
	link("max@example.com")
	mu.Lock()
	joEmail := sent["jo@example.com"]
	mu.Unlock()
	if !strings.Contains(joEmail, "Pat Rider’s PaxVelo family membership") {
		t.Error("the email should say whose membership they're on")
	}

	// Their own page: just their waiver, signed with their own name.
	page := do(t, h, nil, "GET", jo, "", nil).Body.String()
	if !strings.Contains(page, "My Waiver") || !strings.Contains(page, "Pat Rider’s PaxVelo family membership") || !strings.Contains(page, "IN CONSIDERATION") || strings.Contains(page, "Your details") {
		t.Error("an adult's link should open their own waiver, and nothing of the member's")
	}
	tok := strings.TrimPrefix(jo, "/membership?t=")
	ct, fbody = form(url.Values{"t": {tok}, "waiver": {"yes"}, "signature": {"Pat Rider"}})
	if w := do(t, h, nil, "POST", "/membership/waiver", ct, fbody); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "as the club has it: Jo Rider") {
		t.Errorf("an adult signs with their own name: %d", w.Code)
	}
	ct, fbody = form(url.Values{"t": {tok}, "waiver": {"yes"}, "signature": {"Jo Rider"}})
	w := do(t, h, nil, "POST", "/membership/waiver", ct, fbody)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("signing: %d", w.Code)
	}
	if page := do(t, h, nil, "GET", strings.TrimSuffix(w.Header().Get("Location"), "#waiver"), "", nil).Body.String(); !strings.Contains(page, "waiver: signed ✓") {
		t.Error("their page should say it's signed")
	}
	if s, ok := a.store.HouseholdWaiver(m.ID, year, "jo@example.com"); !ok || s.Rider != "Jo Rider" || s.Name != "Pat Rider" || !s.Household {
		t.Errorf("Jo's waiver %+v", s)
	}
	if _, ok := a.store.WaiverFor(m.ID, year); !ok {
		t.Error("Pat signed their own on the join form")
	}

	// The record: Max still hasn't signed.
	unsigned := do(t, h, admin, "GET", "/admin/waivers?show=unsigned", "", nil).Body.String()
	if !strings.Contains(unsigned, "max@example.com") || strings.Contains(unsigned, "jo@example.com") || !strings.Contains(unsigned, "On Pat Rider’s family membership") {
		t.Error("Not signed should list Max, on Pat's membership")
	}
	if !needsWaivers(a) {
		t.Error("Max rides on paid dues without a waiver")
	}
	if signed := do(t, h, admin, "GET", "/admin/waivers", "", nil).Body.String(); !strings.Contains(signed, "jo@example.com") {
		t.Error("Signed should list Jo with her own email")
	}

	// Pat's page lists them, and can send Max's link again, add one more, and remove one.
	mine := do(t, h, nil, "GET", "/membership?t="+m.Token, "", nil).Body.String()
	if !strings.Contains(mine, "Other adults on your membership") || !strings.Contains(mine, "waiver not signed yet") || !strings.Contains(mine, "Add an adult") {
		t.Error("the member's page should list the adults")
	}
	mu.Lock()
	delete(sent, "max@example.com")
	mu.Unlock()
	ct, fbody = form(url.Values{"t": {m.Token}, "do": {"send"}, "email": {"max@example.com"}})
	if w := do(t, h, nil, "POST", "/membership/household", ct, fbody); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "household=sent") {
		t.Errorf("sending again: %d", w.Code)
	}
	link("max@example.com")
	for _, bad := range []url.Values{
		{"adult_name": {"Jo Rider"}, "adult_email": {"jo@example.com"}},
		{"adult_name": {"Kim"}, "adult_email": {"kim@example.com"}},
	} {
		bad.Set("t", m.Token)
		ct, fbody = form(bad)
		if w := do(t, h, nil, "POST", "/membership/household", ct, fbody); w.Code != http.StatusBadRequest {
			t.Errorf("%v should be refused: %d", bad, w.Code)
		}
	}
	ct, fbody = form(url.Values{"t": {m.Token}, "do": {"add"}, "adult_name": {"Kim Rider"}, "adult_email": {"kim@example.com"}})
	if w := do(t, h, nil, "POST", "/membership/household", ct, fbody); w.Code != http.StatusSeeOther {
		t.Errorf("adding: %d", w.Code)
	}
	link("kim@example.com")
	if page := do(t, h, nil, "GET", "/membership?t="+m.Token, "", nil).Body.String(); strings.Contains(page, "Add an adult") {
		t.Errorf("a family membership has room for %d more adults", maxHousehold)
	}
	ct, fbody = form(url.Values{"t": {m.Token}, "do": {"remove"}, "email": {"jo@example.com"}})
	do(t, h, nil, "POST", "/membership/household", ct, fbody)
	if m, _ = a.store.MemberByEmail("pat@example.com"); len(m.Household) != 2 {
		t.Errorf("household after removing Jo %+v", m.Household)
	}
	if w := do(t, h, nil, "GET", jo, "", nil); !strings.Contains(w.Body.String(), "isn’t valid") {
		t.Error("a removed adult's link stops working")
	}
	if _, ok := a.store.HouseholdWaiver(m.ID, year, "jo@example.com"); !ok {
		t.Error("the waiver Jo signed stays on record")
	}

	// "Send me my link" finds an adult by their email too.
	mu.Lock()
	delete(sent, "kim@example.com")
	mu.Unlock()
	ct, fbody = form(url.Values{"email": {"kim@example.com"}})
	do(t, h, nil, "POST", "/membership/link", ct, fbody)
	link("kim@example.com")
}

// An Individual plan has no other adults: the join form's boxes for them are
// hidden, and anything left in them is ignored.
func TestIndividualIgnoresAdults(t *testing.T) {
	a, h, _ := testApp(t)
	ct, body := joinForm(a, url.Values{"plan": {"individual"}, "first": {"Pat"}, "last": {"Rider"}, "email": {"pat@example.com"},
		"phone": {"301-555-0123"}, "emergency_name": {"Lee Rider"}, "emergency_phone": {"240-555-0199"}, "level": {"some"},
		"adult_name": {"Jo"}, "adult_email": {"jo@"}})
	if w := do(t, h, nil, "POST", "/join", ct, body); w.Code != http.StatusOK {
		t.Fatalf("join: %d", w.Code)
	}
	if m, _ := a.store.MemberByEmail("pat@example.com"); len(m.Household) != 0 {
		t.Errorf("household %+v", m.Household)
	}
}

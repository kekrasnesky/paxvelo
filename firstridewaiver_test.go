package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// A member who signs the first-ride waiver instead of the yearly one: the
// form points them to their membership page first, and if they sign it
// anyway it counts as their waiver for the year (the same League text). The
// waiver reminder skips them, their page shows it signed, and the board sees
// them marked First ride only.
func TestFirstRideWaiverCountsForMember(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Oshea", Email: "pat@example.com", Plan: "individual"}, year)
	a.store.SetPaid(pat.ID, year, true)
	pat, _ = a.store.MemberByID(pat.ID)

	own := url.Values{"name": {"Pat Oshea"}, "email": {"pat@example.com"}, "bike": {"own"}, "signature": {"Pat Oshea"}}
	ct, b := form(rideWaiverForm(a, own))
	w := do(t, h, nil, "POST", "/ride-waiver", ct, b)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "You’re a member.") || !strings.Contains(w.Body.String(), `action="/membership/link"`) ||
		!strings.Contains(w.Body.String(), "Sign here anyway") || !strings.Contains(w.Body.String(), `name="member_ok"`) {
		t.Fatalf("a member is pointed to their page first: %d %s", w.Code, w.Body.String())
	}
	if len(a.store.RideWaivers(year, a.cfg.Location)) != 0 || a.store.HasWaiver(pat.ID, year) {
		t.Fatal("nothing is signed until they choose to sign here")
	}

	due := Automation{ID: 1, Trigger: "waiver", Days: 0}
	if _, _, ok := a.autoDue(due, pat); !ok {
		t.Fatal("unsigned, the reminder is due")
	}

	own.Set("member_ok", "1")
	ct, b = form(rideWaiverForm(a, own))
	if w := do(t, h, nil, "POST", "/ride-waiver", ct, b); w.Code != http.StatusSeeOther {
		t.Fatalf("signing anyway: %d %s", w.Code, w.Body.String())
	}
	if !a.store.HasWaiver(pat.ID, year) {
		t.Fatal("their first-ride waiver counts for the year")
	}
	if _, _, ok := a.autoDue(due, pat); ok {
		t.Error("the waiver reminder skips them")
	}
	for _, u := range a.unsignedRiders(year) {
		if u.Member.ID == pat.ID {
			t.Error("they're off the Not signed list")
		}
	}

	page := do(t, h, nil, "GET", "/membership?t="+pat.Token, "", nil).Body.String()
	if !strings.Contains(page, strconv.Itoa(year)+" member waiver") || !strings.Contains(page, "at your first ride") || strings.Contains(page, `action="/membership/waiver#waiver"`) {
		t.Error("their page shows the waiver signed at their first ride, with no form to sign")
	}

	list := do(t, h, admin, "GET", "/admin/members", "", nil).Body.String()
	if !strings.Contains(list, "First ride only") || !strings.Contains(list, "/admin/members?waiver=firstride") {
		t.Error("the member list marks them and can show just them")
	}
	only := do(t, h, admin, "GET", "/admin/members?waiver=firstride", "", nil).Body.String()
	if !strings.Contains(only, `id="m`+strconv.Itoa(pat.ID)+`"`) {
		t.Error("the filter lists them")
	}
	if p := do(t, h, admin, "GET", "/admin/members/"+strconv.Itoa(pat.ID), "", nil).Body.String(); !strings.Contains(p, "First ride only") {
		t.Error("their admin page marks them")
	}
	found := false
	for _, it := range a.attention() {
		found = found || strings.Contains(it.Text, "signed only the first-ride waiver")
	}
	if !found {
		t.Error("Home says who has only the first-ride waiver")
	}

	// Signing the yearly one clears the mark.
	if _, _, err := a.store.SignWaiver(Waiver{MemberID: pat.ID, Year: year, Signature: "Pat Oshea", Where: "Membership page"}, waiverText{HTML: "x", Version: "v"}); err != nil {
		t.Fatal(err)
	}
	if a.store.FirstRideOnly(year)[pat.ID] {
		t.Error("with the yearly waiver signed, they're not First ride only")
	}
}

// Signing for a rider under 18 isn't the member's own waiver, and someone
// not paid up isn't stopped.
func TestFirstRideWaiverMinorAndNonMember(t *testing.T) {
	a, h, _ := testApp(t)
	year := a.cfg.ThisYear()
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Oshea", Email: "pat@example.com", Plan: "individual"}, year)
	a.store.SetPaid(pat.ID, year, true)
	ct, b := form(rideWaiverForm(a, url.Values{"name": {"Pat Oshea"}, "email": {"pat@example.com"}, "signature": {"Pat Oshea"},
		"rider": {"Kid Oshea"}, "rider_age": {"12"}, "guardian": {"yes"}}))
	if w := do(t, h, nil, "POST", "/ride-waiver", ct, b); w.Code != http.StatusSeeOther {
		t.Fatalf("a parent signing for a child isn't stopped: %d", w.Code)
	}
	if a.store.HasWaiver(pat.ID, year) {
		t.Error("a waiver for their child doesn't cover them")
	}

	unpaid, _, _ := a.store.Join(memberDetails{Name: "Una Paid", Email: "una@example.com", Plan: "individual"}, year)
	ct, b = form(rideWaiverForm(a, url.Values{"name": {"Una Paid"}, "email": {"una@example.com"}, "signature": {"Una Paid"}}))
	if w := do(t, h, nil, "POST", "/ride-waiver", ct, b); w.Code != http.StatusSeeOther {
		t.Fatalf("someone not paid up signs straight away: %d", w.Code)
	}
	if !a.store.HasWaiver(unpaid.ID, year) {
		t.Error("it still counts once they're on the list")
	}
}

// Waiver reminders end with a Sign button to the member's own waiver unless
// the words already link to it.
func TestWaiverReminderButton(t *testing.T) {
	a, _, _ := testApp(t)
	got := autoBody(Automation{Trigger: "waiver", Body: "Hi {first_name}, please sign.\n"})
	if got != "Hi {first_name}, please sign.\n\n"+waiverButton {
		t.Errorf("button added: %q", got)
	}
	if b := "Sign [here]({waiver_link})."; autoBody(Automation{Trigger: "waiver", Body: b}) != b {
		t.Error("a body with the link is left alone")
	}
	if b := "Thanks!"; autoBody(Automation{Trigger: "paid", Body: b}) != b {
		t.Error("other automations are left alone")
	}
	if l := a.mailer.personalize("{waiver_link}", Member{Token: "abc"}); l != a.cfg.SiteURL+"/membership?t=abc#waiver" {
		t.Errorf("{waiver_link} = %q", l)
	}
}

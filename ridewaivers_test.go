package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func rideWaiverForm(a *App, v url.Values) url.Values {
	base := url.Values{"ft": {oldToken(a)}, "name": {"Sam Newrider"}, "email": {"Sam@Example.com"}, "bike": {"loaner"},
		"waiver": {"yes"}, "signature": {"sam newrider"}}
	for k, vs := range v {
		if len(vs) == 1 && vs[0] == "" {
			base.Del(k)
		} else {
			base[k] = vs
		}
	}
	return base
}

// A first-time rider signs with a name and email. A club loaner bike adds the
// loaner agreement to the League waiver; their own bike signs the League
// waiver alone. The page has no words beyond the title, the waiver, the
// fields and the button.
func TestRideWaiverLoanerAndOwn(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()

	page := do(t, h, nil, "GET", "/ride-waiver", "", nil)
	body := page.Body.String()
	for _, want := range []string{"Ride Waiver", "PAXVELO LOANER BICYCLE AGREEMENT", "LEAGUE OF AMERICAN BYCYLISTS", `name="bike" value="loaner"`, `name="bike" value="own"`, `name="website"`, `name="ft"`, "sig-canvas"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page should have %q", want)
		}
	}
	if page.Header().Get("X-Robots-Tag") != "noindex" {
		t.Error("the page stays out of search engines")
	}
	if strings.Contains(body, `value="loaner" required checked`) {
		t.Error("no bike is picked until the rider picks one")
	}
	if w := do(t, h, nil, "GET", "/loaner", "", nil); w.Header().Get("Location") != "/ride-waiver?bike=loaner" {
		t.Errorf("/loaner opens the page with the club bike picked: %s", w.Header().Get("Location"))
	} else if !strings.Contains(do(t, h, nil, "GET", "/ride-waiver?bike=loaner", "", nil).Body.String(), `value="loaner" required checked`) {
		t.Error("?bike=loaner picks the club bike")
	}

	ct, b := form(rideWaiverForm(a, nil))
	w := do(t, h, nil, "POST", "/ride-waiver", ct, b)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/ride-waiver?signed=1" {
		t.Fatalf("signing: %d %s %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if done := do(t, h, nil, "GET", "/ride-waiver?signed=1", "", nil).Body.String(); !strings.Contains(done, "Signed.") || strings.Contains(done, `name="signature"`) {
		t.Error("after signing the page says so, without the form")
	}
	ct, b = form(rideWaiverForm(a, url.Values{"name": {"Jo Ownbike"}, "email": {"jo@example.com"}, "bike": {"own"}, "signature": {"Jo Ownbike"}}))
	if w := do(t, h, nil, "POST", "/ride-waiver", ct, b); w.Code != http.StatusSeeOther {
		t.Fatalf("own bike: %d", w.Code)
	}

	ws := a.store.RideWaivers(year, a.cfg.Location)
	if len(ws) != 2 {
		t.Fatalf("ride waivers %+v", ws)
	}
	own, loaner := ws[0], ws[1]
	if loaner.Email != "sam@example.com" || loaner.Name != "Sam Newrider" || !loaner.Loaner() || loaner.MemberID != 0 {
		t.Errorf("loaner waiver %+v", loaner)
	}
	if own.Loaner() || own.Version == loaner.Version {
		t.Errorf("own bike signs another text: %+v", own)
	}
	_, loanerText, _ := a.store.SignedRideWaiver(loaner.ID)
	_, ownText, _ := a.store.SignedRideWaiver(own.ID)
	if !strings.Contains(string(loanerText), "PAXVELO LOANER BICYCLE AGREEMENT") || !strings.Contains(string(loanerText), "LEAGUE OF AMERICAN BYCYLISTS") {
		t.Error("a loaner signs the loaner agreement and the League waiver")
	}
	if strings.Contains(string(ownText), "LOANER") || !strings.Contains(string(ownText), "LEAGUE OF AMERICAN BYCYLISTS") {
		t.Error("their own bike signs the League waiver only")
	}

	// Signing twice the same day counts once.
	ct, b = form(rideWaiverForm(a, nil))
	do(t, h, nil, "POST", "/ride-waiver", ct, b)
	if n := len(a.store.RideWaivers(year, a.cfg.Location)); n != 2 {
		t.Errorf("the same signer, rider and bike the same day count once: %d", n)
	}

	// The admin's record.
	list := do(t, h, admin, "GET", "/admin/waivers?show=rides", "", nil).Body.String()
	if !strings.Contains(list, "/admin/ride-waivers/"+strconv.Itoa(loaner.ID)) || !strings.Contains(list, "First rides (2)") {
		t.Error("the Waivers page lists ride waivers under First rides")
	}
	copyPage := do(t, h, admin, "GET", "/admin/ride-waivers/"+strconv.Itoa(loaner.ID), "", nil).Body.String()
	for _, want := range []string{"sam newrider", "Club loaner bike", "ONE RIDE ONLY", "IN CONSIDERATION of being permitted"} {
		if !strings.Contains(copyPage, want) {
			t.Errorf("the signed copy should show %q", want)
		}
	}
	if csv := do(t, h, admin, "GET", "/admin/ride-waivers.csv?year="+strconv.Itoa(year), "", nil).Body.String(); !strings.Contains(csv, "Sam Newrider,No,,Club loaner bike,Sam Newrider,sam@example.com,,sam newrider,") {
		t.Errorf("ride waivers CSV: %s", csv)
	}
	if w := do(t, h, nil, "GET", "/admin/ride-waivers/"+strconv.Itoa(loaner.ID), "", nil); w.Code != http.StatusSeeOther {
		t.Errorf("signed ride waivers are for admins only: %d", w.Code)
	}

	ct, b = form(url.Values{})
	if w := do(t, h, admin, "POST", "/admin/ride-waivers/"+strconv.Itoa(own.ID)+"/delete", ct, b); w.Code != http.StatusSeeOther {
		t.Errorf("delete: %d", w.Code)
	}
	if n := len(a.store.RideWaivers(year, a.cfg.Location)); n != 1 {
		t.Errorf("deleted: %d left", n)
	}
}

// Every mistake sends the form back, with what was typed, and nothing is kept.
func TestRideWaiverRefused(t *testing.T) {
	a, h, _ := testApp(t)
	for name, v := range map[string]url.Values{
		"no token":          {"ft": {""}},
		"one name":          {"name": {"Sam"}, "signature": {"Sam"}},
		"bad email":         {"email": {"sam@"}},
		"no bike":           {"bike": {""}},
		"odd bike":          {"bike": {"tandem"}},
		"not ticked":        {"waiver": {""}},
		"someone else":      {"signature": {"Pat Rider"}},
		"blank drawing":     {"drawn": {"1"}, "signature_image": {testSignature(true)}},
		"minor, no age":     {"rider": {"Kid Newrider"}, "guardian": {"yes"}},
		"minor, adult age":  {"rider": {"Kid Newrider"}, "rider_age": {"18"}, "guardian": {"yes"}},
		"minor, not ticked": {"rider": {"Kid Newrider"}, "rider_age": {"12"}},
	} {
		ct, b := form(rideWaiverForm(a, v))
		w := do(t, h, nil, "POST", "/ride-waiver", ct, b)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `class="cal-msg cal-err" role="alert"`) {
			t.Errorf("%s: should be refused: %d", name, w.Code)
		}
	}
	if n := len(a.store.RideWaivers(a.cfg.ThisYear(), a.cfg.Location)); n != 0 {
		t.Errorf("refused forms keep nothing: %d", n)
	}
	ct, b := form(rideWaiverForm(a, url.Values{"website": {"http://spam"}}))
	if w := do(t, h, nil, "POST", "/ride-waiver", ct, b); w.Code != http.StatusOK || len(a.store.RideWaivers(a.cfg.ThisYear(), a.cfg.Location)) != 0 {
		t.Error("a bot that fills the hidden box sees thanks, and nothing is kept")
	}
	for i := range maxRideWaivers + 1 {
		ct, b := form(rideWaiverForm(a, url.Values{"email": {"r" + strconv.Itoa(i) + "@example.com"}}))
		w := do(t, h, nil, "POST", "/ride-waiver", ct, b)
		if i < maxRideWaivers && w.Code != http.StatusSeeOther || i == maxRideWaivers && w.Code != http.StatusBadRequest {
			t.Errorf("signing %d from one address: %d", i+1, w.Code)
		}
	}
}

// A parent or guardian signs for a rider under 18, with the release for
// parents and guardians added, and a drawn signature is kept.
func TestRideWaiverMinor(t *testing.T) {
	a, h, _ := testApp(t)
	ct, b := form(rideWaiverForm(a, url.Values{"rider": {"Kid Newrider"}, "rider_age": {"12"}, "guardian": {"yes"}, "bike": {"own"},
		"drawn": {"1"}, "signature_image": {testSignature(false)}}))
	if w := do(t, h, nil, "POST", "/ride-waiver", ct, b); w.Code != http.StatusSeeOther {
		t.Fatalf("minor: %d %s", w.Code, w.Body.String())
	}
	ws := a.store.RideWaivers(a.cfg.ThisYear(), a.cfg.Location)
	if len(ws) != 1 || !ws[0].Minor || ws[0].Rider != "Kid Newrider" || ws[0].RiderAge != 12 || ws[0].RiderName() != "Kid Newrider" || len(ws[0].Drawing) == 0 {
		t.Fatalf("minor waiver %+v", ws)
	}
	if _, text, _ := a.store.SignedRideWaiver(ws[0].ID); !strings.Contains(string(text), "For Parents and Guardians") {
		t.Error("a parent signs the release for parents and guardians too")
	}
}

// Ride waivers find their member by email: someone already on the list when
// they sign, or as soon as they join, and again if they're deleted and added
// back. The member's admin page lists them.
func TestRideWaiverLinksToMember(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	known, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual"}, year)
	ct, b := form(rideWaiverForm(a, url.Values{"name": {"Pat Rider"}, "email": {"PAT@example.com"}, "signature": {"Pat Rider"}}))
	do(t, h, nil, "POST", "/ride-waiver", ct, b)
	ct, b = form(rideWaiverForm(a, nil))
	do(t, h, nil, "POST", "/ride-waiver", ct, b)

	byEmail := map[string]RideWaiver{}
	for _, w := range a.store.RideWaivers(year, a.cfg.Location) {
		byEmail[w.Email] = w
	}
	if byEmail["pat@example.com"].MemberID != known.ID || byEmail["sam@example.com"].MemberID != 0 {
		t.Fatalf("linked at signing: %+v", byEmail)
	}

	joined, _, _ := a.store.Join(memberDetails{Name: "Samuel Newrider", Email: "sam@example.com", Plan: "individual"}, year)
	if got := a.store.RideWaiversOf(joined.ID); len(got) != 1 || got[0].Email != "sam@example.com" {
		t.Fatalf("joining ties their ride waiver to them: %+v", got)
	}
	if page := do(t, h, admin, "GET", "/admin/members/"+strconv.Itoa(joined.ID), "", nil).Body.String(); !strings.Contains(page, "Ride waivers") || !strings.Contains(page, "Club loaner bike") {
		t.Error("the member's page lists their ride waivers")
	}

	a.store.DeleteMembers([]int{joined.ID})
	if len(a.store.RideWaiversOf(joined.ID)) != 0 || len(a.store.RideWaivers(year, a.cfg.Location)) != 2 {
		t.Error("deleting a member keeps their ride waivers, untied")
	}
	back, _, _ := a.store.Join(memberDetails{Name: "Sam Newrider", Email: "sam@example.com", Plan: "individual"}, year)
	if len(a.store.RideWaiversOf(back.ID)) != 1 {
		t.Error("added back, they get their ride waiver again")
	}
}

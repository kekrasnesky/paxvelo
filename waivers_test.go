package main

import (
	"bytes"
	"encoding/base64"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// A member signs the waiver once a year on their own page, with the name the
// club has for them. Each signing keeps the text that was signed, and stays
// on record even after the member is deleted.
func TestWaiverEachYear(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	m, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual"}, year)
	a.store.SetPaid(m.ID, year, true)

	if page := do(t, h, nil, "GET", "/membership?t="+m.Token, "", nil).Body.String(); !strings.Contains(page, "waiver isn’t signed yet") || !strings.Contains(page, "LEAGUE OF AMERICAN BYCYLISTS") {
		t.Error("the membership page should offer this year's waiver to sign")
	}
	if !needsWaivers(a) {
		t.Errorf("a paid member without a waiver needs attention: %+v", a.attention())
	}

	for _, bad := range []url.Values{
		{"t": {m.Token}, "signature": {"Pat Rider"}},
		{"t": {m.Token}, "waiver": {"yes"}, "signature": {"Someone Else"}},
	} {
		ct, body := form(bad)
		if w := do(t, h, nil, "POST", "/membership/waiver", ct, body); w.Code != http.StatusBadRequest {
			t.Errorf("%v should be refused: %d", bad, w.Code)
		}
	}
	ct, body := form(url.Values{"t": {m.Token}, "waiver": {"yes"}, "signature": {" pat  rider "}})
	w := do(t, h, nil, "POST", "/membership/waiver", ct, body)
	if to := w.Header().Get("Location"); w.Code != http.StatusSeeOther || !strings.HasSuffix(to, "&signed=1#waiver") {
		t.Fatalf("signing should go back to their page: %d %s", w.Code, to)
	}
	if page := do(t, h, nil, "GET", strings.TrimSuffix(w.Header().Get("Location"), "#waiver"), "", nil).Body.String(); !strings.Contains(page, "waiver: signed ✓") || !strings.Contains(page, "Thanks!") {
		t.Error("their page should say the waiver is signed")
	}
	ws := a.store.Waivers(year)
	if len(ws) != 1 || ws[0].Signature != "pat rider" || ws[0].Name != "Pat Rider" || ws[0].Where != "Membership page" || ws[0].Version != a.site.waiver.Version {
		t.Fatalf("waivers %+v", ws)
	}
	ct, body = form(url.Values{"t": {m.Token}, "waiver": {"yes"}, "signature": {"Pat Rider"}})
	do(t, h, nil, "POST", "/membership/waiver", ct, body)
	if len(a.store.Waivers(year)) != 1 || needsWaivers(a) {
		t.Error("a member signs once a year")
	}

	// Next year starts unsigned.
	if _, ok := a.store.WaiverFor(m.ID, year+1); ok {
		t.Error("last year's waiver doesn't cover next year")
	}

	// The admin's record: the list, the signed copy with its text, and the spreadsheet.
	if page := do(t, h, admin, "GET", "/admin/waivers", "", nil).Body.String(); !strings.Contains(page, "1 signed for "+strconv.Itoa(year)) || !strings.Contains(page, "/admin/waivers/"+strconv.Itoa(ws[0].ID)) {
		t.Error("the Waivers page should list the signed waiver")
	}
	copyPage := do(t, h, admin, "GET", "/admin/waivers/"+strconv.Itoa(ws[0].ID), "", nil).Body.String()
	for _, want := range []string{"pat rider", "Membership page", "IN CONSIDERATION of being permitted"} {
		if !strings.Contains(copyPage, want) {
			t.Errorf("the signed copy should show %q", want)
		}
	}
	if csv := do(t, h, admin, "GET", "/admin/waivers.csv?year="+strconv.Itoa(year), "", nil).Body.String(); !strings.Contains(csv, ",Pat Rider,pat@example.com,No,,Pat Rider,pat@example.com,,pat rider,") {
		t.Errorf("waivers CSV: %s", csv)
	}
	if w := do(t, h, nil, "GET", "/admin/waivers/"+strconv.Itoa(ws[0].ID), "", nil); w.Code != http.StatusSeeOther {
		t.Errorf("signed waivers are for admins only: %d", w.Code)
	}

	a.store.DeleteMember(m.ID)
	if _, text, ok := a.store.SignedWaiver(ws[0].ID); !ok || !strings.Contains(string(text), "RELEASEES") {
		t.Error("a deleted member's waiver stays on record, with its text")
	}
}

// A parent or guardian signs for each rider under 18 on their own page, once
// a year per rider, with the release for parents and guardians added to the
// text. Their own waiver is separate.
func TestMinorWaivers(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	m, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "family", Phone: "301-555-0123"}, year)
	if page := do(t, h, nil, "GET", "/membership?t="+m.Token, "", nil).Body.String(); !strings.Contains(page, "Sign for a rider under 18") || !strings.Contains(page, "For Parents and Guardians") || !strings.Contains(page, `value="301-555-0123"`) {
		t.Error("the membership page should offer the waiver for a rider under 18")
	}

	good := url.Values{"t": {m.Token}, "rider_first": {"Sam"}, "rider_last": {"Rider"}, "rider_age": {"12"},
		"guardian_phone": {"301-555-0123"}, "guardian": {"yes"}, "signature": {"Pat Rider"}}
	for field, bad := range map[string]string{"rider_last": "", "rider_age": "18", "guardian": "", "signature": "Sam Rider", "guardian_phone": "555"} {
		v := url.Values{}
		for k, vs := range good {
			v[k] = vs
		}
		v.Set(field, bad)
		ct, body := form(v)
		if w := do(t, h, nil, "POST", "/membership/waiver/minor", ct, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `class="minor-sign" open`) {
			t.Errorf("%s %q should be refused: %d", field, bad, w.Code)
		}
	}
	ct, body := form(good)
	w := do(t, h, nil, "POST", "/membership/waiver/minor", ct, body)
	if w.Code != http.StatusSeeOther || !strings.HasSuffix(w.Header().Get("Location"), "&minor=1#minors") {
		t.Fatalf("signing for a rider should go back to their page: %d", w.Code)
	}
	if page := do(t, h, nil, "GET", strings.TrimSuffix(w.Header().Get("Location"), "#minors"), "", nil).Body.String(); !strings.Contains(page, "<strong>Sam Rider</strong>, age 12") || !strings.Contains(page, "waiver isn’t signed yet") {
		t.Error("the page should list the rider, and still ask the member to sign their own")
	}
	ct, body = form(good)
	if w := do(t, h, nil, "POST", "/membership/waiver/minor", ct, body); w.Code != http.StatusBadRequest {
		t.Errorf("a rider is signed for once a year: %d", w.Code)
	}

	ws := a.store.Waivers(year)
	if len(ws) != 1 || !ws[0].Minor || ws[0].Rider != "Sam Rider" || ws[0].RiderAge != 12 || ws[0].Name != "Pat Rider" || ws[0].GuardianPhone != "301-555-0123" || ws[0].Version == a.site.waiver.Version {
		t.Fatalf("waivers %+v", ws)
	}
	if _, ok := a.store.WaiverFor(m.ID, year); ok {
		t.Error("signing for a child isn't the member's own waiver")
	}
	copyPage := do(t, h, admin, "GET", "/admin/waivers/"+strconv.Itoa(ws[0].ID), "", nil).Body.String()
	for _, want := range []string{"Sam Rider, age 12 (under 18)", "Parent or guardian", "IN CONSIDERATION of being permitted", "my minor child's involvement"} {
		if !strings.Contains(copyPage, want) {
			t.Errorf("the signed copy should show %q", want)
		}
	}
	if page := do(t, h, admin, "GET", "/admin/waivers?show=unsigned", "", nil).Body.String(); !strings.Contains(page, "pat@example.com") {
		t.Error("the member still hasn't signed their own waiver")
	}
}

// A parent or guardian can sign for riders under 18 on the join form too:
// one signature signs their own waiver and one for each rider listed.
func TestJoinMinors(t *testing.T) {
	a, h, _ := testApp(t)
	year := a.cfg.ThisYear()
	if page := do(t, h, nil, "GET", "/join", "", nil).Body.String(); !strings.Contains(page, "Signing for riders under 18?") || strings.Count(page, `name="rider_name"`) != maxJoinRiders {
		t.Error("the join form should offer to sign for riders under 18")
	}
	join := func(riders map[string][]string) *httptest.ResponseRecorder {
		v := url.Values{"plan": {"family"}, "first": {"Pat"}, "last": {"Rider"}, "email": {"pat@example.com"},
			"phone": {"301-555-0123"}, "emergency_name": {"Lee Rider"}, "emergency_phone": {"240-555-0199"}, "level": {"some"}}
		for k, vs := range riders {
			v[k] = vs
		}
		ct, body := joinForm(a, v)
		return do(t, h, nil, "POST", "/join", ct, body)
	}
	for problem, riders := range map[string]map[string][]string{
		"rider-name": {"rider_name": {"Sam"}, "rider_age": {"12"}, "guardian": {"yes"}},
		"rider-age":  {"rider_name": {"Sam Rider"}, "rider_age": {"18"}, "guardian": {"yes"}},
		"guardian":   {"rider_name": {"Sam Rider"}, "rider_age": {"12"}},
	} {
		w := join(riders)
		if body := w.Body.String(); w.Code != http.StatusBadRequest || !strings.Contains(body, template.HTMLEscapeString(joinProblems[problem])) || !strings.Contains(body, `class="minor-sign join-minors" open`) {
			t.Errorf("%s: should be refused with the section open: %d", problem, w.Code)
		}
	}
	if len(a.store.Waivers(year)) != 0 {
		t.Fatal("a refused form signs nothing")
	}

	w := join(map[string][]string{"rider_name": {"Sam Rider", "", "Jo Rider"}, "rider_age": {"12", "", "9"}, "guardian": {"yes"}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "You signed the waiver for Sam Rider and Jo Rider too.") {
		t.Fatalf("joining with riders: %d", w.Code)
	}
	ws := a.store.Waivers(year)
	if len(ws) != 3 {
		t.Fatalf("waivers %+v", ws)
	}
	m, _ := a.store.MemberByEmail("pat@example.com")
	if _, ok := a.store.WaiverFor(m.ID, year); !ok {
		t.Error("the parent's own waiver should be signed")
	}
	minors := a.store.MinorWaivers(m.ID, year)
	if len(minors) != 2 || minors[0].Rider != "Sam Rider" || minors[0].RiderAge != 12 || minors[1].Rider != "Jo Rider" ||
		minors[0].GuardianPhone != "301-555-0123" || minors[0].Where != "Join form" || minors[0].Version != a.site.waiver.forMinor().Version {
		t.Errorf("minor waivers %+v", minors)
	}
}

// needsWaivers reports whether Needs attention lists members without a waiver.
func needsWaivers(a *App) bool {
	for _, it := range a.attention() {
		if strings.Contains(it.Text, "insurance waiver") {
			return true
		}
	}
	return false
}

// testSignature is a drawn signature as the page sends it: a 600×300 PNG
// data URL with a line drawn across it, or nothing drawn when blank.
func testSignature(blank bool) string {
	img := image.NewNRGBA(image.Rect(0, 0, 600, 300))
	if !blank {
		for x := 100; x < 500; x++ {
			for y := 140; y < 144; y++ {
				img.Set(x, y, color.NRGBA{0x1d, 0x1b, 0x1a, 0xff})
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// With the page's script, a waiver needs a drawn signature as well as the
// typed name. It's kept with the waiver, and only admins can see it.
func TestDrawnSignature(t *testing.T) {
	for in, want := range map[string]bool{testSignature(false): true, testSignature(true): false, "data:image/png;base64,bm90IGEgcG5n": false, "": false} {
		if _, ok := signatureImage(in); ok != want {
			t.Errorf("signatureImage(%.40q) = %v, want %v", in, ok, want)
		}
	}

	a, h, admin := testApp(t)
	full := url.Values{
		"plan": {"individual"}, "first": {"Pat"}, "last": {"Rider"}, "email": {"pat@example.com"}, "phone": {"301-555-0123"},
		"level": {"some"}, "emergency_name": {"Sam Rider"}, "emergency_phone": {"240-555-0199"}, "drawn": {"1"},
	}
	full.Set("signature_image", testSignature(true))
	ct, body := joinForm(a, full)
	if w := do(t, h, nil, "POST", "/join", ct, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Sign in the box") {
		t.Fatalf("a blank signature box should be refused: %d", w.Code)
	}
	full.Set("signature_image", testSignature(false))
	ct, body = joinForm(a, full)
	if w := do(t, h, nil, "POST", "/join", ct, body); w.Code != http.StatusOK {
		t.Fatalf("join with a drawn signature: %d %s", w.Code, w.Body)
	}
	ws := a.store.Waivers(a.cfg.ThisYear())
	if len(ws) != 1 || len(ws[0].Drawing) == 0 {
		t.Fatalf("the drawn signature should be kept: %+v", ws)
	}
	path := "/admin/waivers/" + strconv.Itoa(ws[0].ID)
	if w := do(t, h, admin, "GET", path+"/signature.png", "", nil); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), ws[0].Drawing) {
		t.Errorf("admins should see the signature: %d", w.Code)
	}
	if w := do(t, h, nil, "GET", path+"/signature.png", "", nil); w.Code == http.StatusOK {
		t.Error("only admins can see a signature")
	}
	if page := do(t, h, admin, "GET", path, "", nil).Body.String(); !strings.Contains(page, `src="`+path+`/signature.png"`) || !strings.Contains(page, "Participant’s signature") {
		t.Error("the signed copy should show the drawn signature")
	}

	// A guardian signing for a child needs one too.
	m := a.store.Members()[0]
	minor := url.Values{"t": {m.Token}, "rider_first": {"Sam"}, "rider_last": {"Rider"}, "rider_age": {"9"},
		"guardian_phone": {"301-555-0123"}, "guardian": {"yes"}, "signature": {"Pat Rider"}, "drawn": {"1"}}
	mct, mbody := form(minor)
	if w := do(t, h, nil, "POST", "/membership/waiver/minor", mct, mbody); w.Code != http.StatusBadRequest {
		t.Errorf("signing for a child without drawing should be refused: %d", w.Code)
	}
	minor.Set("signature_image", testSignature(false))
	mct, mbody = form(minor)
	if w := do(t, h, nil, "POST", "/membership/waiver/minor", mct, mbody); w.Code != http.StatusSeeOther {
		t.Errorf("signing for a child with a drawing: %d", w.Code)
	}
}

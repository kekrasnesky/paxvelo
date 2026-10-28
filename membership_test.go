package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// joinForm is a filled-in Join form, as a rider sends it (with a form token
// from a page loaded a few seconds ago, so it passes the spam check).
func joinForm(a *App, vals url.Values) (string, *strings.Reader) {
	ts := strconv.FormatInt(time.Now().Add(-10*time.Second).Unix(), 10)
	vals.Set("ft", ts+"."+sign(a.formKey(), ts)[:24])
	return "application/x-www-form-urlencoded", strings.NewReader(vals.Encode())
}

func TestCleanPhone(t *testing.T) {
	for in, want := range map[string]string{
		"3015550123":       "301-555-0123",
		"(301) 555-0123":   "301-555-0123",
		"+1 301.555.0123":  "301-555-0123",
		"+44 20 7946 0958": "+44 20 7946 0958",
		"555-0123":         "",
		"call me":          "",
		"":                 "",
	} {
		if got := cleanPhone(in); got != want {
			t.Errorf("cleanPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

// Joining takes a phone number and an emergency contact along with the plan,
// then goes on to PayPal as before. Joining again with the same email updates
// the contact details.
func TestJoinWithEmergencyContact(t *testing.T) {
	a, h, _ := testApp(t)
	a.cfg.PayPalFamily = "https://paypal.example/family"
	full := url.Values{
		"plan": {"family"}, "first": {"Pat"}, "last": {" Rider "}, "email": {"Pat@Example.com"}, "phone": {"(301) 555-0123"},
		"level": {"some"}, "emergency_name": {"Sam Rider"}, "emergency_phone": {"240 555 0199"},
	}

	for field, bad := range map[string]string{"last": "", "phone": "555-0123", "emergency_phone": "+44 20 7946 0958"} {
		form := url.Values{}
		for k, v := range full {
			form[k] = v
		}
		form.Set(field, bad)
		ct, body := joinForm(a, form)
		if w := do(t, h, nil, "POST", "/join", ct, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s %q should be refused: %d", field, bad, w.Code)
		}
	}

	missing := url.Values{}
	for k, v := range full {
		missing[k] = v
	}
	missing.Del("emergency_phone")
	ct, body := joinForm(a, missing)
	w := do(t, h, nil, "POST", "/join", ct, body)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "emergency contact’s 10-digit phone") {
		t.Fatalf("a join without an emergency phone should be refused: %d", w.Code)
	}

	ct, body = joinForm(a, full)
	r := httptest.NewRequest("POST", "/join", body)
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["ok"] != true || got["pay_url"] != "https://paypal.example/family" || !strings.Contains(got["pay_label"].(string), "$45") {
		t.Fatalf("join answer: %s", rec.Body)
	}
	ms := a.store.Members()
	if len(ms) != 1 {
		t.Fatalf("%d members", len(ms))
	}
	m := ms[0]
	if m.Name != "Pat Rider" || m.Email != "pat@example.com" || m.Plan != "family" || m.Phone != "301-555-0123" || m.EmergencyName != "Sam Rider" || m.EmergencyPhone != "240-555-0199" {
		t.Errorf("saved %+v", m)
	}

	// Anyone can type anyone's email into the form, so joining again with
	// someone else's details changes nothing already on file.
	full.Set("emergency_name", "Alex Rider")
	full.Set("phone", "202-555-0100")
	full.Set("plan", "individual")
	ct, body = joinForm(a, full)
	if w := do(t, h, nil, "POST", "/join", ct, body); !strings.Contains(w.Body.String(), "already on the list") {
		t.Errorf("joining again should say they're already on the list")
	}
	if m := a.store.Members()[0]; m.EmergencyName != "Sam Rider" || m.Phone != "301-555-0123" || m.Plan != "family" || len(a.store.Members()) != 1 {
		t.Errorf("joining again must not overwrite what's on file, got %+v", m)
	}
}

// A member from before the phone fields existed can fill them in by joining
// again, but only the blanks. The admin's Add a member can still change them.
func TestJoinFillsBlanksOnly(t *testing.T) {
	a, h, _ := testApp(t)
	a.store.Join(memberDetails{Name: "Old Timer", Email: "old@example.com", Plan: "individual"}, 2026)
	form := url.Values{"plan": {"family"}, "name": {"Someone Else"}, "email": {"old@example.com"}, "phone": {"301-555-0123"},
		"level": {"some"}, "emergency_name": {"Sam Rider"}, "emergency_phone": {"240-555-0199"}}
	ct, body := joinForm(a, form)
	do(t, h, nil, "POST", "/join", ct, body)
	m := a.store.Members()[0]
	if m.Name != "Old Timer" || m.Phone != "301-555-0123" || m.EmergencyName != "Sam Rider" || m.Plan != "individual" {
		t.Errorf("only the blank details should be filled in: %+v", m)
	}
	if got, _, _ := a.store.Join(memberDetails{Name: "Old Timer", Email: "old@example.com", Plan: "family", Phone: "202-555-0100"}, 2026); got.Phone != "202-555-0100" || got.Plan != "family" {
		t.Errorf("an admin should be able to change details: %+v", got)
	}
}

// Someone who unsubscribed isn't signed up again by anyone typing their email
// into the public form, and gets no welcome email. An admin adding them back can.
func TestJoinDoesNotResubscribe(t *testing.T) {
	a, h, _ := testApp(t)
	m, _, _ := a.store.Join(memberDetails{Name: "Kim Rider", Email: "kim@example.com", Plan: "individual"}, 2026)
	a.store.Unsubscribe(m.Token)
	form := url.Values{"plan": {"individual"}, "name": {"Kim Rider"}, "email": {"kim@example.com"}, "phone": {"301-555-0123"},
		"level": {"some"}, "emergency_name": {"Sam Rider"}, "emergency_phone": {"240-555-0199"}}
	ct, body := joinForm(a, form)
	w := do(t, h, nil, "POST", "/join", ct, body)
	if !strings.Contains(w.Body.String(), "unsubscribed from club emails earlier") {
		t.Errorf("the form should explain: %s", w.Body)
	}
	if got := a.store.Members()[0]; got.Active() || got.Phone != "" {
		t.Errorf("the public form must not re-subscribe or change an unsubscribed member: %+v", got)
	}
	if _, res, _ := a.store.Join(memberDetails{Name: "Kim Rider", Email: "kim@example.com", Plan: "individual"}, 2026); res != Rejoined || !a.store.Members()[0].Active() {
		t.Errorf("an admin should be able to add them back: %v", res)
	}
}

// The Members page shows phone numbers and emergency contacts beside the plan
// and dues.
func TestMembersPage(t *testing.T) {
	a, h, c := testApp(t)
	a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual", Phone: "301-555-0123", EmergencyName: "Sam Rider", EmergencyPhone: "240-555-0199"}, 2026)
	page := do(t, h, c, "GET", "/admin", "", nil).Body.String()
	for _, want := range []string{"<th>Phone</th>", "<th>Emergency contact</th>", "<th>Emergency phone</th>", "<th>Plan</th>", "Mark paid", "301-555-0123", "Sam Rider", "240-555-0199"} {
		if !strings.Contains(page, want) {
			t.Errorf("members page missing %q", want)
		}
	}
	if page := do(t, h, c, "GET", "/admin?q=555-0123", "", nil).Body.String(); !strings.Contains(page, "Pat Rider") {
		t.Error("searching by phone number should find Pat")
	}
	csv := do(t, h, c, "GET", "/admin/members.csv", "", nil).Body.String()
	if !strings.HasPrefix(csv, "Name,Email,Phone,Emergency contact,Emergency phone,Plan,") || !strings.Contains(csv, "Sam Rider") {
		t.Errorf("export: %s", csv)
	}
}

// Only the president can remove admins and delete members. Until there is
// one, any admin can name the first; after that only the president can hand
// it on. The Admins tab shows each admin's last login.
func TestPresidentOnly(t *testing.T) {
	a, h, _ := testApp(t)
	pat, _ := a.store.AddAdmin("Pat", "pat@example.com", "")
	sam, _ := a.store.AddAdmin("Sam", "sam@example.com", "")
	m, _, _ := a.store.Join(memberDetails{Name: "Kim", Email: "kim@example.com", Plan: "individual"}, 2026)
	as := func(u AdminUser) *http.Cookie {
		u, _ = a.store.Admin(u.ID)
		rec := httptest.NewRecorder()
		a.setSession(rec, accountSession(u), 3600)
		return rec.Result().Cookies()[0]
	}

	// Sam isn't president: no Delete or Remove buttons, and the posts are refused.
	if page := do(t, h, as(sam), "GET", "/admin", "", nil).Body.String(); strings.Contains(page, ">Delete</button>") {
		t.Error("a non-president shouldn't see Delete")
	}
	do(t, h, as(sam), "POST", "/admin/members/"+itoa(m.ID)+"/delete", "", nil)
	do(t, h, as(sam), "POST", "/admin/admins/"+itoa(pat.ID)+"/remove", "", nil)
	if len(a.store.Members()) != 1 || len(a.store.Admins()) != 2 {
		t.Fatal("a non-president deleted someone")
	}

	// With no president yet, Sam can name Pat. Then only Pat can hand it on.
	do(t, h, as(sam), "POST", "/admin/admins/"+itoa(pat.ID)+"/president", "", nil)
	if u, _ := a.store.Admin(pat.ID); !u.President {
		t.Fatal("any admin should be able to name the first president")
	}
	do(t, h, as(sam), "POST", "/admin/admins/"+itoa(sam.ID)+"/president", "", nil)
	if u, _ := a.store.Admin(sam.ID); u.President {
		t.Fatal("only the president can hand the role on")
	}

	// Pat, the president, can delete members and remove admins.
	if page := do(t, h, as(pat), "GET", "/admin", "", nil).Body.String(); !strings.Contains(page, ">Delete</button>") {
		t.Error("the president should see Delete")
	}
	do(t, h, as(pat), "POST", "/admin/members/"+itoa(m.ID)+"/delete", "", nil)
	if len(a.store.Members()) != 0 {
		t.Error("the president couldn't delete a member")
	}
	a.store.RecordAdminLogin(sam.ID)
	page := do(t, h, as(pat), "GET", "/admin/admins", "", nil).Body.String()
	for _, want := range []string{"<th>Last login</th>", "President</span>", "Never"} {
		if !strings.Contains(page, want) {
			t.Errorf("admins page missing %q", want)
		}
	}
	do(t, h, as(pat), "POST", "/admin/admins/"+itoa(sam.ID)+"/remove", "", nil)
	if len(a.store.Admins()) != 1 {
		t.Error("the president couldn't remove an admin")
	}
}

// The full backup holds every member's contact details and the admins'
// password hashes, so only the president can download it, and it leaves out the
// signing key and any unused password links.
func TestBackupPresidentOnly(t *testing.T) {
	a, h, _ := testApp(t)
	pat, _ := a.store.AddAdmin("Pat", "pat@example.com", "")
	sam, _ := a.store.AddAdmin("Sam", "sam@example.com", "")
	a.store.SetPresident(pat.ID)
	a.store.NewResetLink(sam.ID, time.Hour)
	as := func(u AdminUser) *http.Cookie {
		rec := httptest.NewRecorder()
		a.setSession(rec, accountSession(u), 3600)
		return rec.Result().Cookies()[0]
	}

	w := do(t, h, as(sam), "GET", "/admin/backup", "", nil)
	if w.Code != http.StatusSeeOther || strings.Contains(w.Body.String(), "pat@example.com") {
		t.Errorf("a non-president got the backup: %d", w.Code)
	}
	if page := do(t, h, as(sam), "GET", "/admin", "", nil).Body.String(); strings.Contains(page, "Download a full backup") {
		t.Error("a non-president shouldn't see the backup link")
	}
	w = do(t, h, as(pat), "GET", "/admin/backup", "", nil)
	got := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(got, "sam@example.com") {
		t.Fatalf("the president couldn't download the backup: %d", w.Code)
	}
	if strings.Contains(got, a.store.Secret()) || strings.Contains(got, "reset_hash") {
		t.Error("the backup should leave out the signing key and password links")
	}
}

// The join form asks how experienced someone is (required) and which rides
// interest them (optional), saves both, and the welcome email's {ride_tips}
// turns them into advice. Without answers, {ride_tips} leaves no gap.
func TestJoinRidingAnswers(t *testing.T) {
	a, h, _ := testApp(t)
	form := url.Values{"plan": {"individual"}, "first": {"Kim"}, "last": {"Rider"}, "email": {"kim@example.com"},
		"phone": {"301-555-0123"}, "emergency_name": {"Sam Rider"}, "emergency_phone": {"240-555-0199"},
		"rides": {"thursday", "made-up", "tnr-b"}}
	ct, body := joinForm(a, form)
	if w := do(t, h, nil, "POST", "/join", ct, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "sounds most like you") {
		t.Fatalf("the experience question is required: %d", w.Code)
	}
	form.Set("level", "new")
	ct, body = joinForm(a, form)
	do(t, h, nil, "POST", "/join", ct, body)
	m := a.store.Members()[0]
	if m.Level != "new" || strings.Join(m.Rides, ",") != "tnr-b,thursday" {
		t.Fatalf("saved level %q rides %v", m.Level, m.Rides)
	}
	if m.LevelName() != "New" || m.RideNames() != "Tue B, Thu" {
		t.Errorf("admin labels: %q %q", m.LevelName(), m.RideNames())
	}
	tips := a.mailer.personalize("Hi\n\n{ride_tips}\n\nBye", m)
	for _, want := range []string{"Old Line Cycles shop ride", "**Tuesday Night Ride, B group:**", "**Thursday Night Ride:**", "/rides"} {
		if !strings.Contains(tips, want) {
			t.Errorf("tips missing %q:\n%s", want, tips)
		}
	}
	if got := a.mailer.personalize("Hi\n\n{ride_tips}\n\nBye", Member{Name: "Lee"}); got != "Hi\n\nBye" {
		t.Errorf("no answers should leave no gap: %q", got)
	}
}

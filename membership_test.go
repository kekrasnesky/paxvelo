package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// joinForm is a filled-in Join form, as a rider sends it (with a form token
// from a page loaded a few seconds ago, so it passes the spam check). The club
// terms and waiver boxes are ticked, and the waiver signed with the name typed,
// unless vals says otherwise.
func joinForm(a *App, vals url.Values) (string, *strings.Reader) {
	for k, v := range map[string]string{"terms": "yes", "waiver": "yes", "signature": vals.Get("first") + " " + vals.Get("last") + vals.Get("name")} {
		if !vals.Has(k) {
			vals.Set(k, v)
		}
	}
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
	for _, c := range []struct{ field, value, want string }{
		{"terms", "", "agree to the club terms"},
		{"waiver", "", "agree to the insurance waiver"},
		{"signature", "", "To sign the waiver"},
		{"signature", "Someone Else", "To sign the waiver"},
	} {
		form := url.Values{c.field: {c.value}}
		for k, v := range full {
			form[k] = v
		}
		ct, body = joinForm(a, form)
		if w := do(t, h, nil, "POST", "/join", ct, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) || len(a.store.Members()) != 0 {
			t.Fatalf("a join with %s %q should be refused: %d", c.field, c.value, w.Code)
		}
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
	if m.TermsAgreed == nil || time.Since(*m.TermsAgreed) > time.Minute {
		t.Errorf("when they agreed to the club terms should be saved, got %v", m.TermsAgreed)
	}
	if ws := a.store.Waivers(a.cfg.ThisYear()); len(ws) != 1 || ws[0].MemberID != m.ID || ws[0].Signature != "Pat Rider" || ws[0].Where != "Join form" || time.Since(ws[0].Signed) > time.Minute {
		t.Errorf("this year's waiver should be saved, got %+v", ws)
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

// The Members page shows phone numbers beside the plan and dues; a member's
// own page has their emergency contact.
func TestMembersPage(t *testing.T) {
	a, h, c := testApp(t)
	a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual", Phone: "301-555-0123", EmergencyName: "Sam Rider", EmergencyPhone: "240-555-0199"}, 2026)
	page := do(t, h, c, "GET", "/admin/members", "", nil).Body.String()
	for _, want := range []string{"<th>Phone</th>", `data-sort="plan">Plan</button>`, "Mark paid", "301-555-0123", `href="/admin/members/1"`} {
		if !strings.Contains(page, want) {
			t.Errorf("members page missing %q", want)
		}
	}
	mine := do(t, h, c, "GET", "/admin/members/1", "", nil).Body.String()
	for _, want := range []string{`value="Sam Rider"`, `value="240-555-0199"`, "Not paid for"} {
		if !strings.Contains(mine, want) {
			t.Errorf("member page missing %q", want)
		}
	}
	if page := do(t, h, c, "GET", "/admin/members?q=555-0123", "", nil).Body.String(); !strings.Contains(page, "Pat Rider") {
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
	if page := do(t, h, as(sam), "GET", "/admin/members/"+itoa(m.ID), "", nil).Body.String(); strings.Contains(page, "Delete this member</button>") {
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
	if page := do(t, h, as(pat), "GET", "/admin/members/"+itoa(m.ID), "", nil).Body.String(); !strings.Contains(page, "Delete this member</button>") {
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
	if page := do(t, h, as(sam), "GET", "/admin/members", "", nil).Body.String(); strings.Contains(page, "Download a full backup") {
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

// The join form asks one question, how experienced someone is (required), and
// the welcome email's {ride_tips} gives exactly the advice for that answer.
// Without an answer, {ride_tips} leaves no gap.
func TestJoinRidingAnswers(t *testing.T) {
	a, h, _ := testApp(t)
	form := url.Values{"plan": {"individual"}, "first": {"Kim"}, "last": {"Rider"}, "email": {"kim@example.com"},
		"phone": {"301-555-0123"}, "emergency_name": {"Sam Rider"}, "emergency_phone": {"240-555-0199"}}
	ct, body := joinForm(a, form)
	if w := do(t, h, nil, "POST", "/join", ct, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "sounds most like you") {
		t.Fatalf("the experience question is required: %d", w.Code)
	}
	if page := do(t, h, nil, "GET", "/join", "", nil).Body.String(); strings.Contains(page, `name="rides"`) {
		t.Error("the join form shouldn't ask which rides any more")
	}
	form.Set("level", "new")
	ct, body = joinForm(a, form)
	do(t, h, nil, "POST", "/join", ct, body)
	m := a.store.Members()[0]
	if m.Level != "new" || m.LevelName() != "New" {
		t.Fatalf("saved level %q (%q)", m.Level, m.LevelName())
	}
	tips := a.mailer.personalize("Hi\n\n{ride_tips}\n\nBye", m)
	if want := "Hi\n\n" + levelAdvice["new"] + "\n\nTimes, start spots, and this week’s routes are on Strava and at " + a.cfg.SiteURL + "/rides\n\nBye"; tips != want {
		t.Errorf("tips:\n%s\nwant:\n%s", tips, want)
	}
	if got := a.mailer.personalize("Hi\n\n{ride_tips}\n\nBye", Member{Name: "Lee"}); got != "Hi\n\nBye" {
		t.Errorf("no answer should leave no gap: %q", got)
	}
}

// Admins can rewrite the advice for each answer on the Email tab. Advice left
// as the built-in text, or cleared, stays the built-in advice.
func TestWelcomeTipsEditable(t *testing.T) {
	a, h, admin := testApp(t)
	page := do(t, h, admin, "GET", "/admin/email", "", nil).Body.String()
	for _, c := range riderLevels {
		if !strings.Contains(page, `name="tips_`+c.Value+`"`) {
			t.Errorf("the Email tab should have a box for %q", c.Value)
		}
	}
	ct, body := form(url.Values{"enabled": {"on"}, "subject": {"Welcome"}, "body": {"Hi\n\n{ride_tips}"},
		"tips_new": {"Come to the shop ride.\r\n\r\nSee you there."}, "tips_some": {levelAdvice["some"]}, "tips_racer": {"  "}})
	do(t, h, admin, "POST", "/admin/welcome", ct, body)
	w := a.store.Welcome()
	if len(w.Tips) != 1 || w.Tip("new") != "Come to the shop ride.\n\nSee you there." {
		t.Fatalf("saved tips %q", w.Tips)
	}
	if w.Tip("some") != levelAdvice["some"] || w.Tip("racer") != levelAdvice["racer"] {
		t.Error("unchanged or cleared advice should be the built-in advice")
	}
	if got := a.mailer.personalize("{ride_tips}", Member{Name: "Kim", Level: "new"}); !strings.HasPrefix(got, "Come to the shop ride.\n\nSee you there.\n\nTimes") {
		t.Errorf("the welcome email should use the admin's advice: %q", got)
	}
}

// Joining on the website tells the admins and sends nothing to the new
// member. Marking them paid sends their welcome email, once: undoing and
// marking again, or marking a member an admin added, sends nothing.
func TestWelcomeAfterPaid(t *testing.T) {
	a, h, admin := testApp(t)
	a.store.AddAdmin("Barry Treasurer", "barry@example.com", "test")
	type sent struct{ to, subject, body string }
	mail := make(chan sent, 10)
	a.mailer.onSend = func(to, subject, body string) { mail <- sent{to, subject, body} }
	next := func() (sent, bool) {
		select {
		case s := <-mail:
			return s, true
		case <-time.After(2 * time.Second):
			return sent{}, false
		}
	}
	none := func(when string) {
		select {
		case s := <-mail:
			t.Errorf("%s: unexpected email to %s: %q", when, s.to, s.subject)
		case <-time.After(200 * time.Millisecond):
		}
	}

	ct, body := joinForm(a, url.Values{"plan": {"family"}, "first": {"Kim"}, "last": {"Rider"}, "email": {"kim@example.com"},
		"phone": {"301-555-0123"}, "emergency_name": {"Sam Rider"}, "emergency_phone": {"240-555-0199"}, "level": {"some"}})
	do(t, h, nil, "POST", "/join", ct, body)
	s, ok := next()
	if !ok || s.to != "barry@example.com" || s.subject != "New member: Kim Rider (Family)" {
		t.Fatalf("admins should hear about the signup: %+v", s)
	}
	for _, want := range []string{"**Plan:** Family, $45", "**Riding:** Some group rides", "Mark paid", "/admin/members?q=kim%40example.com"} {
		if !strings.Contains(s.body, want) {
			t.Errorf("signup notice missing %q:\n%s", want, s.body)
		}
	}
	none("after joining")

	m := a.store.Members()[0]
	if !m.WelcomePending {
		t.Fatal("a website signup should be waiting for its welcome email")
	}
	if page := do(t, h, admin, "GET", "/admin/members", "", nil).Body.String(); !strings.Contains(page, "Sends their welcome email") {
		t.Error("the Members page should say marking them paid sends the welcome email")
	}
	paid := func(v string) string {
		ct, body := form(url.Values{"paid": {v}})
		return do(t, h, admin, "POST", "/admin/members/"+strconv.Itoa(m.ID)+"/paid", ct, body).Header().Get("Location")
	}
	if loc := paid("1"); !strings.Contains(loc, "ok=paid-welcome") {
		t.Errorf("marking paid should say the welcome is on its way: %s", loc)
	}
	s, ok = next()
	if !ok || s.to != "kim@example.com" || !strings.Contains(s.subject, "Welcome to PaxVelo, Kim") ||
		!strings.Contains(s.body, "dues are in") || strings.Contains(s.body, "finish up") ||
		!strings.Contains(s.body, levelAdvice["some"]) {
		t.Fatalf("welcome email: %+v", s)
	}
	paid("0")
	if loc := paid("1"); !strings.Contains(loc, "ok=paid") || strings.Contains(loc, "welcome") {
		t.Errorf("a second mark paid shouldn't welcome again: %s", loc)
	}
	none("marking paid again")

	ct2, body2 := form(url.Values{"name": {"Lee Added"}, "email": {"lee@example.com"}, "plan": {"individual"}})
	do(t, h, admin, "POST", "/admin/members", ct2, body2)
	var lee Member
	for _, x := range a.store.Members() {
		if x.Email == "lee@example.com" {
			lee = x
		}
	}
	if lee.ID == 0 || lee.WelcomePending {
		t.Fatalf("admin-added member: %+v", lee)
	}
	ct2, body2 = form(url.Values{"paid": {"1"}})
	do(t, h, admin, "POST", "/admin/members/"+strconv.Itoa(lee.ID)+"/paid", ct2, body2)
	none("marking an admin-added member paid")
}

// A welcome email nobody changed from the old default (which asked people to
// pay) becomes the new default; one an admin wrote is kept.
func TestOldWelcomeBodyUpdated(t *testing.T) {
	for _, c := range []struct{ saved, want string }{{oldWelcomeBody, defaultWelcomeBody}, {"Hi {first_name}", "Hi {first_name}"}} {
		dir := t.TempDir()
		b, _ := json.Marshal(map[string]any{"welcome_email": map[string]any{"enabled": true, "subject": "Hi", "body": c.saved}})
		os.WriteFile(filepath.Join(dir, "paxvelo.json"), b, 0o600)
		s, err := openStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Welcome().Body; got != c.want {
			t.Errorf("saved %.20q: got %.40q", c.saved, got)
		}
	}
}

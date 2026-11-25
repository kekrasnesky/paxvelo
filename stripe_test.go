package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStripe stands in for Stripe's API: it starts Checkout sessions and
// answers for them, paid or not.
type fakeStripe struct {
	mu      sync.Mutex
	started []url.Values
	paid    map[string]bool
	member  map[string]string
	fee     int64 // 0: the fee isn't known yet
}

func newFakeStripe(t *testing.T, a *App) *fakeStripe {
	f := &fakeStripe{paid: map[string]bool{}, member: map[string]string{}, fee: 132}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, _, ok := r.BasicAuth(); !ok || u != "sk_test_abc" {
			http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
			r.ParseForm()
			f.started = append(f.started, r.PostForm)
			id := fmt.Sprintf("cs_test_%d", len(f.started))
			f.member[id] = r.PostForm.Get("metadata[member]")
			fmt.Fprintf(w, `{"id":%q,"url":"https://checkout.stripe.com/c/pay/%s"}`, id, id)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/")
			mem, ok := f.member[id]
			if !ok {
				http.Error(w, `{"error":{"message":"no such session"}}`, http.StatusNotFound)
				return
			}
			status, pi := "unpaid", `null`
			if f.paid[id] {
				status = "paid"
				bt := "null"
				if f.fee != 0 && r.URL.Query().Get("expand[]") != "" {
					bt = fmt.Sprintf(`{"fee":%d}`, f.fee)
				}
				pi = fmt.Sprintf(`{"id":"pi_%s","latest_charge":{"balance_transaction":%s}}`, id, bt)
			}
			fmt.Fprintf(w, `{"id":%q,"status":"complete","payment_status":%q,"amount_total":3500,"currency":"usd",
				"metadata":{"member":%q,"kind":"dues"},"livemode":false,"customer_details":{"email":"pat@example.com"},"payment_intent":%s}`,
				id, status, mem, pi)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a.cfg.StripeAPI, a.cfg.StripeSecretKey, a.cfg.StripeWebhookSecret = srv.URL, "sk_test_abc", "whsec_test"
	return f
}

func (f *fakeStripe) pay(id string) { f.mu.Lock(); f.paid[id] = true; f.mu.Unlock() }

func signStripe(body, secret string, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "." + body))
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

func webhook(t *testing.T, h http.Handler, body, sig string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/stripe/webhook", strings.NewReader(body))
	r.Header.Set("Stripe-Signature", sig)
	r.Header.Set("Origin", "https://stripe.com") // not our site, and that's fine here
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A member pays by card: the pay link goes on to Stripe with their dues,
// and coming back marks them paid once, with Stripe's fee for the treasurer.
func TestStripeDues(t *testing.T) {
	a, h, _ := testApp(t)
	year := a.cfg.ThisYear()
	m, _, err := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual"}, year)
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.PayPalIndividual = "https://paypal.example/ind"
	if got := a.payLink(m); got != "https://paypal.example/ind" {
		t.Fatalf("without Stripe the pay link is PayPal's, got %q", got)
	}
	if w := do(t, h, nil, "GET", "/pay/"+payCode(a.store, m.ID), "", nil); w.Code != http.StatusNotFound {
		t.Errorf("without Stripe the pay page isn't there: %d", w.Code)
	}

	f := newFakeStripe(t, a)
	code := payCode(a.store, m.ID)
	if got := a.payLink(m); got != a.cfg.SiteURL+"/pay/"+code {
		t.Fatalf("with Stripe the pay link is the site's pay page, got %q", got)
	}
	page := do(t, h, nil, "GET", "/membership?t="+m.Token, "", nil).Body.String()
	if !strings.Contains(page, "/pay/"+code) || !strings.Contains(page, "Pay $35") || strings.Contains(page, "with PayPal") {
		t.Error("the membership page's Pay button should go to the pay page")
	}
	for _, bad := range []string{"/pay/" + strconv.Itoa(m.ID) + ".nope", "/pay/999." + payCode(a.store, 999)[4:], "/pay/x"} {
		if w := do(t, h, nil, "GET", bad, "", nil); w.Code != http.StatusNotFound {
			t.Errorf("%s: a pay link that isn't signed is not found, got %d", bad, w.Code)
		}
	}
	// PayPal still set up: both, and the page waits for a pick.
	if b := do(t, h, nil, "GET", "/pay/"+code, "", nil).Body.String(); strings.Contains(b, `"/pay/`+code+`" data-auto`) ||
		!strings.Contains(b, "Pay $35 by card") || !strings.Contains(b, `href="https://paypal.example/ind"`) {
		t.Errorf("card and PayPal side by side: %s", b)
	}
	a.cfg.PayPalIndividual = "" // card only
	w := do(t, h, nil, "GET", "/pay/"+code, "", nil)
	if b := w.Body.String(); strings.Contains(b, "PayPal") || w.Code != 200 || !strings.Contains(b, `"/pay/`+code+`" data-auto`) || !strings.Contains(b, "Pay $35") || strings.Contains(b, "Pat Rider") || strings.Contains(b, "pat@") {
		t.Errorf("the pay page goes on to Stripe and shows no name: %d %s", w.Code, b)
	}

	w = do(t, h, nil, "POST", "/pay/"+code, "application/x-www-form-urlencoded", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "https://checkout.stripe.com/c/pay/cs_test_1" {
		t.Fatalf("Pay goes to Stripe's checkout: %d %q", w.Code, w.Header().Get("Location"))
	}
	sent := f.started[0]
	if sent.Get("line_items[0][price_data][unit_amount]") != "3500" || sent.Get("metadata[member]") != strconv.Itoa(m.ID) ||
		sent.Get("customer_email") != "pat@example.com" || !strings.HasSuffix(sent.Get("success_url"), "/pay/"+code+"?session={CHECKOUT_SESSION_ID}") {
		t.Errorf("checkout should carry the dues and the member: %v", sent)
	}

	// Back before Stripe has the money: nothing is marked.
	if b := do(t, h, nil, "GET", "/pay/"+code+"?session=cs_test_1", "", nil).Body.String(); !strings.Contains(b, "Payment processing") {
		t.Error("an unpaid session waits")
	}
	if got, _ := a.store.MemberByID(m.ID); got.Paid() {
		t.Fatal("not paid yet")
	}
	// Someone else's session can't mark this member paid.
	other, _, _ := a.store.Join(memberDetails{Name: "Sam Other", Email: "sam@example.com", Plan: "individual"}, year)
	f.pay("cs_test_1")
	do(t, h, nil, "GET", "/pay/"+payCode(a.store, other.ID)+"?session=cs_test_1", "", nil)
	if got, _ := a.store.MemberByID(other.ID); got.Paid() {
		t.Fatal("a session for one member marked another paid")
	}

	b := do(t, h, nil, "GET", "/pay/"+code+"?session=cs_test_1", "", nil).Body.String()
	if !strings.Contains(b, "your dues are paid") {
		t.Errorf("back from Stripe, paid: %s", b)
	}
	got, _ := a.store.MemberByID(m.ID)
	if !got.Paid() || len(got.PaidYears) != 1 {
		t.Fatalf("paid once: %+v", got.PaidYears)
	}
	// The webhook for the same payment, and reloading the page, change nothing.
	body := `{"type":"checkout.session.completed","data":{"object":{"object":"checkout.session","id":"cs_test_1","metadata":{"kind":"dues","member":"` + strconv.Itoa(m.ID) + `"}}}}`
	if w := webhook(t, h, body, signStripe(body, "whsec_test", time.Now())); w.Code != 200 {
		t.Errorf("webhook: %d %s", w.Code, w.Body)
	}
	do(t, h, nil, "GET", "/pay/"+code+"?session=cs_test_1", "", nil)
	got, _ = a.store.MemberByID(m.ID)
	ps := a.store.StripePayments()
	if len(got.PaidYears) != 1 || len(ps) != 1 {
		t.Fatalf("a payment counts once: years %v, payments %d", got.PaidYears, len(ps))
	}
	if p := ps[0]; p.Fee != 132 || !p.FeeSure || p.Cents != 3500 || p.Via != "return" || p.Mode != "test" || p.Intent != "pi_cs_test_1" {
		t.Errorf("payment record: %+v", p)
	}
	// Paid up and far from running out: Pay doesn't start another payment.
	if w := do(t, h, nil, "POST", "/pay/"+code, "application/x-www-form-urlencoded", nil); w.Header().Get("Location") != "/pay/"+code || len(f.started) != 1 {
		t.Errorf("paid up: back to the pay page, got %q", w.Header().Get("Location"))
	}

	// The treasurer sees Stripe's own fee.
	d := a.duesIncome(time.Now().In(a.cfg.Location).Year(), "9999-12-31")
	if len(d.Lines) != 1 || d.Lines[0].Fee != 132 || !d.Lines[0].Card {
		t.Errorf("dues income: %+v", d.Lines)
	}
}

// The webhook alone marks a member paid, and works out the fee when Stripe
// hasn't said it yet. Unsigned, stale or foreign events change nothing.
func TestStripeWebhook(t *testing.T) {
	a, h, _ := testApp(t)
	f := newFakeStripe(t, a)
	f.fee = 0
	m, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual"}, a.cfg.ThisYear())
	do(t, h, nil, "POST", "/pay/"+payCode(a.store, m.ID), "application/x-www-form-urlencoded", nil)
	f.pay("cs_test_1")
	body := `{"type":"checkout.session.completed","data":{"object":{"object":"checkout.session","id":"cs_test_1","metadata":{"kind":"dues"}}}}`

	for name, sig := range map[string]string{
		"no signature":    "",
		"wrong secret":    signStripe(body, "whsec_other", time.Now()),
		"too old":         signStripe(body, "whsec_test", time.Now().Add(-10*time.Minute)),
		"other body":      signStripe(body+" ", "whsec_test", time.Now()),
		"timestamp alone": "t=" + strconv.FormatInt(time.Now().Unix(), 10),
	} {
		if w := webhook(t, h, body, sig); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if got, _ := a.store.MemberByID(m.ID); got.Paid() {
		t.Fatal("an unsigned webhook marked someone paid")
	}
	other := `{"type":"checkout.session.completed","data":{"object":{"object":"checkout.session","id":"cs_live_x","metadata":{}}}}`
	if w := webhook(t, h, other, signStripe(other, "whsec_test", time.Now())); w.Code != 200 {
		t.Errorf("a session that isn't dues is passed over: %d", w.Code)
	}
	if w := webhook(t, h, body, signStripe(body, "whsec_test", time.Now())); w.Code != 200 {
		t.Fatalf("webhook: %d %s", w.Code, w.Body)
	}
	got, _ := a.store.MemberByID(m.ID)
	ps := a.store.StripePayments()
	if !got.Paid() || len(ps) != 1 || ps[0].Fee != stripeFeeGuess(3500) || ps[0].FeeSure || ps[0].Via != "webhook" {
		t.Errorf("paid by the webhook: %v %+v", got.Paid(), ps)
	}
	if stripeFeeGuess(3500) != 132 {
		t.Errorf("2.9%% + 30¢ of $35 is $1.32, got %d", stripeFeeGuess(3500))
	}
}

// A Family rider's pay link goes to their plan holder's.
func TestStripeRiderPaysPlan(t *testing.T) {
	a, h, _ := testApp(t)
	newFakeStripe(t, a)
	p, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "family"}, a.cfg.ThisYear())
	r, _, _ := a.store.Join(memberDetails{Name: "Sam Rider", Email: "sam@example.com", Plan: "family"}, a.cfg.ThisYear())
	a.store.mu.Lock()
	a.store.byID(r.ID).FamilyOf = p.ID
	a.store.mu.Unlock()
	w := do(t, h, nil, "GET", "/pay/"+payCode(a.store, r.ID), "", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/pay/"+payCode(a.store, p.ID) {
		t.Errorf("rider's pay link: %d %q", w.Code, w.Header().Get("Location"))
	}
}

// With card payments on, joining goes straight on to pay and comes back to
// a welcome; the payment shows on Treasurer → Payments and the member page.
func TestStripeJoin(t *testing.T) {
	a, h, admin := testApp(t)
	f := newFakeStripe(t, a)
	ct, body := joinForm(a, url.Values{"plan": {"individual"}, "first": {"Pat"}, "last": {"Rider"}, "email": {"pat@example.com"},
		"phone": {"301-555-0123"}, "level": {"some"}, "emergency_name": {"Sam Rider"}, "emergency_phone": {"240-555-0199"}})
	r := httptest.NewRequest("POST", "/join", body)
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	m, ok := a.store.MemberByEmail("pat@example.com")
	code := payCode(a.store, m.ID)
	if !ok || got["go"] != true || got["pay_url"] != a.cfg.SiteURL+"/pay/"+code+"?from=join" {
		t.Fatalf("join answer: %s", rec.Body)
	}
	if b := do(t, h, nil, "GET", "/pay/"+code+"?from=join", "", nil).Body.String(); !strings.Contains(b, `name="from" value="join"`) {
		t.Error("the pay page keeps that it came from joining")
	}
	fct, fbody := form(url.Values{"from": {"join"}})
	do(t, h, nil, "POST", "/pay/"+code, fct, fbody)
	if s := f.started[0]; !strings.HasSuffix(s.Get("success_url"), "&from=join") || !strings.HasSuffix(s.Get("cancel_url"), "?back=1&from=join") {
		t.Errorf("Stripe comes back to the join welcome: %v", s)
	}
	cs := a.store.Checkouts()
	if len(cs) != 1 || cs[0].From != "join" || cs[0].Paid || a.store.Unfinished(m).IsZero() {
		t.Fatalf("the started payment is kept, unfinished: %+v", cs)
	}
	// Left without paying: Payments lists them.
	page := do(t, h, admin, "GET", "/admin/treasurer/payments", "", nil).Body.String()
	if !strings.Contains(page, "Started, not paid") || !strings.Contains(page, "Pat Rider") || !strings.Contains(page, "Stripe test mode") {
		t.Error("Payments should list the unfinished payment")
	}
	if b := do(t, h, admin, "GET", "/admin/members/"+strconv.Itoa(m.ID), "", nil).Body.String(); !strings.Contains(b, "not finished") {
		t.Error("the member page says they started paying")
	}

	f.pay("cs_test_1")
	b := do(t, h, nil, "GET", "/pay/"+code+"?session=cs_test_1&from=join", "", nil).Body.String()
	if !strings.Contains(b, "Welcome to PaxVelo, Pat!") || !strings.Contains(b, "Confirm your email") || !strings.Contains(b, "/calendar") {
		t.Errorf("paid on joining: the welcome: %s", b)
	}
	m, _ = a.store.MemberByID(m.ID)
	if !a.store.Unfinished(m).IsZero() || !a.store.Checkouts()[0].Paid {
		t.Error("paid: no longer unfinished")
	}
	page = do(t, h, admin, "GET", "/admin/treasurer/payments", "", nil).Body.String()
	if strings.Contains(page, "Started, not paid") || !strings.Contains(page, "dashboard.stripe.com/test/payments/pi_cs_test_1") || !strings.Contains(page, "$35.00") {
		t.Errorf("Payments should list the payment: %s", page)
	}
	csv := do(t, h, admin, "GET", "/admin/treasurer/payments.csv", "", nil)
	if !strings.Contains(csv.Body.String(), "Pat Rider,Individual,") || !strings.Contains(csv.Header().Get("Content-Disposition"), "card payments.csv") {
		t.Errorf("spreadsheet: %s", csv.Body)
	}
	if b := do(t, h, admin, "GET", "/admin/members/"+strconv.Itoa(m.ID), "", nil).Body.String(); !strings.Contains(b, "$35.00 by card") {
		t.Error("the member page lists the card payment")
	}
}

// The membership emails: Finish paying the day after someone leaves the card
// payment page, Thanks for renewing on a renewal (not a first payment), and
// Membership ran out a week after dues lapse.
func TestMembershipAutomations(t *testing.T) {
	a, _, _ := testApp(t)
	var mu sync.Mutex
	var got []string
	a.mailer.onSend = func(to, subject, body string) { mu.Lock(); got = append(got, to+"|"+subject+"|"+body); mu.Unlock() }
	sent := func() []string {
		a.mailer.bg.Wait()
		mu.Lock()
		defer mu.Unlock()
		out := got
		got = nil
		return out
	}
	byName := map[string]Automation{}
	for _, au := range a.store.Automations() {
		byName[au.Name] = au
	}
	for name, when := range map[string]string{
		"Thanks for renewing": "The day a member renews",
		"Finish paying":       "1 day after someone starts paying by card, if they haven’t finished",
		"Membership ran out":  "7 days after a member’s dues ran out, if they haven’t renewed",
	} {
		if au, ok := byName[name]; !ok || au.Paused || au.When() != when {
			t.Errorf("%s: %+v (%q)", name, au, au.When())
		}
	}
	loc := a.cfg.Location
	now := time.Now().In(loc)
	nine := time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, loc)
	a.store.mu.Lock()
	for _, au := range a.store.d.Automations {
		au.Created = nine.AddDate(0, 0, -30)
	}
	a.store.mu.Unlock()
	add := func(name string, joinedDaysAgo int) Member {
		m, _, err := a.store.Join(memberDetails{Name: name, Email: strings.ToLower(strings.Fields(name)[0]) + "@example.com", Plan: "individual"}, now.Year())
		if err != nil {
			t.Fatal(err)
		}
		a.store.mu.Lock()
		p := a.store.byID(m.ID)
		p.Joined, p.Unconfirmed = nine.AddDate(0, 0, -joinedDaysAgo), false
		m = *p
		a.store.mu.Unlock()
		return m
	}

	// Left the card payment page yesterday, and someone who left today.
	left := add("Lee Left", 3)
	today := add("Tia Today", 3)
	a.store.RecordCheckout(StripeCheckout{Session: "cs_a", Member: left.ID, Cents: 3500})
	a.store.RecordCheckout(StripeCheckout{Session: "cs_b", Member: today.ID, Cents: 3500})
	a.store.mu.Lock()
	a.store.d.Checkouts[0].Started = nine.AddDate(0, 0, -1)
	a.store.d.Checkouts[1].Started = nine
	a.store.mu.Unlock()
	// A renewal today, and a first payment today.
	renew := add("Rae Renew", 400)
	a.store.mu.Lock()
	a.store.byID(renew.ID).PaidYears = []int{renew.Joined.Year()}
	a.store.mu.Unlock()
	a.store.Pay(renew.ID, true)
	first := add("Finn First", 1)
	a.store.Pay(first.ID, false)
	// Dues ran out 7 days ago.
	lapsed := add("Lou Lapsed", 372)
	a.store.mu.Lock()
	a.store.byID(lapsed.ID).PaidYears = []int{lapsed.Joined.Year()}
	a.store.mu.Unlock()

	a.runAutomations(nine)
	s := sent()
	has := func(prefix string) bool {
		return slices.ContainsFunc(s, func(e string) bool { return strings.HasPrefix(e, prefix) })
	}
	if !has("lee@example.com|Finish paying your PaxVelo dues|") || has("tia@example.com|Finish") {
		t.Errorf("Finish paying goes the day after: %q", s)
	}
	if !has("rae@example.com|Thanks for renewing, Rae|") || has("finn@example.com|Thanks") {
		t.Errorf("Thanks for renewing goes on renewals only: %q", s)
	}
	if !has("lou@example.com|Your PaxVelo membership ran out|") {
		t.Errorf("Membership ran out: %q", s)
	}
	for _, e := range s {
		if strings.HasPrefix(e, "lee@") && !strings.Contains(e, "/pay/") && !strings.Contains(e, "/join") {
			t.Errorf("Finish paying carries a pay link: %q", e)
		}
	}
	// Paid since leaving: no more Finish paying.
	a.store.Pay(left.ID, false)
	lee, _ := a.store.MemberByID(left.ID)
	if !a.store.Unfinished(lee).IsZero() {
		t.Error("paid by other means: not unfinished")
	}
}

// Treasurer → Payments switches dues between card, PayPal and both. A way
// that isn't set up can't be picked, and switching one off only counts
// while the other works.
func TestPayWithSwitch(t *testing.T) {
	a, h, _ := testApp(t)
	admin := adminCookie(a)
	m, _, err := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual"}, a.cfg.ThisYear())
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.PayPalIndividual = "https://paypal.example/ind"
	page := do(t, h, admin, "GET", "/admin/treasurer/payments", "", nil).Body.String()
	if !strings.Contains(page, `value="card" aria-pressed="false" disabled`) || !strings.Contains(page, `value="paypal" aria-pressed="true">`) {
		t.Errorf("without Stripe keys, Card can't be picked and PayPal is on: %s", page)
	}
	newFakeStripe(t, a)
	code := payCode(a.store, m.ID)
	pick := func(with string) {
		t.Helper()
		ct, body := form(url.Values{"with": {with}, "year": {"2026"}})
		if w := do(t, h, admin, "POST", "/admin/treasurer/payments/with", ct, body); w.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d", with, w.Code)
		}
	}
	if !strings.Contains(do(t, h, admin, "GET", "/admin/treasurer/payments", "", nil).Body.String(), `value="both" aria-pressed="true">`) {
		t.Error("both set up: both, to start")
	}

	pick("paypal")
	if got := a.payLink(m); got != "https://paypal.example/ind" {
		t.Errorf("PayPal only: the pay link is PayPal's, got %q", got)
	}
	if w := do(t, h, nil, "GET", "/pay/"+code, "", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "https://paypal.example/ind" {
		t.Errorf("an old card link goes on to PayPal: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(t, h, nil, "POST", "/pay/"+code, "application/x-www-form-urlencoded", nil); w.Header().Get("Location") != "/pay/"+code {
		t.Errorf("no card payment starts: %q", w.Header().Get("Location"))
	}
	if p := do(t, h, nil, "GET", "/membership?t="+m.Token, "", nil).Body.String(); !strings.Contains(p, "Pay $35 with PayPal") {
		t.Error("the membership page says PayPal")
	}

	pick("card")
	if got := a.payLink(m); got != a.cfg.SiteURL+"/pay/"+code {
		t.Errorf("card only: the pay page, got %q", got)
	}
	if b := do(t, h, nil, "GET", "/pay/"+code, "", nil).Body.String(); strings.Contains(b, "PayPal") || !strings.Contains(b, "data-auto") {
		t.Error("card only: no PayPal button, straight on to Stripe")
	}

	pick("both")
	if b := do(t, h, nil, "GET", "/pay/"+code, "", nil).Body.String(); !strings.Contains(b, "Pay $35 by card") || !strings.Contains(b, "Pay with PayPal") {
		t.Error("both: side by side")
	}

	pick("paypal")
	a.cfg.PayPalIndividual, a.cfg.PayPalFamily = "", "" // PayPal taken out of the conf: card it is
	if got := a.payLink(m); got != a.cfg.SiteURL+"/pay/"+code {
		t.Errorf("with no PayPal link, card still works: %q", got)
	}
	if ct, body := form(url.Values{"with": {"cash"}}); do(t, h, admin, "POST", "/admin/treasurer/payments/with", ct, body).Header().Get("Location") == "" || a.store.PayWith() != "paypal" {
		t.Error("an unknown pick isn't saved")
	}
}

// With a PayPal link for one plan only, picking PayPal still shows PayPal
// (the other plan pays by card, as there's no other way).
func TestPayWithOnePlanPayPal(t *testing.T) {
	a, h, _ := testApp(t)
	newFakeStripe(t, a)
	a.cfg.PayPalIndividual = "https://paypal.example/ind"
	a.store.SetPayWith("paypal")
	if page := do(t, h, adminCookie(a), "GET", "/admin/treasurer/payments", "", nil).Body.String(); !strings.Contains(page, `value="paypal" aria-pressed="true">`) {
		t.Error("the switch shows what was picked")
	}
	if card, _ := payWays(a.cfg, a.store, "family"); !card {
		t.Error("Family has no PayPal link, so it stays on card")
	}
}

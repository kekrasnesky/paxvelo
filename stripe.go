package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Dues paid by card through Stripe Checkout. With STRIPE_SECRET_KEY and
// STRIPE_WEBHOOK_SECRET set, every Pay button and email pay link goes to
// /pay/<code> instead of PayPal. That page sends the member on to Stripe's
// own payment page (card numbers never reach the site), and when Stripe says
// the payment went through, the member is marked paid with no admin needed:
// the same as Mark paid, welcome email and household links included.
//
// Stripe tells the site two ways, and either one is enough: it calls
// /stripe/webhook, signed with the webhook secret, and the member's browser
// comes back to /pay/<code>?session=…, which asks Stripe about that payment.
// Each payment (a Checkout session) counts once however often it's heard of.
//
// No Stripe library: the API is form posts over HTTPS, and the webhook
// signature is an HMAC.

// StripePayment is one dues payment made through Stripe.
type StripePayment struct {
	Session string    `json:"session"` // the Checkout session, cs_…
	Member  int       `json:"member"`
	Name    string    `json:"name"`
	Year    int       `json:"year"` // the membership year it paid for (dues.go)
	Cents   int64     `json:"cents"`
	Fee     int64     `json:"fee"`                // what Stripe kept
	FeeSure bool      `json:"fee_sure,omitempty"` // from Stripe; otherwise worked out at its standard rate
	At      time.Time `json:"at"`                 // when it was recorded
	Intent  string    `json:"intent,omitempty"`   // the payment, pi_…, to find it in Stripe's dashboard
	Email   string    `json:"email,omitempty"`    // what they typed at Stripe
	Renewed bool      `json:"renewed,omitempty"`  // paid up already: it added a year
	Plan    string    `json:"plan,omitempty"`     // their plan when they paid
	Via     string    `json:"via,omitempty"`      // "webhook" or "return", whichever was first
	Mode    string    `json:"mode,omitempty"`     // "test" for Stripe test keys
	Note    string    `json:"note,omitempty"`     // anything odd about it
}

// StripeCheckout is a card payment someone started: they pressed Pay and
// went to Stripe. Paid once Stripe says so; the rest are payments left
// unfinished, for admins and the Finish paying email.
type StripeCheckout struct {
	Session string    `json:"session"`
	Member  int       `json:"member"`
	Cents   int64     `json:"cents"`
	Started time.Time `json:"started"`
	From    string    `json:"from,omitempty"` // "join" (the join form) or "pay" (a pay link)
	Paid    bool      `json:"paid,omitempty"`
}

// checkoutKeep is how long started payments are kept.
const checkoutKeep = 180 * 24 * time.Hour

// stripeFeeGuess is Stripe's standard US card rate, 2.9% + 30¢, for a payment
// whose fee Stripe didn't say.
func stripeFeeGuess(cents int64) int64 { return (cents*29+500)/1000 + 30 }

// Stripe is on when both keys are set.
func (c Config) Stripe() bool { return c.StripeSecretKey != "" && c.StripeWebhookSecret != "" }

// ---- Pay links ----

// payKey signs pay links. A pay link only lets someone pay a member's dues,
// so it never runs out and can be forwarded; it opens nothing else.
func payKey(store *Store) []byte {
	k := sha256.Sum256([]byte("pay\x00" + store.Secret()))
	return k[:]
}

// payCode is "member.signature".
func payCode(store *Store, id int) string {
	v := strconv.Itoa(id)
	return v + "." + sign(payKey(store), v)[:24]
}

// readPayCode checks a pay code and returns its member's id.
func readPayCode(store *Store, code string) (int, bool) {
	v, sig, ok := strings.Cut(code, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(sign(payKey(store), v)[:24])) {
		return 0, false
	}
	id, err := strconv.Atoi(v)
	return id, err == nil && id > 0
}

// payLink is where a member pays their dues: the site's own pay page with
// Stripe on, otherwise the PayPal link for their plan ("" if neither is set
// up).
func payLink(cfg Config, store *Store, m Member) string {
	if cfg.Stripe() && m.ID > 0 {
		return cfg.SiteURL + "/pay/" + payCode(store, m.ID)
	}
	return cfg.PayLink(m.Plan)
}

func (a *App) payLink(m Member) string { return payLink(a.cfg, a.store, m) }

// payLabel is the pay button's words.
func (a *App) payLabel(plan string) string {
	if a.cfg.Stripe() {
		return fmt.Sprintf("Pay $%d", a.cfg.Dues(plan))
	}
	return fmt.Sprintf("Pay $%d with PayPal", a.cfg.Dues(plan))
}

// ---- The pay page ----

type payData struct {
	State   string // go (on to Stripe), back (came back without paying), paid, wait (Stripe is still working on it)
	Code    string
	Plan    string // Individual or Family
	Dues    int
	Year    int    // the membership year a payment now pays for, by when it starts
	Through string // paid up through, like June 3, 2027
	Renew   bool   // paid up, but close enough to the end to renew
	Message string
	// PayPal is the plan's PayPal link, offered beside the card while both
	// are set up (empty PAYPAL_*_URL = card only). With it the page waits
	// for them to pick instead of going straight on to Stripe.
	PayPal string
	// Joining: they came from the join form (from=join), so coming back
	// paid is the end of joining.
	Join        bool
	Welcome     bool      // just paid on joining: Welcome to PaxVelo
	First       string    // their first name, on that welcome
	Unconfirmed bool      // their email isn't confirmed yet
	Rides       []RideOcc // rides coming up that suit them, on that welcome
}

// renewWindow is how close to running out paid-up dues can be renewed from
// the pay page.
const renewWindow = 60 * 24 * time.Hour

func (a *App) payView(m Member, code string, join bool) payData {
	d := payData{State: "go", Code: code, Plan: m.PlanName(), Dues: a.cfg.Dues(m.Plan), Join: join, PayPal: a.cfg.PayLink(m.Plan)}
	d.Year = m.anniversary(m.nextPaidYear(time.Now())).Year()
	if m.Paid() {
		d.State, d.Through = "paid", m.DuesExpire().Format("January 2, 2006")
		d.Renew = time.Until(m.DuesExpire()) < renewWindow
	}
	return d
}

func (a *App) renderPay(w http.ResponseWriter, status int, d payData) {
	title := "Pay your dues — " + siteName
	if d.Welcome {
		title = "Welcome — " + siteName
	}
	a.site.render(w, status, "pay", "/pay", title, d)
}

// payPage is the page a pay link opens. Unpaid, it goes straight on to
// Stripe (site.js presses the button); back from Stripe it shows how it went.
func (a *App) payPage(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	id, ok := readPayCode(a.store, code)
	m, found := a.store.MemberByID(id)
	if !ok || !found || !a.cfg.Stripe() || m.FamilyOf != 0 {
		if found && m.FamilyOf != 0 { // a Family rider's dues are the plan holder's
			if p, ok := a.store.MemberByID(m.FamilyOf); ok && a.cfg.Stripe() {
				http.Redirect(w, r, "/pay/"+payCode(a.store, p.ID), http.StatusSeeOther)
				return
			}
		}
		a.site.notFound(w, r)
		return
	}
	q := r.URL.Query()
	join := q.Get("from") == "join"
	if sid := q.Get("session"); sid != "" {
		done, err := a.checkSession(sid, id, "return")
		if err != nil {
			log.Printf("stripe: checking %s on return: %v", sid, err)
		}
		m, _ = a.store.MemberByID(id)
		d := a.payView(m, code, join)
		switch {
		case err == nil && !done && d.State != "paid":
			d.State = "wait"
		case done && join:
			d.Welcome, d.First, d.Unconfirmed = true, m.FirstName(), m.Unconfirmed
			d.Rides = a.ridesFor(m.Level, 3)
		}
		a.renderPay(w, http.StatusOK, d)
		return
	}
	d := a.payView(m, code, join)
	if d.State == "go" && q.Get("back") != "" {
		d.State = "back"
	}
	a.renderPay(w, http.StatusOK, d)
}

// startPay is the Pay button: it starts a Stripe Checkout payment for the
// member's dues and sends them to it.
func (a *App) startPay(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	code := r.PathValue("code")
	id, ok := readPayCode(a.store, code)
	m, found := a.store.MemberByID(id)
	if !ok || !found || !a.cfg.Stripe() || m.FamilyOf != 0 {
		a.site.notFound(w, r)
		return
	}
	join := r.FormValue("from") == "join"
	d := a.payView(m, code, join)
	if d.State == "paid" && !(d.Renew && r.FormValue("renew") != "") {
		http.Redirect(w, r, "/pay/"+code, http.StatusSeeOther)
		return
	}
	if !a.limit.Allow("pay:"+clientIP(r), 20, time.Hour) {
		d.State, d.Message = "back", "Too many tries from your connection. Wait a bit and try again."
		a.renderPay(w, http.StatusTooManyRequests, d)
		return
	}
	cents := int64(a.cfg.Dues(m.Plan)) * 100
	back, from := a.cfg.SiteURL+"/pay/"+code, ""
	if join {
		from = "&from=join"
	}
	f := url.Values{
		"mode":                                   {"payment"},
		"success_url":                            {back + "?session={CHECKOUT_SESSION_ID}" + from},
		"cancel_url":                             {back + "?back=1" + from},
		"client_reference_id":                    {strconv.Itoa(m.ID)},
		"metadata[member]":                       {strconv.Itoa(m.ID)},
		"metadata[kind]":                         {"dues"},
		"metadata[plan]":                         {m.Plan},
		"submit_type":                            {"pay"},
		"line_items[0][quantity]":                {"1"},
		"line_items[0][price_data][currency]":    {"usd"},
		"line_items[0][price_data][unit_amount]": {strconv.FormatInt(cents, 10)},
		"line_items[0][price_data][product_data][name]": {fmt.Sprintf("PaxVelo %s membership", m.PlanName())},
		"payment_intent_data[description]":              {fmt.Sprintf("PaxVelo %s dues", strings.ToLower(m.PlanName()))},
		"payment_intent_data[metadata][member]":         {strconv.Itoa(m.ID)},
	}
	if m.Email != "" {
		f.Set("customer_email", m.Email)
	}
	var s stripeSession
	if err := a.stripe("POST", "/v1/checkout/sessions", f, &s); err != nil || s.URL == "" {
		log.Printf("stripe: starting a payment for member %d: %v", m.ID, err)
		d.State, d.Message = "back", "Card payments aren’t working right now. Try again in a few minutes."
		a.renderPay(w, http.StatusBadGateway, d)
		return
	}
	src := "pay"
	if join {
		src = "join"
	}
	a.store.RecordCheckout(StripeCheckout{Session: s.ID, Member: m.ID, Cents: cents, From: src})
	http.Redirect(w, r, s.URL, http.StatusSeeOther)
}

// ---- Stripe's API ----

// stripeSession is the part of a Checkout session the site reads.
type stripeSession struct {
	ID                string            `json:"id"`
	URL               string            `json:"url"`
	Status            string            `json:"status"`         // open, complete, expired
	PaymentStatus     string            `json:"payment_status"` // paid, unpaid, no_payment_required
	AmountTotal       int64             `json:"amount_total"`
	Currency          string            `json:"currency"`
	ClientReferenceID string            `json:"client_reference_id"`
	Metadata          map[string]string `json:"metadata"`
	Livemode          bool              `json:"livemode"`
	CustomerDetails   struct {
		Email string `json:"email"`
	} `json:"customer_details"`
	// The payment: just its id, or (asked for with expand) the payment with
	// its charge and the fee Stripe kept.
	PaymentIntent json.RawMessage `json:"payment_intent"`
}

// payment reads the payment's id and, when it was expanded, Stripe's fee.
func (s stripeSession) payment() (id string, fee int64, sure bool) {
	if len(s.PaymentIntent) == 0 || string(s.PaymentIntent) == "null" {
		return "", 0, false
	}
	if json.Unmarshal(s.PaymentIntent, &id) == nil {
		return id, 0, false
	}
	var pi struct {
		ID           string `json:"id"`
		LatestCharge struct {
			BalanceTransaction *struct {
				Fee int64 `json:"fee"`
			} `json:"balance_transaction"`
		} `json:"latest_charge"`
	}
	if json.Unmarshal(s.PaymentIntent, &pi) != nil {
		return "", 0, false
	}
	if bt := pi.LatestCharge.BalanceTransaction; bt != nil {
		return pi.ID, bt.Fee, true
	}
	return pi.ID, 0, false
}

var stripeHTTP = &http.Client{Timeout: 20 * time.Second}

// stripe calls Stripe's API with the secret key, reading the answer into out.
func (a *App) stripe(method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, a.cfg.StripeAPI+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(a.cfg.StripeSecretKey, "")
	req.Header.Set("Stripe-Version", "2024-06-20")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := stripeHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(b, &e)
		return fmt.Errorf("stripe %s %s: %s %s", method, path, resp.Status, e.Error.Message)
	}
	return json.Unmarshal(b, out)
}

// checkSession asks Stripe about a Checkout session and, if it's a paid dues
// payment, records it (once). want is the member it should be for, or 0 for
// any (the webhook). It reports whether the payment is in.
func (a *App) checkSession(id string, want int, via string) (bool, error) {
	if !strings.HasPrefix(id, "cs_") || len(id) > 255 || strings.ContainsAny(id, "/?#&") {
		return false, errors.New("not a Checkout session id")
	}
	var s stripeSession
	q := url.Values{"expand[]": {"payment_intent.latest_charge.balance_transaction"}}
	if err := a.stripe("GET", "/v1/checkout/sessions/"+id+"?"+q.Encode(), nil, &s); err != nil {
		// A key that may not read charges can still read the session; the
		// fee is then worked out.
		if err2 := a.stripe("GET", "/v1/checkout/sessions/"+id, nil, &s); err2 != nil {
			return false, err
		}
	}
	member, _ := strconv.Atoi(s.Metadata["member"])
	if s.Metadata["kind"] != "dues" || member == 0 || (want != 0 && member != want) {
		return false, fmt.Errorf("session %s isn't a dues payment for member %d", id, want)
	}
	if s.PaymentStatus != "paid" {
		return false, nil
	}
	a.recordStripe(s, member, via)
	return true, nil
}

// recordStripe marks the member paid for a Stripe payment, once, then does
// what Mark paid does: the welcome email when it's due, waiver links for
// the other adults on a Family plan, and a note to admins' phones.
func (a *App) recordStripe(s stripeSession, member int, via string) {
	intent, fee, sure := s.payment()
	if !sure {
		fee = stripeFeeGuess(s.AmountTotal)
	}
	p := StripePayment{Session: s.ID, Member: member, Cents: s.AmountTotal, Fee: fee, FeeSure: sure,
		Intent: intent, Email: s.CustomerDetails.Email, Via: via}
	if !s.Livemode {
		p.Mode = "test"
	}
	if !strings.EqualFold(s.Currency, "usd") {
		p.Note = "paid in " + strings.ToUpper(s.Currency)
	}
	m, isNew, err := a.store.RecordStripe(p)
	if err != nil {
		log.Printf("stripe: recording %s: %v", s.ID, err)
		return
	}
	if !isNew {
		return
	}
	log.Printf("stripe: %s paid $%s for member %d (%s)", s.ID, moneyInput(s.AmountTotal), member, m.Name)
	a.afterPaid(member)
	a.alertPaid(m, s.AmountTotal)
	if a.mailer != nil {
		a.mailer.Go(func() { a.runAutomations(time.Now()) }) // the receipt now, not in 15 minutes (from 9 am)
	}
}

// stripeWebhook is where Stripe says a payment went through.
func (a *App) stripeWebhook(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.Stripe() {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil {
		http.Error(w, "unreadable", http.StatusBadRequest)
		return
	}
	if !stripeSigned(body, r.Header.Get("Stripe-Signature"), a.cfg.StripeWebhookSecret, time.Now()) {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}
	var ev struct {
		Type string `json:"type"`
		Data struct {
			Object struct {
				Object   string            `json:"object"`
				ID       string            `json:"id"`
				Metadata map[string]string `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	o := ev.Data.Object
	switch ev.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		if o.Object != "checkout.session" || o.Metadata["kind"] != "dues" {
			break // not one of ours: Payment Links, invoices…
		}
		// Ask Stripe rather than trust the event, and get the fee with it.
		if _, err := a.checkSession(o.ID, 0, "webhook"); err != nil {
			log.Printf("stripe: webhook %s: %v", o.ID, err)
			http.Error(w, "try again", http.StatusInternalServerError) // Stripe tries again later
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// stripeTolerance is how old a webhook's signature may be, against replays.
const stripeTolerance = 5 * time.Minute

// stripeSigned checks a Stripe-Signature header ("t=…,v1=…[,v1=…]"): an
// HMAC-SHA256 of "t.body" with the webhook secret, made within the tolerance.
func stripeSigned(body []byte, header, secret string, now time.Time) bool {
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return false
	}
	if d := now.Sub(time.Unix(n, 0)); d > stripeTolerance || d < -stripeTolerance {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return slices.ContainsFunc(sigs, func(s string) bool { return hmac.Equal([]byte(s), []byte(want)) })
}

// ---- Store ----

// RecordStripe records a Stripe payment and marks the member paid for the
// next membership year (a year more, if they're paid up), unless that
// payment was recorded before. It reports whether it was new.
func (s *Store) RecordStripe(p StripePayment) (Member, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.ContainsFunc(s.d.Payments, func(q StripePayment) bool { return q.Session == p.Session }) {
		return Member{}, false, nil
	}
	m := s.byID(p.Member)
	if m == nil { // deleted since: keep the payment, so the treasurer can see it
		p.Note = strings.TrimSpace(p.Note + " member deleted")
		p.At = time.Now().UTC()
		s.d.Payments = append(s.d.Payments, p)
		return Member{}, false, s.save()
	}
	now := time.Now()
	p.Name, p.Plan, p.Renewed, p.At = m.Name, m.Plan, m.PaidAt(now), now.UTC()
	p.Year = m.nextPaidYear(now)
	s.d.Payments = append(s.d.Payments, p)
	s.setPaid(m, p.Year, true)
	for i := range s.d.Checkouts {
		if s.d.Checkouts[i].Session == p.Session {
			s.d.Checkouts[i].Paid = true
		}
	}
	return *m, true, s.save()
}

// RecordCheckout keeps a payment someone just started, dropping ones older
// than checkoutKeep.
func (s *Store) RecordCheckout(c StripeCheckout) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	c.Started = now
	keep := s.d.Checkouts[:0:0]
	for _, o := range s.d.Checkouts {
		if now.Sub(o.Started) < checkoutKeep {
			keep = append(keep, o)
		}
	}
	s.d.Checkouts = append(keep, c)
	return s.save()
}

// Checkouts is every payment started in the last checkoutKeep, newest first.
func (s *Store) Checkouts() []StripeCheckout {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.d.Checkouts)
	slices.Reverse(out)
	return out
}

// unfinished is when a member last started paying without finishing, or
// zero: their latest checkout, if it wasn't paid and no payment came after.
func unfinished(cs []StripeCheckout, m Member) time.Time {
	var last *StripeCheckout
	for i := range cs {
		if cs[i].Member == m.ID && (last == nil || cs[i].Started.After(last.Started)) {
			last = &cs[i]
		}
	}
	if last == nil || last.Paid || m.lastPaidOn().After(last.Started) {
		return time.Time{}
	}
	return last.Started
}

// Unfinished is when a member last started paying without finishing, or zero.
func (s *Store) Unfinished(m Member) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return unfinished(s.d.Checkouts, m)
}

// StripePayments is every Stripe payment, newest first.
func (s *Store) StripePayments() []StripePayment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.d.Payments)
	slices.Reverse(out)
	return out
}

// stripeFees is the fee Stripe kept on each member's dues, by member and
// membership year, for the treasurer (duesIncome).
func (s *Store) stripeFees() map[int]map[int]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int]map[int]int64{}
	for _, p := range s.d.Payments {
		if out[p.Member] == nil {
			out[p.Member] = map[int]int64{}
		}
		out[p.Member][p.Year] += p.Fee
	}
	return out
}

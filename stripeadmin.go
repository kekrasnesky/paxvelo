package main

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Treasurer → Payments: dues paid by card through Stripe (stripe.go), a
// year at a time, and the card payments people started in the last month
// and left unfinished. Board only, like the rest of the Treasurer tab.

type paymentsView struct {
	On, Test   bool // Stripe keys set; test keys
	Rows       []payRow
	Total      int64
	Fees       int64
	New, Renew int
	Unfinished []unfinishedRow
	// The Card / PayPal / Both switch: what's set up, and how members pay
	// now ("card", "paypal", "both", or "" with neither set up).
	CanCard, CanPayPal bool
	With               string
	Setup              paySetupView
}

func (v paymentsView) Net() int64 { return v.Total - v.Fees }

type payRow struct {
	StripePayment
	Gone bool // the member was deleted since
}

func (p StripePayment) PlanName() string { return planLabel(p.Plan) }

// DashURL is the payment in Stripe's dashboard.
func (p payRow) DashURL() string {
	if p.Intent == "" {
		return ""
	}
	base := "https://dashboard.stripe.com/"
	if p.Mode == "test" {
		base += "test/"
	}
	return base + "payments/" + p.Intent
}

type unfinishedRow struct {
	Member  Member
	Started time.Time
	Cents   int64
	Join    bool // from the join form
}

// unfinishedFor is how far back Payments lists unfinished card payments.
const unfinishedFor = 30 * 24 * time.Hour

func (a *App) paymentsView(year int) *paymentsView {
	c := a.payCfg()
	v := &paymentsView{On: c.Stripe(), Test: c.Stripe() && !isLiveKey(c.StripeSecretKey),
		CanCard: c.Stripe(), CanPayPal: c.PayPalIndividual != "" || c.PayPalFamily != ""}
	switch w := a.store.PayWith(); {
	case w == "card" && v.CanCard, w == "paypal" && v.CanPayPal:
		v.With = w
	case v.CanCard && v.CanPayPal:
		v.With = "both"
	case v.CanCard:
		v.With = "card"
	case v.CanPayPal:
		v.With = "paypal"
	}
	for _, p := range a.store.StripePayments() {
		if p.At.In(a.cfg.Location).Year() != year {
			continue
		}
		_, found := a.store.MemberByID(p.Member)
		v.Rows = append(v.Rows, payRow{StripePayment: p, Gone: !found})
		v.Total += p.Cents
		v.Fees += p.Fee
		if p.Renewed || p.Year > 0 && a.paidBefore(p) {
			v.Renew++
		} else {
			v.New++
		}
	}
	cs := a.store.Checkouts()
	seen := map[int]bool{}
	for _, c := range cs { // newest first
		if seen[c.Member] || time.Since(c.Started) > unfinishedFor {
			continue
		}
		seen[c.Member] = true
		m, ok := a.store.MemberByID(c.Member)
		if !ok || unfinished(cs, m).IsZero() {
			continue
		}
		v.Unfinished = append(v.Unfinished, unfinishedRow{Member: m, Started: c.Started, Cents: c.Cents, Join: c.From == "join"})
	}
	return v
}

// paidBefore reports whether the member had paid a year before this
// payment's, so it was a renewal.
func (a *App) paidBefore(p StripePayment) bool {
	m, ok := a.store.MemberByID(p.Member)
	if !ok {
		return false
	}
	for _, y := range m.PaidYears {
		if y < p.Year {
			return true
		}
	}
	return false
}

// isLiveKey reports whether a Stripe key takes real money.
func isLiveKey(k string) bool {
	return len(k) > 8 && (k[:8] == "sk_live_" || k[:8] == "rk_live_")
}

func (a *App) paymentsPage(w http.ResponseWriter, r *http.Request) {
	year := a.moneyYear(r)
	m := &moneyView{Year: year, Years: a.moneyYears(year), Payments: a.paymentsView(year)}
	m.Payments.Setup = a.paySetupView(r)
	v := adminView{Title: "Payments", Tab: "payments", Money: m}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "money-payments", v)
}

// payWith is the Card / PayPal / Both switch on Payments.
func (a *App) payWith(w http.ResponseWriter, r *http.Request) {
	to := "/admin/treasurer/payments?year=" + url.QueryEscape(r.FormValue("year"))
	v := r.FormValue("with")
	if v == "both" {
		v = ""
	}
	if err := a.store.SetPayWith(v); err != nil {
		back(w, r, to+"&err=save")
		return
	}
	back(w, r, to)
}

// paymentsDownload is a year's card payments as a spreadsheet.
func (a *App) paymentsDownload(w http.ResponseWriter, r *http.Request) {
	year := a.moneyYear(r)
	v := a.paymentsView(year)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%d card payments.csv"`, year))
	cw := csv.NewWriter(w)
	cw.Write([]string{"Date", "Member", "Plan", "Membership year", "Amount", "Fee", "Net", "Renewal", "Stripe payment", "Test"})
	for _, p := range v.Rows {
		test := ""
		if p.Mode == "test" {
			test = "test"
		}
		renew := ""
		if p.Renewed {
			renew = "yes"
		}
		cw.Write([]string{p.At.In(a.cfg.Location).Format("2006-01-02"), csvSafe(p.Name), planLabel(p.Plan), strconv.Itoa(p.Year),
			moneyInput(p.Cents), moneyInput(p.Fee), moneyInput(p.Cents - p.Fee), renew, p.Intent, test})
	}
	cw.Flush()
}

// memberPayments is one member's card payments, newest first, and when they
// last left the card payment page unfinished (zero if not).
func (a *App) memberPayments(m Member) ([]payRow, time.Time) {
	var out []payRow
	for _, p := range a.store.StripePayments() {
		if p.Member == m.ID {
			out = append(out, payRow{StripePayment: p})
		}
	}
	return out, a.store.Unfinished(m)
}

package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The yearly kit order: the board prices the items and opens it, members
// reserve sizes from their own link at the member price and pay with PayPal,
// and the board marks orders paid and sees the totals by size.
func TestKitOrder(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	a.store.Import([]Member{
		{Name: "Pat Rider", Email: "pat@example.com", PaidYears: []int{year}, Joined: time.Now().AddDate(-1, 0, -1)},
		{Name: "Lee Lapsed", Email: "lee@example.com", PaidYears: []int{year - 3}, Joined: time.Now().AddDate(-4, 0, 0)},
	})
	pat, _ := a.store.MemberByEmail("pat@example.com")
	lee, _ := a.store.MemberByEmail("lee@example.com")

	// The two seeded pieces have no price yet, so the page shows none.
	page := do(t, h, nil, "GET", "/kit", "", nil).Body.String()
	if !strings.Contains(page, "Closed right now") || strings.Contains(page, "Club jersey") || !strings.Contains(page, "Order any time from the team store") {
		t.Errorf("a closed, unpriced store: %s", page)
	}

	shop := a.store.Kit()
	if len(shop.Items) != 2 {
		t.Fatalf("seeded items: %+v", shop.Items)
	}
	jersey, bibs := *shop.Items[0], *shop.Items[1]
	jersey.MemberPrice, jersey.Price = 7000, 9500
	bibs.MemberPrice, bibs.Price = 8000, 11000
	a.store.SaveKitItem(jersey)
	a.store.SaveKitItem(bibs)
	tomorrow := time.Now().In(clubTZ).AddDate(0, 0, 1).Format("2006-01-02")
	ct, body := form(url.Values{"name": {"2027 club kit order"}, "open": {"1"}, "closes": {tomorrow}, "pay_url": {"https://paypal.me/paxvelo"}})
	if w := do(t, h, admin, "POST", "/admin/kit/shop", ct, body); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "ok=kit-saved") {
		t.Fatalf("saving the shop: %d %s", w.Code, w.Header().Get("Location"))
	}

	// A visitor sees the prices but no form.
	page = do(t, h, nil, "GET", "/kit", "", nil).Body.String()
	if !strings.Contains(page, "Open now") || !strings.Contains(page, "$70.00") || !strings.Contains(page, "Save $25.00") || strings.Contains(page, "Reserve my kit") {
		t.Errorf("a visitor's page: %s", page)
	}

	// Pat's link shows the form, and remembers them on /kit.
	w := do(t, h, nil, "GET", "/kit?t="+pat.Token, "", nil)
	if !strings.Contains(w.Body.String(), "Reserve my kit") || !strings.Contains(w.Header().Get("Set-Cookie"), "pv_kit=") {
		t.Errorf("a member's page: %s", w.Body)
	}
	// Jersey in M (size 2) and two bibs in L (size 3).
	ct, body = form(url.Values{"t": {pat.Token}, "q-1-2": {"1"}, "q-2-3": {"2"}, "q-2-0": {"0"}, "note": {"Long legs"}})
	if w := do(t, h, nil, "POST", "/kit/order", ct, body); w.Code != http.StatusSeeOther {
		t.Fatalf("ordering: %d %s", w.Code, w.Body)
	}
	o, ok := a.store.KitOrderFor(pat.ID)
	if !ok || o.Total() != 23000 || o.Summary() != "Club jersey M, Bib shorts L ×2" || o.Note != "Long legs" {
		t.Fatalf("Pat's order: %+v", o)
	}
	page = do(t, h, nil, "GET", "/kit?t="+pat.Token+"&saved=1", "", nil).Body.String()
	if !strings.Contains(page, "https://paypal.me/paxvelo/230.00USD") || !strings.Contains(page, "Save changes") {
		t.Errorf("after ordering, the pay button: %s", page)
	}
	if page := do(t, h, nil, "GET", "/membership?t="+pat.Token, "", nil).Body.String(); !strings.Contains(page, "See my order") || !strings.Contains(page, "2027 club kit order is open until") {
		t.Errorf("their membership page should point to the order: %s", page)
	}

	// Lee's dues ran out: no member price.
	if page := do(t, h, nil, "GET", "/kit?t="+lee.Token, "", nil).Body.String(); !strings.Contains(page, "membership has run out") || strings.Contains(page, "Reserve my kit") {
		t.Errorf("a lapsed member's page: %s", page)
	}
	ct, body = form(url.Values{"t": {lee.Token}, "q-1-2": {"1"}})
	if w := do(t, h, nil, "POST", "/kit/order", ct, body); w.Code != http.StatusBadRequest {
		t.Errorf("a lapsed member ordering: %d", w.Code)
	}

	// The board's page, totals and spreadsheet.
	if page := do(t, h, admin, "GET", "/admin/kit", "", nil).Body.String(); !strings.Contains(page, "Pat Rider") || !strings.Contains(page, "Mark paid") || !strings.Contains(page, "$0.00</strong> <span class=\"muted\">of $230.00") {
		t.Errorf("the admin page: %s", page)
	}
	if csv := do(t, h, admin, "GET", "/admin/kit/orders.csv", "", nil).Body.String(); !strings.Contains(csv, "Pat Rider,pat@example.com,Bib shorts,L,2,80.00,160.00,230.00,,Long legs") {
		t.Errorf("csv: %s", csv)
	}
	tally := a.store.Kit().Tally(a.store.Kit().RoundOrders(1))
	if len(tally) != 2 || tally[0].Item != "Club jersey" || tally[1].Total != 2 || tally[1].Sizes[0].Size != "L" {
		t.Errorf("tally: %+v", tally)
	}

	// Paid: it can't change from the member's link any more.
	ct, body = form(url.Values{"paid": {"1"}})
	do(t, h, admin, "POST", "/admin/kit/orders/"+strconv.Itoa(o.ID)+"/paid", ct, body)
	ct, body = form(url.Values{"t": {pat.Token}, "q-1-2": {"3"}})
	if w := do(t, h, nil, "POST", "/kit/order", ct, body); w.Code != http.StatusConflict {
		t.Errorf("changing a paid order: %d", w.Code)
	}
	if o, _ := a.store.KitOrderFor(pat.ID); !o.Paid() || o.Total() != 23000 {
		t.Errorf("the paid order should stay: %+v", o)
	}

	// The email: each member's own link.
	if w := do(t, h, admin, "POST", "/admin/kit/email", "", nil); !strings.Contains(w.Header().Get("Location"), "ok=kit-drafted") {
		t.Fatalf("kit email: %s", w.Header().Get("Location"))
	}
	d := a.store.Draft()
	if d.Subject != "2027 club kit order is open" || !strings.Contains(d.Body, "[[Reserve my kit]({kit_link})]") || !strings.Contains(d.Body, "**Club jersey:** $70.00 (regular price $95.00)") {
		t.Errorf("draft: %+v", d)
	}
	if got := a.mailer.personalize("{kit_link}", pat); got != a.cfg.SiteURL+"/kit?t="+pat.Token {
		t.Errorf("{kit_link} = %q", got)
	}

	// Closed: nothing changes.
	yesterday := time.Now().In(clubTZ).AddDate(0, 0, -1).Format("2006-01-02")
	a.store.SetKitShop("2027 club kit order", true, yesterday, "")
	if _, err := a.store.PlaceKitOrder(pat, map[int]map[string]int{1: {"S": 1}}, "", time.Now()); err != errKitClosed {
		t.Errorf("ordering after it closed: %v", err)
	}

	// Next year's order starts empty; this year's orders stay.
	ct, body = form(url.Values{"name": {"2028 club kit order"}})
	do(t, h, admin, "POST", "/admin/kit/round", ct, body)
	if _, ok := a.store.KitOrderFor(pat.ID); ok {
		t.Error("a new round starts with no orders")
	}
	if k := a.store.Kit(); k.Open || len(k.RoundOrders(1)) != 1 || len(k.Rounds()) != 2 {
		t.Errorf("new round: open %v, rounds %v", k.Open, k.Rounds())
	}
}

// The pay button adds the total to a PayPal.me link; any other link is used as is.
func TestKitPayLink(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"https://paypal.me/paxvelo", "https://paypal.me/paxvelo/45.50USD"},
		{"https://www.paypal.me/paxvelo/", "https://www.paypal.me/paxvelo/45.50USD"},
		{"https://www.paypal.com/ncp/payment/ABC", "https://www.paypal.com/ncp/payment/ABC"},
		{"", ""},
	} {
		if got := (KitShop{PayURL: c.url}).PayLink(4550); got != c.want {
			t.Errorf("%q: %q, want %q", c.url, got, c.want)
		}
	}
}


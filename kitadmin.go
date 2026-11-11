package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Website → Club kit (board): the store's items, the order window and the
// orders, with the totals by size for Jakroo (kit.go).

type kitAdminView struct {
	Shop      KitShop
	Round     int // the round shown
	Current   bool
	Open      bool
	Orders    []KitOrder
	Tally     []KitTally
	Total     int64
	PaidTotal int64
	Paid      int
	Removed   *KitItem
	Edit      int // the item whose box opens
}

func (a *App) kitAdmin(w http.ResponseWriter, r *http.Request) {
	shop := a.store.Kit()
	v := kitAdminView{Shop: shop, Round: shop.Round, Open: shop.IsOpen(time.Now()), Removed: shop.Removed}
	if n, err := strconv.Atoi(r.URL.Query().Get("round")); err == nil && n > 0 {
		v.Round = n
	}
	v.Current = v.Round == shop.Round
	v.Edit, _ = strconv.Atoi(r.URL.Query().Get("item"))
	v.Orders = shop.RoundOrders(v.Round)
	v.Tally = shop.Tally(v.Orders)
	for _, o := range v.Orders {
		v.Total += o.Total()
		if o.Paid() {
			v.PaidTotal += o.Total()
			v.Paid++
		}
	}
	flash, errMsg := flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "kit", adminView{Title: "Club kit", Tab: "kit", Flash: flash, Error: errMsg, Kit: &v})
}

func (a *App) saveKitShop(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	closes := strings.TrimSpace(r.FormValue("closes"))
	if closes != "" {
		if _, err := time.Parse("2006-01-02", closes); err != nil {
			back(w, r, "/admin/kit?err=kit-date")
			return
		}
	}
	pay := strings.TrimSpace(r.FormValue("pay_url"))
	if pay != "" {
		if u, err := url.Parse(pay); err != nil || u.Scheme != "https" || u.Host == "" {
			back(w, r, "/admin/kit?err=kit-pay")
			return
		}
	}
	name := cleanLine(r.FormValue("name"), 80)
	if err := a.store.SetKitShop(name, r.FormValue("open") != "", closes, pay); err != nil {
		back(w, r, "/admin/kit?err=save")
		return
	}
	back(w, r, "/admin/kit?ok=kit-saved")
}

// newKitRound starts next year's order.
func (a *App) newKitRound(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := cleanLine(r.FormValue("name"), 80)
	if name == "" {
		name = strconv.Itoa(time.Now().In(clubTZ).Year()+1) + " club kit order"
	}
	if err := a.store.NewKitRound(name); err != nil {
		back(w, r, "/admin/kit?err=save")
		return
	}
	back(w, r, "/admin/kit?ok=kit-round")
}

// saveKitItem adds or changes one item, with its photo.
func (a *App) saveKitItem(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPhoto+(1<<20))
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		back(w, r, "/admin/kit?err=save")
		return
	}
	id, _ := strconv.Atoi(r.FormValue("id"))
	it := KitItem{ID: id}
	if id != 0 {
		for _, old := range a.store.Kit().Items {
			if old.ID == id {
				it = *old
			}
		}
	}
	it.Name = cleanLine(r.FormValue("name"), 80)
	it.Desc = cleanLine(r.FormValue("desc"), 300)
	it.Hidden = r.FormValue("hidden") != ""
	if it.Name == "" {
		back(w, r, "/admin/kit?err=name#items")
		return
	}
	var ok1, ok2 bool
	it.MemberPrice, ok1 = parseMoney(or(strings.TrimSpace(r.FormValue("member_price")), "0"))
	it.Price, ok2 = parseMoney(or(strings.TrimSpace(r.FormValue("price")), "0"))
	if !ok1 || !ok2 || it.MemberPrice < 0 || it.Price < 0 {
		back(w, r, "/admin/kit?err=kit-amount&item="+strconv.Itoa(id)+"#items")
		return
	}
	it.Sizes = nil
	for _, s := range strings.Split(r.FormValue("sizes"), ",") {
		if s = cleanLine(s, 12); s != "" && !containsFold(it.Sizes, s) {
			it.Sizes = append(it.Sizes, s)
		}
	}
	if len(it.Sizes) == 0 {
		it.Sizes = []string{"One size"}
	}
	if r.FormValue("photo_remove") != "" {
		it.Photo = ""
	}
	if src, err := a.savePhoto(r, "photo"); err != nil {
		back(w, r, "/admin/kit?err=kit-photo#items")
		return
	} else if src != "" {
		it.Photo = src
	}
	saved, err := a.store.SaveKitItem(it)
	if err != nil {
		back(w, r, "/admin/kit?err=save")
		return
	}
	back(w, r, "/admin/kit?ok=kit-item#item-"+strconv.Itoa(saved))
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// cleanLine is s on one line, trimmed and cut to n characters.
func cleanLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

func (a *App) moveKitItem(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	by := 1
	if r.FormValue("dir") == "up" {
		by = -1
	}
	a.store.MoveKitItem(id, by)
	back(w, r, "/admin/kit#item-"+strconv.Itoa(id))
}

func (a *App) removeKitItem(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if _, err := a.store.RemoveKitItem(id); err != nil {
		back(w, r, "/admin/kit?err=save")
		return
	}
	back(w, r, "/admin/kit#items")
}

func (a *App) restoreKitItem(w http.ResponseWriter, r *http.Request) {
	if err := a.store.RestoreKitItem(); err != nil {
		back(w, r, "/admin/kit?err=save")
		return
	}
	back(w, r, "/admin/kit?ok=kit-restored#items")
}

func (a *App) kitPaid(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	r.ParseForm()
	paid := r.FormValue("paid") == "1"
	if err := a.store.SetKitPaid(id, paid, time.Now()); err != nil {
		back(w, r, "/admin/kit?err=save")
		return
	}
	code := "kit-paid"
	if !paid {
		code = "kit-unpaid"
	}
	back(w, r, "/admin/kit?ok="+code+"#o"+strconv.Itoa(id))
}

// kitEmail writes an email about the open order into the Email members
// draft, with each member's own order link as a button.
func (a *App) kitEmail(w http.ResponseWriter, r *http.Request) {
	shop := a.store.Kit()
	if d := a.store.Draft(); d.Subject != "" || d.Body != "" {
		back(w, r, "/admin/email?err=kit-draft")
		return
	}
	name := or(shop.Name, "The club kit order")
	var b strings.Builder
	b.WriteString("Hi {first_name},\n\n")
	b.WriteString("The club’s kit order is open")
	if l := shop.ClosesLabel(); l != "" {
		b.WriteString(" until " + l)
	}
	b.WriteString(". We order together once a year, and as a member you get the club price:\n\n")
	for _, it := range shop.Items {
		if !it.Shown() {
			continue
		}
		line := "- **" + it.Name + ":** " + money(it.MemberPrice)
		if it.Saving() > 0 {
			line += " (regular price " + money(it.Price) + ")"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nPick your pieces and sizes here. The link is just for you, and you can change your order until it closes:\n\n")
	b.WriteString("[[Reserve my kit]({kit_link})]\n\n")
	if shop.PayURL != "" {
		b.WriteString("Once you’ve reserved, the page has a button to pay with PayPal. Your order goes in once it’s paid.\n\n")
	}
	b.WriteString("Not sure of your size? Ask on Discord: plenty of members have been through it.\n\nSee you on the road!\nPaxVelo")
	if err := a.store.SetDraft(EmailDraft{Subject: name + " is open", Body: b.String()}); err != nil {
		back(w, r, "/admin/email?err=save")
		return
	}
	back(w, r, "/admin/email?ok=kit-drafted")
}

// kitCSV is a round's orders as a spreadsheet: one row per piece and size.
func (a *App) kitCSV(w http.ResponseWriter, r *http.Request) {
	shop := a.store.Kit()
	round := shop.Round
	if n, err := strconv.Atoi(r.URL.Query().Get("round")); err == nil && n > 0 {
		round = n
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	cw.Write([]string{"Name", "Email", "Item", "Size", "Qty", "Each", "Line total", "Order total", "Paid on", "Note"})
	for _, o := range shop.RoundOrders(round) {
		for _, l := range o.Lines {
			cw.Write([]string{o.Name, o.Email, l.Name, l.Size, strconv.Itoa(l.Qty), moneyInput(l.Each), moneyInput(l.LineTotal()), moneyInput(o.Total()), o.PaidOn, o.Note})
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		log.Printf("kit csv: %v", err)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="kit-orders-%d.csv"`, round))
	w.Write(buf.Bytes())
}

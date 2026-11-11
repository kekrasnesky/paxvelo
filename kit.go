package main

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// The club kit store (Website → Club kit, the public /kit page). Once a year
// the club puts in one bulk order with Jakroo. Members reserve their pieces
// and sizes from their own link (/kit?t=<member token>, sent in a club email
// with {kit_link}), see the member price the club's subsidy brings it down
// to, and pay their total with PayPal; admins press Mark paid, and the
// totals by size are what goes to Jakroo. The team store stays open all year
// at the regular price.
//
// The store is a list of items the board builds (photo, name, prices,
// sizes), so a new piece (gloves, a vest) is a new card, not new code. Each
// bulk order is a round: starting next year's keeps the old orders, and a
// member has one order per round, changeable until it closes or is paid.

type KitShop struct {
	Name     string      `json:"name,omitempty"`    // "2027 club kit order"
	Open     bool        `json:"open,omitempty"`    // taking orders (until Closes)
	Closes   string      `json:"closes,omitempty"`  // last day to order, 2006-01-02; empty = until it's switched off
	PayURL   string      `json:"pay_url,omitempty"` // a PayPal.me link: the member's total is added to it
	Round    int         `json:"round"`
	NextItem int         `json:"next_item,omitempty"`
	Items    []*KitItem  `json:"items,omitempty"`
	NextOrd  int         `json:"next_order,omitempty"`
	Orders   []*KitOrder `json:"orders,omitempty"`
	Seeded   bool        `json:"seeded,omitempty"`
	Removed  *KitItem    `json:"removed,omitempty"` // the last item removed, for Undo
}

type KitItem struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Desc        string   `json:"desc,omitempty"`
	Photo       string   `json:"photo,omitempty"`
	MemberPrice int64    `json:"member_price"` // cents: what a member pays in the bulk order
	Price       int64    `json:"price"`        // cents: the regular price (the team store)
	Sizes       []string `json:"sizes"`
	Hidden      bool     `json:"hidden,omitempty"`
}

// Saving is how much the club's subsidy takes off the regular price.
func (i KitItem) Saving() int64 {
	if i.Price > i.MemberPrice {
		return i.Price - i.MemberPrice
	}
	return 0
}

// Shown says whether members see it: not hidden, and priced.
func (i KitItem) Shown() bool { return !i.Hidden && i.MemberPrice > 0 }

// SizeList is its sizes as the admin types them.
func (i KitItem) SizeList() string { return strings.Join(i.Sizes, ", ") }

type KitOrder struct {
	ID      int       `json:"id"`
	Round   int       `json:"round"`
	Member  int       `json:"member"`
	Name    string    `json:"name"`
	Email   string    `json:"email"`
	Lines   []KitLine `json:"lines"`
	Note    string    `json:"note,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	PaidOn  string    `json:"paid_on,omitempty"` // 2006-01-02, when an admin marked it paid
}

// KitLine is one piece in one size. Name and Each are kept as they were
// when ordered, so changing an item's price later doesn't change an order.
type KitLine struct {
	Item int    `json:"item"`
	Name string `json:"name"`
	Size string `json:"size"`
	Qty  int    `json:"qty"`
	Each int64  `json:"each"` // cents
}

// LineTotal is the line's price: each × how many.
func (l KitLine) LineTotal() int64 { return int64(l.Qty) * l.Each }

func (o KitOrder) Total() int64 {
	var t int64
	for _, l := range o.Lines {
		t += int64(l.Qty) * l.Each
	}
	return t
}

func (o KitOrder) Paid() bool { return o.PaidOn != "" }

// Summary is the order on one line: "Club jersey M ×2, Bib shorts L".
func (o KitOrder) Summary() string {
	var parts []string
	for _, l := range o.Lines {
		s := l.Name + " " + l.Size
		if l.Qty > 1 {
			s += fmt.Sprintf(" ×%d", l.Qty)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// Qty is how many of an item in a size the order has (for the form).
func (o *KitOrder) Qty(item int, size string) int {
	if o == nil {
		return 0
	}
	for _, l := range o.Lines {
		if l.Item == item && l.Size == size {
			return l.Qty
		}
	}
	return 0
}

// kitSizes are a new item's sizes until the board changes them.
var kitSizes = []string{"XS", "S", "M", "L", "XL", "2XL", "3XL"}

// maxKitQty keeps a typo (11 jerseys) from going in.
const maxKitQty = 5

// IsOpen says whether members can order today.
func (k KitShop) IsOpen(now time.Time) bool {
	return k.Open && (k.Closes == "" || now.In(clubTZ).Format("2006-01-02") <= k.Closes)
}

// ClosesLabel is the last day to order, like "Sunday, January 31".
func (k KitShop) ClosesLabel() string {
	t, err := time.Parse("2006-01-02", k.Closes)
	if err != nil {
		return ""
	}
	return t.Format("Monday, January 2")
}

// PayLink is where a member pays total: the PayPal.me link with the amount
// on the end, so PayPal fills it in. Any other link is used as it is.
func (k KitShop) PayLink(total int64) string {
	if k.PayURL == "" || total <= 0 {
		return ""
	}
	u, err := url.Parse(k.PayURL)
	if err != nil {
		return ""
	}
	if host := strings.TrimPrefix(strings.ToLower(u.Host), "www."); host == "paypal.me" {
		return strings.TrimRight(k.PayURL, "/") + "/" + moneyInput(total) + "USD"
	}
	return k.PayURL
}

// KitTally is how many of each item in each size a round's orders add up
// to: the bulk order for Jakroo.
type KitTally struct {
	Item  string
	Sizes []KitCount
	Total int
	Paid  int // pieces in paid orders
}

type KitCount struct {
	Size      string
	Qty, Paid int
}

// ---- Store ----

// seedKit starts the store with the club's two pieces, unpriced (so not on
// the website) until the board fills in this year's prices. Callers hold s.mu.
func (s *Store) seedKit() {
	k := &s.d.Kit
	if k.Seeded {
		return
	}
	k.Seeded = true
	if k.Round == 0 {
		k.Round = 1
	}
	for _, it := range []KitItem{
		{Name: "Club jersey", Desc: "Short sleeves, the club’s blue design.", Photo: "/photos/kit-jersey.webp"},
		{Name: "Bib shorts", Desc: "Black, with the club’s colors on the legs.", Photo: "/photos/kit-bibs.webp"},
	} {
		k.NextItem++
		it.ID, it.Sizes = k.NextItem, slices.Clone(kitSizes)
		k.Items = append(k.Items, &it)
	}
}

// Kit returns a copy of the store.
func (s *Store) Kit() KitShop {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.d.Kit
	k.Items = make([]*KitItem, len(s.d.Kit.Items))
	for i, it := range s.d.Kit.Items {
		c := *it
		c.Sizes = slices.Clone(it.Sizes)
		k.Items[i] = &c
	}
	k.Orders = make([]*KitOrder, len(s.d.Kit.Orders))
	for i, o := range s.d.Kit.Orders {
		c := *o
		c.Lines = slices.Clone(o.Lines)
		k.Orders[i] = &c
	}
	return k
}

// SetKitShop saves the order window and pay link.
func (s *Store) SetKitShop(name string, open bool, closes, payURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &s.d.Kit
	k.Name, k.Open, k.Closes, k.PayURL = name, open, closes, payURL
	return s.save()
}

// NewKitRound starts next year's order: closed, with no orders yet. The old
// round's orders stay.
func (s *Store) NewKitRound(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &s.d.Kit
	k.Round++
	k.Open, k.Closes, k.Name = false, "", name
	return s.save()
}

// SaveKitItem adds an item (ID 0) or changes one.
func (s *Store) SaveKitItem(it KitItem) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &s.d.Kit
	if it.ID == 0 {
		k.NextItem++
		it.ID = k.NextItem
		k.Items = append(k.Items, &it)
		return it.ID, s.save()
	}
	for _, old := range k.Items {
		if old.ID == it.ID {
			*old = it
			return it.ID, s.save()
		}
	}
	return 0, errNotFound
}

// MoveKitItem moves an item one place up (-1) or down (+1).
func (s *Store) MoveKitItem(id, by int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.d.Kit.Items
	i := slices.IndexFunc(items, func(it *KitItem) bool { return it.ID == id })
	j := i + by
	if i < 0 || j < 0 || j >= len(items) {
		return nil
	}
	items[i], items[j] = items[j], items[i]
	return s.save()
}

// RemoveKitItem takes an item out of the store. Orders keep their lines.
func (s *Store) RemoveKitItem(id int) (KitItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &s.d.Kit
	i := slices.IndexFunc(k.Items, func(it *KitItem) bool { return it.ID == id })
	if i < 0 {
		return KitItem{}, errNotFound
	}
	it := *k.Items[i]
	k.Items = slices.Delete(k.Items, i, i+1)
	k.Removed = &it
	return it, s.save()
}

// RestoreKitItem puts the last removed item back at the end.
func (s *Store) RestoreKitItem() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &s.d.Kit
	if k.Removed == nil {
		return nil
	}
	it := *k.Removed
	k.Items, k.Removed = append(k.Items, &it), nil
	return s.save()
}

// KitOrderFor is a member's order in the current round, if they have one.
func (s *Store) KitOrderFor(member int) (KitOrder, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.d.Kit.Orders {
		if o.Member == member && o.Round == s.d.Kit.Round {
			c := *o
			c.Lines = slices.Clone(o.Lines)
			return c, true
		}
	}
	return KitOrder{}, false
}

var (
	errKitClosed = errors.New("the kit order is closed")
	errKitPaid   = errors.New("this order is paid")
)

// PlaceKitOrder sets a member's order in the current round to want (item ID
// → size → how many), priced at today's member prices. Nothing wanted
// removes the order. A paid order can't change here.
func (s *Store) PlaceKitOrder(m Member, want map[int]map[string]int, note string, now time.Time) (KitOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := &s.d.Kit
	if !k.IsOpen(now) {
		return KitOrder{}, errKitClosed
	}
	var lines []KitLine
	for _, it := range k.Items {
		if !it.Shown() {
			continue
		}
		for _, size := range it.Sizes {
			if q := want[it.ID][size]; q > 0 {
				lines = append(lines, KitLine{Item: it.ID, Name: it.Name, Size: size, Qty: min(q, maxKitQty), Each: it.MemberPrice})
			}
		}
	}
	i := slices.IndexFunc(k.Orders, func(o *KitOrder) bool { return o.Member == m.ID && o.Round == k.Round })
	if i >= 0 && k.Orders[i].Paid() {
		return *k.Orders[i], errKitPaid
	}
	if len(lines) == 0 {
		if i >= 0 {
			k.Orders = slices.Delete(k.Orders, i, i+1)
			return KitOrder{}, s.save()
		}
		return KitOrder{}, nil
	}
	if i < 0 {
		k.NextOrd++
		k.Orders = append(k.Orders, &KitOrder{ID: k.NextOrd, Round: k.Round, Member: m.ID, Created: now.UTC()})
		i = len(k.Orders) - 1
	}
	o := k.Orders[i]
	o.Name, o.Email, o.Lines, o.Note, o.Updated = m.Name, m.Email, lines, note, now.UTC()
	return *o, s.save()
}

// SetKitPaid marks an order paid today, or not paid.
func (s *Store) SetKitPaid(id int, paid bool, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.d.Kit.Orders {
		if o.ID == id {
			o.PaidOn = ""
			if paid {
				o.PaidOn = now.In(clubTZ).Format("2006-01-02")
			}
			return s.save()
		}
	}
	return errNotFound
}

// RoundOrders are a round's orders, oldest first.
func (k KitShop) RoundOrders(round int) []KitOrder {
	var out []KitOrder
	for _, o := range k.Orders {
		if o.Round == round {
			out = append(out, *o)
		}
	}
	return out
}

// Rounds are the rounds that have orders, plus the current one, newest first.
func (k KitShop) Rounds() []int {
	out := []int{k.Round}
	for _, o := range k.Orders {
		if !slices.Contains(out, o.Round) {
			out = append(out, o.Round)
		}
	}
	slices.Sort(out)
	slices.Reverse(out)
	return out
}

// Tally adds up orders by item and size, in the store's order of items and
// sizes, with anything no longer in the store at the end.
func (k KitShop) Tally(orders []KitOrder) []KitTally {
	type key struct{ name, size string }
	qty, paid := map[key]int{}, map[key]int{}
	var names []string
	sizes := map[string][]string{}
	add := func(name, size string) {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
		if !slices.Contains(sizes[name], size) {
			sizes[name] = append(sizes[name], size)
		}
	}
	for _, it := range k.Items {
		for _, sz := range it.Sizes {
			sizes[it.Name] = append(sizes[it.Name], sz)
		}
	}
	for _, o := range orders {
		for _, l := range o.Lines {
			qty[key{l.Name, l.Size}] += l.Qty
			if o.Paid() {
				paid[key{l.Name, l.Size}] += l.Qty
			}
		}
	}
	for _, it := range k.Items { // store order first
		for _, o := range orders {
			for _, l := range o.Lines {
				if l.Name == it.Name {
					add(l.Name, l.Size)
				}
			}
		}
	}
	for _, o := range orders {
		for _, l := range o.Lines {
			add(l.Name, l.Size)
		}
	}
	var out []KitTally
	for _, n := range names {
		t := KitTally{Item: n}
		for _, sz := range sizes[n] {
			if q := qty[key{n, sz}]; q > 0 {
				t.Sizes = append(t.Sizes, KitCount{Size: sz, Qty: q, Paid: paid[key{n, sz}]})
				t.Total += q
				t.Paid += paid[key{n, sz}]
			}
		}
		out = append(out, t)
	}
	return out
}

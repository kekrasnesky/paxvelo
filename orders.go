package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A Squarespace orders export (Commerce → Orders → Download CSV) holds each
// dues payment with its date. Uploaded on Members → Import from a
// spreadsheet, it sets each payer's dues to run a year from the day they
// paid: their join day becomes the payment day (keeping the year they first
// joined) and the year paid is the year of the payment, so DuesExpire is the
// payment date a year on. People not on the list yet are added. Addresses and
// phone numbers in the file are not used.

// duesOrder is someone's latest membership payment in the file.
type duesOrder struct {
	Email, Name, Plan string
	Paid              time.Time
}

// bom is the byte-order mark some spreadsheets put before the first heading.
const bom = string(rune(0xFEFF))

// isOrdersExport tells a Squarespace orders export from a member list.
func isOrdersExport(header []string) bool {
	has := func(name string) bool {
		return slices.ContainsFunc(header, func(h string) bool {
			return strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(h, bom)), name)
		})
	}
	return has("Paid at") && has("Lineitem name") && has("Email")
}

// parseOrders reads the membership payments from an orders export, keeping
// each person's latest. Refunded, cancelled and unpaid orders don't count.
func parseOrders(records [][]string, loc *time.Location) []duesOrder {
	col := map[string]int{}
	for i, h := range records[0] {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, bom)))] = i
	}
	byEmail := map[string]duesOrder{}
	var order []string
	for _, rec := range records[1:] {
		get := func(name string) string {
			if i, ok := col[name]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		email, ok := cleanEmail(get("email"))
		item := strings.ToLower(get("lineitem name"))
		if !ok || !strings.Contains(item, "membership") || get("cancelled at") != "" ||
			(get("financial status") != "" && !strings.EqualFold(get("financial status"), "paid")) {
			continue
		}
		if r, _ := strconv.ParseFloat(get("amount refunded"), 64); r > 0 {
			continue
		}
		paid := orderDate(or(get("paid at"), get("created at")), loc)
		if paid.IsZero() {
			continue
		}
		o := duesOrder{Email: email, Name: cleanName(get("billing name")), Plan: "individual", Paid: paid}
		if strings.Contains(item, "family") {
			o.Plan = "family"
		}
		if prev, seen := byEmail[email]; !seen {
			order = append(order, email)
		} else if !paid.After(prev.Paid) {
			continue
		}
		byEmail[email] = o
	}
	list := make([]duesOrder, len(order))
	for i, e := range order {
		list[i] = byEmail[e]
	}
	return list
}

// orderDate reads Squarespace's "10/7/2026 8:03" (club time) as noon on that
// day, the way join dates are kept.
func orderDate(s string, loc *time.Location) time.Time {
	if i := strings.IndexByte(s, ' '); i > 0 {
		s = s[:i]
	}
	for _, layout := range []string{"1/2/2006", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.Add(12 * time.Hour).UTC()
		}
	}
	return time.Time{}
}

// ApplyOrders sets each payer's dues to run a year from their payment, adding
// anyone not on the list. It returns how many were updated and the IDs of
// those added.
func (s *Store) ApplyOrders(orders []duesOrder) (updated int, added []int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range orders {
		year := o.Paid.In(clubTZ).Year()
		m := s.byEmail(o.Email)
		if m == nil {
			m = s.newMember(o.Name, o.Email, o.Plan)
			m.Joined = o.Paid
			added = append(added, m.ID)
		} else {
			// Keep the year they first joined; the day moves to the payment.
			p, j := o.Paid.In(clubTZ), m.Joined.In(clubTZ)
			if m.Joined.IsZero() || j.Year() >= p.Year() {
				m.Joined = o.Paid
			} else {
				m.Joined = time.Date(j.Year(), p.Month(), p.Day(), 12, 0, 0, 0, clubTZ).UTC()
			}
			m.Plan = o.Plan
			updated++
		}
		// The payment covers its year and nothing later, so the dues end on
		// the payment day a year on.
		years := m.PaidYears[:0:0]
		for _, y := range m.PaidYears {
			if y < year {
				years = append(years, y)
			} else {
				delete(m.PaidOn, y)
			}
		}
		m.PaidYears = append(years, year)
		if m.PaidOn == nil {
			m.PaidOn = map[int]time.Time{}
		}
		m.PaidOn[year] = o.Paid
	}
	if len(orders) > 0 {
		err = s.save()
	}
	return updated, added, err
}

// importOrders is importCSV for an orders export.
func (a *App) importOrders(w http.ResponseWriter, r *http.Request, records [][]string) {
	orders := parseOrders(records, a.cfg.Location)
	if len(orders) == 0 {
		back(w, r, "/admin/members?err=no-orders")
		return
	}
	updated, added, err := a.store.ApplyOrders(orders)
	if err != nil {
		back(w, r, "/admin/members?err=save")
		return
	}
	ids := make([]string, len(added))
	for i, id := range added {
		ids[i] = strconv.Itoa(id)
	}
	back(w, r, fmt.Sprintf("/admin/members?ok=orders&n=%d&added=%s", updated, strings.Join(ids, ",")))
}

// ordersFlash is the message after an orders import, naming anyone who was
// added (by ID in the address, so no names or emails go in it).
func (a *App) ordersFlash(r *http.Request) string {
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("n"))
	flash := fmt.Sprintf("Dues set from the orders file: %d %s now paid up until a year after their payment.", n, plural(n, "member is", "members are"))
	var names []string
	for _, s := range strings.Split(q.Get("added"), ",") {
		if id, err := strconv.Atoi(s); err == nil {
			if m, ok := a.store.MemberByID(id); ok {
				names = append(names, m.Name+" ("+m.Email+")")
			}
		}
	}
	if len(names) > 0 {
		flash += fmt.Sprintf(" %d weren’t on the list and were added: %s.", len(names), strings.Join(names, ", "))
	}
	return flash
}

// readCSV reads an uploaded spreadsheet's rows.
func readCSV(f io.Reader) ([][]string, error) {
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true
	return cr.ReadAll()
}

package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The treasurer's books (admin Treasurer tab): expenses and income lines
// typed in by hand, each with a category and an optional receipt, the
// year's budget, and the board report built from them. Membership income is
// worked out from the dues marked paid on Members (duesIncome), unless the
// treasurer switches that off and types it in instead. Amounts are kept in
// cents. Receipts are files in data/receipts, served only to admins.

// Treasury is everything on the Treasurer tab, kept in the data file.
type Treasury struct {
	Seeded      bool     `json:"seeded,omitempty"`
	IncomeCats  []string `json:"income_categories,omitempty"`
	ExpenseCats []string `json:"expense_categories,omitempty"`
	// DuesCat is the income category the dues marked paid on Members count
	// in; empty when the treasurer types membership income in instead.
	DuesCat    string              `json:"dues_category,omitempty"`
	NextEntry  int                 `json:"next_entry_id,omitempty"`
	Entries    []*LedgerEntry      `json:"entries,omitempty"`
	Removed    *LedgerEntry        `json:"removed_entry,omitempty"` // the one removed last, for Undo
	Years      map[int]*BudgetYear `json:"years,omitempty"`
	NextReport int                 `json:"next_report_id,omitempty"`
	Reports    []*SavedReport      `json:"reports,omitempty"`
	RemovedRep *SavedReport        `json:"removed_report,omitempty"`
}

// LedgerEntry is one expense or income line.
type LedgerEntry struct {
	ID          int       `json:"id"`
	Kind        string    `json:"kind"` // "expense" or "income"
	Date        string    `json:"date"` // 2006-01-02
	Cents       int64     `json:"cents"`
	Category    string    `json:"category"`
	What        string    `json:"what,omitempty"`
	Who         string    `json:"who,omitempty"`          // who spent it
	Receipt     string    `json:"receipt,omitempty"`      // file name in data/receipts
	ReceiptName string    `json:"receipt_name,omitempty"` // the name it was uploaded with
	AddedBy     string    `json:"added_by,omitempty"`
	Added       time.Time `json:"added"`
}

// ReceiptPDF reports whether the receipt is a PDF rather than a photo.
func (e LedgerEntry) ReceiptPDF() bool { return strings.HasSuffix(e.Receipt, ".pdf") }

// DateLabel is the date as the treasurer writes it, like 7/31/2026.
func (e LedgerEntry) DateLabel() string { return mdy(e.Date) }

// BudgetYear is what the treasurer sets for a year's report.
type BudgetYear struct {
	Beginning int64             `json:"beginning_cents,omitempty"`
	Budget    map[string]int64  `json:"budget,omitempty"`    // by category; missing = blank on the report
	RowNotes  map[string]string `json:"row_notes,omitempty"` // the Notes column, by category, or noteBeginning/noteEnding
	Notes     []string          `json:"notes,omitempty"`     // numbered notes under the report
}

const (
	noteBeginning = "#beginning"
	noteEnding    = "#ending"
)

// Barry's categories, as he keeps them in Quicken.
var (
	defaultIncomeCats  = []string{"Membership Income", "Dividend Income", "Jakroo Commission"}
	defaultExpenseCats = []string{"Donations", "Club Rides & Parties:Food & Drink", "Insurance", "Kit Expenses:Sock Order",
		"Office Expenses:PO Box Rental", "Office Expenses:Quicken", "Registration Fees", "Taxes:Prep Fees", "Bank Fees",
		"Website Fees: SquareSpace", "Website Fees: Domain/Marketing", "Website Fees: Google Workspace"}
)

// SavedReport is a report kept as it was for a board meeting.
type SavedReport struct {
	ID      int       `json:"id"`
	Meeting string    `json:"meeting"` // the meeting date, 2006-01-02
	Report  Report    `json:"report"`
	SavedBy string    `json:"saved_by,omitempty"`
	Saved   time.Time `json:"saved"`
}

// MeetingLabel is the meeting date, like November 10, 2026.
func (s SavedReport) MeetingLabel() string {
	t, err := time.Parse("2006-01-02", s.Meeting)
	if err != nil {
		return s.Meeting
	}
	return t.Format("January 2, 2006")
}

// ---- Money ----

// parseMoney reads an amount as people type it: 12, 12.5, $1,234.56, -5 or
// (5.00) for a refund.
func parseMoney(s string) (int64, bool) {
	s = strings.Join(strings.Fields(s), "")
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg, s = true, s[1:len(s)-1]
	}
	if t, ok := strings.CutPrefix(s, "-"); ok {
		neg, s = !neg, t
	}
	s = strings.ReplaceAll(strings.TrimPrefix(s, "$"), ",", "")
	if t, ok := strings.CutPrefix(s, "-"); ok {
		neg, s = !neg, t
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" && frac == "" || len(frac) > 2 || len(whole) > 10 {
		return 0, false
	}
	for _, r := range whole + frac {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	w, _ := strconv.ParseInt("0"+whole, 10, 64)
	f, _ := strconv.ParseInt((frac + "00")[:2], 10, 64)
	c := w*100 + f
	if neg {
		c = -c
	}
	return c, true
}

// money writes cents as dollars: $1,234.56, or -$5.00.
func money(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	d := strconv.FormatInt(c/100, 10)
	for i := len(d) - 3; i > 0; i -= 3 {
		d = d[:i] + "," + d[i:]
	}
	return fmt.Sprintf("%s$%s.%02d", sign, d, c%100)
}

// moneyInput writes cents for a form box: 1234.56.
func moneyInput(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

// mdy writes a 2006-01-02 date the way the treasurer does: 7/31/2026.
func mdy(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("1/2/2006")
}

// ---- Store ----

func (s *Store) seedTreasury() {
	t := &s.d.Treasury
	if t.Seeded {
		return
	}
	t.IncomeCats, t.ExpenseCats, t.DuesCat, t.Seeded = slices.Clone(defaultIncomeCats), slices.Clone(defaultExpenseCats), defaultIncomeCats[0], true
}

// TreasuryCats returns the income and expense categories, in report order,
// and the one dues count in.
func (s *Store) TreasuryCats() (income, expense []string, dues string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.d.Treasury
	return slices.Clone(t.IncomeCats), slices.Clone(t.ExpenseCats), t.DuesCat
}

// Ledger returns the expense or income lines dated in a year (every kind
// for kind ""), newest first.
func (s *Store) Ledger(kind string, year int) []LedgerEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	y := strconv.Itoa(year)
	var out []LedgerEntry
	for _, e := range s.d.Treasury.Entries {
		if (kind == "" || e.Kind == kind) && strings.HasPrefix(e.Date, y+"-") {
			out = append(out, *e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// LedgerYears lists the years with lines or a budget, newest first.
func (s *Store) LedgerYears() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int
	for _, e := range s.d.Treasury.Entries {
		if y, err := strconv.Atoi(e.Date[:min(4, len(e.Date))]); err == nil {
			out = append(out, y)
		}
	}
	for y := range s.d.Treasury.Years {
		out = append(out, y)
	}
	slices.Sort(out)
	out = slices.Compact(out)
	slices.Reverse(out)
	return out
}

// WhoSpent lists the names typed as who spent something, for suggestions.
func (s *Store) WhoSpent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.d.Treasury.Entries {
		if e.Who != "" && !slices.Contains(out, e.Who) {
			out = append(out, e.Who)
		}
	}
	return out
}

func (s *Store) LedgerEntry(id int) (LedgerEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.ledgerEntry(id); e != nil {
		return *e, true
	}
	return LedgerEntry{}, false
}

func (s *Store) ledgerEntry(id int) *LedgerEntry {
	for _, e := range s.d.Treasury.Entries {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// addCategory puts a new category at the end of its kind's list.
func (t *Treasury) addCategory(kind, name string) {
	list := &t.ExpenseCats
	if kind == "income" {
		list = &t.IncomeCats
	}
	if name != "" && !slices.Contains(*list, name) {
		*list = append(*list, name)
	}
}

func (s *Store) AddLedgerEntry(e LedgerEntry) (LedgerEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.d.Treasury
	t.NextEntry = max(t.NextEntry, 1)
	e.ID, e.Added = t.NextEntry, time.Now().UTC()
	t.NextEntry++
	t.addCategory(e.Kind, e.Category)
	c := e
	t.Entries = append(t.Entries, &c)
	return e, s.save()
}

// UpdateLedgerEntry saves changes to a line, keeping who added it and when.
func (s *Store) UpdateLedgerEntry(e LedgerEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.ledgerEntry(e.ID)
	if old == nil {
		return errors.New("that line was removed")
	}
	e.Kind, e.AddedBy, e.Added = old.Kind, old.AddedBy, old.Added
	s.d.Treasury.addCategory(e.Kind, e.Category)
	*old = e
	return s.save()
}

// RemoveLedgerEntry takes a line out, keeping it for Undo. It returns the
// receipt of the line removed before it, which can't come back any more.
func (s *Store) RemoveLedgerEntry(id int) (gone string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.d.Treasury
	i := slices.IndexFunc(t.Entries, func(e *LedgerEntry) bool { return e.ID == id })
	if i < 0 {
		return "", nil
	}
	if t.Removed != nil {
		gone = t.Removed.Receipt
	}
	t.Removed = t.Entries[i]
	t.Entries = slices.Delete(t.Entries, i, i+1)
	return gone, s.save()
}

// RestoreLedgerEntry puts back the line removed last (Undo).
func (s *Store) RestoreLedgerEntry(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.d.Treasury
	if t.Removed == nil || t.Removed.ID != id || s.ledgerEntry(id) != nil {
		return false
	}
	t.Entries = append(t.Entries, t.Removed)
	t.addCategory(t.Removed.Kind, t.Removed.Category)
	t.Removed = nil
	s.saveOrLog()
	return true
}

// Budget returns a copy of what's set for a year.
func (s *Store) Budget(year int) BudgetYear {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.d.Treasury.Years[year]
	if b == nil {
		return BudgetYear{}
	}
	return BudgetYear{Beginning: b.Beginning, Budget: cloneMap(b.Budget), RowNotes: cloneMap(b.RowNotes), Notes: slices.Clone(b.Notes)}
}

func cloneMap[V any](m map[string]V) map[string]V {
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// catRow is one category as the Budget page sends it back: its name before
// (empty for a new one) and now.
type catRow struct {
	Kind, Orig, Name string
}

// SaveBudget saves a year's budget and the category lists as the Budget page
// has them: in its order, renamed (every line, budget and note follows the
// new name) and without the rows taken out. A category with lines can't go:
// those come back at the end of their list, and their names are returned.
func (s *Store) SaveBudget(year int, b BudgetYear, rows []catRow) (kept []string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.d.Treasury
	used := map[string]bool{}
	for _, e := range t.Entries {
		used[e.Kind+"\x00"+e.Category] = true
	}
	rename := func(from, to string) {
		for _, e := range t.Entries {
			if e.Category == from {
				e.Category = to
			}
		}
		if t.Removed != nil && t.Removed.Category == from {
			t.Removed.Category = to
		}
		for _, y := range t.Years {
			if v, ok := y.Budget[from]; ok {
				delete(y.Budget, from)
				y.Budget[to] = v
			}
			if v, ok := y.RowNotes[from]; ok {
				delete(y.RowNotes, from)
				y.RowNotes[to] = v
			}
		}
		if t.DuesCat == from {
			t.DuesCat = to
		}
	}
	for _, kind := range []string{"income", "expense"} {
		old := &t.ExpenseCats
		if kind == "income" {
			old = &t.IncomeCats
		}
		var list []string
		seen := map[string]bool{}
		for _, r := range rows {
			if r.Kind != kind {
				continue
			}
			name := cleanName(r.Name)
			if name == "" {
				name = r.Orig
			}
			if name == "" || seen[strings.ToLower(name)] {
				continue
			}
			if r.Orig != "" && r.Orig != name && slices.Contains(*old, r.Orig) {
				rename(r.Orig, name)
				used[kind+"\x00"+name] = used[kind+"\x00"+r.Orig]
				delete(used, kind+"\x00"+r.Orig)
			}
			seen[strings.ToLower(name)] = true
			list = append(list, name)
		}
		for _, c := range *old {
			if !slices.Contains(list, c) && used[kind+"\x00"+c] && !seen[strings.ToLower(c)] {
				list = append(list, c)
				kept = append(kept, c)
			}
		}
		*old = list
	}
	if t.DuesCat != "" && !slices.Contains(t.IncomeCats, t.DuesCat) {
		t.DuesCat = ""
	}
	if t.Years == nil {
		t.Years = map[int]*BudgetYear{}
	}
	t.Years[year] = &b
	return kept, s.save()
}

// SetDuesCategory picks the income category membership dues count in, or
// "" to leave them out (membership income is typed in instead).
func (s *Store) SetDuesCategory(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != "" && !slices.Contains(s.d.Treasury.IncomeCats, name) {
		return errors.New("no such category")
	}
	s.d.Treasury.DuesCat = name
	return s.save()
}

// CategoryUse counts the lines in each category, by kind.
func (s *Store) CategoryUse() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, e := range s.d.Treasury.Entries {
		out[e.Kind+"\x00"+e.Category]++
	}
	return out
}

// ---- Saved reports ----

func (s *Store) SaveReport(r SavedReport) (SavedReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.d.Treasury
	t.NextReport = max(t.NextReport, 1)
	r.ID, r.Saved = t.NextReport, time.Now().UTC()
	t.NextReport++
	c := r
	t.Reports = append(t.Reports, &c)
	return r, s.save()
}

// SavedReports returns the saved reports, newest meeting first.
func (s *Store) SavedReports() []SavedReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SavedReport, 0, len(s.d.Treasury.Reports))
	for _, r := range s.d.Treasury.Reports {
		out = append(out, *r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Meeting != out[j].Meeting {
			return out[i].Meeting > out[j].Meeting
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func (s *Store) SavedReport(id int) (SavedReport, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.d.Treasury.Reports {
		if r.ID == id {
			return *r, true
		}
	}
	return SavedReport{}, false
}

func (s *Store) RemoveReport(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.d.Treasury
	i := slices.IndexFunc(t.Reports, func(r *SavedReport) bool { return r.ID == id })
	if i < 0 {
		return nil
	}
	t.RemovedRep = t.Reports[i]
	t.Reports = slices.Delete(t.Reports, i, i+1)
	return s.save()
}

func (s *Store) RestoreReport(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.d.Treasury
	if t.RemovedRep == nil || t.RemovedRep.ID != id {
		return false
	}
	t.Reports = append(t.Reports, t.RemovedRep)
	t.RemovedRep = nil
	s.saveOrLog()
	return true
}

// SetTreasurer marks an admin as the treasurer, who can remove lines and
// saved reports on the Treasurer tab, as the president can.
func (s *Store) SetTreasurer(id int, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.adminWhere(func(u *AdminUser) bool { return u.ID == id })
	if u == nil {
		return errors.New("that admin was removed")
	}
	u.Treasurer = on
	return s.save()
}

// ---- Membership income ----

// duesLine is one dues payment counted as income.
type duesLine struct {
	Member  string
	Plan    string
	Date    string // 2006-01-02
	Cents   int64
	Guessed bool // marked paid without a date (imported): counted on their join day
}

func (d duesLine) DateLabel() string { return mdy(d.Date) }

// duesIncome is a year's membership income up to a day, from what Members
// knows: each year marked paid counts once, on the day it was marked paid
// (or, from a Squarespace orders import, the day it was paid), at today's
// dues for the member's plan. Years imported without a date count on the
// member's join day in that year. Deleted members aren't counted.
func (a *App) duesIncome(year int, through string) (total int64, individual, family int, lines []duesLine) {
	y := strconv.Itoa(year)
	for _, m := range a.store.Members() {
		for _, p := range m.PaidYears {
			t, ok := m.PaidOn[p]
			if !ok {
				t = m.anniversary(p)
			}
			d := t.In(a.cfg.Location).Format("2006-01-02")
			if !strings.HasPrefix(d, y+"-") || d > through {
				continue
			}
			c := int64(a.cfg.Dues(m.Plan)) * 100
			total += c
			if m.Plan == "family" {
				family++
			} else {
				individual++
			}
			lines = append(lines, duesLine{Member: m.Name, Plan: m.PlanName(), Date: d, Cents: c, Guessed: !ok})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Date > lines[j].Date })
	return total, individual, family, lines
}

// ---- The board report ----

// Report is the treasurer's Income/Expense by Category report for a year,
// up to a day, laid out like the one Barry sends the board.
type Report struct {
	Year    int         `json:"year"`
	Through string      `json:"through"` // 2006-01-02
	Rows    []ReportRow `json:"rows"`
	Notes   []string    `json:"notes,omitempty"`
}

// ReportRow is a line of the report. Kind is "balance", "head", "line",
// "total" or "gap" (an empty line).
type ReportRow struct {
	Kind      string `json:"kind"`
	Label     string `json:"label,omitempty"`
	Budget    int64  `json:"budget,omitempty"`
	HasBudget bool   `json:"has_budget,omitempty"`
	Actual    int64  `json:"actual,omitempty"`
	HasActual bool   `json:"has_actual,omitempty"`
	Note      string `json:"note,omitempty"`
}

const reportTitle = "PaxVelo Income/Expense by Category"

func (r Report) Title() string { return reportTitle }

// Subtitle is like "2026 Budget - YTD Actuals through 7/31/2026".
func (r Report) Subtitle() string {
	return fmt.Sprintf("%d Budget - YTD Actuals through %s", r.Year, mdy(r.Through))
}

// Heads are the column headings.
func (r Report) Heads() []string {
	return []string{fmt.Sprintf("%d Budget", r.Year), "YTD Actuals", "Notes"}
}

// BudgetText and ActualText are a row's amounts as the report shows them:
// blank when there's none, "$ -" for zero.
func (r ReportRow) BudgetText() string { return reportMoney(r.Budget, r.HasBudget) }
func (r ReportRow) ActualText() string { return reportMoney(r.Actual, r.HasActual) }

func reportMoney(c int64, has bool) string {
	switch {
	case !has:
		return ""
	case c == 0:
		return "$ -"
	}
	return money(c)
}

// buildReport adds up a year's lines from January 1 to through.
func (a *App) buildReport(through time.Time) Report {
	year := through.Year()
	thru := through.Format("2006-01-02")
	rep := Report{Year: year, Through: thru}
	income, expense, duesCat := a.store.TreasuryCats()
	b := a.store.Budget(year)
	rep.Notes = b.Notes

	actual := map[string]int64{}
	has := map[string]bool{}
	for _, e := range a.store.Ledger("", year) {
		if e.Date > thru {
			continue
		}
		k := e.Kind + "\x00" + e.Category
		actual[k] += e.Cents
		has[k] = true
		// a line whose category isn't on the lists (shouldn't happen) still counts
		list := &expense
		if e.Kind == "income" {
			list = &income
		}
		if !slices.Contains(*list, e.Category) {
			*list = append(*list, e.Category)
		}
	}
	duesLabel := ""
	if duesCat != "" {
		total, ind, fam, _ := a.duesIncome(year, thru)
		k := "income\x00" + duesCat
		actual[k] += total
		has[k] = true
		duesLabel = fmt.Sprintf("%s (%d Individual/%d Family)", duesCat, ind, fam)
	}

	balance := func(label, note string, v int64) ReportRow {
		return ReportRow{Kind: "balance", Label: label, Budget: v, HasBudget: true, Actual: v, HasActual: true, Note: note}
	}
	section := func(kind, head string, cats []string) (budget, act int64, anyBudget bool) {
		rep.Rows = append(rep.Rows, ReportRow{Kind: "head", Label: head})
		for _, c := range cats {
			row := ReportRow{Kind: "line", Label: c, Note: b.RowNotes[c]}
			if kind == "income" && c == duesCat && duesLabel != "" {
				row.Label = duesLabel
			}
			row.Budget, row.HasBudget = b.Budget[c]
			row.Actual, row.HasActual = actual[kind+"\x00"+c], has[kind+"\x00"+c]
			budget += row.Budget
			act += row.Actual
			anyBudget = anyBudget || row.HasBudget
			rep.Rows = append(rep.Rows, row)
		}
		rep.Rows = append(rep.Rows, ReportRow{Kind: "total", Label: "TOTAL " + head, Budget: budget, HasBudget: anyBudget, Actual: act, HasActual: true})
		return
	}
	gap := ReportRow{Kind: "gap"}
	rep.Rows = append(rep.Rows, balance("BEGINNING BALANCE", b.RowNotes[noteBeginning], b.Beginning), gap)
	ib, ia, ih := section("income", "INCOME", income)
	rep.Rows = append(rep.Rows, gap)
	eb, ea, eh := section("expense", "EXPENSES", expense)
	rep.Rows = append(rep.Rows, gap,
		ReportRow{Kind: "total", Label: "NET TOTAL GAIN/LOSS", Budget: ib - eb, HasBudget: ih || eh, Actual: ia - ea, HasActual: true}, gap)
	end := balance("ENDING BALANCE", b.RowNotes[noteEnding], b.Beginning+ia-ea)
	end.Budget = b.Beginning + ib - eb
	rep.Rows = append(rep.Rows, end)
	return rep
}

// csvRows is the report for a spreadsheet: amounts as plain numbers.
func (r Report) csvRows() [][]string {
	num := func(c int64, has bool) string {
		if !has {
			return ""
		}
		return moneyInput(c)
	}
	out := [][]string{{r.Title()}, {r.Subtitle()}, {}, append([]string{""}, r.Heads()...)}
	for _, row := range r.Rows {
		out = append(out, []string{row.Label, num(row.Budget, row.HasBudget), num(row.Actual, row.HasActual), row.Note})
	}
	if len(r.Notes) > 0 {
		out = append(out, []string{}, []string{"Notes"})
		for _, n := range r.Notes {
			out = append(out, []string{n})
		}
	}
	for i, row := range out { // four columns on every line, for spreadsheets
		out[i] = append(row, make([]string, 4-len(row))...)
	}
	return out
}

// markdown is the report as a Markdown table, like the one Barry sends.
func (r Report) markdown() string {
	esc := func(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
	bold := func(s string, on bool) string {
		if on && s != "" {
			return "**" + esc(s) + "**"
		}
		return esc(s)
	}
	var b strings.Builder
	h := r.Heads()
	fmt.Fprintf(&b, "| %s |  |  |  |\n| ----- | ----- | ----- | ----- |\n", bold(r.Title(), true))
	fmt.Fprintf(&b, "| %s |  |  |  |\n|  |  |  |  |\n", bold(r.Subtitle(), true))
	fmt.Fprintf(&b, "|  | **%s** | **%s** | **%s** |\n", h[0], h[1], h[2])
	for _, row := range r.Rows {
		strong := row.Kind != "line"
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", bold(row.Label, strong), bold(row.BudgetText(), strong), bold(row.ActualText(), strong), esc(row.Note))
	}
	if len(r.Notes) > 0 {
		b.WriteString("|  |  |  |  |\n| **Notes** |  |  |  |\n")
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "| %s |  |  |  |\n", esc(n))
		}
	}
	return b.String()
}

// ---- Receipts ----

const receiptSide = 2000 // long side of a receipt photo: small print stays readable

// shrinkReceipt makes a receipt photo at most receiptSide pixels on its long
// side, the right way up, without the camera's hidden details. Unlike
// newsletter photos it never steps down further, so the writing stays sharp.
func shrinkReceipt(b []byte, ext string) ([]byte, string) {
	switch ext {
	case ".webp":
		return stripWebPMetadata(b), ext
	case ".jpg", ".png":
	default:
		return b, ext
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > photoMaxPixels {
		return stripMetadata(b, ext), ext
	}
	turn := 1
	if ext == ".jpg" {
		turn = jpegOrientation(b)
	}
	if max(cfg.Width, cfg.Height) <= receiptSide && turn == 1 {
		return stripMetadata(b, ext), ext
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return stripMetadata(b, ext), ext
	}
	img := orient(downscale(src, receiptSide), turn)
	var out bytes.Buffer
	outExt := ".jpg"
	if !img.Opaque() {
		outExt, err = ".png", png.Encode(&out, img)
	} else {
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 85})
	}
	if err != nil {
		return stripMetadata(b, ext), ext
	}
	return out.Bytes(), outExt
}

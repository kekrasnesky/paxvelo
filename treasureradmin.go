package main

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The Treasurer tab (treasurer.go has the books themselves): Expenses and
// Income list a year's lines with a form to add one, Budget sets the year's
// beginning balance, budget, notes and categories, and Reports draws the
// board report to print, download or keep for a meeting. Every admin can
// see and add; only the treasurer or the president removes.

// moneyView is what the Treasurer pages show.
type moneyView struct {
	Year     int
	Years    []int
	Kind     string // "expense" or "income": the list on the page
	Cat      string // the category the list is narrowed to
	Entries  []LedgerEntry
	Total    int64
	Cats     []string // the list's kind's categories
	Who      []string // suggestions for who spent it
	Form     LedgerEntry
	Removed  int // the line just removed, for Undo
	DuesCat  string
	Dues     *duesView
	Budget   *budgetView
	Report   *Report
	Through  string // the report's last day, 2006-01-02
	Meeting  string // the meeting date to save it for
	Saved    []SavedReport
	SavedOne *SavedReport
	Download string // the report's download query, like through=2026-07-31
	RemovedR int    // the saved report just removed, for Undo
	// CanRemove: the logged-in admin is the treasurer or the president.
	CanRemove bool
}

// duesView is the membership income worked out from Members.
type duesView struct {
	Total              int64
	Individual, Family int
	Lines              []duesLine
	Guessed            int
	Through            string
	DuesIndividual     int
	DuesFamily         int
	IncomeCats         []string
}

type budgetView struct {
	Beginning        string
	NoteBeg, NoteEnd string
	Income, Expense  []budgetRow
	Notes            []string
	IncomeTotal      string
	ExpenseTotal     string
}

// budgetRow is a category on the Budget page.
type budgetRow struct {
	Kind, Name, Budget, Note string
	Used                     int  // lines in it, so it can't be removed
	Dues                     bool // the dues from Members count in it
}

// boardOnly is auth for the Treasurer tab: other admins are sent to Home.
func (a *App) boardOnly(h http.HandlerFunc) http.Handler {
	return a.auth(func(w http.ResponseWriter, r *http.Request) {
		if !a.canSeeMoney(r) {
			back(w, r, "/admin?err=board-only")
			return
		}
		h(w, r)
	})
}

// treasurerOnly is auth for removing on the Treasurer tab.
func (a *App) treasurerOnly(from string, h http.HandlerFunc) http.Handler {
	return a.boardOnly(func(w http.ResponseWriter, r *http.Request) {
		if !a.canRemoveMoney(r) {
			back(w, r, from+"?err=treasurer-only")
			return
		}
		h(w, r)
	})
}

func (a *App) receiptsDir() string { return filepath.Join(a.cfg.DataDir, "receipts") }

// moneyYear is the year asked for, or this year.
func (a *App) moneyYear(r *http.Request) int {
	if y, err := strconv.Atoi(r.URL.Query().Get("year")); err == nil && y > 1990 && y < 3000 {
		return y
	}
	return a.cfg.ThisYear()
}

func (a *App) moneyYears(also int) []int {
	ys := append(a.store.LedgerYears(), a.cfg.ThisYear(), also)
	slices.Sort(ys)
	ys = slices.Compact(ys)
	slices.Reverse(ys)
	return ys
}

// ---- Expenses and Income ----

func (a *App) expensesPage(w http.ResponseWriter, r *http.Request) {
	a.ledgerPage(w, r, "expense", LedgerEntry{}, "")
}

func (a *App) incomePage(w http.ResponseWriter, r *http.Request) {
	a.ledgerPage(w, r, "income", LedgerEntry{}, "")
}

func ledgerPath(kind string) string {
	if kind == "income" {
		return "/admin/treasurer/income"
	}
	return "/admin/treasurer"
}

// ledgerPage lists a year's expenses or income, with the form to add one
// (filled in with form, after a mistake).
func (a *App) ledgerPage(w http.ResponseWriter, r *http.Request, kind string, form LedgerEntry, problem string) {
	year := a.moneyYear(r)
	q := r.URL.Query()
	income, expense, duesCat := a.store.TreasuryCats()
	m := &moneyView{Year: year, Years: a.moneyYears(year), Kind: kind, Cat: q.Get("cat"), Cats: expense, DuesCat: duesCat, CanRemove: a.canRemoveMoney(r)}
	if kind == "income" {
		m.Cats = income
	}
	m.Removed, _ = strconv.Atoi(q.Get("removed"))
	for _, e := range a.store.Ledger(kind, year) {
		if m.Cat == "" || e.Category == m.Cat {
			m.Entries = append(m.Entries, e)
			m.Total += e.Cents
		}
	}
	if form.Date == "" {
		form = LedgerEntry{Kind: kind, Date: a.today().Format("2006-01-02")}
		if m.Cat != "" {
			form.Category = m.Cat
		}
	}
	m.Form = form
	m.Who = a.whoSuggestions()
	if kind == "income" {
		through := a.today().Format("2006-01-02")
		if year != a.cfg.ThisYear() {
			through = fmt.Sprintf("%d-12-31", year)
		}
		d := &duesView{Through: through, DuesIndividual: a.cfg.DuesIndividual, DuesFamily: a.cfg.DuesFamily, IncomeCats: income}
		d.Total, d.Individual, d.Family, d.Lines = a.duesIncome(year, through)
		for _, l := range d.Lines {
			if l.Guessed {
				d.Guessed++
			}
		}
		m.Dues = d
		if duesCat != "" && (m.Cat == "" || m.Cat == duesCat) {
			m.Total += d.Total
		}
	}
	title := map[string]string{"expense": "Expenses", "income": "Income"}[kind]
	v := adminView{Title: title, Tab: map[string]string{"expense": "expenses", "income": "income"}[kind], Money: m}
	v.Flash, v.Error = flashFrom(r)
	if problem != "" {
		v.Flash, v.Error = "", problem
	}
	a.renderAdmin(w, r, http.StatusOK, "money-ledger", v)
}

// whoSuggestions are the board's names and anyone typed before.
func (a *App) whoSuggestions() []string {
	var out []string
	for _, b := range a.store.Board() {
		if b.Name != "" && !slices.Contains(out, b.Name) {
			out = append(out, b.Name)
		}
	}
	for _, n := range a.store.WhoSpent() {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

const maxReceipt = 20 << 20

var errNoReceipt = errors.New("no file")

// readLedgerForm reads the add or edit form. The receipt, when one was
// chosen, is saved and its file name returned with the line.
func (a *App) readLedgerForm(w http.ResponseWriter, r *http.Request, e LedgerEntry) (LedgerEntry, string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReceipt+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return e, "That receipt is too big. Receipts can be up to 20 MB."
	}
	e.Date = strings.TrimSpace(r.FormValue("date"))
	e.What = cleanName(r.FormValue("what"))
	e.Who = cleanName(r.FormValue("who"))
	e.Category = cleanName(r.FormValue("category"))
	if e.Category == "+" {
		e.Category = cleanName(r.FormValue("new_category"))
	}
	amount := r.FormValue("amount")
	c, ok := parseMoney(amount)
	e.Cents = c
	switch {
	case !validDate(e.Date):
		return e, "Enter the date."
	case !ok || c == 0:
		return e, "Enter the amount, like 106.00."
	case e.Category == "":
		return e, "Pick a category, or type a new one."
	}
	if r.MultipartForm != nil && len(r.MultipartForm.File["receipt"]) > 0 {
		hdr := r.MultipartForm.File["receipt"][0]
		name, err := a.saveReceipt(hdr)
		if err != nil && err != errNoReceipt {
			return e, "The receipt wasn’t saved: " + err.Error()
		}
		if err == nil {
			e.Receipt, e.ReceiptName = name, filepath.Base(hdr.Filename)
		}
	}
	return e, ""
}

func validDate(d string) bool {
	_, err := time.Parse("2006-01-02", d)
	return err == nil
}

// saveReceipt stores a receipt: a PDF as it is, a photo shrunk
// (shrinkReceipt). It returns the file's name in data/receipts.
func (a *App) saveReceipt(hdr *multipart.FileHeader) (string, error) {
	if hdr.Size == 0 {
		return "", errNoReceipt
	}
	f, err := hdr.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxReceipt+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxReceipt {
		return "", errors.New("it’s over 20 MB")
	}
	var ext string
	if bytes.HasPrefix(b, []byte("%PDF-")) {
		ext = ".pdf"
	} else if x, ok := photoTypes[http.DetectContentType(b)]; ok {
		b, ext = shrinkReceipt(b, x)
	} else {
		return "", errors.New("use a photo (JPG, PNG or WebP) or a PDF")
	}
	if err := os.MkdirAll(a.receiptsDir(), 0o700); err != nil {
		return "", err
	}
	name := randHex(8) + ext
	return name, os.WriteFile(filepath.Join(a.receiptsDir(), name), b, 0o600)
}

var receiptName = regexp.MustCompile(`^[0-9a-f]{16}\.(jpg|png|webp|gif|pdf)$`)

func (a *App) dropReceipt(name string) {
	if receiptName.MatchString(name) {
		os.Remove(filepath.Join(a.receiptsDir(), name))
	}
}

// receiptFile shows a receipt, to logged-in admins only.
func (a *App) receiptFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !receiptName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	http.ServeFile(w, r, filepath.Join(a.receiptsDir(), name))
}

func (a *App) addLedger(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind != "income" {
		kind = "expense"
	}
	e, problem := a.readLedgerForm(w, r, LedgerEntry{Kind: kind})
	if problem != "" {
		a.dropReceipt(e.Receipt)
		a.ledgerPage(w, r, kind, e, problem)
		return
	}
	e.AddedBy, _ = a.adminName(r)
	if _, err := a.store.AddLedgerEntry(e); err != nil {
		a.dropReceipt(e.Receipt)
		back(w, r, ledgerPath(kind)+"?err=save")
		return
	}
	back(w, r, fmt.Sprintf("%s?year=%s&ok=money-added", ledgerPath(kind), e.Date[:4]))
}

func (a *App) ledgerEntryFromPath(w http.ResponseWriter, r *http.Request) (LedgerEntry, bool) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	e, ok := a.store.LedgerEntry(id)
	if !ok {
		back(w, r, "/admin/treasurer?err=money-gone")
	}
	return e, ok
}

func (a *App) editLedgerPage(w http.ResponseWriter, r *http.Request) {
	e, ok := a.ledgerEntryFromPath(w, r)
	if !ok {
		return
	}
	a.renderLedgerEdit(w, r, e, "")
}

func (a *App) renderLedgerEdit(w http.ResponseWriter, r *http.Request, e LedgerEntry, problem string) {
	income, expense, _ := a.store.TreasuryCats()
	m := &moneyView{Kind: e.Kind, Form: e, Cats: expense, Who: a.whoSuggestions(), Year: a.cfg.ThisYear(), CanRemove: a.canRemoveMoney(r)}
	if e.Kind == "income" {
		m.Cats = income
	}
	if len(e.Date) >= 4 {
		m.Year, _ = strconv.Atoi(e.Date[:4])
	}
	tab := map[string]string{"expense": "expenses", "income": "income"}[e.Kind]
	title := map[string]string{"expense": "Expense", "income": "Income"}[e.Kind]
	a.renderAdmin(w, r, http.StatusOK, "money-edit", adminView{Title: title, Tab: tab, Money: m, Error: problem})
}

func (a *App) saveLedger(w http.ResponseWriter, r *http.Request) {
	old, ok := a.ledgerEntryFromPath(w, r)
	if !ok {
		return
	}
	e, problem := a.readLedgerForm(w, r, LedgerEntry{ID: old.ID, Kind: old.Kind})
	if problem != "" {
		a.dropReceipt(e.Receipt)
		a.renderLedgerEdit(w, r, e, problem)
		return
	}
	switch {
	case e.Receipt != "": // a new one replaces it
		a.dropReceipt(old.Receipt)
	case r.FormValue("drop_receipt") != "":
		a.dropReceipt(old.Receipt)
	default:
		e.Receipt, e.ReceiptName = old.Receipt, old.ReceiptName
	}
	if err := a.store.UpdateLedgerEntry(e); err != nil {
		back(w, r, ledgerPath(old.Kind)+"?err=money-gone")
		return
	}
	back(w, r, fmt.Sprintf("%s?year=%s&ok=money-saved", ledgerPath(e.Kind), e.Date[:4]))
}

func (a *App) removeLedger(w http.ResponseWriter, r *http.Request) {
	e, ok := a.ledgerEntryFromPath(w, r)
	if !ok {
		return
	}
	gone, err := a.store.RemoveLedgerEntry(e.ID)
	if err != nil {
		back(w, r, ledgerPath(e.Kind)+"?err=save")
		return
	}
	if gone != "" && gone != e.Receipt {
		a.dropReceipt(gone)
	}
	back(w, r, fmt.Sprintf("%s?year=%s&removed=%d", ledgerPath(e.Kind), e.Date[:4], e.ID))
}

func (a *App) restoreLedger(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	a.store.RestoreLedgerEntry(id)
	e, ok := a.store.LedgerEntry(id)
	if !ok {
		back(w, r, "/admin/treasurer")
		return
	}
	back(w, r, fmt.Sprintf("%s?year=%s&ok=money-restored#e%d", ledgerPath(e.Kind), e.Date[:4], id))
}

// duesCategory picks where the dues from Members count, or "" for nowhere.
func (a *App) duesCategory(w http.ResponseWriter, r *http.Request) {
	if err := a.store.SetDuesCategory(r.FormValue("dues")); err != nil {
		back(w, r, "/admin/treasurer/income?err=save")
		return
	}
	back(w, r, "/admin/treasurer/income?year="+r.FormValue("year")+"&ok=money-dues")
}

// ---- Budget ----

func (a *App) budgetPage(w http.ResponseWriter, r *http.Request) {
	year := a.moneyYear(r)
	income, expense, duesCat := a.store.TreasuryCats()
	b := a.store.Budget(year)
	use := a.store.CategoryUse()
	bv := &budgetView{Beginning: moneyInput(b.Beginning), NoteBeg: b.RowNotes[noteBeginning], NoteEnd: b.RowNotes[noteEnding], Notes: b.Notes}
	if b.Beginning == 0 {
		bv.Beginning = ""
	}
	rows := func(kind string, cats []string) (out []budgetRow, total int64) {
		for _, c := range cats {
			row := budgetRow{Kind: kind, Name: c, Note: b.RowNotes[c], Used: use[kind+"\x00"+c], Dues: kind == "income" && c == duesCat}
			if v, ok := b.Budget[c]; ok {
				row.Budget = moneyInput(v)
				total += v
			}
			out = append(out, row)
		}
		return
	}
	var it, et int64
	bv.Income, it = rows("income", income)
	bv.Expense, et = rows("expense", expense)
	bv.IncomeTotal, bv.ExpenseTotal = money(it), money(et)
	m := &moneyView{Year: year, Years: a.moneyYears(year), Budget: bv, CanRemove: a.canRemoveMoney(r)}
	v := adminView{Title: "Budget", Tab: "budget", Money: m}
	v.Flash, v.Error = flashFrom(r)
	if kept := r.URL.Query().Get("kept"); kept != "" {
		v.Flash = "Saved. " + kept + " still " + plural(strings.Count(kept, ";")+1, "has lines, so it stays", "have lines, so they stay") + "."
	}
	a.renderAdmin(w, r, http.StatusOK, "money-budget", v)
}

func (a *App) saveBudget(w http.ResponseWriter, r *http.Request) {
	year := a.moneyYear(r)
	r.ParseForm()
	f := r.PostForm
	b := BudgetYear{Budget: map[string]int64{}, RowNotes: map[string]string{}}
	b.Beginning, _ = parseMoney(f.Get("beginning"))
	if n := strings.TrimSpace(f.Get("note_beginning")); n != "" {
		b.RowNotes[noteBeginning] = n
	}
	if n := strings.TrimSpace(f.Get("note_ending")); n != "" {
		b.RowNotes[noteEnding] = n
	}
	for _, n := range f["note"] {
		if n = strings.TrimSpace(n); n != "" {
			b.Notes = append(b.Notes, n)
		}
	}
	kinds, origs, names, budgets, notes := f["cat_kind"], f["cat_orig"], f["cat_name"], f["cat_budget"], f["cat_note"]
	var rows []catRow
	for i := range kinds {
		if i >= len(origs) || i >= len(names) || i >= len(budgets) || i >= len(notes) {
			break
		}
		row := catRow{Kind: kinds[i], Orig: origs[i], Name: cleanName(names[i])}
		rows = append(rows, row)
		name := or(row.Name, row.Orig)
		if v, ok := parseMoney(budgets[i]); ok && name != "" {
			b.Budget[name] = v
		}
		if n := strings.TrimSpace(notes[i]); n != "" && name != "" {
			b.RowNotes[name] = n
		}
	}
	kept, err := a.store.SaveBudget(year, b, rows)
	if err != nil {
		back(w, r, fmt.Sprintf("/admin/treasurer/budget?year=%d&err=save", year))
		return
	}
	if len(kept) > 0 {
		back(w, r, fmt.Sprintf("/admin/treasurer/budget?year=%d&kept=%s", year, url.QueryEscape(strings.Join(kept, "; "))))
		return
	}
	back(w, r, fmt.Sprintf("/admin/treasurer/budget?year=%d&ok=money-budget", year))
}

// ---- Reports ----

// reportThrough is the report's last day: asked for, or the end of last month.
func (a *App) reportThrough(r *http.Request) time.Time {
	if t, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("through"), a.cfg.Location); err == nil {
		return t
	}
	t := a.today()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
}

func (a *App) reportsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	m := &moneyView{Saved: a.store.SavedReports(), CanRemove: a.canRemoveMoney(r), Meeting: a.today().Format("2006-01-02")}
	m.RemovedR, _ = strconv.Atoi(q.Get("removed"))
	if id, err := strconv.Atoi(q.Get("saved")); err == nil {
		sr, ok := a.store.SavedReport(id)
		if !ok {
			back(w, r, "/admin/treasurer/reports?err=money-gone")
			return
		}
		m.SavedOne, m.Report, m.Through = &sr, &sr.Report, sr.Report.Through
		m.Download = "saved=" + strconv.Itoa(id)
	} else {
		through := a.reportThrough(r)
		rep := a.buildReport(through)
		m.Report, m.Through = &rep, rep.Through
		m.Download = "through=" + rep.Through
	}
	m.Year = m.Report.Year
	v := adminView{Title: "Reports", Tab: "reports", Money: m}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "money-reports", v)
}

// reportDownload sends the report as a CSV or Markdown file, named like
// "2026-07-31 Monthly Report.csv".
func (a *App) reportDownload(w http.ResponseWriter, r *http.Request) {
	var rep Report
	if id, err := strconv.Atoi(r.URL.Query().Get("saved")); err == nil {
		sr, ok := a.store.SavedReport(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		rep = sr.Report
	} else {
		rep = a.buildReport(a.reportThrough(r))
	}
	name := rep.Through + " Monthly Report"
	if strings.HasSuffix(r.URL.Path, ".md") {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.md"`)
		io.WriteString(w, rep.markdown())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
	cw := csv.NewWriter(w)
	cw.WriteAll(rep.csvRows())
}

func (a *App) saveReport(w http.ResponseWriter, r *http.Request) {
	through, err := time.ParseInLocation("2006-01-02", r.FormValue("through"), a.cfg.Location)
	meeting := r.FormValue("meeting")
	if err != nil || !validDate(meeting) {
		back(w, r, "/admin/treasurer/reports?err=money-meeting")
		return
	}
	me, _ := a.adminName(r)
	sr, err := a.store.SaveReport(SavedReport{Meeting: meeting, Report: a.buildReport(through), SavedBy: me})
	if err != nil {
		back(w, r, "/admin/treasurer/reports?err=save")
		return
	}
	back(w, r, fmt.Sprintf("/admin/treasurer/reports?saved=%d&ok=money-report-saved", sr.ID))
}

func (a *App) removeReport(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if err := a.store.RemoveReport(id); err != nil {
		back(w, r, "/admin/treasurer/reports?err=save")
		return
	}
	back(w, r, fmt.Sprintf("/admin/treasurer/reports?removed=%d", id))
}

func (a *App) restoreReport(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	a.store.RestoreReport(id)
	back(w, r, "/admin/treasurer/reports?ok=money-restored")
}

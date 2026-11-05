package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseMoney(t *testing.T) {
	for in, want := range map[string]int64{
		"12": 1200, "12.5": 1250, "$1,234.56": 123456, " $ 106.00 ": 10600, ".59": 59, "-5": -500, "(5.00)": -500, "$-3.20": -320, "0": 0,
	} {
		if got, ok := parseMoney(in); !ok || got != want {
			t.Errorf("parseMoney(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.234", "12.3.4", "$", "1e5"} {
		if _, ok := parseMoney(bad); ok {
			t.Errorf("parseMoney(%q) should fail", bad)
		}
	}
	for c, want := range map[int64]string{0: "$0.00", 5: "$0.05", 1287346: "$12,873.46", -15132: "-$151.32", 100000000: "$1,000,000.00"} {
		if got := money(c); got != want {
			t.Errorf("money(%d) = %q, want %q", c, got, want)
		}
	}
	if moneyInput(-15132) != "-151.32" || moneyInput(1287346) != "12873.46" {
		t.Error("moneyInput")
	}
}

// The report adds up to Barry's own numbers for July 2026.
func TestReportMatchesBarrys(t *testing.T) {
	a, _, _ := testApp(t)
	if err := a.store.SetDuesCategory(""); err != nil {
		t.Fatal(err)
	}
	add := func(kind, date, cat string, cents int64) {
		if _, err := a.store.AddLedgerEntry(LedgerEntry{Kind: kind, Date: date, Category: cat, Cents: cents}); err != nil {
			t.Fatal(err)
		}
	}
	add("income", "2026-03-01", "Membership Income", 1875_00)
	add("income", "2026-06-30", "Dividend Income", 1_59)
	add("income", "2026-05-02", "Jakroo Commission", 215_95)
	add("expense", "2026-02-10", "Insurance", 789_00)
	add("expense", "2026-01-15", "Office Expenses:PO Box Rental", 106_00)
	add("expense", "2026-01-20", "Office Expenses:Quicken", 82_56)
	add("expense", "2026-02-01", "Registration Fees", 75_00)
	add("expense", "2026-04-01", "Taxes:Prep Fees", 250_00)
	add("expense", "2026-07-31", "Bank Fees", 240_10)
	add("expense", "2026-03-15", "Website Fees: SquareSpace", 292_56)
	add("expense", "2026-06-01", "Website Fees: Domain/Marketing", 106_00)
	add("expense", "2026-08-01", "Website Fees: Google Workspace", 84_00) // after the report's last day
	add("expense", "2025-12-31", "Insurance", 700_00)                     // last year

	income, expense, _ := a.store.TreasuryCats()
	var rows []catRow
	for _, c := range income {
		rows = append(rows, catRow{Kind: "income", Orig: c, Name: c})
	}
	for _, c := range expense {
		rows = append(rows, catRow{Kind: "expense", Orig: c, Name: c})
	}
	b := BudgetYear{Beginning: 12780_14, Budget: map[string]int64{
		"Membership Income": 2375_00, "Dividend Income": 3_20,
		"Donations": 0, "Club Rides & Parties:Food & Drink": 500_00, "Insurance": 700_00, "Kit Expenses:Sock Order": 0,
		"Office Expenses:PO Box Rental": 106_00, "Office Expenses:Quicken": 0, "Registration Fees": 75_00, "Taxes:Prep Fees": 250_00,
		"Bank Fees": 280_00, "Website Fees: SquareSpace": 276_00, "Website Fees: Domain/Marketing": 20_00, "Website Fees: Google Workspace": 84_00,
	}, RowNotes: map[string]string{noteBeginning: "(1)", "Insurance": "(2)", "Registration Fees": "(3)"},
		Notes: []string{"(1) Not including $215.72 in Share Savings Acct", "(2) Through League of American Cyclists", "(3) League of American Cyclists"}}
	if _, err := a.store.SaveBudget(2026, b, rows); err != nil {
		t.Fatal(err)
	}

	rep := a.buildReport(time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC))
	if rep.Subtitle() != "2026 Budget - YTD Actuals through 7/31/2026" || rep.Title() != "PaxVelo Income/Expense by Category" {
		t.Errorf("titles: %q / %q", rep.Title(), rep.Subtitle())
	}
	got := map[string][3]string{}
	var order []string
	for _, r := range rep.Rows {
		if r.Kind != "gap" {
			got[r.Label] = [3]string{r.BudgetText(), r.ActualText(), r.Note}
			order = append(order, r.Label)
		}
	}
	for label, want := range map[string][3]string{
		"BEGINNING BALANCE":                 {"$12,780.14", "$12,780.14", "(1)"},
		"Membership Income":                 {"$2,375.00", "$1,875.00", ""},
		"Jakroo Commission":                 {"", "$215.95", ""},
		"TOTAL INCOME":                      {"$2,378.20", "$2,092.54", ""},
		"Donations":                         {"$ -", "", ""},
		"Insurance":                         {"$700.00", "$789.00", "(2)"},
		"Website Fees: Google Workspace":    {"$84.00", "", ""},
		"TOTAL EXPENSES":                    {"$2,291.00", "$1,941.22", ""},
		"NET TOTAL GAIN/LOSS":               {"$87.20", "$151.32", ""},
		"ENDING BALANCE":                    {"$12,867.34", "$12,931.46", ""},
		"Club Rides & Parties:Food & Drink": {"$500.00", "", ""},
	} {
		if got[label] != want {
			t.Errorf("%s: got %q, want %q", label, got[label], want)
		}
	}
	wantOrder := []string{"BEGINNING BALANCE", "INCOME", "Membership Income", "Dividend Income", "Jakroo Commission", "TOTAL INCOME", "EXPENSES", "Donations"}
	if !slices.Equal(order[:len(wantOrder)], wantOrder) || order[len(order)-1] != "ENDING BALANCE" || order[len(order)-2] != "NET TOTAL GAIN/LOSS" || order[len(order)-3] != "TOTAL EXPENSES" {
		t.Errorf("row order: %q", order)
	}
	if len(rep.Notes) != 3 || !strings.HasPrefix(rep.Notes[0], "(1) Not including") {
		t.Errorf("notes: %q", rep.Notes)
	}

	md := rep.markdown()
	for _, want := range []string{"| **PaxVelo Income/Expense by Category** |", "| **2026 Budget - YTD Actuals through 7/31/2026** |", "| Insurance | $700.00 | $789.00 | (2) |", "| **ENDING BALANCE** | **$12,867.34** | **$12,931.46** |", "| Donations | $ - |"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown is missing %q:\n%s", want, md)
		}
	}
}

// Membership income counts each year marked paid on the day it was marked
// (or the join day, for imported years with no date), at the plan's dues.
func TestDuesIncome(t *testing.T) {
	a, _, _ := testApp(t)
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 15, 0, 0, 0, time.UTC) }
	a.store.mu.Lock()
	for i, m := range []Member{
		{Name: "Ann", Plan: "individual", Joined: day(2024, 3, 1), PaidYears: []int{2025, 2026}, PaidOn: map[int]time.Time{2025: day(2025, 3, 2), 2026: day(2026, 2, 20)}},
		{Name: "Bo Family", Plan: "family", Joined: day(2026, 5, 5), PaidYears: []int{2026}, PaidOn: map[int]time.Time{2026: day(2026, 5, 6)}},
		{Name: "Cy Imported", Plan: "individual", Joined: day(2019, 6, 10), PaidYears: []int{2026}},                                              // no date: June 10
		{Name: "Di Late", Plan: "individual", Joined: day(2026, 9, 1), PaidYears: []int{2026}, PaidOn: map[int]time.Time{2026: day(2026, 9, 2)}}, // after July
	} {
		m.ID = 100 + i
		a.store.d.Members = append(a.store.d.Members, &m)
	}
	a.store.mu.Unlock()
	total, ind, fam, lines := a.duesIncome(2026, "2026-07-31")
	if total != 35_00+45_00+35_00 || ind != 2 || fam != 1 || len(lines) != 3 {
		t.Fatalf("dues: %d, %d individual, %d family, %+v", total, ind, fam, lines)
	}
	if !lines[0].Guessed || lines[0].Member != "Cy Imported" || lines[0].Date != "2026-06-10" {
		t.Errorf("an imported year should count on the join day: %+v", lines[0])
	}
	rep := a.buildReport(time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC))
	for _, r := range rep.Rows {
		if strings.HasPrefix(r.Label, "Membership Income") {
			if r.Label != "Membership Income (2 Individual/1 Family)" || r.Actual != 115_00 {
				t.Errorf("membership row: %+v", r)
			}
			return
		}
	}
	t.Error("no membership row")
}

func receiptForm(t *testing.T, fields map[string]string, file string, content []byte) (string, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if file != "" {
		fw, _ := mw.CreateFormFile("receipt", file)
		fw.Write(content)
	}
	mw.Close()
	return mw.FormDataContentType(), &buf
}

// Expenses with receipts: board members add and edit; receipts are only for
// logged-in admins; only the treasurer or the president removes, with Undo.
func TestExpensesAndReceipts(t *testing.T) {
	a, h, _ := testApp(t)
	pat, _ := a.store.AddAdmin("Pat", "pat@example.com", "")
	barry, _ := a.store.AddAdmin("Barry", "barry@example.com", "")
	sam, _ := a.store.AddAdmin("Sam", "sam@example.com", "")
	a.store.SetPresident(pat.ID)
	as := func(u AdminUser) *http.Cookie {
		u, _ = a.store.Admin(u.ID)
		rec := httptest.NewRecorder()
		a.setSession(rec, accountSession(u), 3600)
		return rec.Result().Cookies()[0]
	}
	// The president makes Barry the treasurer; Sam can't.
	do(t, h, as(sam), "POST", "/admin/admins/"+fmt.Sprint(barry.ID)+"/treasurer", "application/x-www-form-urlencoded", strings.NewReader("on=1"))
	if u, _ := a.store.Admin(barry.ID); u.Treasurer {
		t.Fatal("only the president names the treasurer")
	}
	do(t, h, as(pat), "POST", "/admin/admins/"+fmt.Sprint(barry.ID)+"/treasurer", "application/x-www-form-urlencoded", strings.NewReader("on=1"))
	if u, _ := a.store.Admin(barry.ID); !u.Treasurer {
		t.Fatal("the president should be able to name the treasurer")
	}

	// Only board members see the tab: Ola, an admin who isn't on the board,
	// doesn't, until the president marks her as one.
	ola, _ := a.store.AddAdmin("Ola", "ola@example.com", "")
	board := func(u AdminUser, on string) {
		do(t, h, as(pat), "POST", "/admin/admins/"+fmt.Sprint(u.ID)+"/board", "application/x-www-form-urlencoded", strings.NewReader("on="+on))
	}
	board(sam, "1")
	if home := do(t, h, as(ola), "GET", "/admin", "", nil).Body.String(); strings.Contains(home, `href="/admin/treasurer"`) {
		t.Error("an admin who isn't on the board shouldn't see the Treasurer tab")
	}
	for _, p := range []string{"/admin/treasurer", "/admin/treasurer/income", "/admin/treasurer/budget", "/admin/treasurer/reports", "/admin/treasurer/report.csv"} {
		if w := do(t, h, as(ola), "GET", p, "", nil); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "err=board-only") {
			t.Errorf("%s for a non-board admin: %d %s", p, w.Code, w.Header().Get("Location"))
		}
	}
	ct0, body0 := receiptForm(t, map[string]string{"date": "2026-02-10", "amount": "5", "category": "Bank Fees"}, "", nil)
	do(t, h, as(ola), "POST", "/admin/treasurer/entries?kind=expense", ct0, body0)
	if len(a.store.Ledger("", 2026)) != 0 {
		t.Error("a non-board admin shouldn't be able to add a line")
	}
	do(t, h, as(ola), "POST", "/admin/admins/"+fmt.Sprint(ola.ID)+"/board", "application/x-www-form-urlencoded", strings.NewReader("on=1"))
	if u, _ := a.store.Admin(ola.ID); u.Board {
		t.Error("only the president marks board members")
	}
	board(ola, "1")
	if w := do(t, h, as(ola), "GET", "/admin/treasurer", "", nil); w.Code != 200 {
		t.Errorf("a board member should open the tab: %d", w.Code)
	}
	board(ola, "")
	if w := do(t, h, as(ola), "GET", "/admin/treasurer", "", nil); w.Code == 200 {
		t.Error("taken off the board, she shouldn't open the tab any more")
	}

	for _, p := range []string{"/admin/treasurer", "/admin/treasurer/income", "/admin/treasurer/budget", "/admin/treasurer/reports"} {
		w := do(t, h, as(sam), "GET", p, "", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `href="/admin/treasurer"`) {
			t.Errorf("%s: %d", p, w.Code)
		}
	}

	// A big photo receipt is shrunk; a PDF is kept as it is.
	img := image.NewRGBA(image.Rect(0, 0, 3000, 1200))
	for x := 0; x < 3000; x += 7 {
		img.Set(x, x%1200, color.RGBA{200, 10, 10, 255})
	}
	var photo bytes.Buffer
	jpeg.Encode(&photo, img, &jpeg.Options{Quality: 95})
	ct, body := receiptForm(t, map[string]string{"date": "2026-02-10", "amount": "$789.00", "category": "Insurance", "what": "League insurance", "who": "Barry"}, "IMG_1234.jpg", photo.Bytes())
	if w := do(t, h, as(sam), "POST", "/admin/treasurer/entries?kind=expense", ct, body); w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "ok=money-added") {
		t.Fatalf("add: %d %s %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	pdf := []byte("%PDF-1.4\n% a receipt\n%%EOF\n")
	ct, body = receiptForm(t, map[string]string{"date": "2026-01-15", "amount": "106", "category": "+", "new_category": "Office Expenses:Stamps", "who": "Karl"}, "stamps.pdf", pdf)
	do(t, h, as(sam), "POST", "/admin/treasurer/entries?kind=expense", ct, body)

	list := a.store.Ledger("expense", 2026)
	if len(list) != 2 || list[0].Category != "Insurance" || list[0].Cents != 789_00 || list[1].Category != "Office Expenses:Stamps" {
		t.Fatalf("ledger: %+v", list)
	}
	if _, exp, _ := a.store.TreasuryCats(); !slices.Contains(exp, "Office Expenses:Stamps") {
		t.Error("a new category typed on the form should join the list")
	}
	shrunk, err := os.ReadFile(filepath.Join(a.receiptsDir(), list[0].Receipt))
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(shrunk)); err != nil || cfg.Width != receiptSide {
		t.Errorf("photo receipt should be %dpx wide: %v %d", receiptSide, err, cfg.Width)
	}
	if b, _ := os.ReadFile(filepath.Join(a.receiptsDir(), list[1].Receipt)); !bytes.Equal(b, pdf) || !list[1].ReceiptPDF() {
		t.Error("a PDF receipt should be kept as it is")
	}
	// Receipts open for admins only.
	rp := "/admin/treasurer/receipts/" + list[1].Receipt
	if w := do(t, h, as(sam), "GET", rp, "", nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), pdf) {
		t.Errorf("admin receipt: %d", w.Code)
	}
	if w := do(t, h, as(ola), "GET", rp, "", nil); w.Code == 200 {
		t.Error("a receipt shouldn't open for an admin who isn't on the board")
	}
	if w := do(t, h, nil, "GET", rp, "", nil); w.Code == 200 {
		t.Error("a receipt shouldn't open without logging in")
	}
	if w := do(t, h, as(sam), "GET", "/admin/treasurer/receipts/..%2Fpaxvelo.json", "", nil); w.Code == 200 {
		t.Error("only receipt files can be fetched")
	}
	// Something that's neither a photo nor a PDF is refused, and nothing is added.
	ct, body = receiptForm(t, map[string]string{"date": "2026-01-15", "amount": "5", "category": "Bank Fees"}, "notes.txt", []byte("hello"))
	if w := do(t, h, as(sam), "POST", "/admin/treasurer/entries?kind=expense", ct, body); w.Code != 200 || !strings.Contains(w.Body.String(), "PDF") {
		t.Errorf("bad receipt: %d", w.Code)
	}
	if len(a.store.Ledger("expense", 2026)) != 2 {
		t.Error("a refused line shouldn't be added")
	}

	// Editing: any admin; the receipt stays unless replaced or removed.
	id := fmt.Sprint(list[0].ID)
	if w := do(t, h, as(sam), "GET", "/admin/treasurer/entries/"+id, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "League insurance") {
		t.Errorf("edit page: %d", w.Code)
	}
	ct, body = receiptForm(t, map[string]string{"date": "2026-02-11", "amount": "790.50", "category": "Insurance", "what": "League insurance", "who": "Barry"}, "", nil)
	do(t, h, as(sam), "POST", "/admin/treasurer/entries/"+id, ct, body)
	if e, _ := a.store.LedgerEntry(list[0].ID); e.Cents != 790_50 || e.Date != "2026-02-11" || e.Receipt != list[0].Receipt {
		t.Errorf("edited: %+v", e)
	}

	// Removing: not Sam; Barry can, and Undo puts it back.
	page := do(t, h, as(sam), "GET", "/admin/treasurer", "", nil).Body.String()
	if strings.Contains(page, "/entries/"+id+"/remove") {
		t.Error("Sam shouldn't see Remove")
	}
	do(t, h, as(sam), "POST", "/admin/treasurer/entries/"+id+"/remove", "", nil)
	if _, ok := a.store.LedgerEntry(list[0].ID); !ok {
		t.Fatal("Sam shouldn't be able to remove a line")
	}
	w := do(t, h, as(barry), "POST", "/admin/treasurer/entries/"+id+"/remove", "", nil)
	if _, ok := a.store.LedgerEntry(list[0].ID); ok {
		t.Fatal("the treasurer should be able to remove a line")
	}
	if page := do(t, h, as(barry), "GET", w.Header().Get("Location"), "", nil).Body.String(); !strings.Contains(page, "/entries/"+id+"/restore") {
		t.Error("Remove should offer Undo")
	}
	do(t, h, as(barry), "POST", "/admin/treasurer/entries/"+id+"/restore", "", nil)
	if e, ok := a.store.LedgerEntry(list[0].ID); !ok || e.Receipt == "" {
		t.Fatal("Undo should put the line back with its receipt")
	}
	if _, err := os.Stat(filepath.Join(a.receiptsDir(), list[0].Receipt)); err != nil {
		t.Error("the receipt file should still be there after Undo")
	}
	// The president can remove too.
	do(t, h, as(pat), "POST", "/admin/treasurer/entries/"+fmt.Sprint(list[1].ID)+"/remove", "", nil)
	if _, ok := a.store.LedgerEntry(list[1].ID); ok {
		t.Error("the president should be able to remove a line")
	}
}

// The Budget page renames, reorders, adds and removes categories; a
// category with lines can't be removed, and a rename follows every line.
func TestBudgetCategories(t *testing.T) {
	a, h, admin := testApp(t)
	a.store.AddLedgerEntry(LedgerEntry{Kind: "expense", Date: "2026-03-01", Category: "Bank Fees", Cents: 10_00})
	a.store.AddLedgerEntry(LedgerEntry{Kind: "income", Date: "2026-03-01", Category: "Membership Income", Cents: 35_00})
	v := url.Values{"beginning": {"$12,780.14"}, "note_beginning": {"(1)"}, "note": {"(1) Not including savings", "", "(2) Through the League"}}
	row := func(kind, orig, name, budget, note string) {
		v["cat_kind"] = append(v["cat_kind"], kind)
		v["cat_orig"] = append(v["cat_orig"], orig)
		v["cat_name"] = append(v["cat_name"], name)
		v["cat_budget"] = append(v["cat_budget"], budget)
		v["cat_note"] = append(v["cat_note"], note)
	}
	row("income", "Dividend Income", "Dividend Income", "3.20", "")
	row("income", "Membership Income", "Membership Dues", "2375", "")
	row("expense", "Insurance", "Insurance", "700", "(2)")
	row("expense", "", "Trophies", "", "")
	// Bank Fees and the rest are left out: Bank Fees has a line, so it stays.
	ct, body := form(v)
	w := do(t, h, admin, "POST", "/admin/treasurer/budget?year=2026", ct, body)
	if !strings.Contains(w.Header().Get("Location"), "kept=Bank+Fees") {
		t.Errorf("save: %s", w.Header().Get("Location"))
	}
	income, expense, dues := a.store.TreasuryCats()
	if !slices.Equal(income, []string{"Dividend Income", "Membership Dues"}) || !slices.Equal(expense, []string{"Insurance", "Trophies", "Bank Fees"}) || dues != "Membership Dues" {
		t.Errorf("categories: %q %q dues in %q", income, expense, dues)
	}
	if l := a.store.Ledger("income", 2026); l[0].Category != "Membership Dues" {
		t.Errorf("a rename should follow the lines: %q", l[0].Category)
	}
	b := a.store.Budget(2026)
	if b.Beginning != 12780_14 || b.Budget["Membership Dues"] != 2375_00 || b.Budget["Insurance"] != 700_00 || b.RowNotes["Insurance"] != "(2)" || b.RowNotes[noteBeginning] != "(1)" || len(b.Notes) != 2 {
		t.Errorf("budget: %+v", b)
	}
	if _, ok := b.Budget["Trophies"]; ok {
		t.Error("a blank budget should stay blank, not $0")
	}
	if page := do(t, h, admin, "GET", w.Header().Get("Location"), "", nil).Body.String(); !strings.Contains(page, "Bank Fees still has lines") || !strings.Contains(page, `value="12780.14"`) {
		t.Error("the Budget page should say Bank Fees stayed, and show the balance")
	}
}

// Reports download as CSV and Markdown, and are saved for a meeting, which
// keeps their numbers when the books change.
func TestReportsSavedAndDownloaded(t *testing.T) {
	a, h, admin := testApp(t)
	a.store.SetDuesCategory("")
	a.store.AddLedgerEntry(LedgerEntry{Kind: "expense", Date: "2026-07-01", Category: "Insurance", Cents: 789_00})

	w := do(t, h, admin, "GET", "/admin/treasurer/report.csv?through=2026-07-31", "", nil)
	if !strings.Contains(w.Header().Get("Content-Disposition"), "2026-07-31 Monthly Report.csv") {
		t.Errorf("csv name: %q", w.Header().Get("Content-Disposition"))
	}
	recs, err := csv.NewReader(w.Body).ReadAll()
	if err != nil || recs[0][0] != reportTitle || recs[1][0] != "2026 Budget - YTD Actuals through 7/31/2026" || !slices.Equal(recs[3], []string{"", "2026 Budget", "YTD Actuals", "Notes"}) {
		t.Fatalf("csv: %v %q", err, recs[:4])
	}
	found := false
	for _, r := range recs {
		if r[0] == "TOTAL EXPENSES" && r[2] == "789.00" {
			found = true
		}
	}
	if !found {
		t.Error("csv should have the expenses total as a number")
	}
	if md := do(t, h, admin, "GET", "/admin/treasurer/report.md?through=2026-07-31", "", nil).Body.String(); !strings.Contains(md, "| Insurance |  | $789.00 |") {
		t.Errorf("markdown:\n%s", md)
	}
	page := do(t, h, admin, "GET", "/admin/treasurer/reports?through=2026-07-31", "", nil).Body.String()
	if !strings.Contains(page, "2026 Budget - YTD Actuals through 7/31/2026") || !strings.Contains(page, "$789.00") {
		t.Error("the report page should show the report")
	}

	ct, body := form(url.Values{"through": {"2026-07-31"}, "meeting": {"2026-08-12"}})
	w = do(t, h, admin, "POST", "/admin/treasurer/reports", ct, body)
	m := regexp.MustCompile(`saved=(\d+)`).FindStringSubmatch(w.Header().Get("Location"))
	if m == nil {
		t.Fatalf("save: %s", w.Header().Get("Location"))
	}
	a.store.AddLedgerEntry(LedgerEntry{Kind: "expense", Date: "2026-07-02", Category: "Insurance", Cents: 1_00})
	saved := do(t, h, admin, "GET", "/admin/treasurer/reports?saved="+m[1], "", nil).Body.String()
	if !strings.Contains(saved, "Meeting of August 12, 2026") || !strings.Contains(saved, "$789.00") || strings.Contains(saved, "$790.00") {
		t.Error("a saved report should keep its numbers")
	}
	if now := do(t, h, admin, "GET", "/admin/treasurer/reports?through=2026-07-31", "", nil).Body.String(); !strings.Contains(now, "$790.00") {
		t.Error("today's report should have the new line")
	}
	do(t, h, admin, "POST", "/admin/treasurer/reports/"+m[1]+"/remove", "", nil)
	if len(a.store.SavedReports()) != 0 {
		t.Fatal("remove")
	}
	do(t, h, admin, "POST", "/admin/treasurer/reports/"+m[1]+"/restore", "", nil)
	if len(a.store.SavedReports()) != 1 {
		t.Error("Undo should put the saved report back")
	}
}

func TestTreasuryKeptInDataFile(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if inc, exp, dues := s.TreasuryCats(); len(inc) != 3 || len(exp) != 12 || dues != "Membership Income" {
		t.Fatalf("Barry's categories: %q %q %q", inc, exp, dues)
	}
	s.AddLedgerEntry(LedgerEntry{Kind: "expense", Date: "2026-01-15", Category: "Office Expenses:PO Box Rental", Cents: 106_00, Who: "Karl"})
	s2, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l := s2.Ledger("expense", 2026); len(l) != 1 || l[0].Cents != 106_00 || l[0].Who != "Karl" {
		t.Errorf("after reopening: %+v", l)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "paxvelo.json")); !strings.Contains(string(b), `"treasury"`) {
		t.Error("the books should be in paxvelo.json")
	}
}

// Admins named on the Board page are marked as board members once, when the
// mark first arrives; after that it's the president's to change.
func TestAdminBoardSeededFromBoardPage(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.d.AdminBoardSeeded = false
	s.d.Board = []BoardMember{{Name: "Barry  Treasurer", Role: "Treasurer"}}
	barry, _ := s.AddAdmin("barry treasurer", "barry@example.com", "")
	helper, _ := s.AddAdmin("Hal Helper", "hal@example.com", "")
	s.save()
	s, _ = openStore(dir)
	if b, _ := s.Admin(barry.ID); !b.Board {
		t.Error("an admin named on the Board page should be marked as a board member")
	}
	if hh, _ := s.Admin(helper.ID); hh.Board {
		t.Error("an admin not on the Board page shouldn't be")
	}
	s.SetAdminBoard(barry.ID, false)
	s, _ = openStore(dir)
	if b, _ := s.Admin(barry.ID); b.Board {
		t.Error("the one-time marking shouldn't undo the president's change")
	}
}

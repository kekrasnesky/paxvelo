package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Expense reports and purchase requests: any board member sends one, the
// treasurer and the president both answer (the sender's own role counts),
// and once both say yes it's on Expenses by itself.
func TestSpendRequests(t *testing.T) {
	a, h, _ := testApp(t)
	t.Cleanup(a.mailer.bg.Wait)
	var mu sync.Mutex
	var mail []string
	a.mailer.onSend = func(to, subject, body string) {
		mu.Lock()
		mail = append(mail, to+"|"+subject+"|"+body)
		mu.Unlock()
	}
	sent := func() []string {
		a.mailer.bg.Wait()
		mu.Lock()
		defer mu.Unlock()
		out := mail
		mail = nil
		return out
	}
	karl, _ := a.store.AddAdmin("Karl", "karl@example.com", "")
	gabe, _ := a.store.AddAdmin("Gabriel Murray", "gabe@example.com", "")
	allie, _ := a.store.AddAdmin("Allie Kampmeyer", "allie@example.com", "")

	a.store.SetPresident(karl.ID)
	a.store.SetBoard([]BoardMember{{Name: "Karl", Role: "President"}, {Name: "Gabe", Role: "Treasurer"}, {Name: "Allie Kampmeyer", Role: "Vice President"}})
	a.store.mu.Lock()
	a.store.d.Members = append(a.store.d.Members, &Member{ID: 701, Name: "Gabriel Murray", Email: "gabe@example.com"})
	a.store.d.Board[1].Member = 701
	a.store.mu.Unlock()
	as := func(u AdminUser) *http.Cookie {
		u, _ = a.store.Admin(u.ID)
		rec := httptest.NewRecorder()
		a.setSession(rec, accountSession(u), 3600)
		return rec.Result().Cookies()[0]
	}
	send := func(u AdminUser, fields map[string]string, file string, content []byte) *httptest.ResponseRecorder {
		ct, body := receiptForm(t, fields, file, content)
		return do(t, h, as(u), "POST", "/admin/treasurer/requests", ct, body)
	}
	post := func(u AdminUser, path string, v url.Values) string {
		ct, body := form(v)
		return do(t, h, as(u), "POST", path, ct, body).Header().Get("Location")
	}

	// A missing "what for" comes back with the form filled in.
	if w := send(allie, map[string]string{"kind": "report", "date": "2026-11-01", "amount": "45"}, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Say what it’s for.") || !strings.Contains(w.Body.String(), `value="45.00"`) {
		t.Fatalf("missing what: %d", w.Code)
	}

	// Allie (VP) sends an expense report with a receipt: both answers wanted.
	pdf := []byte("%PDF-1.4\n% pizza\n%%EOF\n")
	w := send(allie, map[string]string{"kind": "report", "date": "2026-11-01", "amount": "$86.40", "what": "Pizza for the kickoff ride", "note": "Two large pies"}, "pizza.pdf", pdf)
	if !strings.Contains(w.Header().Get("Location"), "ok=request-sent") {
		t.Fatalf("send: %d %s", w.Code, w.Header().Get("Location"))
	}
	q := a.store.SpendRequests()[0]
	if q.Status != reqWaiting || q.Treasurer != nil || q.President != nil || q.By != "Allie Kampmeyer" || q.Cents != 86_40 || q.Receipt == "" {
		t.Fatalf("new request: %+v", q)
	}
	got := sent()
	if len(got) != 2 || !strings.Contains(strings.Join(got, "\n"), "gabe@example.com|Reimburse Allie Kampmeyer $86.40: Pizza for the kickoff ride") ||
		!strings.Contains(strings.Join(got, "\n"), "karl@example.com|") || !strings.Contains(got[0], "Pizza for the kickoff ride") {
		t.Fatalf("the treasurer and president should be emailed: %q", got)
	}

	// Home and the Requests badge show it to them, not to Allie.
	if home := do(t, h, as(gabe), "GET", "/admin", "", nil).Body.String(); !strings.Contains(home, "1 spending request is waiting for your OK.") {
		t.Error("Home should ask the treasurer for an answer")
	}
	page := do(t, h, as(karl), "GET", "/admin/treasurer/requests", "", nil).Body.String()
	if !strings.Contains(page, `Requests <span class="badge">1`) || !strings.Contains(page, "/requests/1/approve") {
		t.Error("the president should see the request to approve, with a badge")
	}
	if page := do(t, h, as(allie), "GET", "/admin/treasurer/requests", "", nil).Body.String(); strings.Contains(page, "/requests/1/approve") || !strings.Contains(page, "/requests/1/withdraw") {
		t.Error("the sender can withdraw but not approve")
	}
	if loc := post(allie, "/admin/treasurer/requests/1/approve", nil); !strings.Contains(loc, "err=request-not-yours") {
		t.Errorf("the VP's approve: %s", loc)
	}

	// The treasurer says yes: still waiting for the president, nobody emailed.
	if loc := post(gabe, "/admin/treasurer/requests/1/approve", nil); !strings.Contains(loc, "ok=request-ok") {
		t.Fatalf("treasurer approve: %s", loc)
	}
	if q, _ := a.store.SpendRequest(1); q.Status != reqWaiting || q.Treasurer == nil || q.Treasurer.By != "Gabriel Murray" || len(a.store.Ledger("expense", 2026)) != 0 {
		t.Fatalf("after the treasurer: %+v", q)
	}
	if loc := post(gabe, "/admin/treasurer/requests/1/approve", nil); !strings.Contains(loc, "err=request-not-yours") {
		t.Error("the treasurer can't answer twice")
	}
	if len(sent()) != 0 {
		t.Error("nobody is emailed until both have answered")
	}

	// The president says yes: it's on Expenses with its receipt, and Allie hears.
	if loc := post(karl, "/admin/treasurer/requests/1/approve", nil); !strings.Contains(loc, "ok=request-approved") {
		t.Fatalf("president approve: %s", loc)
	}
	q, _ = a.store.SpendRequest(1)
	lines := a.store.Ledger("expense", 2026)
	if q.Status != reqApproved || len(lines) != 1 || q.Entry != lines[0].ID {
		t.Fatalf("approved: %+v %+v", q, lines)
	}
	e := lines[0]
	if e.Date != "2026-11-01" || e.Cents != 86_40 || e.Category != "Club Rides & Parties:Food & Drink" || e.Who != "Allie Kampmeyer" || e.Receipt != q.Receipt || e.Request != 1 {
		t.Errorf("expense line: %+v", e)
	}
	if got := sent(); len(got) != 1 || !strings.HasPrefix(got[0], "allie@example.com|Approved: Pizza for the kickoff ride|") || !strings.Contains(got[0], "reimburse you by PayPal") {
		t.Errorf("Allie should hear it's approved: %q", got)
	}
	found := false
	for _, row := range a.buildReport(time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)).Rows {
		found = found || (row.Label == "Club Rides & Parties:Food & Drink" && row.Actual == 86_40)
	}
	if !found {
		t.Error("the approved expense should be on the report")
	}
	// The treasurer pays it back; removing the line keeps the request's receipt.
	if home := do(t, h, as(gabe), "GET", "/admin", "", nil).Body.String(); !strings.Contains(home, "1 approved request to reimburse.") {
		t.Error("Home should remind the treasurer to pay it back")
	}
	post(gabe, "/admin/treasurer/requests/1/paid", url.Values{"paid": {"1"}})
	if q, _ := a.store.SpendRequest(1); q.PaidBack == nil {
		t.Error("paid back")
	}
	post(gabe, fmt.Sprintf("/admin/treasurer/entries/%d/remove", e.ID), nil)
	ct, body := receiptForm(t, map[string]string{"date": "2026-11-02", "amount": "1", "category": "Bank Fees"}, "", nil)
	do(t, h, as(gabe), "POST", "/admin/treasurer/entries?kind=expense", ct, body)
	added := a.store.Ledger("expense", 2026)[0]
	post(gabe, fmt.Sprintf("/admin/treasurer/entries/%d/remove", added.ID), nil) // pushes the pizza line out of Undo
	if _, err := os.Stat(filepath.Join(a.receiptsDir(), q.Receipt)); err != nil {
		t.Error("a request's receipt should stay while the request shows it")
	}

	// A rejection needs a reason and tells the sender why.
	send(allie, map[string]string{"kind": "purchase", "date": "2026-11-20", "amount": "300", "what": "Club socks"}, "", nil)
	sent()
	if loc := post(karl, "/admin/treasurer/requests/2/reject", nil); !strings.Contains(loc, "err=request-reason") {
		t.Errorf("reject without a reason: %s", loc)
	}
	post(karl, "/admin/treasurer/requests/2/reject", url.Values{"reason": {"Let's wait for the spring order"}})
	if q, _ := a.store.SpendRequest(2); q.Status != reqRejected || len(a.store.Ledger("expense", 2026)) != 0 {
		t.Errorf("rejected: %+v", q)
	}
	if got := sent(); len(got) != 1 || !strings.HasPrefix(got[0], "allie@example.com|Not approved: Club socks|") || !strings.Contains(got[0], "spring order") {
		t.Errorf("rejection email: %q", got)
	}
	if loc := post(gabe, "/admin/treasurer/requests/2/approve", nil); !strings.Contains(loc, "err=request-not-yours") {
		t.Error("a rejected request can't be approved")
	}

	// The treasurer's own purchase request needs only the president; once
	// bought, its receipt and final amount update the expense line.
	send(gabe, map[string]string{"kind": "purchase", "date": "2026-11-15", "amount": "120", "what": "PO box renewal"}, "", nil)
	q, _ = a.store.SpendRequest(3)
	if q.Treasurer == nil || !q.Treasurer.Own || q.President != nil {
		t.Fatalf("the treasurer's own answer should count: %+v", q)
	}
	if got := sent(); len(got) != 1 || !strings.HasPrefix(got[0], "karl@example.com|OK to buy? PO box renewal, $120.00 (Gabriel Murray)") {
		t.Errorf("only the president should be asked: %q", got)
	}
	post(karl, "/admin/treasurer/requests/3/approve", nil)
	q, _ = a.store.SpendRequest(3)
	if got := sent(); q.Status != reqApproved || len(got) != 1 || !strings.Contains(got[0], "Go ahead and buy it") {
		t.Fatalf("purchase approved: %+v %q", q, got)
	}
	var jpg bytes.Buffer
	jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 40, 30)), nil)
	ct, body = receiptForm(t, map[string]string{"date": "2026-11-18", "amount": "112.50"}, "box.jpg", jpg.Bytes())
	if w := do(t, h, as(gabe), "POST", "/admin/treasurer/requests/3/bought", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=request-bought") {
		t.Fatalf("bought: %s", w.Header().Get("Location"))
	}
	q, _ = a.store.SpendRequest(3)
	e, _ = a.store.LedgerEntry(q.Entry)
	if q.Spent != 112_50 || q.Bought != "2026-11-18" || q.Receipt == "" || e.Cents != 112_50 || e.Date != "2026-11-18" || e.Receipt != q.Receipt {
		t.Errorf("bought: %+v %+v", q, e)
	}

	// Withdraw, with Undo.
	send(allie, map[string]string{"kind": "report", "date": "2026-11-03", "amount": "20", "what": "Wire fee"}, "", nil)
	sent()
	if loc := post(gabe, "/admin/treasurer/requests/4/withdraw", nil); !strings.Contains(loc, "err=request-not-yours") {
		t.Error("only the sender withdraws")
	}
	post(allie, "/admin/treasurer/requests/4/withdraw", nil)
	if q, _ := a.store.SpendRequest(4); q.Status != reqWithdrawn {
		t.Errorf("withdrawn: %+v", q)
	}
	post(allie, "/admin/treasurer/requests/4/restore", nil)
	if q, _ := a.store.SpendRequest(4); q.Status != reqWaiting {
		t.Errorf("undo withdraw: %+v", q)
	}

	// Pages draw for everyone on the board, and the data file keeps them.
	for _, u := range []AdminUser{karl, gabe, allie} {
		if w := do(t, h, as(u), "GET", "/admin/treasurer/requests", "", nil); w.Code != 200 {
			t.Errorf("requests page for %s: %d", u.Name, w.Code)
		}
	}
	if exp := do(t, h, as(gabe), "GET", "/admin/treasurer?year=2026", "", nil).Body.String(); !strings.Contains(exp, `href="/admin/treasurer/requests#r3"`) {
		t.Error("an expense from a request should link to it")
	}
	again, err := openStore(a.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.SpendRequests()) != 4 {
		t.Error("requests should be kept in the data file")
	}
}

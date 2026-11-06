package main

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// reqKit is a club with Karl (president), Gabe (treasurer, Gabriel on his
// account) and Allie (VP), and helpers to act as each of them.
type reqKit struct {
	a                 *App
	h                 http.Handler
	karl, gabe, allie AdminUser
	mu                sync.Mutex
	mail              []string
}

func newReqKit(t *testing.T) *reqKit {
	a, h, _ := testApp(t)
	k := &reqKit{a: a, h: h}
	a.mailer.onSend = func(to, subject, body string) {
		k.mu.Lock()
		k.mail = append(k.mail, to+"|"+subject+"|"+body)
		k.mu.Unlock()
	}
	k.karl, _ = a.store.AddAdmin("Karl", "karl@example.com", "")
	k.gabe, _ = a.store.AddAdmin("Gabriel Murray", "gabe@example.com", "")
	k.allie, _ = a.store.AddAdmin("Allie Kampmeyer", "allie@example.com", "")
	a.store.SetPresident(k.karl.ID)
	a.store.SetBoard([]BoardMember{{Name: "Karl", Role: "President"}, {Name: "Gabriel Murray", Role: "Treasurer"}, {Name: "Allie Kampmeyer", Role: "Vice President"}})
	return k
}

func (k *reqKit) sent() []string {
	k.a.mailer.bg.Wait()
	k.mu.Lock()
	defer k.mu.Unlock()
	out := k.mail
	k.mail = nil
	return out
}

func (k *reqKit) as(u AdminUser) *http.Cookie {
	u, _ = k.a.store.Admin(u.ID)
	rec := httptest.NewRecorder()
	k.a.setSession(rec, accountSession(u), 3600)
	return rec.Result().Cookies()[0]
}

func (k *reqKit) send(t *testing.T, u AdminUser, fields map[string]string, file string, content []byte) *httptest.ResponseRecorder {
	ct, body := receiptForm(t, fields, file, content)
	return do(t, k.h, k.as(u), "POST", "/admin/treasurer/requests", ct, body)
}

func (k *reqKit) post(t *testing.T, u AdminUser, path string, v url.Values) string {
	ct, body := form(v)
	return do(t, k.h, k.as(u), "POST", path, ct, body).Header().Get("Location")
}

func (k *reqKit) budget(t *testing.T, cat string, cents int64) {
	t.Helper()
	k.a.store.mu.Lock()
	y := k.a.store.d.Treasury.Years
	if y == nil {
		y = map[int]*BudgetYear{}
		k.a.store.d.Treasury.Years = y
	}
	b := y[2026]
	if b == nil {
		b = &BudgetYear{Budget: map[string]int64{}}
		y[2026] = b
	}
	b.Budget[cat] = cents
	k.a.store.mu.Unlock()
}

func TestGuessCategory(t *testing.T) {
	cats := append([]string(nil), defaultExpenseCats...)
	for what, want := range map[string]string{
		"Pizza for the kickoff ride":    "Club Rides & Parties:Food & Drink",
		"Coffee and donuts":             "Club Rides & Parties:Food & Drink",
		"Club socks, 40 pairs":          "Kit Expenses:Sock Order",
		"PO box renewal":                "Office Expenses:PO Box Rental",
		"Liability insurance":           "Insurance",
		"Quicken subscription":          "Office Expenses:Quicken",
		"Wire fee":                      "Bank Fees",
		"Domain renewal for paxvelo.cc": "Website Fees: Domain/Marketing",
		"Something else entirely":       "Club Rides & Parties:Food & Drink", // the club-rides category when nothing fits
	} {
		if got := guessCategory(what, cats, nil, 1, "Allie"); got != want {
			t.Errorf("guess %q = %q, want %q", what, got, want)
		}
	}
	// Past requests teach it: the treasurer filed the last banner as Donations.
	past := []SpendRequest{{What: "Banner for the charity ride", Category: "Donations", ByID: 2, Status: reqApproved}}
	if got := guessCategory("New banner", cats, past, 1, "Allie"); got != "Donations" {
		t.Errorf("a past request's category should win: %q", got)
	}
	// The sender's last request, when no word helps.
	past = []SpendRequest{{What: "Tolls", Category: "Registration Fees", ByID: 1, Status: reqApproved}}
	if got := guessCategory("Misc", cats, past, 1, "Allie"); got != "Registration Fees" {
		t.Errorf("the sender's last category: %q", got)
	}
	if got := guessCategory("Misc", nil, nil, 1, "Allie"); got != "" {
		t.Errorf("no categories: %q", got)
	}
}

// Up to the limit, within the category's budget, the treasurer's yes is
// enough; above it, or over budget, both answers are needed.
func TestOneApprovalLimit(t *testing.T) {
	k := newReqKit(t)
	food := "Club Rides & Parties:Food & Drink"
	k.budget(t, food, 400_00)

	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-01", "amount": "30", "what": "Pizza"}, "", nil)
	q, _ := k.a.store.SpendRequest(1)
	if !q.One || q.Category != food {
		t.Fatalf("$30 within budget should need one answer: %+v", q)
	}
	if got := k.sent(); len(got) != 1 || !strings.HasPrefix(got[0], "gabe@example.com|") {
		t.Errorf("only the treasurer should be asked: %q", got)
	}
	page := do(t, k.h, k.as(k.karl), "GET", requestsPath, "", nil).Body.String()
	if strings.Contains(page, "/requests/1/approve") {
		t.Error("the president isn't asked for a small one")
	}
	if !strings.Contains(page, html.EscapeString(food)+": $400.00 of $400.00 left in 2026") {
		t.Error("the board sees what's left of the budget")
	}
	if loc := k.post(t, k.gabe, "/admin/treasurer/requests/1/approve", nil); !strings.Contains(loc, "ok=request-approved") {
		t.Fatalf("the treasurer's yes should settle it: %s", loc)
	}

	// Over the limit: two answers.
	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-02", "amount": "80", "what": "Pizza party"}, "", nil)
	if q, _ := k.a.store.SpendRequest(2); q.One {
		t.Error("$80 is over the limit")
	}
	// Over what's left of the budget: two answers.
	k.budget(t, food, 50_00) // $30 spent, $20 left
	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-03", "amount": "25", "what": "Coffee"}, "", nil)
	if q, _ := k.a.store.SpendRequest(3); q.One {
		t.Error("$25 is over the $20 left")
	}
	k.sent()

	// The treasurer's own small one needs the president.
	k.budget(t, food, 400_00)
	k.send(t, k.gabe, map[string]string{"kind": "report", "date": "2026-11-04", "amount": "12", "what": "Ice for the cooler"}, "", nil)
	q, _ = k.a.store.SpendRequest(4)
	if !q.One || q.Status != reqWaiting || !q.Treasurer.Own {
		t.Fatalf("treasurer's own: %+v", q)
	}
	if got := k.sent(); len(got) != 1 || !strings.HasPrefix(got[0], "karl@example.com|") {
		t.Errorf("the president should be asked: %q", got)
	}
	k.post(t, k.karl, "/admin/treasurer/requests/4/approve", nil)
	if q, _ := k.a.store.SpendRequest(4); q.Status != reqApproved {
		t.Errorf("the president's yes settles the treasurer's own: %+v", q)
	}

	// Changing the category to one with no budget makes it two answers.
	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-05", "amount": "10", "what": "Bagels"}, "", nil)
	k.post(t, k.gabe, "/admin/treasurer/requests/5/category", url.Values{"category": {"Registration Fees"}})
	if q, _ := k.a.store.SpendRequest(5); q.One || q.Category != "Registration Fees" {
		t.Errorf("category change: %+v", q)
	}
	if loc := k.post(t, k.allie, "/admin/treasurer/requests/5/category", url.Values{"category": {food}}); !strings.Contains(loc, "err=request-not-yours") {
		t.Error("only the treasurer or president changes the category")
	}

	// A blank limit means two answers always; raising it again settles a
	// request the treasurer already said yes to.
	k.post(t, k.gabe, "/admin/treasurer/budget/one-ok", url.Values{"limit": {""}})
	if k.a.store.OneLimit() != 0 {
		t.Fatal("blank limit")
	}
	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-06", "amount": "60", "what": "Pizza"}, "", nil)
	k.post(t, k.gabe, "/admin/treasurer/requests/6/approve", nil)
	if q, _ := k.a.store.SpendRequest(6); q.One || q.Status != reqWaiting {
		t.Fatalf("limit 0: %+v", q)
	}
	k.sent()
	k.post(t, k.karl, "/admin/treasurer/budget/one-ok", url.Values{"limit": {"75"}})
	if q, _ := k.a.store.SpendRequest(6); !q.One || q.Status != reqApproved {
		t.Errorf("raising the limit should settle it: %+v", q)
	}
	if got := k.sent(); len(got) != 1 || !strings.HasPrefix(got[0], "allie@example.com|Approved") {
		t.Errorf("Allie hears: %q", got)
	}
	sam, _ := k.a.store.AddAdmin("Sam", "sam@example.com", "")
	if loc := k.post(t, sam, "/admin/treasurer/budget/one-ok", url.Values{"limit": {"500"}}); k.a.store.OneLimit() != 75_00 {
		t.Errorf("the board sets the limit, not website admins: %s", loc)
	}
}

var answerLinkRE = regexp.MustCompile(`https://www\.paxvelo\.club(/request-answer/[^?)\s]+)\?do=approve`)

// The email's Approve and Reject links: opening one changes nothing; the
// page's button answers once; old or tampered links are refused.
func TestAnswerFromEmail(t *testing.T) {
	k := newReqKit(t)
	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-01", "amount": "86.40", "what": "Pizza for the kickoff ride"}, "pizza.pdf", []byte("%PDF-1.4\n%%EOF\n"))
	links := map[string]string{}
	for _, m := range k.sent() {
		to, _, _ := strings.Cut(m, "|")
		if l := answerLinkRE.FindStringSubmatch(m); l != nil {
			links[to] = l[1]
		}
		if !strings.Contains(m, "[[Approve](") || !strings.Contains(m, "?do=reject") {
			t.Errorf("the email needs Approve and Reject: %q", m)
		}
	}
	gabe, karl := links["gabe@example.com"], links["karl@example.com"]
	if gabe == "" || karl == "" || gabe == karl {
		t.Fatalf("each approver gets their own link: %q", links)
	}

	// Opening it (as an email scanner would) does nothing.
	page := do(t, k.h, nil, "GET", gabe+"?do=approve", "", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Pizza for the kickoff ride") || !strings.Contains(page.Body.String(), `value="approve"`) {
		t.Fatalf("answer page: %d", page.Code)
	}
	if q, _ := k.a.store.SpendRequest(1); q.Treasurer != nil {
		t.Fatal("opening the link must not answer")
	}
	if w := do(t, k.h, nil, "GET", gabe+"/receipt", "", nil); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "%PDF") {
		t.Errorf("the receipt opens from the link: %d", w.Code)
	}

	// The button answers, once.
	approve := func(link string) string {
		ct, body := form(url.Values{"answer": {"approve"}})
		return do(t, k.h, nil, "POST", link, ct, body).Body.String()
	}
	if body := approve(gabe); !strings.Contains(body, "waiting for the president") {
		t.Errorf("after the treasurer: %s", body)
	}
	if q, _ := k.a.store.SpendRequest(1); q.Treasurer == nil || q.Treasurer.By != "Gabriel Murray" || q.Status != reqWaiting {
		t.Fatalf("treasurer's answer: %+v", q)
	}
	if body := approve(gabe); !strings.Contains(body, "Your answer is in") {
		t.Errorf("a second press only shows the state: %s", body)
	}

	// Reject needs a reason.
	ct, body := form(url.Values{"answer": {"reject"}})
	if w := do(t, k.h, nil, "POST", karl, ct, body).Body.String(); !strings.Contains(w, "Say why it’s rejected.") {
		t.Error("reject without a reason")
	}
	ct, body = form(url.Values{"answer": {"reject"}, "reason": {"Over the party budget"}})
	if w := do(t, k.h, nil, "POST", karl, ct, body).Body.String(); !strings.Contains(w, "Rejected.") {
		t.Errorf("reject: %s", w)
	}
	if q, _ := k.a.store.SpendRequest(1); q.Status != reqRejected || q.President.Reason != "Over the party budget" {
		t.Fatalf("rejected: %+v", q)
	}
	if got := k.sent(); len(got) != 1 || !strings.Contains(got[0], "Over the party budget") {
		t.Errorf("Allie hears why: %q", got)
	}
	if body := approve(karl); !strings.Contains(body, "Already rejected by Karl") {
		t.Errorf("reused link: %s", body)
	}

	// Expired and tampered links are refused, and nothing changes.
	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-09", "amount": "20", "what": "Wire fee"}, "", nil)
	k.sent()
	q2, _ := k.a.store.SpendRequest(2)
	old := answerPath + k.a.answerToken(q2, k.gabe, time.Now().Add(-time.Minute))
	if body := approve(old); !strings.Contains(body, "This link has expired") {
		t.Error("an expired link should be refused")
	}
	good := k.a.answerToken(q2, k.gabe, time.Now().Add(time.Hour))
	if body := approve(answerPath + strings.Replace(good, "2.", "3.", 1)); !strings.Contains(body, "This link has expired") {
		t.Error("a changed link should be refused")
	}
	allies := answerPath + k.a.answerToken(q2, k.allie, time.Now().Add(time.Hour))
	if body := approve(allies); strings.Contains(body, "Approved.") {
		t.Error("a link for someone whose answer isn't wanted does nothing")
	}
	if q, _ := k.a.store.SpendRequest(2); q.Treasurer != nil || q.President != nil {
		t.Errorf("nothing should have been answered: %+v", q)
	}
	if w := do(t, k.h, nil, "GET", old+"/receipt", "", nil); w.Code != 404 {
		t.Error("an expired link's receipt")
	}
}

// The same sender, amount and date within three days: a warning with Send
// anyway, and the receipt they picked is kept.
func TestDuplicateWarning(t *testing.T) {
	k := newReqKit(t)
	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-01", "amount": "45", "what": "Pizza"}, "", nil)
	w := k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-03", "amount": "45.00", "what": "Pizza again"}, "p.pdf", []byte("%PDF-1.4\n%%EOF\n"))
	page := w.Body.String()
	if w.Code != 200 || !strings.Contains(page, "You sent $45.00 on Nov 1 already.") || !strings.Contains(page, `name="dup_ok"`) {
		t.Fatalf("duplicate warning: %d %s", w.Code, w.Header().Get("Location"))
	}
	if n := len(k.a.store.SpendRequests()); n != 1 {
		t.Fatalf("nothing sent yet: %d", n)
	}
	kept := regexp.MustCompile(`name="kept" value="([^"]+)"`).FindStringSubmatch(page)
	if kept == nil {
		t.Fatal("the receipt should be kept for Send anyway")
	}
	// A different amount, or a week apart, isn't a duplicate.
	if loc := k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-09", "amount": "45", "what": "Pizza"}, "", nil).Header().Get("Location"); !strings.Contains(loc, "ok=request-sent") {
		t.Errorf("a week later: %s", loc)
	}
	// Send anyway, with the kept receipt.
	loc := k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-03", "amount": "45", "what": "Pizza again", "kept": kept[1], "dup_ok": "1"}, "", nil).Header().Get("Location")
	if !strings.Contains(loc, "ok=request-sent") {
		t.Fatalf("send anyway: %s", loc)
	}
	q := k.a.store.SpendRequests()[0]
	if q.What != "Pizza again" || q.Receipt == "" || q.ReceiptName != "p.pdf" {
		t.Errorf("sent anyway with the receipt: %+v", q)
	}
	// Someone else's kept receipt isn't accepted.
	w = k.send(t, k.karl, map[string]string{"kind": "report", "date": "2026-10-01", "amount": "5", "what": "x", "kept": kept[1]}, "", nil)
	if q := k.a.store.SpendRequests()[0]; q.By == "Karl" && q.Receipt != "" {
		t.Error("a kept receipt is only for whoever uploaded it")
	}
}

// Reimbursements go by PayPal to the sender's email, and the treasurer's
// list says so.
func TestPayBack(t *testing.T) {
	k := newReqKit(t)
	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-01", "amount": "45", "what": "Pizza"}, "", nil)
	if page := do(t, k.h, k.as(k.allie), "GET", requestsPath, "", nil).Body.String(); !strings.Contains(page, "Reimbursed by PayPal to allie@example.com") || !strings.Contains(page, `class="track"`) || strings.Contains(page, "pay_by") {
		t.Error("her form says PayPal (no other way), and her request shows as a tracker")
	}
	k.post(t, k.gabe, "/admin/treasurer/requests/1/approve", nil)
	k.post(t, k.karl, "/admin/treasurer/requests/1/approve", nil)
	page := do(t, k.h, k.as(k.gabe), "GET", requestsPath, "", nil).Body.String()
	if !strings.Contains(page, "Pay <strong>Allie</strong> $45.00 <span class=\"pay-how\">— PayPal allie@example.com</span>") {
		t.Error("the treasurer's to-pay list")
	}
	k.sent()
	k.post(t, k.gabe, "/admin/treasurer/requests/1/paid", url.Values{"paid": {"1"}})
	if got := k.sent(); len(got) != 1 || !strings.HasPrefix(got[0], "PaxVeloClub@gmail.com|Reimbursed: Allie Kampmeyer $45.00, Pizza|") ||
		!strings.Contains(got[0], "PayPal allie@example.com") || !strings.Contains(got[0], "marked by Gabriel Murray") {
		t.Errorf("the reimbursement receipt goes to the club inbox only: %q", got)
	}
	k.post(t, k.gabe, "/admin/treasurer/requests/1/paid", nil) // Not reimbursed (undo) sends nothing
	k.post(t, k.gabe, "/admin/treasurer/requests/1/paid", url.Values{"paid": {"1"}})
	if got := k.sent(); len(got) != 1 {
		t.Errorf("pressing Paid again after an undo sends one more receipt: %q", got)
	}

	// A PayPal email on her own Admins entry is used instead of her login.
	if loc := k.post(t, k.allie, "/admin/admins/paypal", url.Values{"paypal": {"not an email"}}); !strings.Contains(loc, "err=paypal-email") {
		t.Errorf("a bad PayPal email: %s", loc)
	}
	k.post(t, k.allie, "/admin/admins/paypal", url.Values{"paypal": {"Allie.K@Yahoo.com"}})
	if u, _ := k.a.store.Admin(k.allie.ID); u.PayPal != "allie.k@yahoo.com" {
		t.Fatalf("PayPal saved: %+v", u)
	}
	if page := do(t, k.h, k.as(k.allie), "GET", requestsPath, "", nil).Body.String(); !strings.Contains(page, "Reimbursed by PayPal to allie.k@yahoo.com") {
		t.Error("her form shows her PayPal email")
	}
	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-04", "amount": "12", "what": "Ice"}, "", nil)
	k.post(t, k.gabe, "/admin/treasurer/requests/2/approve", nil)
	k.post(t, k.karl, "/admin/treasurer/requests/2/approve", nil)
	if page := do(t, k.h, k.as(k.gabe), "GET", requestsPath, "", nil).Body.String(); !strings.Contains(page, "— PayPal allie.k@yahoo.com") {
		t.Error("the treasurer's list uses her PayPal email")
	}
	k.post(t, k.allie, "/admin/admins/paypal", url.Values{"paypal": {""}})
	k.post(t, k.gabe, "/admin/treasurer/requests/2/paid", url.Values{"paid": {"1"}})
	if u, _ := k.a.store.Admin(k.allie.ID); u.PayPal != "" || u.PayPalEmail() != "allie@example.com" {
		t.Errorf("blank goes back to her login email: %+v", u)
	}
	k.sent()
	if page := do(t, k.h, k.as(k.gabe), "GET", requestsPath, "", nil).Body.String(); strings.Contains(page, `id="pay"`) {
		t.Error("paid back leaves the list")
	}
	// Website admins see only their own, with no budget figures.
	sam, _ := k.a.store.AddAdmin("Sam", "sam@example.com", "")
	k.send(t, sam, map[string]string{"kind": "purchase", "date": "2026-11-02", "amount": "15", "what": "Cones"}, "", nil)
	page = do(t, k.h, k.as(sam), "GET", requestsPath, "", nil).Body.String()
	if strings.Contains(page, "<h3>Pizza</h3>") || !strings.Contains(page, "<h3>Cones</h3>") || strings.Contains(page, "left in 2026") || strings.Contains(page, `class="req-hint`) {
		t.Error("a website admin sees only their own, no budgets")
	}
}

// Reminders go once each, from 9 am.
func TestRequestReminders(t *testing.T) {
	k := newReqKit(t)
	loc := k.a.cfg.Location
	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-01", "amount": "80", "what": "Pizza"}, "", nil)
	k.send(t, k.allie, map[string]string{"kind": "purchase", "date": "2026-11-01", "amount": "300", "what": "Socks"}, "", nil)
	k.send(t, k.allie, map[string]string{"kind": "report", "date": "2026-11-01", "amount": "20", "what": "Wire fee"}, "", nil)
	for _, id := range []int{2, 3} {
		k.post(t, k.gabe, "/admin/treasurer/requests/"+string(rune('0'+id))+"/approve", nil)
		k.post(t, k.karl, "/admin/treasurer/requests/"+string(rune('0'+id))+"/approve", nil)
	}
	k.sent()
	monday := time.Date(2026, 11, 16, 10, 0, 0, 0, loc) // a Monday
	k.a.store.mu.Lock()
	for _, q := range k.a.store.d.Treasury.Requests {
		q.Sent = monday.Add(-4 * 24 * time.Hour)
		if q.Approved != nil {
			t := monday.Add(-11 * 24 * time.Hour)
			q.Approved = &t
		}
	}
	k.a.store.mu.Unlock()

	k.a.runRequestReminders(monday.Add(-2 * time.Hour)) // 8 am: nothing yet
	if got := k.sent(); len(got) != 0 {
		t.Fatalf("nothing before 9 am: %q", got)
	}
	k.a.runRequestReminders(monday)
	got := strings.Join(k.sent(), "\n")
	for _, want := range []string{
		"gabe@example.com|Reminder: Reimburse Allie Kampmeyer $80.00: Pizza|",
		"karl@example.com|Reminder: Reimburse Allie Kampmeyer $80.00: Pizza|",
		"gabe@example.com|To reimburse: 1 request, $20.00|",
		"allie@example.com|Receipt for Socks?|",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if !strings.Contains(got, "[[Approve](") {
		t.Error("the reminder has the Approve button")
	}
	// Again (another tick, or after a restart): nothing new.
	k.a.runRequestReminders(monday.Add(15 * time.Minute))
	again, err := openStore(k.a.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	k.a.store = again
	k.a.runRequestReminders(monday.Add(time.Hour))
	if got := k.sent(); len(got) != 0 {
		t.Errorf("each reminder goes once: %q", got)
	}
	// Next Monday the pay-back list goes again.
	k.a.runRequestReminders(monday.AddDate(0, 0, 7))
	if got := k.sent(); len(got) != 1 || !strings.Contains(got[0], "To reimburse") {
		t.Errorf("next week's list: %q", got)
	}
}

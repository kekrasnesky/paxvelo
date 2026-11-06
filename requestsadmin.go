package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Treasurer → Requests (requests.go has the rules): the sender's own
// requests as a tracker, the form to send one, the ones waiting with Approve
// and Reject for the treasurer and the president, who to pay back, and the
// ones settled.

type requestsView struct {
	Answer  []requestRow // waiting for this admin's answer
	PayBack []requestRow // approved, to pay back (officers)
	Mine    []requestRow // the logged-in admin's own, for the tracker
	Waiting []requestRow // the rest still waiting (board)
	Done    []requestRow // settled (board)
	Form    SpendRequest
	Kept    string // a receipt already uploaded with the form, signed
	Dup     string // "You sent $45.00 on Nov 1 already."
	Hint    string // the budget line by the amount (board)
	Over    bool
	Board   bool // sees budgets and everyone's requests
	Events  []reqEventChoice
	Cats    []string
	PayPal  string // the sender's email, where PayPal reimburses them
	// No admin holds the role, so requests would wait forever.
	NoTreasurer, NoPresident bool
	Withdrawn                int // the one just withdrawn, for Undo
}

// reqEventChoice is a ride or event in the form's picker.
type reqEventChoice struct{ Key, Label string }

// requestRow is a request with what the logged-in admin may do to it.
type requestRow struct {
	SpendRequest
	Decide    bool   // their answer is wanted
	SetCat    bool   // they may change its category (treasurer or president, while waiting)
	Withdraw  bool   // they sent it and it's still waiting
	AddBought bool   // an approved purchase: add the receipt and final amount
	PayBack   bool   // an approved expense report: mark it paid back
	Hint      string // budget line for the board
	Over      bool
	PayTo     string   // "PayPal allie@example.com"
	First     string   // the sender's first name
	Cats      []string // for SetCat
	Shown     bool     // drawn as a card above the Done table
}

func (a *App) isTreasurer(u AdminUser) bool {
	return slices.ContainsFunc(a.boardRoles(u), isTreasurerRole)
}

// mayOpenReceipt reports whether the logged-in admin may open a receipt
// file: the board any, a website admin only one on their own request.
func (a *App) mayOpenReceipt(r *http.Request, name string) bool {
	if a.isBoard(r) {
		return true
	}
	u, ok := a.currentAdmin(r)
	if !ok {
		return false
	}
	for _, q := range a.store.SpendRequests() {
		if q.Receipt == name && ownRequest(q, u) {
			return true
		}
	}
	return false
}

// isPresident: the president, or the setup password (as for canManage).
func isPresident(u AdminUser) bool { return u.President || u.ID == 0 }

func ownRequest(q SpendRequest, u AdminUser) bool {
	return q.ByID == u.ID && (u.ID != 0 || sameName(q.By, u.Name))
}

// wantsAnswer reports whether a waiting request needs this admin's answer:
// the treasurer's always, the president's unless one answer is enough.
func (a *App) wantsAnswer(q SpendRequest, u AdminUser) bool {
	if q.Status != reqWaiting || ownRequest(q, u) {
		return false
	}
	return (q.Treasurer == nil && a.isTreasurer(u)) || (q.President == nil && q.presidentNeeded() && isPresident(u))
}

func (a *App) officer(u AdminUser) bool { return a.isTreasurer(u) || isPresident(u) }

// payTo is where the treasurer sends a reimbursement: always PayPal, to
// the sender's email (as their account has it now).
func (a *App) payTo(q SpendRequest) string {
	email := q.ByEmail
	if q.ByID != 0 {
		if u, ok := a.store.Admin(q.ByID); ok && u.PayPalEmail() != "" {
			email = u.PayPalEmail()
		}
	}
	return strings.TrimSpace("PayPal " + email)
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

func (a *App) requestRowFor(q SpendRequest, u AdminUser) requestRow {
	officer := a.officer(u)
	row := requestRow{SpendRequest: q,
		Decide:    a.wantsAnswer(q, u),
		SetCat:    q.Status == reqWaiting && officer,
		Withdraw:  q.Status == reqWaiting && ownRequest(q, u),
		AddBought: q.Status == reqApproved && q.Kind == reqPurchase && (officer || ownRequest(q, u)),
		PayBack:   q.Status == reqApproved && q.Kind == reqReport && officer,
		First:     firstWord(q.By),
	}
	if q.Kind == reqReport {
		row.PayTo = a.payTo(q)
	}
	if row.SetCat {
		_, row.Cats, _ = a.store.TreasuryCats()
	}
	return row
}

const requestsPath = "/admin/treasurer/requests"

func (a *App) requestsPage(w http.ResponseWriter, r *http.Request) {
	a.renderRequests(w, r, requestsView{})
}

// reqEventChoices are the rides and events of the last three weeks and the
// next month, for the form's picker.
func (a *App) reqEventChoices() []reqEventChoice {
	today := a.today()
	var out []reqEventChoice
	for _, o := range a.store.Rides(today.AddDate(0, 0, -21), today.AddDate(0, 0, 31)) {
		if o.Cancelled {
			continue
		}
		t, _ := time.Parse("2006-01-02", o.Date)
		out = append(out, reqEventChoice{Key: o.Key, Label: t.Format("Mon Jan 2") + " · " + o.Name})
	}
	return out
}

// renderRequests draws the page; in carries the form (and its problem) when
// it comes back.
func (a *App) renderRequests(w http.ResponseWriter, r *http.Request, in requestsView, problem ...string) {
	u, _ := a.currentAdmin(r)
	if u.ID != 0 {
		u, _ = a.store.Admin(u.ID) // with their saved payback
	}
	_, expense, _ := a.store.TreasuryCats()
	rv := &in
	rv.Cats, rv.NoTreasurer, rv.NoPresident = expense, true, true
	rv.Board, rv.Events = a.isBoard(r), a.reqEventChoices()
	rv.PayPal = u.PayPalEmail()
	for _, ad := range a.store.Admins() {
		if a.isTreasurer(ad) {
			rv.NoTreasurer = false
		}
		if ad.President {
			rv.NoPresident = false
		}
	}
	rv.Withdrawn, _ = strconv.Atoi(r.URL.Query().Get("withdrawn"))
	recent := time.Now().AddDate(0, 0, -30)
	for _, q := range a.store.SpendRequests() {
		own := ownRequest(q, u)
		if !rv.Board && !own {
			continue // website admins see only their own
		}
		row := a.requestRowFor(q, u)
		if rv.Board && q.Status == reqWaiting {
			row.Hint, row.Over = a.budgetHint(q.Category, q.Cents, q.Date)
		}
		card := row
		card.Shown = true
		switch {
		case row.Decide:
			rv.Answer = append(rv.Answer, card)
		case own && q.Status != reqWithdrawn && (!q.Finished() || q.Sent.After(recent)):
			rv.Mine = append(rv.Mine, card)
		case q.Status == reqWaiting:
			rv.Waiting = append(rv.Waiting, card)
		default:
			card.Shown = false
		}
		row.Shown = card.Shown
		if row.PayBack && q.PaidBack == nil {
			rv.PayBack = append(rv.PayBack, row)
		}
		if rv.Board && q.Status != reqWaiting && len(rv.Done) < 60 {
			rv.Done = append(rv.Done, row)
		}
	}
	// Waiting ones oldest first.
	slices.Reverse(rv.Answer)
	slices.Reverse(rv.Waiting)
	slices.Reverse(rv.PayBack)
	if rv.Form.Date == "" {
		rv.Form.Kind, rv.Form.Date = reqReport, a.today().Format("2006-01-02")
	}
	v := adminView{Title: "Requests", Tab: "requests", Money: &moneyView{Requests: rv}}
	v.Flash, v.Error = flashFrom(r)
	if len(problem) > 0 && problem[0] != "" {
		v.Flash, v.Error = "", problem[0]
	}
	a.renderAdmin(w, r, http.StatusOK, "money-requests", v)
}

// readAmountDate reads the date, amount and receipt boxes shared by the
// request form and the purchase's receipt form. The form has two receipt
// boxes (the camera, and a photo or PDF already on the device); whichever
// was used counts.
func (a *App) readAmountDate(w http.ResponseWriter, r *http.Request) (date string, cents int64, receipt, receiptName, problem string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReceipt+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return "", 0, "", "", "That receipt is too big. Receipts can be up to 20 MB."
	}
	if r.MultipartForm != nil {
		for _, hdr := range r.MultipartForm.File["receipt"] {
			if hdr.Size == 0 {
				continue
			}
			name, err := a.saveReceipt(hdr)
			if err != nil && err != errNoReceipt {
				return "", 0, "", "", "The receipt wasn’t saved: " + err.Error()
			}
			if err == nil {
				receipt, receiptName = name, filepath.Base(hdr.Filename)
				break
			}
		}
	}
	date = strings.TrimSpace(r.FormValue("date"))
	c, ok := parseMoney(r.FormValue("amount"))
	switch {
	case !validDate(date):
		return date, c, receipt, receiptName, "Enter the date."
	case !ok || c <= 0:
		return date, c, receipt, receiptName, "Enter the amount, like 45.00."
	}
	return date, c, receipt, receiptName, ""
}

// A receipt uploaded with a form that comes back (a missing box, or the
// duplicate warning) is kept, so it needn't be picked again: the form
// carries its file name, signed for this admin.
func (a *App) keepKey() []byte {
	k := sha256.Sum256([]byte("spend-keep\x00" + a.store.Secret()))
	return k[:]
}

func (a *App) keptReceipt(u AdminUser, name, orig string) string {
	if name == "" {
		return ""
	}
	v := name + "|" + orig
	return v + "|" + sign(a.keepKey(), fmt.Sprintf("%d|%s", u.ID, v))[:32]
}

func (a *App) readKept(u AdminUser, kept string) (name, orig string) {
	i := strings.LastIndex(kept, "|")
	if i < 0 {
		return "", ""
	}
	v, sig := kept[:i], kept[i+1:]
	if !hmac.Equal([]byte(sig), []byte(sign(a.keepKey(), fmt.Sprintf("%d|%s", u.ID, v))[:32])) {
		return "", ""
	}
	name, orig, _ = strings.Cut(v, "|")
	if !receiptName.MatchString(name) || a.store.ReceiptInUse(name) {
		return "", ""
	}
	return name, orig
}

// duplicateOf finds a request from the same sender for the same amount
// within three days, which is most likely the same one sent twice.
func (a *App) duplicateOf(q SpendRequest, u AdminUser) (SpendRequest, bool) {
	d, err := time.Parse("2006-01-02", q.Date)
	if err != nil {
		return SpendRequest{}, false
	}
	for _, p := range a.store.SpendRequests() {
		if !ownRequest(p, u) || p.Cents != q.Cents || p.Status == reqWithdrawn || p.Status == reqRejected {
			continue
		}
		pd, err := time.Parse("2006-01-02", p.Date)
		if err == nil && pd.Sub(d).Abs() <= 3*24*time.Hour {
			return p, true
		}
	}
	return SpendRequest{}, false
}

// sendRequest saves a new request and emails whoever's answer it needs.
func (a *App) sendRequest(w http.ResponseWriter, r *http.Request) {
	u, _ := a.currentAdmin(r)
	if u.ID != 0 {
		u, _ = a.store.Admin(u.ID)
	}
	q := SpendRequest{Kind: reqReport}
	date, cents, receipt, receiptName, problem := a.readAmountDate(w, r)
	if receipt == "" {
		receipt, receiptName = a.readKept(u, r.FormValue("kept"))
	}
	if r.FormValue("kind") == reqPurchase {
		q.Kind = reqPurchase
	}
	q.Date, q.Cents, q.Receipt, q.ReceiptName = date, cents, receipt, receiptName
	q.What = cleanName(r.FormValue("what"))
	q.Note = strings.TrimSpace(r.FormValue("note"))
	if len(q.Note) > 1000 {
		q.Note = q.Note[:1000]
	}
	if key := r.FormValue("event"); key != "" {
		for _, e := range a.store.Rides(a.today().AddDate(0, 0, -60), a.today().AddDate(0, 0, 60)) {
			if e.Key == key {
				q.EventKey, q.EventName, q.EventDate = e.Key, e.Name, e.Date
			}
		}
	}
	q.By, q.ByID, q.ByEmail = u.Name, u.ID, u.Email
	q.Category = a.guessFor(q.What, u)
	if problem == "" && q.What == "" {
		problem = "Say what it’s for."
	}
	again := func(problem, dup string) {
		in := requestsView{Form: q, Kept: a.keptReceipt(u, q.Receipt, q.ReceiptName), Dup: dup}
		if a.isBoard(r) {
			in.Hint, in.Over = a.budgetHint(q.Category, q.Cents, q.Date)
		}
		a.renderRequests(w, r, in, problem)
	}
	if problem != "" {
		again(problem, "")
		return
	}
	if p, dup := a.duplicateOf(q, u); dup && r.FormValue("dup_ok") == "" {
		t, _ := time.Parse("2006-01-02", p.Date)
		again("", fmt.Sprintf("You sent %s on %s already.", money(p.Cents), t.Format("Jan 2")))
		return
	}
	// The sender's own role counts as said.
	own := &Decision{By: u.Name, OK: true, Own: true}
	if a.isTreasurer(u) {
		d := *own
		q.Treasurer = &d
	}
	if isPresident(u) && u.ID != 0 {
		d := *own
		q.President = &d
	}
	q.One = a.oneApproval(q)
	saved, err := a.store.AddSpendRequest(q)
	if err != nil {
		a.dropReceipt(q.Receipt)
		back(w, r, requestsPath+"?err=save")
		return
	}
	if saved.Status == reqApproved {
		a.mailer.Go(func() { a.mailRequestSettled(saved) })
		back(w, r, fmt.Sprintf("%s?ok=request-approved#r%d", requestsPath, saved.ID))
		return
	}
	a.mailer.Go(func() { a.mailRequestSent(saved) })
	back(w, r, fmt.Sprintf("%s?ok=request-sent#r%d", requestsPath, saved.ID))
}

// guessRequest answers the form as it's typed in: the category the site
// would file it under and what's left of its budget (board only).
func (a *App) guessRequest(w http.ResponseWriter, r *http.Request) {
	u, _ := a.currentAdmin(r)
	q := r.URL.Query()
	cat := a.guessFor(q.Get("what"), u)
	cents, _ := parseMoney(q.Get("amount"))
	hint, over := a.budgetHint(cat, cents, q.Get("date"))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"category": cat, "hint": hint, "over": over})
}

func (a *App) requestFromPath(w http.ResponseWriter, r *http.Request) (SpendRequest, AdminUser, bool) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	u, _ := a.currentAdmin(r)
	q, ok := a.store.SpendRequest(id)
	if !ok {
		back(w, r, requestsPath+"?err=money-gone")
	}
	return q, u, ok
}

// decide records u's answer and emails the sender once it's settled. It
// reports whether it was settled.
func (a *App) decide(q SpendRequest, u AdminUser, ok bool, reason string) (SpendRequest, bool, error) {
	q, settled, err := a.store.DecideSpendRequest(q.ID, a.isTreasurer(u), isPresident(u), Decision{By: u.Name, OK: ok, Reason: reason})
	if err == nil && settled {
		a.mailer.Go(func() { a.mailRequestSettled(q) })
	}
	return q, settled, err
}

// decideRequest is Approve (ok) or Reject, by the treasurer or the president.
func (a *App) decideRequest(ok bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, u, found := a.requestFromPath(w, r)
		if !found {
			return
		}
		to := fmt.Sprintf("%s?%%s#r%d", requestsPath, q.ID)
		if !a.wantsAnswer(q, u) {
			back(w, r, fmt.Sprintf(to, "err=request-not-yours"))
			return
		}
		reason := strings.TrimSpace(r.FormValue("reason"))
		if !ok && reason == "" {
			back(w, r, fmt.Sprintf(to, "err=request-reason"))
			return
		}
		_, settled, err := a.decide(q, u, ok, reason)
		if err != nil {
			back(w, r, fmt.Sprintf(to, "err=request-not-yours"))
			return
		}
		flash := "request-ok"
		if settled {
			flash = map[bool]string{true: "request-approved", false: "request-rejected"}[ok]
		}
		back(w, r, fmt.Sprintf(to, "ok="+flash))
	}
}

// categoryRequest is the treasurer (or the president) changing a waiting
// request's category, which can change whether one answer is enough.
func (a *App) categoryRequest(w http.ResponseWriter, r *http.Request) {
	q, u, found := a.requestFromPath(w, r)
	if !found {
		return
	}
	_, cats, _ := a.store.TreasuryCats()
	cat := r.FormValue("category")
	if !a.requestRowFor(q, u).SetCat || !slices.Contains(cats, cat) {
		back(w, r, fmt.Sprintf("%s?err=request-not-yours#r%d", requestsPath, q.ID))
		return
	}
	q.Category = cat
	saved, settled, err := a.store.ReweighSpendRequest(q.ID, cat, a.oneApproval(q))
	if err != nil {
		back(w, r, requestsPath+"?err=save")
		return
	}
	if settled {
		a.mailer.Go(func() { a.mailRequestSettled(saved) })
		back(w, r, fmt.Sprintf("%s?ok=request-approved#r%d", requestsPath, q.ID))
		return
	}
	back(w, r, fmt.Sprintf("%s?ok=money-saved#r%d", requestsPath, q.ID))
}

// oneLimit saves the most the treasurer's yes alone approves, and weighs
// the requests waiting again.
func (a *App) oneLimit(w http.ResponseWriter, r *http.Request) {
	c, ok := parseMoney(r.FormValue("limit"))
	if strings.TrimSpace(r.FormValue("limit")) == "" {
		c, ok = 0, true
	}
	if !ok || c < 0 {
		back(w, r, "/admin/treasurer/budget?err=request-limit")
		return
	}
	if a.store.SetOneLimit(c) != nil {
		back(w, r, "/admin/treasurer/budget?err=save")
		return
	}
	for _, q := range a.store.SpendRequests() {
		if q.Status != reqWaiting {
			continue
		}
		if one := a.oneApproval(q); one != q.One {
			if saved, settled, err := a.store.ReweighSpendRequest(q.ID, "", one); err == nil && settled {
				a.mailer.Go(func() { a.mailRequestSettled(saved) })
			}
		}
	}
	back(w, r, "/admin/treasurer/budget?ok=money-saved")
}

func (a *App) withdrawRequest(w http.ResponseWriter, r *http.Request) {
	q, u, found := a.requestFromPath(w, r)
	if !found {
		return
	}
	if !ownRequest(q, u) || a.store.WithdrawSpendRequest(q.ID, false) != nil {
		back(w, r, requestsPath+"?err=request-not-yours")
		return
	}
	back(w, r, fmt.Sprintf("%s?withdrawn=%d", requestsPath, q.ID))
}

func (a *App) restoreRequest(w http.ResponseWriter, r *http.Request) {
	q, u, found := a.requestFromPath(w, r)
	if !found {
		return
	}
	if ownRequest(q, u) {
		a.store.WithdrawSpendRequest(q.ID, true)
	}
	back(w, r, fmt.Sprintf("%s?ok=money-restored#r%d", requestsPath, q.ID))
}

// boughtRequest saves an approved purchase's receipt and final amount, which
// update its line on Expenses.
func (a *App) boughtRequest(w http.ResponseWriter, r *http.Request) {
	q, u, found := a.requestFromPath(w, r)
	if !found {
		return
	}
	if !a.requestRowFor(q, u).AddBought {
		back(w, r, requestsPath+"?err=request-not-yours")
		return
	}
	date, cents, receipt, receiptName, problem := a.readAmountDate(w, r)
	if problem != "" {
		a.dropReceipt(receipt)
		a.renderRequests(w, r, requestsView{}, problem)
		return
	}
	old, err := a.store.BoughtSpendRequest(q.ID, cents, date, receipt, receiptName)
	if err != nil {
		a.dropReceipt(receipt)
		back(w, r, requestsPath+"?err=save")
		return
	}
	if old != "" && !a.store.ReceiptInUse(old) {
		a.dropReceipt(old)
	}
	back(w, r, fmt.Sprintf("%s?ok=request-bought#r%d", requestsPath, q.ID))
}

func (a *App) paidBackRequest(w http.ResponseWriter, r *http.Request) {
	q, u, found := a.requestFromPath(w, r)
	if !found {
		return
	}
	paid := r.FormValue("paid") != ""
	if !a.requestRowFor(q, u).PayBack || a.store.PaidBackSpendRequest(q.ID, paid) != nil {
		back(w, r, requestsPath+"?err=request-not-yours")
		return
	}
	if paid && q.PaidBack == nil {
		q, _ = a.store.SpendRequest(q.ID)
		a.mailer.Go(func() { a.mailReimbursed(q, u.Name) })
	}
	back(w, r, fmt.Sprintf("%s?ok=money-saved#r%d", requestsPath, q.ID))
}

// clubInbox gets the club's copy of every reimbursement, and nobody else
// does: the person reimbursed has PayPal's own receipt.
const clubInbox = "PaxVeloClub@gmail.com"

// mailReimbursed is the reimbursement receipt for the club's records, sent
// when the treasurer presses Paid.
func (a *App) mailReimbursed(q SpendRequest, by string) {
	approved := []string{}
	for _, d := range []*Decision{q.Treasurer, q.President} {
		if d != nil && d.OK {
			approved = append(approved, d.By)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** was reimbursed %s by PayPal.\n\n", q.By, money(q.Cents))
	b.WriteString("- **Paid to:** " + a.payTo(q) + "\n")
	b.WriteString("- **Paid on:** " + q.PaidBackLabel() + ", marked by " + by + "\n")
	b.WriteString("- **Approved by:** " + andList(approved) + "\n")
	b.WriteString(requestDetails(q))
	if q.Receipt != "" {
		b.WriteString("- **Receipt:** on the request\n")
	}
	fmt.Fprintf(&b, "\n[Open the request](%s)", a.requestLink(q))
	subject := fmt.Sprintf("Reimbursed: %s %s, %s", q.By, money(q.Cents), q.What)
	a.mailer.SendDirect(mailRequest, a.cfg.FromName, clubInbox, subject, b.String())
}

// ---- Emails ----

func (a *App) requestLink(q SpendRequest) string {
	return fmt.Sprintf("%s%s#r%d", a.cfg.SiteURL, requestsPath, q.ID)
}

func requestDetails(q SpendRequest) string {
	s := "- **What for:** " + q.What + "\n" +
		"- **Amount:** " + money(q.Amount()) + "\n" +
		"- **Category:** " + q.Category + "\n" +
		"- **Date:** " + q.DateLabel() + "\n"
	if e := q.EventLabel(); e != "" {
		s += "- **For:** " + e + "\n"
	}
	if q.Note != "" {
		s += "- **Notes:** " + strings.Join(strings.Fields(q.Note), " ") + "\n"
	}
	return s
}

// requestAsk is the email's first line, like "Allie bought something for
// the club for $45.00."
func requestAsk(q SpendRequest) string {
	if q.Kind == reqPurchase {
		return fmt.Sprintf("**%s** wants to buy something for %s.", q.By, money(q.Cents))
	}
	return fmt.Sprintf("**%s** bought something for the club for %s.", q.By, money(q.Cents))
}

func requestSubject(q SpendRequest) string {
	if q.Kind == reqPurchase {
		return fmt.Sprintf("OK to buy? %s, %s (%s)", q.What, money(q.Cents), q.By)
	}
	return fmt.Sprintf("Reimburse %s %s: %s", q.By, money(q.Cents), q.What)
}

// mailRequestAsk asks one approver for their answer, with the receipt and
// buttons that answer from the email (requestanswer.go). A reminder says so.
func (a *App) mailRequestAsk(q SpendRequest, u AdminUser, reminder bool) {
	link := a.answerURL(q, u)
	var b strings.Builder
	subject := requestSubject(q)
	if reminder {
		subject = "Reminder: " + subject
		fmt.Fprintf(&b, "This has been waiting for your answer since %s.\n\n", q.Sent.In(clubTZ).Format("Monday, January 2"))
	}
	b.WriteString(requestAsk(q) + "\n\n")
	if q.Receipt != "" && !q.ReceiptPDF() {
		fmt.Fprintf(&b, "![Receipt](%s/receipt)\n\n", link)
	}
	b.WriteString(requestDetails(q))
	if q.Receipt != "" && q.ReceiptPDF() {
		fmt.Fprintf(&b, "- **Receipt:** [%s](%s/receipt)\n", or(q.ReceiptName, "PDF"), link)
	}
	fmt.Fprintf(&b, "\n[[Approve](%s?do=approve)]\n\n[Reject](%s?do=reject) · [Open on the site](%s)", link, link, a.requestLink(q))
	a.mailer.SendDirect(mailRequest, u.Name, u.Email, subject, b.String()) // a failure is logged by send
}

// mailRequestSent asks whoever's answer is wanted to look at a new request.
func (a *App) mailRequestSent(q SpendRequest) {
	for _, u := range a.store.Admins() {
		if u.Email != "" && a.wantsAnswer(q, u) {
			a.mailRequestAsk(q, u, false)
		}
	}
}

// mailRequestSettled tells the sender the answer.
func (a *App) mailRequestSettled(q SpendRequest) {
	if q.ByEmail == "" {
		return
	}
	var subject, body string
	if no := q.Rejection(); no != nil {
		subject = "Not approved: " + q.What
		body = fmt.Sprintf("Your request for **%s** (%s) wasn’t approved by %s.\n\n**Why:** %s\n\n[Open the request](%s)",
			q.What, money(q.Cents), no.By, no.Reason, a.requestLink(q))
	} else if q.Kind == reqPurchase {
		subject = "Approved: " + q.What
		body = fmt.Sprintf("Your request to buy **%s** (%s) is approved. Go ahead and buy it.\n\n"+
			"When you have the receipt, snap or upload it on the request with the final amount.\n\n[[Add the receipt](%s)]",
			q.What, money(q.Cents), a.requestLink(q))
	} else {
		subject = "Approved: " + q.What
		body = fmt.Sprintf("Your request for **%s** (%s) is approved. The treasurer will reimburse you by PayPal to %s.\n\n[Open the request](%s)",
			q.What, money(q.Cents), strings.TrimPrefix(a.payTo(q), "PayPal "), a.requestLink(q))
	}
	a.mailer.SendDirect(mailRequest, q.By, q.ByEmail, subject, body)
}

// ---- Needs attention ----

// requestAttention is what the Requests page needs from this admin: answers,
// money to pay back, and receipts for their approved purchases. It also
// returns the number waiting for their answer, for the Requests badge.
func (a *App) requestAttention(u AdminUser) (items []attentionItem, answer int) {
	payBack, receipts := 0, []string{}
	for _, q := range a.store.SpendRequests() {
		switch {
		case a.wantsAnswer(q, u):
			answer++
		case q.Status == reqApproved && q.Kind == reqReport && q.PaidBack == nil && a.isTreasurer(u):
			payBack++
		case q.Status == reqApproved && q.Kind == reqPurchase && q.Bought == "" && ownRequest(q, u):
			receipts = append(receipts, q.What)
		}
	}
	if answer > 0 {
		items = append(items, attentionItem{
			Text: fmt.Sprintf("%d spending %s waiting for your OK.", answer, plural(answer, "request is", "requests are")),
			Href: requestsPath, Link: "Review",
		})
	}
	if payBack > 0 {
		items = append(items, attentionItem{
			Text: fmt.Sprintf("%d approved %s to reimburse.", payBack, plural(payBack, "request", "requests")),
			Href: requestsPath + "#pay", Link: "See which",
		})
	}
	if len(receipts) > 0 {
		items = append(items, attentionItem{
			Text: "Add the receipt for " + andList(receipts) + ".",
			Href: requestsPath + "#mine", Link: "Add it",
		})
	}
	return items, answer
}

// attentionFor is Home's list for the logged-in admin: the club's, plus
// their spending requests when they're on the board.
func (a *App) attentionFor(r *http.Request) (items []attentionItem, requests int) {
	items = a.attention()
	if u, ok := a.currentAdmin(r); ok {
		mine, n := a.requestAttention(u)
		items = append(mine, items...)
		requests = n
	}
	if !a.isBoard(r) {
		items = slices.DeleteFunc(items, func(it attentionItem) bool { return boardHref(it.Href) })
	}
	return items, requests
}

func (a *App) attentionItems(r *http.Request) []attentionItem {
	items, _ := a.attentionFor(r)
	return items
}

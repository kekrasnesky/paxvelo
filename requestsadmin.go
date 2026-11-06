package main

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Treasurer → Requests (requests.go has the rules): the form to send an
// expense report or purchase request, the ones waiting with Approve and
// Reject for the treasurer and the president, and the ones settled.

type requestsView struct {
	Waiting, Done []requestRow
	Form          SpendRequest
	Cats          []string
	// No admin holds the role, so requests would wait forever.
	NoTreasurer, NoPresident bool
	Withdrawn                int // the one just withdrawn, for Undo
}

// requestRow is a request with what the logged-in admin may do to it.
type requestRow struct {
	SpendRequest
	Decide    bool // their answer is wanted
	Withdraw  bool // they sent it and it's still waiting
	AddBought bool // an approved purchase: add the receipt and final amount
	PayBack   bool // an approved expense report: mark it paid back
}

func (a *App) isTreasurer(u AdminUser) bool {
	return slices.ContainsFunc(a.boardRoles(u), isTreasurerRole)
}

// isPresident: the president, or the setup password (as for canManage).
func isPresident(u AdminUser) bool { return u.President || u.ID == 0 }

func ownRequest(q SpendRequest, u AdminUser) bool {
	return q.ByID == u.ID && (u.ID != 0 || sameName(q.By, u.Name))
}

// wantsAnswer reports whether a waiting request needs this admin's answer.
func (a *App) wantsAnswer(q SpendRequest, u AdminUser) bool {
	if q.Status != reqWaiting || ownRequest(q, u) {
		return false
	}
	return (q.Treasurer == nil && a.isTreasurer(u)) || (q.President == nil && isPresident(u))
}

func (a *App) requestRowFor(q SpendRequest, u AdminUser) requestRow {
	officer := a.isTreasurer(u) || isPresident(u)
	return requestRow{SpendRequest: q,
		Decide:    a.wantsAnswer(q, u),
		Withdraw:  q.Status == reqWaiting && ownRequest(q, u),
		AddBought: q.Status == reqApproved && q.Kind == reqPurchase && (officer || ownRequest(q, u)),
		PayBack:   q.Status == reqApproved && q.Kind == reqReport && officer,
	}
}

const requestsPath = "/admin/treasurer/requests"

func (a *App) requestsPage(w http.ResponseWriter, r *http.Request) {
	a.renderRequests(w, r, SpendRequest{}, "")
}

func (a *App) renderRequests(w http.ResponseWriter, r *http.Request, form SpendRequest, problem string) {
	u, _ := a.currentAdmin(r)
	_, expense, _ := a.store.TreasuryCats()
	rv := &requestsView{Cats: expense, NoTreasurer: true, NoPresident: true}
	for _, ad := range a.store.Admins() {
		if a.isTreasurer(ad) {
			rv.NoTreasurer = false
		}
		if ad.President {
			rv.NoPresident = false
		}
	}
	rv.Withdrawn, _ = strconv.Atoi(r.URL.Query().Get("withdrawn"))
	for _, q := range a.store.SpendRequests() {
		row := a.requestRowFor(q, u)
		if q.Status == reqWaiting {
			rv.Waiting = append(rv.Waiting, row)
		} else if len(rv.Done) < 60 {
			rv.Done = append(rv.Done, row)
		}
	}
	// Those waiting for this admin come first, oldest first.
	slices.Reverse(rv.Waiting)
	slices.SortStableFunc(rv.Waiting, func(x, y requestRow) int {
		if x.Decide == y.Decide {
			return 0
		}
		if x.Decide {
			return -1
		}
		return 1
	})
	if form.Date == "" {
		form = SpendRequest{Kind: reqReport, Date: a.today().Format("2006-01-02")}
	}
	rv.Form = form
	v := adminView{Title: "Requests", Tab: "requests", Money: &moneyView{Requests: rv}}
	v.Flash, v.Error = flashFrom(r)
	if problem != "" {
		v.Flash, v.Error = "", problem
	}
	a.renderAdmin(w, r, http.StatusOK, "money-requests", v)
}

// readAmountDate reads the date, amount and receipt boxes shared by the
// request form and the purchase's receipt form.
func (a *App) readAmountDate(w http.ResponseWriter, r *http.Request) (date string, cents int64, receipt, receiptName, problem string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxReceipt+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return "", 0, "", "", "That receipt is too big. Receipts can be up to 20 MB."
	}
	date = strings.TrimSpace(r.FormValue("date"))
	c, ok := parseMoney(r.FormValue("amount"))
	switch {
	case !validDate(date):
		return date, c, "", "", "Enter the date."
	case !ok || c <= 0:
		return date, c, "", "", "Enter the amount, like 45.00."
	}
	if r.MultipartForm != nil && len(r.MultipartForm.File["receipt"]) > 0 {
		hdr := r.MultipartForm.File["receipt"][0]
		name, err := a.saveReceipt(hdr)
		if err != nil && err != errNoReceipt {
			return date, c, "", "", "The receipt wasn’t saved: " + err.Error()
		}
		if err == nil {
			receipt, receiptName = name, filepath.Base(hdr.Filename)
		}
	}
	return date, c, receipt, receiptName, ""
}

// sendRequest saves a new expense report or purchase request and emails the
// treasurer and the president.
func (a *App) sendRequest(w http.ResponseWriter, r *http.Request) {
	u, _ := a.currentAdmin(r)
	q := SpendRequest{Kind: reqReport}
	date, cents, receipt, receiptName, problem := a.readAmountDate(w, r)
	if r.FormValue("kind") == reqPurchase {
		q.Kind = reqPurchase
	}
	q.Date, q.Cents, q.Receipt, q.ReceiptName = date, cents, receipt, receiptName
	q.What, q.Category = cleanName(r.FormValue("what")), r.FormValue("category")
	q.Note = strings.TrimSpace(r.FormValue("note"))
	if len(q.Note) > 1000 {
		q.Note = q.Note[:1000]
	}
	_, cats, _ := a.store.TreasuryCats()
	if problem == "" {
		switch {
		case !slices.Contains(cats, q.Category):
			problem = "Pick a category."
		case q.What == "":
			problem = "Say what it’s for."
		}
	}
	if problem != "" {
		a.dropReceipt(q.Receipt)
		q.Receipt = ""
		a.renderRequests(w, r, q, problem)
		return
	}
	q.By, q.ByID, q.ByEmail = u.Name, u.ID, u.Email
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

func (a *App) requestFromPath(w http.ResponseWriter, r *http.Request) (SpendRequest, AdminUser, bool) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	u, _ := a.currentAdmin(r)
	q, ok := a.store.SpendRequest(id)
	if !ok {
		back(w, r, requestsPath+"?err=money-gone")
	}
	return q, u, ok
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
		q, settled, err := a.store.DecideSpendRequest(q.ID, a.isTreasurer(u), isPresident(u), Decision{By: u.Name, OK: ok, Reason: reason})
		if err != nil {
			back(w, r, fmt.Sprintf(to, "err=request-not-yours"))
			return
		}
		flash := "request-ok"
		if settled {
			a.mailer.Go(func() { a.mailRequestSettled(q) })
			flash = map[bool]string{true: "request-approved", false: "request-rejected"}[ok]
		}
		back(w, r, fmt.Sprintf(to, "ok="+flash))
	}
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
		a.renderRequests(w, r, SpendRequest{}, problem)
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
	if !a.requestRowFor(q, u).PayBack || a.store.PaidBackSpendRequest(q.ID, r.FormValue("paid") != "") != nil {
		back(w, r, requestsPath+"?err=request-not-yours")
		return
	}
	back(w, r, fmt.Sprintf("%s?ok=money-saved#r%d", requestsPath, q.ID))
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
	if q.Note != "" {
		s += "- **Notes:** " + strings.Join(strings.Fields(q.Note), " ") + "\n"
	}
	if q.Receipt != "" {
		s += "- **Receipt:** attached on the site\n"
	}
	return s
}

// mailRequestSent asks the treasurer and the president (whichever answer is
// still wanted) to look at a new request.
func (a *App) mailRequestSent(q SpendRequest) {
	who := map[bool]string{true: "the treasurer’s", false: "the president’s"}
	need := []string{}
	if q.Treasurer == nil {
		need = append(need, who[true])
	}
	if q.President == nil {
		need = append(need, who[false])
	}
	subject := fmt.Sprintf("%s from %s: %s", q.KindLabel(), q.By, money(q.Cents))
	what := map[string]string{reqReport: "an expense report", reqPurchase: "a purchase request"}[q.Kind]
	body := fmt.Sprintf("**%s** sent %s.\n\n%s\nIt needs %s OK.\n\n[Open the request](%s)",
		q.By, what, requestDetails(q), strings.Join(need, " and "), a.requestLink(q))
	for _, u := range a.store.Admins() {
		if u.Email != "" && a.wantsAnswer(q, u) {
			a.mailer.SendDirect(mailRequest, u.Name, u.Email, subject, body) // a failure is logged by send
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
		body = fmt.Sprintf("Your %s for **%s** (%s) wasn’t approved by %s.\n\n**Why:** %s\n\n[Open the request](%s)",
			strings.ToLower(q.KindLabel()), q.What, money(q.Cents), no.By, no.Reason, a.requestLink(q))
	} else if q.Kind == reqPurchase {
		subject = "Approved: " + q.What
		body = fmt.Sprintf("Your purchase request for **%s** (%s) is approved by the treasurer and the president. Go ahead and buy it.\n\n"+
			"It’s on the club’s expenses already. When you have the receipt, add it on the request with the final amount.\n\n[Add the receipt](%s)",
			q.What, money(q.Cents), a.requestLink(q))
	} else {
		subject = "Approved: " + q.What
		body = fmt.Sprintf("Your expense report for **%s** (%s) is approved by the treasurer and the president. It’s on the club’s expenses, and the treasurer will pay you back.\n\n[Open the request](%s)",
			q.What, money(q.Cents), a.requestLink(q))
	}
	a.mailer.SendDirect(mailRequest, q.By, q.ByEmail, subject, body)
}

// ---- Needs attention ----

// requestAttention is what the Requests page needs from this admin: answers,
// money to pay back, and receipts for their approved purchases. It also
// returns the number waiting for their answer, for the Requests badge.
func (a *App) requestAttention(u AdminUser) (items []attentionItem, answer int) {
	payBack, receipts := 0, []string{}
	officer := a.isTreasurer(u) || isPresident(u)
	for _, q := range a.store.SpendRequests() {
		switch {
		case a.wantsAnswer(q, u):
			answer++
		case q.Status == reqApproved && q.Kind == reqReport && q.PaidBack == nil && a.isTreasurer(u):
			payBack++
		case q.Status == reqApproved && q.Kind == reqPurchase && q.Bought == "" && ownRequest(q, u) && !officer:
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
			Text: fmt.Sprintf("%d approved expense %s to pay back.", payBack, plural(payBack, "report", "reports")),
			Href: requestsPath + "#done", Link: "See which",
		})
	}
	if len(receipts) > 0 {
		items = append(items, attentionItem{
			Text: "Add the receipt for " + andList(receipts) + ".",
			Href: requestsPath + "#done", Link: "Add it",
		})
	}
	return items, answer
}

// attentionFor is Home's list for the logged-in admin: the club's, plus
// their spending requests when they're on the board.
func (a *App) attentionFor(r *http.Request) (items []attentionItem, requests int) {
	items = a.attention()
	if u, ok := a.currentAdmin(r); ok && a.canSeeMoney(r) {
		mine, n := a.requestAttention(u)
		items = append(mine, items...)
		requests = n
	}
	return items, requests
}

func (a *App) attentionItems(r *http.Request) []attentionItem {
	items, _ := a.attentionFor(r)
	return items
}

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Answering a spending request from the email. The treasurer's and the
// president's emails have Approve and Reject buttons: each opens a page
// (no login) with the request and a button that answers it. Opening the
// link never answers, because email scanners open every link in an email;
// only pressing the button does (a POST). The link is signed for one
// approver and one request, lasts 7 days, and works once: after their
// answer is in, it only shows what was decided.

const answerPath = "/request-answer/"

// answerLife is how long an email's Approve and Reject links work.
const answerLife = 7 * 24 * time.Hour

func (a *App) answerKey() []byte {
	k := sha256.Sum256([]byte("spend-answer\x00" + a.store.Secret()))
	return k[:]
}

// answerToken is "request.admin.expiry.signature".
func (a *App) answerToken(q SpendRequest, u AdminUser, expires time.Time) string {
	v := fmt.Sprintf("%d.%d.%d", q.ID, u.ID, expires.Unix())
	return v + "." + sign(a.answerKey(), v)[:32]
}

func (a *App) answerURL(q SpendRequest, u AdminUser) string {
	return a.cfg.SiteURL + answerPath + a.answerToken(q, u, time.Now().Add(answerLife))
}

// readAnswerToken checks a link's token and returns its request and admin.
func (a *App) readAnswerToken(tok string) (SpendRequest, AdminUser, bool) {
	i := strings.LastIndex(tok, ".")
	if i < 0 {
		return SpendRequest{}, AdminUser{}, false
	}
	v, sig := tok[:i], tok[i+1:]
	if !hmac.Equal([]byte(sig), []byte(sign(a.answerKey(), v)[:32])) {
		return SpendRequest{}, AdminUser{}, false
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return SpendRequest{}, AdminUser{}, false
	}
	id, _ := strconv.Atoi(parts[0])
	uid, _ := strconv.Atoi(parts[1])
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().After(time.Unix(exp, 0)) {
		return SpendRequest{}, AdminUser{}, false
	}
	q, ok := a.store.SpendRequest(id)
	if !ok {
		return SpendRequest{}, AdminUser{}, false
	}
	u, ok := a.store.Admin(uid)
	return q, u, ok
}

// answerView is the page an email's button opens.
type answerView struct {
	Q       SpendRequest
	Token   string
	State   string // "ask", "done" (just answered), "answered" (by someone, before), "expired"
	Reject  bool   // opened from Reject: the reason box is open
	Problem string
	Result  string // what happened, in words
	Hint    string
	Over    bool
}

func (a *App) renderAnswer(w http.ResponseWriter, r *http.Request, av answerView) {
	w.Header().Set("Referrer-Policy", "no-referrer") // the token stays out of other sites' logs
	w.Header().Set("Cache-Control", "no-store")
	v := adminView{Title: "Spending request", Answer: &av}
	a.renderAdmin(w, r, http.StatusOK, "req-answer", v)
}

// settledWords says what became of a request someone already answered.
func settledWords(q SpendRequest) string {
	switch q.Status {
	case reqApproved:
		by := []string{}
		for _, d := range []*Decision{q.Treasurer, q.President} {
			if d != nil && !d.Own {
				by = append(by, d.By)
			}
		}
		if len(by) == 0 {
			return "It’s approved."
		}
		return "Already approved by " + andList(by) + "."
	case reqRejected:
		return "Already rejected by " + q.Rejection().By + "."
	case reqWithdrawn:
		return q.By + " withdrew it."
	}
	return ""
}

// answerPage is what an email's Approve or Reject link opens. It changes
// nothing.
func (a *App) answerPage(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	q, u, ok := a.readAnswerToken(tok)
	if !ok {
		a.renderAnswer(w, r, answerView{State: "expired"})
		return
	}
	av := answerView{Q: q, Token: tok, State: "ask", Reject: r.URL.Query().Get("do") == "reject"}
	av.Hint, av.Over = a.budgetHint(q.Category, q.Cents, q.Date)
	if !a.wantsAnswer(q, u) {
		av.State, av.Result = "answered", settledWords(q)
		if av.Result == "" {
			av.Result = "Your answer is in. It’s waiting for the other one."
		}
	}
	a.renderAnswer(w, r, av)
}

// answerPost is the page's Approve or Reject button.
func (a *App) answerPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	tok := r.PathValue("token")
	q, u, ok := a.readAnswerToken(tok)
	if !ok {
		a.renderAnswer(w, r, answerView{State: "expired"})
		return
	}
	if !a.wantsAnswer(q, u) {
		a.answerPage(w, r)
		return
	}
	yes := r.FormValue("answer") == "approve"
	reason := strings.TrimSpace(r.FormValue("reason"))
	if len(reason) > 300 {
		reason = reason[:300]
	}
	if !yes && reason == "" {
		av := answerView{Q: q, Token: tok, State: "ask", Reject: true, Problem: "Say why it’s rejected."}
		av.Hint, av.Over = a.budgetHint(q.Category, q.Cents, q.Date)
		a.renderAnswer(w, r, av)
		return
	}
	q, settled, err := a.decide(q, u, yes, reason)
	if err != nil {
		a.answerPage(w, r)
		return
	}
	av := answerView{Q: q, State: "done"}
	switch {
	case !yes:
		av.Result = "Rejected. " + q.By + " will get an email with your reason."
	case settled:
		av.Result = "Approved. It’s on Expenses now, and " + q.By + " will get an email."
	default:
		av.Result = "Approved. It’s waiting for the president now."
	}
	a.renderAnswer(w, r, av)
}

// answerReceipt shows the request's receipt to whoever has the link, for the
// photo in the email and on the page.
func (a *App) answerReceipt(w http.ResponseWriter, r *http.Request) {
	q, _, ok := a.readAnswerToken(r.PathValue("token"))
	if !ok || !receiptName.MatchString(q.Receipt) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(a.receiptsDir(), q.Receipt))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, q.Receipt, time.Time{}, f)
}

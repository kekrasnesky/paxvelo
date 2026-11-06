package main

import (
	"fmt"
	"strings"
	"time"
)

// Spending-request reminders, run with the automations every 15 minutes
// from 9 am club time. Each goes once, recorded on the request (or, for the
// weekly list, on the treasury) before it's sent, so a restart never sends
// it again:
//
//   - an approver who hasn't answered in 3 days gets one reminder, with the
//     same Approve and Reject buttons;
//   - on Mondays the treasurer gets the approved reimbursements not paid
//     yet (skipped when there are none);
//   - an approved purchase with no receipt 10 days on: the sender gets one
//     "snap or upload the receipt" email.

const (
	answerNudgeAfter  = 3 * 24 * time.Hour
	receiptNudgeAfter = 10 * 24 * time.Hour
	// Older ones than this are left alone (the reminders started after
	// they were approved).
	receiptNudgeUntil = 30 * 24 * time.Hour
)

func (a *App) runRequestReminders(now time.Time) {
	local := now.In(a.cfg.Location)
	if local.Hour() < 9 {
		return
	}
	admins := a.store.Admins()
	var payBack []SpendRequest
	for _, q := range a.store.SpendRequests() {
		switch {
		case q.Status == reqWaiting && now.Sub(q.Sent) >= answerNudgeAfter:
			for _, u := range admins {
				if u.Email != "" && a.wantsAnswer(q, u) && a.store.MarkNudged(q.ID, u.ID) {
					a.mailRequestAsk(q, u, true)
				}
			}
		case q.Status == reqApproved && q.Kind == reqReport && q.PaidBack == nil:
			payBack = append(payBack, q)
		case q.Status == reqApproved && q.Kind == reqPurchase && q.Bought == "" && !q.ReceiptNudged && q.ByEmail != "":
			if age := now.Sub(q.approvedAt()); age >= receiptNudgeAfter && age < receiptNudgeUntil && a.store.MarkReceiptNudged(q.ID) {
				a.mailReceiptNudge(q)
			}
		}
	}
	if local.Weekday() == time.Monday && len(payBack) > 0 && a.store.MarkPayList(local.Format("2006-01-02")) {
		a.mailPayList(payBack, admins)
	}
}

// mailPayList is the treasurer's Monday list of people to pay back.
func (a *App) mailPayList(list []SpendRequest, admins []AdminUser) {
	var b strings.Builder
	total := int64(0)
	for i := len(list) - 1; i >= 0; i-- { // oldest first
		q := list[i]
		total += q.Cents
		line := fmt.Sprintf("- Pay **%s** %s: %s", q.By, money(q.Cents), q.What)
		if p := a.payTo(q); p != "" {
			line += " — " + p
		}
		b.WriteString(line + "\n")
	}
	subject := fmt.Sprintf("To reimburse: %d %s, %s", len(list), plural(len(list), "request", "requests"), money(total))
	body := "These approved requests haven’t been reimbursed yet:\n\n" + b.String() +
		fmt.Sprintf("\nPress **Paid** on each once the PayPal payment is sent.\n\n[[Open Requests](%s%s#pay)]", a.cfg.SiteURL, requestsPath)
	for _, u := range admins {
		if u.Email != "" && a.isTreasurer(u) {
			a.mailer.SendDirect(mailRequest, u.Name, u.Email, subject, body)
		}
	}
}

// mailReceiptNudge asks the sender of an approved purchase for its receipt.
func (a *App) mailReceiptNudge(q SpendRequest) {
	subject := "Receipt for " + q.What + "?"
	body := fmt.Sprintf("Hi %s,\n\nYour request to buy **%s** (%s) was approved on %s. When you have the receipt, snap a photo of it or upload it, with the final amount, so it’s on the club’s books.\n\n[[Add the receipt](%s)]",
		firstWord(q.By), q.What, money(q.Cents), q.approvedAt().In(clubTZ).Format("January 2"), a.requestLink(q))
	a.mailer.SendDirect(mailRequest, q.By, q.ByEmail, subject, body)
}

package main

import (
	"errors"
	"slices"
	"time"
)

// Spending requests (Treasurer → Requests). A board member sends an expense
// report (they paid, the club pays them back) or a purchase request (asking
// before buying). Each needs the treasurer's and the president's yes; the
// sender's own role counts as said. Once both have said yes the site adds it
// to Expenses itself, so it's on the next report without retyping; a
// purchase's receipt and final amount, added later, update that line.

// SpendRequest is one expense report or purchase request.
type SpendRequest struct {
	ID          int       `json:"id"`
	Kind        string    `json:"kind"` // reqReport or reqPurchase
	Date        string    `json:"date"` // spent on, or to buy on; 2006-01-02
	Cents       int64     `json:"cents"`
	Category    string    `json:"category"`
	What        string    `json:"what"`
	Note        string    `json:"note,omitempty"`
	Receipt     string    `json:"receipt,omitempty"` // file in data/receipts (a quote, for a purchase)
	ReceiptName string    `json:"receipt_name,omitempty"`
	By          string    `json:"by"`
	ByID        int       `json:"by_id,omitempty"` // their admin account
	ByEmail     string    `json:"by_email,omitempty"`
	Sent        time.Time `json:"sent"`
	Treasurer   *Decision `json:"treasurer,omitempty"`
	President   *Decision `json:"president,omitempty"`
	Status      string    `json:"status"`          // reqWaiting, reqApproved, reqRejected, reqWithdrawn
	Entry       int       `json:"entry,omitempty"` // the expense line added on approval
	// Spent is a purchase's final amount, with its receipt, once bought.
	Spent    int64      `json:"spent_cents,omitempty"`
	Bought   string     `json:"bought,omitempty"`    // 2006-01-02
	PaidBack *time.Time `json:"paid_back,omitempty"` // an expense report's money sent to them
}

// Decision is the treasurer's or the president's answer.
type Decision struct {
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	OK     bool      `json:"ok"`
	Reason string    `json:"reason,omitempty"`
	Own    bool      `json:"own,omitempty"` // they sent it themselves
}

const (
	reqReport   = "report"
	reqPurchase = "purchase"

	reqWaiting   = "waiting"
	reqApproved  = "approved"
	reqRejected  = "rejected"
	reqWithdrawn = "withdrawn"
)

func (q SpendRequest) KindLabel() string {
	if q.Kind == reqPurchase {
		return "Purchase request"
	}
	return "Expense report"
}

func (q SpendRequest) IsPurchase() bool  { return q.Kind == reqPurchase }
func (q SpendRequest) DateLabel() string { return mdy(q.Date) }
func (q SpendRequest) ReceiptPDF() bool  { return LedgerEntry{Receipt: q.Receipt}.ReceiptPDF() }

// Amount is what it cost: the final amount once a purchase is bought.
func (q SpendRequest) Amount() int64 {
	if q.Spent != 0 {
		return q.Spent
	}
	return q.Cents
}

// rejection is the "no", if there was one.
func (q SpendRequest) Rejection() *Decision {
	for _, d := range []*Decision{q.Treasurer, q.President} {
		if d != nil && !d.OK {
			return d
		}
	}
	return nil
}

// settle works out the status from the two answers.
func (q *SpendRequest) settle() {
	switch {
	case q.Rejection() != nil:
		q.Status = reqRejected
	case q.Treasurer != nil && q.President != nil:
		q.Status = reqApproved
	default:
		q.Status = reqWaiting
	}
}

// ---- Store ----

// SpendRequests lists the requests, newest first.
func (s *Store) SpendRequests() []SpendRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SpendRequest, 0, len(s.d.Treasury.Requests))
	for i := len(s.d.Treasury.Requests) - 1; i >= 0; i-- {
		out = append(out, *s.d.Treasury.Requests[i])
	}
	return out
}

func (s *Store) SpendRequest(id int) (SpendRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.spendRequest(id); q != nil {
		return *q, true
	}
	return SpendRequest{}, false
}

func (s *Store) spendRequest(id int) *SpendRequest {
	for _, q := range s.d.Treasury.Requests {
		if q.ID == id {
			return q
		}
	}
	return nil
}

// AddSpendRequest saves a new request. Its Treasurer and President answers
// are already filled in for a sender who holds that role; when both are, it's
// approved (and added to Expenses) straight away.
func (s *Store) AddSpendRequest(q SpendRequest) (SpendRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.d.Treasury
	t.NextRequest = max(t.NextRequest, 1)
	q.ID, q.Sent = t.NextRequest, time.Now().UTC()
	t.NextRequest++
	q.settle()
	c := q
	t.Requests = append(t.Requests, &c)
	if c.Status == reqApproved {
		s.bookRequest(&c)
	}
	return c, s.save()
}

var errNotWaiting = errors.New("that request isn't waiting any more")

// DecideSpendRequest records the treasurer's or the president's answer (or
// both, for someone who is both). It returns the request and whether this
// answer settled it.
func (s *Store) DecideSpendRequest(id int, asTreasurer, asPresident bool, d Decision) (SpendRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.spendRequest(id)
	if q == nil || q.Status != reqWaiting {
		return SpendRequest{}, false, errNotWaiting
	}
	d.At = time.Now().UTC()
	did := false
	if asTreasurer && q.Treasurer == nil {
		dd := d
		q.Treasurer, did = &dd, true
	}
	if asPresident && q.President == nil {
		dd := d
		q.President, did = &dd, true
	}
	if !did {
		return *q, false, errNotWaiting
	}
	q.settle()
	if q.Status == reqApproved {
		s.bookRequest(q)
	}
	return *q, q.Status != reqWaiting, s.save()
}

// bookRequest adds an approved request to Expenses: an expense report on
// the day it was spent, a purchase on the day it was approved (moved to the
// day it was bought when its receipt comes in).
func (s *Store) bookRequest(q *SpendRequest) {
	date := q.Date
	if q.Kind == reqPurchase {
		date = time.Now().In(clubTZ).Format("2006-01-02")
	}
	e := s.addLedger(LedgerEntry{Kind: "expense", Date: date, Cents: q.Cents, Category: q.Category, What: q.What, Who: q.By,
		Receipt: q.Receipt, ReceiptName: q.ReceiptName, AddedBy: q.By, Request: q.ID})
	q.Entry = e.ID
}

// WithdrawSpendRequest takes back a request still waiting (or puts it back:
// Undo).
func (s *Store) WithdrawSpendRequest(id int, back bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.spendRequest(id)
	switch {
	case q == nil:
		return errNotWaiting
	case back && q.Status == reqWithdrawn:
		q.settle()
	case !back && q.Status == reqWaiting:
		q.Status = reqWithdrawn
	default:
		return errNotWaiting
	}
	return s.save()
}

// BoughtSpendRequest saves an approved purchase's final amount, day and
// receipt, and updates its line on Expenses (unless the treasurer removed
// it). It returns the receipt it replaced.
func (s *Store) BoughtSpendRequest(id int, cents int64, date, receipt, receiptName string) (old string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.spendRequest(id)
	if q == nil || q.Status != reqApproved || q.Kind != reqPurchase {
		return "", errNotWaiting
	}
	q.Spent, q.Bought = cents, date
	if receipt != "" {
		old = q.Receipt
		q.Receipt, q.ReceiptName = receipt, receiptName
	}
	if e := s.ledgerEntry(q.Entry); e != nil {
		e.Cents, e.Date = cents, date
		if receipt != "" {
			e.Receipt, e.ReceiptName = receipt, receiptName
		}
	}
	return old, s.save()
}

// PaidBackSpendRequest marks an approved expense report paid back, or not.
func (s *Store) PaidBackSpendRequest(id int, paid bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.spendRequest(id)
	if q == nil || q.Status != reqApproved || q.Kind != reqReport {
		return errNotWaiting
	}
	q.PaidBack = nil
	if paid {
		now := time.Now().UTC()
		q.PaidBack = &now
	}
	return s.save()
}

// receiptInUse reports whether a line (or the one kept for Undo) or a
// request still points at a receipt file.
func (s *Store) ReceiptInUse(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.d.Treasury
	return slices.ContainsFunc(t.Entries, func(e *LedgerEntry) bool { return e.Receipt == name }) ||
		(t.Removed != nil && t.Removed.Receipt == name) || s.requestReceipt(name)
}

// RequestReceipt reports whether a request points at a receipt file, which
// it shares with its line on Expenses.
func (s *Store) RequestReceipt(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requestReceipt(name)
}

func (s *Store) requestReceipt(name string) bool {
	return name != "" && slices.ContainsFunc(s.d.Treasury.Requests, func(q *SpendRequest) bool { return q.Receipt == name })
}

// reqStep is one of the two answers, for the page.
type reqStep struct {
	Role string
	D    *Decision
}

func (q SpendRequest) Steps() []reqStep {
	return []reqStep{{"Treasurer", q.Treasurer}, {"President", q.President}}
}

// PaidBackLabel is the day it was paid back, like 11/6/2026.
func (q SpendRequest) PaidBackLabel() string {
	if q.PaidBack == nil {
		return ""
	}
	return q.PaidBack.In(clubTZ).Format("1/2/2006")
}

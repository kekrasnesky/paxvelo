package main

import (
	"errors"
	"slices"
	"time"
)

// Spending requests (Treasurer → Requests). Any admin asks to be reimbursed
// for something they bought for the club ("I already bought it") or asks
// before buying ("I want to buy something"). Reimbursements go by PayPal to
// their email, never any other way. The treasurer and the president
// answer; the sender's own role counts as said. A small amount within its
// category's budget needs only the treasurer's yes (One). Once approved the
// site adds it to Expenses itself, so it's on the next report without
// retyping; a purchase's receipt and final amount, added later, update that
// line. The answers can come from the email too (requestanswer.go), and
// reminders go by themselves (requestreminders.go).

// SpendRequest is one expense report or purchase request.
type SpendRequest struct {
	ID          int       `json:"id"`
	Kind        string    `json:"kind"` // reqReport or reqPurchase
	Date        string    `json:"date"` // spent on, or to buy on; 2006-01-02
	Cents       int64     `json:"cents"`
	Category    string    `json:"category"` // guessed (guessCategory); the treasurer can change it
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
	// One: small and within budget, so the treasurer's yes is enough (or the
	// president's, when the treasurer sent it).
	One      bool       `json:"one,omitempty"`
	Approved *time.Time `json:"approved,omitempty"`
	// The ride or event it was for, if any (a RideOcc key, with its name and
	// day as they were).
	EventKey  string `json:"event_key,omitempty"`
	EventName string `json:"event_name,omitempty"`
	EventDate string `json:"event_date,omitempty"`
	// Reminders already sent: approvers reminded to answer, and the sender
	// reminded to add a purchase's receipt.
	Nudged        []int `json:"nudged,omitempty"`
	ReceiptNudged bool  `json:"receipt_nudged,omitempty"`
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
		return "Purchase"
	}
	return "Reimbursement"
}

func (q SpendRequest) IsPurchase() bool  { return q.Kind == reqPurchase }
func (q SpendRequest) DateLabel() string { return mdy(q.Date) }
func (q SpendRequest) ReceiptPDF() bool  { return LedgerEntry{Receipt: q.Receipt}.ReceiptPDF() }

// EventLabel is the ride or event it was for, like "Coffee Ride, Nov 7".
func (q SpendRequest) EventLabel() string {
	if q.EventName == "" {
		return ""
	}
	if t, err := time.Parse("2006-01-02", q.EventDate); err == nil {
		return q.EventName + ", " + t.Format("Jan 2")
	}
	return q.EventName
}

// Amount is what it cost: the final amount once a purchase is bought.
func (q SpendRequest) Amount() int64 {
	if q.Spent != 0 {
		return q.Spent
	}
	return q.Cents
}

// Rejection is the "no", if there was one.
func (q SpendRequest) Rejection() *Decision {
	for _, d := range []*Decision{q.Treasurer, q.President} {
		if d != nil && !d.OK {
			return d
		}
	}
	return nil
}

// presidentNeeded: two answers, or one when the treasurer sent it (their
// own counts, so the president's is the one that's wanted).
func (q SpendRequest) presidentNeeded() bool {
	return !q.One || (q.Treasurer != nil && q.Treasurer.Own)
}

// settle works out the status from the answers.
func (q *SpendRequest) settle() {
	switch {
	case q.Rejection() != nil:
		q.Status = reqRejected
	case q.Treasurer != nil && (q.President != nil || !q.presidentNeeded()):
		q.Status = reqApproved
		if q.Approved == nil {
			now := time.Now().UTC()
			q.Approved = &now
		}
	default:
		q.Status = reqWaiting
	}
}

// approvedAt is when it was approved: older requests have no Approved, so
// it's the last answer.
func (q SpendRequest) approvedAt() time.Time {
	if q.Approved != nil {
		return *q.Approved
	}
	var t time.Time
	for _, d := range []*Decision{q.Treasurer, q.President} {
		if d != nil && d.At.After(t) {
			t = d.At
		}
	}
	return t
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
// are already filled in for a sender who holds that role; when that's all
// it needs, it's approved (and added to Expenses) straight away.
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
	if asPresident && q.President == nil && (q.presidentNeeded() || !did) {
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

// ReweighSpendRequest changes a waiting request's category (unless cat is
// empty) and whether one answer is enough. That can settle it: the
// treasurer said yes already and now that's all it needs.
func (s *Store) ReweighSpendRequest(id int, cat string, one bool) (SpendRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.spendRequest(id)
	if q == nil || q.Status != reqWaiting {
		return SpendRequest{}, false, errNotWaiting
	}
	if cat != "" {
		q.Category = cat
	}
	q.One = one
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

// ReceiptInUse reports whether a line (or the one kept for Undo) or a
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

// ---- The one-answer limit ----

// defaultOneLimit: up to $50 within budget, the treasurer's yes is enough.
const defaultOneLimit = 50_00

// OneLimit is the most one answer approves; 0 means every request needs two.
func (s *Store) OneLimit() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.d.Treasury.OneLimit; l != nil {
		return *l
	}
	return defaultOneLimit
}

func (s *Store) SetOneLimit(c int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c = max(c, 0)
	s.d.Treasury.OneLimit = &c
	return s.save()
}

// ---- Reminders, each sent once ----

// MarkNudged records that an approver is being reminded about a request,
// and reports false if they were already.
func (s *Store) MarkNudged(id, admin int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.spendRequest(id)
	if q == nil || slices.Contains(q.Nudged, admin) {
		return false
	}
	q.Nudged = append(q.Nudged, admin)
	s.save()
	return true
}

// MarkReceiptNudged records the "add the receipt" reminder for a purchase.
func (s *Store) MarkReceiptNudged(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.spendRequest(id)
	if q == nil || q.ReceiptNudged {
		return false
	}
	q.ReceiptNudged = true
	s.save()
	return true
}

// MarkPayList records the week's pay-back list to the treasurer (day is that
// Monday), and reports false if it went already.
func (s *Store) MarkPayList(day string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.Treasury.PayListSent >= day {
		return false
	}
	s.d.Treasury.PayListSent = day
	s.save()
	return true
}

// ---- For the page ----

// reqStep is one of the answers wanted, for the page.
type reqStep struct {
	Role string
	D    *Decision
}

// Steps are the answers it needs: the treasurer's, and the president's
// unless one is enough.
func (q SpendRequest) Steps() []reqStep {
	steps := []reqStep{{"Treasurer", q.Treasurer}}
	if q.presidentNeeded() || q.President != nil {
		steps = append(steps, reqStep{"President", q.President})
	}
	return steps
}

// trackStep is one step of the sender's tracker: Sent → Approved → Paid
// back (or Receipt added, for a purchase).
type trackStep struct {
	Label, When string
	State       string // "done", "now" (the step it's waiting on), "no" (rejected), ""
}

func (q SpendRequest) Track() []trackStep {
	day := func(t time.Time) string { return t.In(clubTZ).Format("Jan 2") }
	steps := []trackStep{{Label: "Sent", When: day(q.Sent), State: "done"}}
	switch q.Status {
	case reqRejected:
		return append(steps, trackStep{Label: "Not approved", When: day(q.Rejection().At), State: "no"})
	case reqWithdrawn:
		return append(steps, trackStep{Label: "Withdrawn", State: "no"})
	case reqWaiting:
		steps = append(steps, trackStep{Label: "Approved", State: "now"})
	default:
		steps = append(steps, trackStep{Label: "Approved", When: day(q.approvedAt()), State: "done"})
	}
	last := trackStep{Label: "Reimbursed"}
	if q.Kind == reqPurchase {
		last.Label = "Receipt added"
		if q.Bought != "" {
			last.State = "done"
			if t, err := time.Parse("2006-01-02", q.Bought); err == nil {
				last.When = t.Format("Jan 2")
			}
		}
	} else if q.PaidBack != nil {
		last.State, last.When = "done", day(*q.PaidBack)
	}
	if q.Status == reqApproved && last.State == "" {
		last.State = "now"
	}
	return append(steps, last)
}

// Finished: nothing more will happen to it.
func (q SpendRequest) Finished() bool {
	switch q.Status {
	case reqWaiting:
		return false
	case reqApproved:
		return (q.Kind == reqReport && q.PaidBack != nil) || (q.Kind == reqPurchase && q.Bought != "")
	}
	return true
}

// PaidBackLabel is the day it was paid back, like 11/6/2026.
func (q SpendRequest) PaidBackLabel() string {
	if q.PaidBack == nil {
		return ""
	}
	return q.PaidBack.In(clubTZ).Format("1/2/2006")
}

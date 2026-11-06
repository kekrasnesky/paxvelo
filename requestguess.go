package main

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Senders don't pick a category: the site guesses one from the words in
// "What for", and the treasurer changes it on the request if it's wrong.
// Requests the treasurer already filed teach it: "pizza" goes where the last
// pizza went.

// catHints are words that point at one of Barry's categories (matched by a
// piece of its name, so renamed categories still match).
var catHints = []struct {
	words []string
	cat   string
}{
	{[]string{"pizza", "food", "drink", "drinks", "coffee", "beer", "wine", "snack", "snacks", "lunch", "dinner", "breakfast",
		"party", "picnic", "bbq", "cookout", "donut", "donuts", "bagel", "bagels", "water", "ice", "cake", "catering", "potluck",
		"social", "cups", "plates", "napkins", "soda", "gatorade", "fruit"}, "food & drink"},
	{[]string{"sock", "socks", "jersey", "jerseys", "kit", "bib", "bibs", "vest", "vests", "cap", "caps", "jakroo", "shirt", "shirts"}, "kit"},
	{[]string{"po box", "p.o. box", "postage", "stamps", "stamp", "mailbox"}, "po box"},
	{[]string{"quicken"}, "quicken"},
	{[]string{"insurance", "liability", "policy"}, "insurance"},
	{[]string{"registration", "entry fee", "race fee", "event fee", "signup", "sign-up"}, "registration"},
	{[]string{"tax", "taxes", "cpa", "accountant", "990", "irs"}, "taxes"},
	{[]string{"bank", "wire", "service charge", "overdraft"}, "bank fees"},
	{[]string{"google workspace", "gmail", "g suite", "workspace"}, "google workspace"},
	{[]string{"domain", "marketing", "ads", "flyer", "flyers", "banner", "banners", "printing", "cards"}, "domain/marketing"},
	{[]string{"squarespace", "website", "hosting", "server"}, "squarespace"},
	{[]string{"donation", "donate", "charity", "sponsor", "sponsorship"}, "donation"},
}

// guessStop are words too common to tell two requests apart.
var guessStop = map[string]bool{"the": true, "and": true, "for": true, "from": true, "with": true, "our": true, "club": true,
	"ride": true, "rides": true, "event": true, "paxvelo": true, "new": true, "some": true, "this": true, "that": true,
	"order": true, "expenses": true, "fees": true, "office": true, "rental": true, "prep": true}

func guessWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// guessCategory picks an expense category for a request: from a past
// request with the same words (the sender's first), a category whose name
// is in the words, the hint words above, the sender's last request, and
// finally the club-rides category.
func guessCategory(what string, cats []string, past []SpendRequest, byID int, by string) string {
	if len(cats) == 0 {
		return ""
	}
	words := guessWords(what)
	has := map[string]bool{}
	for _, w := range words {
		has[w] = true
		has[strings.TrimSuffix(w, "s")] = true
	}
	text := " " + strings.Join(words, " ") + " "
	mine := func(q SpendRequest) bool { return q.ByID == byID && (byID != 0 || sameName(q.By, by)) }
	usable := func(q SpendRequest) bool {
		return q.Status != reqWithdrawn && slices.Contains(cats, q.Category)
	}
	// Past requests sharing a telling word: the sender's own first, newest first.
	for _, own := range []bool{true, false} {
		for _, q := range past {
			if mine(q) != own || !usable(q) {
				continue
			}
			for _, w := range guessWords(q.What) {
				if len(w) >= 4 && !guessStop[w] && (has[w] || has[strings.TrimSuffix(w, "s")]) {
					return q.Category
				}
			}
		}
	}
	// A category named in the words, like "insurance".
	for _, c := range cats {
		for _, w := range guessWords(c) {
			if len(w) >= 5 && !guessStop[w] && has[w] {
				return c
			}
		}
	}
	find := func(piece string) string {
		for _, c := range cats {
			if strings.Contains(strings.ToLower(c), piece) {
				return c
			}
		}
		return ""
	}
	for _, h := range catHints {
		for _, w := range h.words {
			if (strings.Contains(w, " ") && strings.Contains(text, " "+w+" ")) || has[w] {
				if c := find(h.cat); c != "" {
					return c
				}
			}
		}
	}
	for _, q := range past {
		if mine(q) && usable(q) {
			return q.Category
		}
	}
	if c := find("club rides"); c != "" {
		return c
	}
	return cats[0]
}

// guessFor is guessCategory with the club's categories and past requests.
func (a *App) guessFor(what string, u AdminUser) string {
	_, cats, _ := a.store.TreasuryCats()
	return guessCategory(what, cats, a.store.SpendRequests(), u.ID, u.Name)
}

// budgetLeft is what's left of a category's budget for a year: the budget
// less the expense lines so far (and PayPal's fees, in their category).
// has is false when the category has no budget that year.
func (a *App) budgetLeft(cat string, year int) (left, budget int64, has bool) {
	budget, has = a.store.Budget(year).Budget[cat]
	if !has {
		return 0, 0, false
	}
	spent := int64(0)
	for _, e := range a.store.Ledger("expense", year) {
		if e.Category == cat {
			spent += e.Cents
		}
	}
	if fee := a.store.PayPalFee(); fee.Cat == cat {
		spent += a.duesIncome(year, fmt.Sprintf("%d-12-31", year)).Fees
	}
	return budget - spent, budget, true
}

func requestYear(date string) int {
	if t, err := time.Parse("2006-01-02", date); err == nil {
		return t.Year()
	}
	return time.Now().In(clubTZ).Year()
}

// budgetHint is the line by the amount, like "Club Rides & Parties:Food &
// Drink: $140.00 of $400.00 left in 2026", and whether the amount goes over.
func (a *App) budgetHint(cat string, cents int64, date string) (hint string, over bool) {
	if cat == "" {
		return "", false
	}
	year := requestYear(date)
	left, budget, has := a.budgetLeft(cat, year)
	if !has {
		return fmt.Sprintf("%s: no budget in %d", cat, year), false
	}
	if left < 0 {
		return fmt.Sprintf("%s: %s over the %s budget for %d", cat, money(-left), money(budget), year), cents > 0
	}
	hint = fmt.Sprintf("%s: %s of %s left in %d", cat, money(left), money(budget), year)
	return hint, cents > left
}

// oneApproval reports whether the treasurer's yes alone is enough: up to
// the limit, and within what's left of the category's budget.
func (a *App) oneApproval(q SpendRequest) bool {
	limit := a.store.OneLimit()
	if limit <= 0 || q.Cents > limit {
		return false
	}
	left, _, has := a.budgetLeft(q.Category, requestYear(q.Date))
	return has && q.Cents <= left
}

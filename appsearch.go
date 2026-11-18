package main

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The admin app's search box: members (name, email), rides and events from
// 4 weeks back to 8 weeks ahead (and any waiting for approval), and RSVP
// invitations, each linking to its admin page. Every level can open those
// pages (a website admin's member page is read-only), so all admins get
// the same results. The page asks /admin/app/search as they type; without
// scripts the box submits to /admin/app?q=.

const searchMax = 8 // results in each group

// appHit is one result.
type appHit struct{ Title, Sub, Href string }

// appHits are the results, in groups.
type appHits struct {
	Q                      string
	Members, Rides, Invite []appHit
}

func (h *appHits) Empty() bool { return len(h.Members)+len(h.Rides)+len(h.Invite) == 0 }

// matchAll reports whether every word of q is in one of the texts.
func matchAll(words []string, texts ...string) bool {
	all := strings.ToLower(strings.Join(texts, " "))
	for _, w := range words {
		if !strings.Contains(all, w) {
			return false
		}
	}
	return true
}

// appSearch finds q, or nil when it's too short to search.
func (a *App) appSearch(q string) *appHits {
	q = strings.TrimSpace(q)
	if utf8.RuneCountInString(q) < 2 {
		return nil
	}
	if len(q) > 100 {
		q = q[:100]
	}
	words := strings.Fields(strings.ToLower(q))
	h := &appHits{Q: q}

	for _, m := range a.store.Members() {
		if len(h.Members) == searchMax {
			break
		}
		if matchAll(words, m.Name, m.Email) {
			h.Members = append(h.Members, appHit{Title: or(m.Name, m.Email), Sub: m.Email, Href: fmt.Sprintf("/admin/members/%d", m.ID)})
		}
	}

	today := a.today()
	// Waiting for approval, then coming up (soonest first), then past
	// (latest first).
	rides := append(a.store.HeldRides(), a.store.Rides(today, today.AddDate(0, 0, 56))...)
	past := a.store.Rides(today.AddDate(0, 0, -28), today.AddDate(0, 0, -1))
	slices.Reverse(past)
	rides = append(rides, past...)
	for _, o := range rides {
		if len(h.Rides) == searchMax {
			break
		}
		if !matchAll(words, o.Name, o.Place, o.KindName()) {
			continue
		}
		sub := o.ShortDate()
		if t := o.TimeLabel(); t != "" {
			sub += " · " + t
		}
		switch {
		case o.Held:
			sub += " · waiting for approval"
		case o.Cancelled:
			sub += " · cancelled"
		}
		h.Rides = append(h.Rides, appHit{Title: o.Name, Sub: sub, Href: "/admin/rides/" + o.Key})
	}

	for _, e := range a.store.RSVPEvents() {
		if len(h.Invite) == searchMax {
			break
		}
		if matchAll(words, e.Title, e.Place) {
			sub := shortDate(e.Date)
			if e.Closed {
				sub += " · closed"
			}
			h.Invite = append(h.Invite, appHit{Title: e.Title, Sub: strings.TrimSpace(sub + " · " + strconv.Itoa(len(e.Replies)) + " " + plural(len(e.Replies), "answer", "answers")), Href: "/admin/rsvp/" + strconv.Itoa(e.ID)})
		}
	}
	return h
}

// appSearchPart is the results alone, for the search box as it's typed in.
func (a *App) appSearchPart(w http.ResponseWriter, r *http.Request) {
	h := a.appSearch(r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if h == nil {
		return
	}
	if err := a.site.admin.ExecuteTemplate(w, "app-results", h); err != nil {
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
	}
}

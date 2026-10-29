package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The admin's navigation, grouped by job so it fits on a phone: a short bar
// of groups, and under it the screens of the current group. Admins, used
// rarely, sits in the corner by Log out. Every screen keeps its own address.

type adminSection struct{ Tab, Label, Href string }

type adminGroup struct {
	Label, Href string
	Sections    []adminSection
}

var adminGroups = []adminGroup{
	{"Home", "/admin", []adminSection{{"attention", "Needs attention", "/admin"}}},
	{"Members", "/admin/members", []adminSection{{"members", "Members", "/admin/members"}}},
	{"Email", "/admin/email", []adminSection{{"email", "Email members", "/admin/email"}, {"newsletter", "Newsletter", "/admin/newsletter"}}},
	{"Rides", "/admin/calendar", []adminSection{{"calendar", "Calendar", "/admin/calendar"}, {"rides", "Weekly rides", "/admin/rides"}, {"history", "History", "/admin/history"}}},
	{"Website", "/admin/pages", []adminSection{{"pages", "Pages", "/admin/pages"}, {"home-photos", "Home photos", "/admin/home-photos"},
		{"board", "Board", "/admin/board"}, {"instagram", "Instagram", "/admin/instagram"}, {"minutes", "Minutes", "/admin/minutes"}}},
}

var adminsSection = adminSection{"admins", "Admins", "/admin/admins"}

type adminNav struct {
	Groups   []navGroup
	Sections []navSection // the current group's screens, when it has more than one
	Admins   navSection   // the Admins screen, in the corner
}

type navGroup struct {
	Label, Href string
	Current     bool
	Badge       int // things needing attention, on Home
}

type navSection struct {
	Label, Href string
	Current     bool
}

func adminNavFor(tab string, attention int) adminNav {
	nav := adminNav{Admins: navSection{adminsSection.Label, adminsSection.Href, tab == adminsSection.Tab}}
	for _, g := range adminGroups {
		ng := navGroup{Label: g.Label, Href: g.Href}
		if g.Label == "Home" {
			ng.Badge = attention
		}
		for _, s := range g.Sections {
			if s.Tab == tab {
				ng.Current = true
			}
		}
		if ng.Current && len(g.Sections) > 1 {
			for _, s := range g.Sections {
				nav.Sections = append(nav.Sections, navSection{s.Label, s.Href, s.Tab == tab})
			}
		}
		nav.Groups = append(nav.Groups, ng)
	}
	return nav
}

// ---- Needs attention ----

// attentionItem is one thing an admin should look at, linking to where it's done.
type attentionItem struct {
	Text, Href, Link string
}

// attention lists what needs an admin: rides waiting for approval, rides this
// week without a leader, an email send that stopped, and unpublished
// newsletter drafts for this month or later.
func (a *App) attention() []attentionItem {
	var out []attentionItem
	if held := a.store.HeldRides(); len(held) > 0 {
		out = append(out, attentionItem{
			Text: fmt.Sprintf("%d %s added on the website %s waiting for approval.", len(held), plural(len(held), "ride", "rides"), plural(len(held), "is", "are")),
			Href: "/admin/calendar", Link: "Review",
		})
	}

	today := a.today()
	for _, o := range a.store.Rides(today, today.AddDate(0, 0, 6)) {
		if o.Cancelled {
			continue
		}
		var open []string
		for _, s := range o.Slots() {
			if s.Leader == nil {
				open = append(open, s.Group)
			}
		}
		if len(open) == 0 {
			continue
		}
		need := "needs a leader"
		if open[0] != "" {
			need = "needs a leader for the " + strings.Join(open, " and ")
		}
		out = append(out, attentionItem{
			Text: fmt.Sprintf("%s, %s, %s.", o.Name, o.DateLabel(), need),
			Href: "/admin/rides/" + o.Key, Link: "Open",
		})
	}

	for _, c := range a.store.Campaigns() {
		if c.Status != "paused" && c.Status != "sending" {
			continue
		}
		state := "is still sending"
		if c.Status == "paused" {
			state = "is paused"
		}
		out = append(out, attentionItem{
			Text: fmt.Sprintf("The email “%s” %s: %d of %d sent.", c.Subject, state, c.Sent, c.Total),
			Href: "/admin/email", Link: "Check",
		})
	}

	now := time.Now().In(a.cfg.Location)
	thisMonth := now.Year()*12 + int(now.Month())
	published := map[int]bool{}
	for _, is := range a.store.Issues() {
		if is.Published != nil {
			published[is.Year*12+is.Month] = true
		}
	}
	for _, is := range a.store.Issues() {
		if m := is.Year*12 + is.Month; is.Published == nil && m >= thisMonth && !published[m] {
			published[m] = true // one reminder per month, for its newest draft
			out = append(out, attentionItem{
				Text: fmt.Sprintf("The %s %d newsletter is a draft, not published yet.", time.Month(is.Month), is.Year),
				Href: fmt.Sprintf("/admin/newsletter/%d", is.ID), Link: "Edit",
			})
		}
	}
	return out
}

type attentionView struct {
	Items  []attentionItem
	Unpaid int // active members who haven't paid this year's dues
	Active int
}

func (a *App) attentionPage(w http.ResponseWriter, r *http.Request) {
	items := a.attention()
	d := attentionView{Items: items}
	year := a.cfg.ThisYear()
	for _, m := range a.store.Members() {
		if m.Active() {
			d.Active++
			if !m.PaidFor(year) {
				d.Unpaid++
			}
		}
	}
	v := adminView{Title: "Needs attention", Tab: "attention", Year: year, Attention: &d}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "attention", v)
}

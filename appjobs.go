package main

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Phone jobs in the admin app: three short screens reached from the app's
// Home, each a thin page over the handlers the computer screens use, whose
// buttons come back to the screen they were pressed on (homeReturn):
//
//   - Rides today: today's and tomorrow's rides with On / Cancel / Put it
//     back (cancelRideAdmin), the job done from the car park.
//   - Approvals: the Needs you lines with a button (a held ride, a loaner
//     request, a spending request this admin answers).
//   - Mark paid (board): members whose dues aren't paid, joiners from the
//     website first, for when PayPal's email comes in (markPaid).

const (
	appTodayPath     = "/admin/app/today"
	appApprovalsPath = "/admin/app/approvals"
	appPaidPath      = "/admin/app/paid"
)

// listReturns are the lists a button may send the admin back to.
var listReturns = map[string]bool{"/admin": true, adminAppPath: true, appTodayPath: true, appApprovalsPath: true, appPaidPath: true}

type appJobsView struct {
	Days  []appDay
	Items []attentionItem
	Paid  []appPaidRow
}

// appDay is a day's rides on Rides today.
type appDay struct {
	Label string
	Rides []RideOcc
}

// appPaidRow is a member on Mark paid.
type appPaidRow struct {
	ID        int
	Name, Sub string
}

// todayRides are today's and tomorrow's rides, cancelled ones included.
func (a *App) todayRides() []appDay {
	today := a.today()
	var days []appDay
	for i, label := range []string{"Today", "Tomorrow"} {
		d := today.AddDate(0, 0, i)
		days = append(days, appDay{Label: label, Rides: a.store.Rides(d, d)})
	}
	return days
}

// approvals are the Needs you lines that have buttons.
func (a *App) approvals(r *http.Request) []attentionItem {
	items, _ := a.attentionFor(r)
	var out []attentionItem
	for _, it := range items {
		if len(it.Acts) > 0 {
			out = append(out, it)
		}
	}
	return out
}

// unpaidMembers are members whose dues aren't paid: website joiners waiting
// for Mark paid first (newest first), then lapsed members, most recent first.
func (a *App) unpaidMembers() []appPaidRow {
	var ms []Member
	for _, m := range a.store.Members() {
		if m.Standing() == standUnpaid {
			ms = append(ms, m)
		}
	}
	last := func(m Member) int {
		if len(m.PaidYears) == 0 {
			return 0
		}
		return slices.Max(m.PaidYears)
	}
	slices.SortFunc(ms, func(x, y Member) int {
		switch {
		case x.WelcomePending != y.WelcomePending:
			if x.WelcomePending {
				return -1
			}
			return 1
		case x.WelcomePending:
			return y.Joined.Compare(x.Joined)
		case last(x) != last(y):
			return last(y) - last(x)
		}
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	out := make([]appPaidRow, 0, len(ms))
	for _, m := range ms {
		var sub []string
		if m.WelcomePending {
			sub = append(sub, "Joined "+m.Joined.In(a.cfg.Location).Format("Jan 2"))
		} else if y := last(m); y > 0 {
			sub = append(sub, fmt.Sprintf("Last paid %d", y))
		}
		if m.Plan == "family" {
			sub = append(sub, "Family")
		}
		out = append(out, appPaidRow{ID: m.ID, Name: m.Name, Sub: strings.Join(sub, " · ")})
	}
	return out
}

func (a *App) appTodayPage(w http.ResponseWriter, r *http.Request) {
	a.appJobPage(w, r, "Rides today", "app-today", &appJobsView{Days: a.todayRides()})
}

func (a *App) appApprovalsPage(w http.ResponseWriter, r *http.Request) {
	a.appJobPage(w, r, "Approvals", "app-approvals", &appJobsView{Items: withReturn(a.approvals(r), appApprovalsPath)})
}

func (a *App) appPaidPage(w http.ResponseWriter, r *http.Request) {
	a.appJobPage(w, r, "Mark paid", "app-paid", &appJobsView{Paid: a.unpaidMembers()})
}

func (a *App) appJobPage(w http.ResponseWriter, r *http.Request, title, tpl string, j *appJobsView) {
	v := adminView{Title: title, Tab: tpl, Jobs: j}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, tpl, v)
}

// appJobTiles is the Jobs row on the app's Home, with what's waiting on each.
func (a *App) appJobTiles(r *http.Request, approvals int) appTileGroup {
	today := 0
	for _, o := range a.store.Rides(a.today(), a.today()) {
		if !o.Cancelled {
			today++
		}
	}
	g := appTileGroup{Label: "Jobs", Key: "jobs", Tiles: []appTile{
		{"Rides today", appTodayPath, "bell", today},
		{"Approvals", appApprovalsPath, "check", approvals},
	}}
	if a.isBoard(r) {
		waiting := 0
		for _, m := range a.store.Members() {
			if m.WelcomePending && !m.Paid() && time.Since(m.Joined) < signupDays*24*time.Hour {
				waiting++
			}
		}
		g.Tiles = append(g.Tiles, appTile{"Mark paid", appPaidPath, "wallet", waiting})
	}
	return g
}

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The Members page counts members by whether they paid this year, not by
// whether they're on the email list: a Squarespace import brings newsletter
// sign-ups and lapsed members too.
func TestMemberStanding(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	now := time.Now()
	a.store.Import([]Member{
		{Name: "Paid Pat", Email: "pat@example.com", PaidYears: []int{year - 1, year}},
		{Name: "Lapsed Lee", Email: "lee@example.com", PaidYears: []int{year - 2}},
		{Name: "List Lou", Email: "lou@example.com"},
		{Name: "Gone Gil", Email: "gil@example.com", Unsubscribed: &now},
	})
	a.store.JoinPublic(memberDetails{Name: "New Nia", Email: "nia@example.com", Plan: "individual"}, year)

	page := do(t, h, admin, "GET", "/admin/members", "", nil).Body.String()
	for _, want := range []string{
		"1 member for " + itoa(year) + ". 5 on the list in all, 1 of them unsubscribed from emails.",
		"Members for " + itoa(year) + " (1)", "Not paid yet (2)", "Email list only (2)", "Everyone (5)",
		"Last paid " + itoa(year-2),
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Members should show %q", want)
		}
	}
	list := do(t, h, admin, "GET", "/admin/members?dues=list", "", nil).Body.String()
	if !strings.Contains(list, "List Lou") || !strings.Contains(list, "Gone Gil") || strings.Contains(list, "Lapsed Lee") || strings.Contains(list, "New Nia") {
		t.Error("Email list only is those who never paid")
	}
	home := do(t, h, admin, "GET", "/admin", "", nil).Body.String()
	if strings.Contains(home, "<dt>Active</dt>") || !strings.Contains(home, `dues=paid">1</a>`) {
		t.Error("Home counts members for the year")
	}
}

// The president can delete several people at once and fix a member's
// record; other admins can't.
func TestPresidentMemberTools(t *testing.T) {
	a, h, _ := testApp(t)
	year := a.cfg.ThisYear()
	a.store.Import([]Member{
		{Name: "Pat", Email: "pat@example.com", PaidYears: []int{year - 2, year - 1, year}},
		{Name: "Lou", Email: "lou@example.com"},
		{Name: "Kim", Email: "kim@example.com"},
	})
	pat, _ := a.store.MemberByEmail("pat@example.com")
	lou, _ := a.store.MemberByEmail("lou@example.com")
	kim, _ := a.store.MemberByEmail("kim@example.com")
	session := func(name, email string, isPresident bool) *http.Cookie {
		u, _ := a.store.AddAdmin(name, email, "")
		hash, _ := hashPassword(name + " has a password")
		u, _ = a.store.SetAdminPassword(u.ID, hash)
		if isPresident {
			a.store.SetPresident(u.ID)
		}
		rec := httptest.NewRecorder()
		a.setSession(rec, accountSession(u), 3600)
		return rec.Result().Cookies()[0]
	}
	sam, president := session("Sam", "sam@example.com", false), session("Kai", "kai@example.com", true)

	if page := do(t, h, president, "GET", "/admin/members", "", nil).Body.String(); !strings.Contains(page, "Delete them") {
		t.Error("the president gets Delete them")
	}
	if page := do(t, h, sam, "GET", "/admin/members", "", nil).Body.String(); strings.Contains(page, "Delete them") {
		t.Error("other admins don't")
	}
	ids := url.Values{"id": {itoa(lou.ID), itoa(kim.ID)}, "return": {"/admin/members?dues=list"}}
	ct, body := form(ids)
	if w := do(t, h, sam, "POST", "/admin/members/delete", ct, body); !strings.Contains(w.Header().Get("Location"), "err=president-only") {
		t.Errorf("another admin deleting: %s", w.Header().Get("Location"))
	}
	ct, body = form(ids)
	w := do(t, h, president, "POST", "/admin/members/delete", ct, body)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "ok=deleted-many") || !strings.Contains(loc, "dues=list") {
		t.Errorf("after deleting: %s", loc)
	}
	if _, ok := a.store.MemberByEmail("lou@example.com"); ok {
		t.Error("Lou should be gone")
	}
	if _, ok := a.store.MemberByEmail("pat@example.com"); !ok {
		t.Error("Pat wasn't ticked")
	}
	if page := do(t, h, president, "GET", w.Header().Get("Location"), "", nil).Body.String(); !strings.Contains(page, "Deleted 2 people.") {
		t.Error("the page should say how many were deleted")
	}

	// Fix Pat's record: they didn't pay two years ago, joined in March, and asked for no emails.
	page := "/admin/members/" + itoa(pat.ID)
	if body := do(t, h, president, "GET", page, "", nil).Body.String(); !strings.Contains(body, `name="years" value="`+itoa(year-2)+", "+itoa(year-1)+", "+itoa(year)+`"`) {
		t.Error("the record box should show the years paid")
	}
	if body := do(t, h, sam, "GET", page, "", nil).Body.String(); strings.Contains(body, `name="years"`) {
		t.Error("only the president can change years paid")
	}
	rec2 := url.Values{"years": {itoa(year-1) + ", " + itoa(year)}, "joined": {itoa(year) + "-03-14"}}
	ct, body = form(rec2)
	if w := do(t, h, sam, "POST", page+"/record", ct, body); !strings.Contains(w.Header().Get("Location"), "err=president-only") {
		t.Error("another admin can't save the record")
	}
	ct, body = form(rec2)
	if w := do(t, h, president, "POST", page+"/record", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=record-saved") {
		t.Fatalf("saving the record: %s", w.Header().Get("Location"))
	}
	got, _ := a.store.MemberByID(pat.ID)
	if len(got.PaidYears) != 2 || got.PaidFor(year-2) || !got.PaidFor(year) || got.Joined.Format("2006-01-02") != itoa(year)+"-03-14" || got.Active() {
		t.Errorf("record: %+v", got)
	}
	ct, body = form(url.Values{"years": {"last year"}, "emails": {"on"}})
	if w := do(t, h, president, "POST", page+"/record", ct, body); !strings.Contains(w.Header().Get("Location"), "err=bad-years") {
		t.Error("years must be years")
	}
	ct, body = form(url.Values{"years": {""}, "emails": {"on"}})
	do(t, h, president, "POST", page+"/record", ct, body)
	if got, _ := a.store.MemberByID(pat.ID); len(got.PaidYears) != 0 || !got.Active() {
		t.Errorf("clearing the years and turning emails back on: %+v", got)
	}
}

func TestParseYears(t *testing.T) {
	for in, want := range map[string]string{"2024, 2025": "2024, 2025", "2025 2024 2025": "2024, 2025", "": ""} {
		got, ok := parseYears(in, 2026)
		if !ok || (Member{PaidYears: got}).YearsPaidText() != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"1999", "2028", "24", "twenty"} {
		if _, ok := parseYears(bad, 2026); ok {
			t.Errorf("%q should be refused", bad)
		}
	}
}

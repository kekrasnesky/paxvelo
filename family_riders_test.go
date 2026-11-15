package main

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// Adults on a Family plan are riders: members too, so club emails reach
// them, sharing their plan's dues and their waiver link.
func TestRidersGetClubEmails(t *testing.T) {
	a, _, _ := testApp(t)
	year := a.cfg.ThisYear()
	pat, _, err := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "family"}, year)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.AddHousehold(pat.ID, []HouseholdAdult{{Name: "Jo Rider", Email: "jo@example.com"}}, false); err != nil {
		t.Fatal(err)
	}
	jo, ok := a.store.MemberByEmail("jo@example.com")
	if !ok || jo.FamilyOf != pat.ID || jo.Plan != "family" {
		t.Fatalf("rider record %+v", jo)
	}
	if _, h, _ := a.store.HouseholdByEmail("jo@example.com"); h.Token == "" || jo.Token != h.Token {
		t.Fatal("the rider shares their adult link token")
	}

	// Club emails: the rider is in the audience, with their own unsubscribe.
	c, err := a.store.StartCampaign("Spring", "Hi", "all", year)
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 2 {
		t.Fatalf("an email to everyone should reach Pat and Jo, got %d", c.Total)
	}

	// Dues: paying for the plan pays for the rider, counted once.
	if _, err := a.store.Pay(pat.ID, false); err != nil {
		t.Fatal(err)
	}
	if jo, _ = a.store.MemberByEmail("jo@example.com"); !jo.Paid() {
		t.Fatal("a rider's dues are their plan's")
	}
	if sum := a.duesIncome(year, "9999-12-31"); len(sum.Lines) != 1 {
		t.Fatalf("dues income should count the plan once, got %d lines", len(sum.Lines))
	}
}

func TestRiderLinkAndRemoval(t *testing.T) {
	a, _, _ := testApp(t)
	year := a.cfg.ThisYear()
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "family"}, year)
	a.store.AddHousehold(pat.ID, []HouseholdAdult{{Name: "Jo Rider", Email: "jo@example.com"}}, false)
	jo, _ := a.store.MemberByEmail("jo@example.com")

	// Their link is their waiver page, for their plan: signing counts as a
	// household waiver, as before riders were members.
	if w, name, _, ok := a.waiverSigner(jo.Token); !ok || !w.Household || w.MemberID != pat.ID || name != "Jo Rider" {
		t.Fatalf("rider link signs the family waiver: %+v %q %v", w, name, ok)
	}

	if err := a.store.UpdateHousehold(pat.ID, "jo@example.com", "Joanna Rider", "joanna@example.com"); err != nil {
		t.Fatal(err)
	}
	if r, ok := a.store.MemberByEmail("joanna@example.com"); !ok || r.Name != "Joanna Rider" || r.FamilyOf != pat.ID {
		t.Fatalf("renamed rider %+v", r)
	}
	if _, ok := a.store.MemberByEmail("jo@example.com"); ok {
		t.Fatal("the old email should be gone")
	}
	if err := a.store.UpdateHousehold(pat.ID, "joanna@example.com", "Joanna", "pat@example.com"); err != errEmailTaken {
		t.Fatalf("an email another member has is refused: %v", err)
	}

	if err := a.store.RemoveHousehold(pat.ID, "joanna@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.store.MemberByEmail("joanna@example.com"); ok {
		t.Fatal("removing an adult removes their rider record")
	}
}

func TestRidersGoWithThePlan(t *testing.T) {
	a, _, _ := testApp(t)
	year := a.cfg.ThisYear()
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "family"}, year)
	a.store.AddHousehold(pat.ID, []HouseholdAdult{{Name: "Jo Rider", Email: "jo@example.com"}, {Name: "Kim Rider", Email: "kim@example.com"}}, false)
	if err := a.store.DeleteMember(pat.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(a.store.Members()); n != 0 {
		t.Fatalf("deleting the plan holder deletes their riders too, %d left", n)
	}
}

func TestRiderDeletedDirectlyLeavesThePlan(t *testing.T) {
	a, _, _ := testApp(t)
	year := a.cfg.ThisYear()
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "family"}, year)
	a.store.AddHousehold(pat.ID, []HouseholdAdult{{Name: "Jo Rider", Email: "jo@example.com"}}, false)
	jo, _ := a.store.MemberByEmail("jo@example.com")
	if err := a.store.DeleteMember(jo.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := a.store.MemberByID(pat.ID); len(p.Household) != 0 {
		t.Fatalf("a deleted rider leaves the plan, so it isn't recreated at startup: %+v", p.Household)
	}
}

func TestEnsureRidersForOldFamilies(t *testing.T) {
	a, _, _ := testApp(t)
	year := a.cfg.ThisYear()
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "family"}, year)
	a.store.mu.Lock()
	a.store.byID(pat.ID).Household = []HouseholdAdult{{Name: "Jo Rider", Email: "jo@example.com", Token: "tok"}}
	a.store.ensureRiders()
	a.store.mu.Unlock()
	if jo, ok := a.store.MemberByEmail("jo@example.com"); !ok || jo.FamilyOf != pat.ID || jo.Token != "tok" {
		t.Fatalf("a family listed before riders gets its rider: %+v", jo)
	}
}

// Anyone can type a member's email on the "email me my link" form. A rider
// isn't sent their link until the plan's dues are paid, as adults aren't.
func TestRiderLinkWaitsForDues(t *testing.T) {
	a, h, _ := testApp(t)
	year := a.cfg.ThisYear()
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "family"}, year)
	a.store.AddHousehold(pat.ID, []HouseholdAdult{{Name: "Jo Rider", Email: "jo@example.com"}}, false)
	sent := 0
	a.mailer.onSend = func(to, subject, body string) { sent++ }
	form := url.Values{"email": {"jo@example.com"}}.Encode()
	do(t, h, nil, "POST", "/membership/link", "application/x-www-form-urlencoded", strings.NewReader(form))
	if sent != 0 {
		t.Fatalf("no link before the dues are paid, sent %d", sent)
	}
}

// The president and board edit a family plan from the member's admin page.
func TestAdminEditsFamilyPlan(t *testing.T) {
	a, h, admin := testApp(t)
	year := a.cfg.ThisYear()
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "family"}, year)
	post := func(form url.Values) string {
		w := do(t, h, admin, "POST", "/admin/members/"+strconv.Itoa(pat.ID)+"/family", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
		return w.Header().Get("Location")
	}
	if loc := post(url.Values{"do": {"add"}, "adult_name": {"Jo Rider"}, "adult_email": {"jo@example.com"}}); !strings.Contains(loc, "ok=family-added") {
		t.Fatalf("add: %s", loc)
	}
	if jo, ok := a.store.MemberByEmail("jo@example.com"); !ok || jo.FamilyOf != pat.ID {
		t.Fatal("an added adult becomes a rider")
	}
	if loc := post(url.Values{"do": {"edit"}, "old_email": {"jo@example.com"}, "adult_name": {"Joanna Rider"}, "adult_email": {"jo@example.com"}}); !strings.Contains(loc, "ok=family-saved") {
		t.Fatalf("edit: %s", loc)
	}
	if jo, _ := a.store.MemberByEmail("jo@example.com"); jo.Name != "Joanna Rider" {
		t.Fatalf("edited name %q", jo.Name)
	}
	page := do(t, h, admin, "GET", "/admin/members/"+strconv.Itoa(pat.ID), "", nil).Body.String()
	if !strings.Contains(page, "Joanna Rider") || !strings.Contains(page, "Add adult") {
		t.Error("the member page lists the rider with the controls to change them")
	}
	if loc := post(url.Values{"do": {"remove"}, "old_email": {"jo@example.com"}}); !strings.Contains(loc, "ok=family-removed") {
		t.Fatalf("remove: %s", loc)
	}
	if _, ok := a.store.MemberByEmail("jo@example.com"); ok {
		t.Fatal("removing takes the rider off too")
	}
}

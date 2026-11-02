package main

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// Every email goes in the sent log: the kind, who to, the subject and the
// result, never the body. The Sent log page shows it newest first, with
// search and filters, and Home warns about failures.
func TestSentLog(t *testing.T) {
	a, h, c := testApp(t)
	pat, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual"}, 2026)
	a.mailer.SendDirect(mailMyLink, pat.Name, pat.Email, "Your PaxVelo membership link", "secret link https://example.com/membership?t=abc")
	a.mailer.SendTest("karl@example.com", "Spring kickoff", "Hi")
	a.mailer.log.Add(sentEmail{Time: time.Now().UTC(), Kind: mailCampaign, Campaign: 3, To: "sam@example.com", Name: "Sam", Subject: "November news", Result: "failed", Error: "550 mailbox unavailable"})

	sent := a.mailer.log.All()
	if len(sent) != 3 || sent[0].Subject != "November news" || sent[2].Kind != mailMyLink || sent[2].Result != "test mode" {
		t.Fatalf("log, newest first: %+v", sent)
	}
	if sent[1].Kind != mailTest || sent[1].Subject != "[Test] Spring kickoff" {
		t.Errorf("test email entry: %+v", sent[1])
	}

	page := do(t, h, c, "GET", "/admin/email/log", "", nil).Body.String()
	for _, want := range []string{"Sent log", "Pat Rider", "pat@example.com", "Your PaxVelo membership link", "November news", "550 mailbox unavailable", "Test mode, not sent", `class="log-fail"`} {
		if !strings.Contains(page, want) {
			t.Errorf("sent log page is missing %q", want)
		}
	}
	if strings.Contains(page, "secret link") {
		t.Error("the sent log must not show what an email said")
	}
	failed := do(t, h, c, "GET", "/admin/email/log?result=failed", "", nil).Body.String()
	if !strings.Contains(failed, "November news") || strings.Contains(failed, "Spring kickoff") {
		t.Error("Failed only should show only failures")
	}
	q := do(t, h, c, "GET", "/admin/email/log?"+url.Values{"q": {"spring"}}.Encode(), "", nil).Body.String()
	if !strings.Contains(q, "Spring kickoff") || strings.Contains(q, "November news") {
		t.Error("search should find by subject")
	}
	kind := do(t, h, c, "GET", "/admin/email/log?"+url.Values{"kind": {mailMyLink}}.Encode(), "", nil).Body.String()
	if !strings.Contains(kind, "Your PaxVelo membership link") || strings.Contains(kind, "Spring kickoff") {
		t.Error("the kind filter should narrow it")
	}
	if home := do(t, h, c, "GET", "/admin", "", nil).Body.String(); !strings.Contains(home, "1 email didn’t send in the last 7 days.") {
		t.Error("Home should warn about the failed email")
	}
}

// The Members list says whether each member signed this year's waiver.
func TestMembersWaiverColumn(t *testing.T) {
	a, h, c := testApp(t)
	year := a.cfg.ThisYear()
	aadit, _, _ := a.store.Join(memberDetails{Name: "Aadit", Email: "aadit@example.com", Plan: "individual"}, year)
	a.store.Join(memberDetails{Name: "Bea", Email: "bea@example.com", Plan: "individual"}, year)
	a.store.mu.Lock()
	a.store.d.Waivers = append(a.store.d.Waivers, &Waiver{ID: 1, MemberID: aadit.ID, Year: year}, &Waiver{ID: 2, MemberID: 2, Year: year - 1})
	a.store.mu.Unlock()
	page := do(t, h, c, "GET", "/admin/members", "", nil).Body.String()
	if !strings.Contains(page, itoa(year)+" waiver</button>") {
		t.Error("the Members list should have this year's waiver column")
	}
	row := func(name string) string {
		i := strings.Index(page, `data-name="`+name+`"`)
		j := strings.Index(page[i:], "</tr>")
		return page[i : i+j]
	}
	if !strings.Contains(row("Aadit"), "Signed</span>") {
		t.Error("Aadit signed this year")
	}
	if !strings.Contains(row("Bea"), "Not signed") {
		t.Error("last year's waiver doesn't count for this year")
	}
}

package main

import (
	"net/url"
	"strings"
	"testing"
)

// An issue goes out as an email: its stories with their kickers, photos and
// buttons, the rides, and a button to read it online. Admins preview it,
// send a test, and send it to members once it's published; sending it
// leaves the Email members draft alone.
func TestEmailIssue(t *testing.T) {
	a, h, c := testApp(t)
	ct, body := form(url.Values{"month": {"11"}, "year": {"2026"}})
	do(t, h, c, "POST", "/admin/newsletter", ct, body)
	is := a.store.Issues()[0]
	is.Tagline, is.Cover = "New website, same snacks.", "/media/cover.jpg"
	is.Blocks = []Block{
		{ID: "a1", Kind: "big", Story: Story{Kicker: "New around here", Heading: "Our new website is live", Subhead: "No password required.", Body: "See the [calendar](/calendar) and the [kit store]({kit}).", Photo: "/media/web.jpg", Caption: "Mike struggling", LinkLabel: "Have a look", LinkURL: "/"}},
		{ID: "a2", Kind: "share"},
	}
	is, _ = a.store.SaveIssue(is)
	mail := a.issueEmail(is)
	for _, want := range []string{"Hi {first_name}, here’s the November 2026 issue", "New website, same snacks.", "![PaxVelo Newsletter](/media/cover.jpg)", "^^ New around here", "## Our new website is live", "*No password required.*",
		"![Mike struggling](/media/web.jpg)", "[calendar](/calendar)", "[[Have a look](/)]", "[[Share your photos](/share-photos)]", "[[Read it on the website](" + is.Path() + ")]"} {
		if !strings.Contains(mail, want) {
			t.Errorf("issue email is missing %q:\n%s", want, mail)
		}
	}
	if strings.Contains(mail, "{kit}") {
		t.Error("{kit} should be filled in")
	}
	html := string(emailHTML(mail, "https://www.paxvelo.club"))
	for _, want := range []string{`<hr style=`, "text-transform:uppercase", "<em>No password required.</em>", `src="https://www.paxvelo.club/media/web.jpg"`, `href="https://www.paxvelo.club/calendar"`} {
		if !strings.Contains(html, want) {
			t.Errorf("issue email HTML is missing %q", want)
		}
	}

	page := "/admin/newsletter/" + itoa(is.ID)
	if w := do(t, h, c, "GET", page+"/email-preview", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Hi Pat, here’s the November 2026 issue") {
		t.Errorf("preview: %d", w.Code)
	}
	if p := do(t, h, c, "GET", page, "", nil).Body.String(); !strings.Contains(p, `id="email-it"`) || !strings.Contains(p, "Send a test") {
		t.Error("the issue page should have the Email this issue panel")
	}

	// A test goes to one address, logged as a test.
	ct, body = form(url.Values{"test_to": {"karl@example.com"}, "subject": {"November news"}})
	if loc := do(t, h, c, "POST", page+"/email-test", ct, body).Header().Get("Location"); !strings.Contains(loc, "ok=issue-tested") {
		t.Fatalf("test: %s", loc)
	}
	if sent := a.mailer.log.All(); len(sent) != 1 || sent[0].Kind != mailTest || sent[0].Subject != "[Test] November news" || sent[0].To != "karl@example.com" {
		t.Errorf("test email in the sent log: %+v", sent)
	}

	// Members only get a published issue, and it leaves the Email draft alone.
	a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual"}, 2026)
	a.store.SetDraft(EmailDraft{Subject: "Half-written", Body: "Still working on this"})
	ct, body = form(url.Values{"audience": {"all"}})
	if loc := do(t, h, c, "POST", page+"/email-send", ct, body).Header().Get("Location"); !strings.Contains(loc, "err=not-published") {
		t.Errorf("a draft issue shouldn't go to members: %s", loc)
	}
	now := a.store.Issues()[0]
	pub := now.Updated
	now.Published = &pub
	a.store.SaveIssue(now)
	ct, body = form(url.Values{"audience": {"all"}, "subject": {"November news"}})
	if loc := do(t, h, c, "POST", page+"/email-send", ct, body).Header().Get("Location"); !strings.Contains(loc, "ok=issue-sending") || !strings.Contains(loc, "n=1") {
		t.Fatalf("send: %s", loc)
	}
	camps := a.issueCampaigns(is.ID)
	if len(camps) != 1 || camps[0].Subject != "November news" || camps[0].Total != 1 || !strings.Contains(camps[0].Body, "## Our new website is live") {
		t.Errorf("issue campaign: %+v", camps)
	}
	if d := a.store.Draft(); d.Subject != "Half-written" {
		t.Error("sending the newsletter shouldn't throw away the Email members draft")
	}
	if p := do(t, h, c, "GET", page, "", nil).Body.String(); !strings.Contains(p, "<h3>Emailed</h3>") {
		t.Error("the issue page should list when it was emailed")
	}
}

// Italics, kicker lines and dividers in any email.
func TestEmailMarks(t *testing.T) {
	html := string(emailHTML("^^ Rider of the month\n\n## Meet Gabe\n\n*The man behind the cookies*\n\n---\n\n5 * 3 is 15", ""))
	for _, want := range []string{"text-transform:uppercase;color:#9a2b29\">Rider of the month</p>", "<em>The man behind the cookies</em>", "<hr style=", "5 * 3 is 15"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in %s", want, html)
		}
	}
	if txt := emailText("^^ Rider of the month\n\n*cookies*", ""); !strings.Contains(txt, "RIDER OF THE MONTH") || strings.Contains(txt, "*") {
		t.Errorf("plain text: %q", txt)
	}
}

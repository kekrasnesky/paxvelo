package main

import (
	"bytes"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// A password link that can't be sent (here, nothing is listening on the mail
// port) shows in the server log with who it was for and why it failed.
func TestFailedEmailIsLogged(t *testing.T) {
	a, _, _ := testApp(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	a.mailer.cfg.SMTPHost, a.mailer.cfg.SMTPPort = "127.0.0.1", port

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	if err := a.mailer.SendDirect(mailPassword, "Pat", "pat@example.com", "Reset your PaxVelo admin password", "Hi"); err == nil {
		t.Fatal("sending should fail with no mail server")
	}
	got := buf.String()
	for _, want := range []string{"email to pat@example.com failed", "Reset your PaxVelo admin password"} {
		if !strings.Contains(got, want) {
			t.Errorf("log should mention %q, got: %s", want, got)
		}
	}
	// The sent log has it too, as failed, with the reason.
	sent := a.mailer.log.All()
	if len(sent) != 1 || sent[0].Kind != mailPassword || !sent[0].Failed() || sent[0].Error == "" || sent[0].To != "pat@example.com" {
		t.Errorf("sent log: %+v", sent)
	}
	if n := a.mailer.log.FailedSince(time.Now().Add(-time.Hour)); n != 1 {
		t.Errorf("failed in the last hour = %d", n)
	}
}

// Only emails to members carry an unsubscribe link and Gmail's one-click
// Unsubscribe button. A password reset or a ride notice doesn't.
func TestUnsubscribeOnlyOnMemberEmails(t *testing.T) {
	a, _, _ := testApp(t)
	m := a.mailer
	member := string(m.build("Pat", "pat@example.com", "News", "Hi", "<p>Hi</p>", "https://example.com/unsubscribe?t=abc"))
	if !strings.Contains(member, "List-Unsubscribe: <https://example.com/unsubscribe?t=abc>") || !strings.Contains(member, "List-Unsubscribe-Post") {
		t.Error("a member email should have the unsubscribe headers")
	}
	direct := string(m.build("Pat", "pat@example.com", "Reset", "Hi", "<p>Hi</p>", ""))
	if strings.Contains(direct, "List-Unsubscribe") {
		t.Error("a password email shouldn't have unsubscribe headers")
	}
	page, err := a.site.renderEmail("Reset", "Hi", memberLinks{}, a.cfg.SiteURL)
	if err != nil || strings.Contains(page, "Unsubscribe") {
		t.Errorf("a password email shouldn't mention unsubscribing (err %v)", err)
	}
	if page, _ := a.site.renderEmail("News", "Hi", memberLinks{Mine: "https://example.com/m", Unsub: "https://example.com/u"}, a.cfg.SiteURL); !strings.Contains(page, "Unsubscribe") {
		t.Error("a member email should have the unsubscribe link")
	}
}

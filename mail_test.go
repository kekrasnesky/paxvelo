package main

import (
	"bytes"
	"log"
	"net"
	"os"
	"strings"
	"testing"
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

	if err := a.mailer.SendDirect("Pat", "pat@example.com", "Reset your PaxVelo admin password", "Hi"); err == nil {
		t.Fatal("sending should fail with no mail server")
	}
	got := buf.String()
	for _, want := range []string{"email to pat@example.com failed", "Reset your PaxVelo admin password"} {
		if !strings.Contains(got, want) {
			t.Errorf("log should mention %q, got: %s", want, got)
		}
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
	page, err := a.site.renderEmail("Reset", "Hi", "", a.cfg.SiteURL)
	if err != nil || strings.Contains(page, "Unsubscribe") {
		t.Errorf("a password email shouldn't mention unsubscribing (err %v)", err)
	}
	if page, _ := a.site.renderEmail("News", "Hi", "https://example.com/u", a.cfg.SiteURL); !strings.Contains(page, "Unsubscribe") {
		t.Error("a member email should have the unsubscribe link")
	}
}

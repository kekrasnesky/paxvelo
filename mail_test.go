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

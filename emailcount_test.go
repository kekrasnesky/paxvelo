package main

import (
	"strings"
	"testing"
)

func TestEmailAllowance(t *testing.T) {
	a, h, admin := testApp(t)
	a.mailer.cfg.SMTPHost = "smtp.example.com" // the card shows only when sending for real
	a.cfg.EmailLimit = 300
	for range 12 {
		a.store.CountEmail()
	}
	page := do(t, h, admin, "GET", "/admin/email", "", nil).Body.String()
	for _, want := range []string{"288 emails left today", "12 of 300 sent", `style="width:4%"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Email tab is missing %q", want)
		}
	}
	if got := (emailAllowance{Sent: 290, Limit: 300}); got.Left() != 10 || got.Percent() != 96 {
		t.Errorf("allowance: %d left, %d%%", got.Left(), got.Percent())
	}
	if got := (emailAllowance{Sent: 320, Limit: 300}); got.Left() != 0 || got.Percent() != 100 {
		t.Errorf("past the limit: %d left, %d%%", got.Left(), got.Percent())
	}
}

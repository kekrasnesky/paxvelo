package main

import (
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// Anyone can ask for ride alerts by email in the Ride alerts panel. Nothing
// comes until they press Confirm (opening the link changes nothing);
// then a cancellation or a put-back emails them with a Stop link, except
// a rider who said "I'm in" with that address (they get the rider email).
func TestRideAlertEmails(t *testing.T) {
	a, h, _ := rideApp(t)
	var mu sync.Mutex
	var mails []string
	a.mailer.onSend = func(to, subject, body string) {
		if to == a.cfg.RidesEmail {
			return
		}
		mu.Lock()
		mails = append(mails, to+"|"+subject+"|"+body)
		mu.Unlock()
	}
	take := func() []string {
		a.mailer.bg.Wait()
		mu.Lock()
		defer mu.Unlock()
		out := mails
		mails = nil
		return out
	}
	ask := func(body string) int {
		return do(t, h, nil, "POST", "/alerts/subscribe", "application/json", strings.NewReader(body)).Code
	}

	if c := ask(`{"email":"not an email"}`); c != 400 {
		t.Errorf("a bad address: %d", c)
	}
	if c := ask(`{"email":"bot@example.com","website":"spam"}`); c != 200 || len(take()) != 0 || len(a.store.d.AlertEmails) != 0 {
		t.Error("the honeypot should keep nothing and send nothing, with the same answer")
	}
	if c := ask(`{"email":"Pat@Example.com"}`); c != 200 {
		t.Fatalf("asking: %d", c)
	}
	got := take()
	if len(got) != 1 || !strings.HasPrefix(got[0], "pat@example.com|Confirm ride alerts|") {
		t.Fatalf("confirm email: %q", got)
	}
	link := regexp.MustCompile(`/alerts/email\?t=[0-9a-f]+`).FindString(got[0])
	if link == "" {
		t.Fatal("the confirm email should link to the confirm page")
	}
	tok := strings.TrimPrefix(link, "/alerts/email?t=")

	day := func(n int) string { return a.today().AddDate(0, 0, n).Format("2006-01-02") }
	o, err := a.store.AddRide(CalRide{Date: day(1), Time: "18:30", Name: "Zwift Spin"})
	if err != nil {
		t.Fatal(err)
	}
	// Opening the link is not confirming.
	if p := do(t, h, nil, "GET", link, "", nil).Body.String(); !strings.Contains(p, "pat@example.com") || !strings.Contains(p, `name="t" value="`+tok+`"`) {
		t.Error("the confirm page should show the address and a Confirm button")
	}
	a.applyStatus(o.Key, statusCancel, "Server down", "Jane", nil)
	if got := take(); len(got) != 0 {
		t.Fatalf("an unconfirmed address got %q", got)
	}
	a.applyStatus(o.Key, statusOpen, "", "Jane", nil)
	take()

	ct, body := form(url.Values{"t": {tok}})
	if p := do(t, h, nil, "POST", "/alerts/email", ct, body).Body.String(); !strings.Contains(p, "Ride alerts are on") {
		t.Fatal("Confirm should turn alerts on")
	}
	if c := ask(`{"email":"pat@example.com"}`); c != 200 || len(take()) != 0 {
		t.Error("asking again once confirmed should send nothing")
	}

	a.applyStatus(o.Key, statusCancel, "Server down", "Jane", nil)
	got = take()
	if len(got) != 1 || !strings.Contains(got[0], "pat@example.com|Cancelled: Zwift Spin") || !strings.Contains(got[0], "Server down") {
		t.Fatalf("cancellation email: %q", got)
	}
	a.applyStatus(o.Key, statusOpen, "", "Jane", nil)
	if got := take(); len(got) != 1 || !strings.Contains(got[0], "|Back on: Zwift Spin") {
		t.Fatalf("back-on email: %q", got)
	}

	// A rider with the same address gets only the rider email.
	a.store.UpdateRide(o.Key, func(c *CalRide) error {
		c.Riders = append(c.Riders, RideRider{ID: "1", Name: "Pat", Email: "pat@example.com"})
		return nil
	})
	a.applyStatus(o.Key, statusCancel, "", "Jane", nil)
	if got := take(); len(got) != 1 {
		t.Errorf("a rider on the list should get one email, got %q", got)
	}

	// Stop: the page asks, Gmail's one-click posts straight to it.
	if p := do(t, h, nil, "GET", link+"&stop=1", "", nil).Body.String(); !strings.Contains(p, `value="stop"`) {
		t.Error("the Stop link should show a Stop button")
	}
	if len(a.store.AlertEmails()) != 1 {
		t.Fatal("still on before Stop is pressed")
	}
	do(t, h, nil, "POST", link+"&stop=1", "application/x-www-form-urlencoded", strings.NewReader("List-Unsubscribe=One-Click"))
	if len(a.store.AlertEmails()) != 0 {
		t.Error("one-click Unsubscribe should stop them")
	}
}

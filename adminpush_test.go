package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Admins turn alerts on from the admin app: the phone is tied to them, only
// they can change or forget it, and the setup password can't turn them on.
func TestAdminPushSubscribe(t *testing.T) {
	a, h, setup := testApp(t)
	web, board, _ := adminLevels(t, a)
	wes, _ := a.store.AdminByEmail("wes@example.com")
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	keys := map[string]string{"p256dh": b64.EncodeToString(priv.PublicKey().Bytes()), "auth": b64.EncodeToString(make([]byte, 16))}
	fcm := "https://fcm.googleapis.com/fcm/send/admin1"
	post := func(c *http.Cookie, path string, v any) (int, string) {
		b, _ := json.Marshal(v)
		w := do(t, h, c, "POST", path, "application/json", strings.NewReader(string(b)))
		return w.Code, w.Body.String()
	}

	if c, _ := post(web, "/admin/push/subscribe", map[string]any{"endpoint": "https://evil.example/x", "keys": keys}); c != 400 {
		t.Errorf("a made-up push address = %d", c)
	}
	c, body := post(web, "/admin/push/subscribe", map[string]any{"endpoint": fcm, "keys": keys})
	if c != 200 || !strings.Contains(body, `"rides"`) {
		t.Fatalf("subscribe = %d %s", c, body)
	}
	if a.store.AdminPushCount(wes.ID) != 1 || a.store.PushCount() != 0 {
		t.Fatal("the phone should be Wes's admin phone, not a ride-alert phone")
	}
	if c, body := post(web, "/admin/push/kinds", map[string]any{"endpoint": fcm, "kinds": []string{"requests"}}); c != 200 || body != "{\"kinds\":[\"requests\"]}\n" {
		t.Errorf("kinds = %d %s", c, body)
	}
	if c, _ := post(board, "/admin/push/kinds", map[string]any{"endpoint": fcm}); c != 404 {
		t.Errorf("another admin reading Wes's phone = %d", c)
	}
	post(board, "/admin/push/unsubscribe", map[string]any{"endpoint": fcm})
	if a.store.AdminPushCount(wes.ID) != 1 {
		t.Error("another admin forgot Wes's phone")
	}

	// The push service gives the phone a new address: sw.js posts it to
	// /push/subscribe with the old one, and it stays an admin phone.
	moved := "https://fcm.googleapis.com/fcm/send/admin2"
	b, _ := json.Marshal(map[string]any{"endpoint": moved, "keys": keys, "old": fcm})
	do(t, h, nil, "POST", "/push/subscribe", "application/json", strings.NewReader(string(b)))
	if _, err := a.store.AdminPushKinds(wes.ID, moved, nil); err != nil || a.store.PushCount() != 0 {
		t.Errorf("moved phone: %v, ride-alert phones %d", err, a.store.PushCount())
	}

	post(web, "/admin/push/unsubscribe", map[string]any{"endpoint": moved})
	if a.store.AdminPushCount(wes.ID) != 0 {
		t.Error("Turn off alerts should forget the phone")
	}

	// Removing an admin forgets their phones.
	a.store.AddAdminPush(wes.ID, PushSub{Endpoint: fcm, P256dh: keys["p256dh"], Auth: keys["auth"]})
	a.store.DeleteAdmin(wes.ID)
	if a.store.AdminPushCount(wes.ID) != 0 {
		t.Error("a removed admin's phone still gets alerts")
	}

	// The setup password has no account to tie a phone to: no bell.
	if c, _ := post(setup, "/admin/push/subscribe", map[string]any{"endpoint": fcm, "keys": keys}); c != 403 {
		t.Errorf("setup password subscribe = %d", c)
	}
	if strings.Contains(do(t, h, setup, "GET", adminAppPath, "", nil).Body.String(), `id="app-alerts"`) {
		t.Error("the setup password shouldn't get the Alerts switches")
	}
	if !strings.Contains(do(t, h, board, "GET", adminAppPath, "", nil).Body.String(), `id="app-alerts"`) {
		t.Error("an admin should get the Alerts switches")
	}
}

// Alerts go to the admins they're for: everyone for rides and photos, the
// board for new members, and only whoever's answer a request wants.
func TestAdminAlerts(t *testing.T) {
	a, _, _ := testApp(t)
	adminLevels(t, a)
	wes, _ := a.store.AdminByEmail("wes@example.com")
	bea, _ := a.store.AdminByEmail("bea@example.com")
	wesPhone, beaPhone := newFakePhone(t, http.StatusCreated), newFakePhone(t, http.StatusCreated)
	a.store.AddAdminPush(wes.ID, wesPhone.sub())
	a.store.AddAdminPush(bea.ID, beaPhone.sub())
	titles := func(p *fakePhone) []string {
		a.push.wg.Wait()
		var out []string
		for _, m := range p.messages() {
			out = append(out, m.Title)
		}
		return out
	}

	o, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 2).Format("2006-01-02"), Name: "Hilly Loop", Held: true})
	a.alertHeldRide(o, "Sam")
	a.push.wg.Wait() // sent in the background: keep the order
	a.alertSignup(Member{ID: 9, Name: "New Rider", Plan: "individual"})
	if got := titles(wesPhone); !slices.Equal(got, []string{"Waiting for approval: Hilly Loop"}) {
		t.Errorf("website admin got %q", got)
	}
	if got := titles(beaPhone); !slices.Equal(got, []string{"Waiting for approval: Hilly Loop", "New member: New Rider"}) {
		t.Errorf("board member got %q", got)
	}
	if m := beaPhone.messages()[0]; m.URL != "/admin/rides/"+o.Key || m.Icon != "/static/img/admin-192.png" {
		t.Errorf("alert = %+v", m)
	}

	// A kind turned off stops coming.
	a.store.AdminPushKinds(bea.ID, beaPhone.sub().Endpoint, []string{"requests"})
	a.alertHeldRide(o, "Sam")
	if n := len(titles(beaPhone)); n != 2 {
		t.Errorf("rides switched off, still got %d", n)
	}

	// A request only reaches whoever's answer is wanted, and its answer only
	// the sender.
	q := SpendRequest{ID: 5, Kind: reqReport, Cents: 4500, What: "Water bottles", By: "Wes Site", ByID: wes.ID}
	a.alertAdmins("requests", pushMsg{Title: "Needs your OK"}, 0, func(u AdminUser) bool { return u.ID == bea.ID })
	a.push.wg.Wait()
	a.alertRequestSettled(q)
	if got := titles(wesPhone); len(got) != 3 || got[2] != "Approved: Water bottles" { // rides still on for Wes: he got the second ride alert too
		t.Errorf("sender got %q", got)
	}
	if got := titles(beaPhone); len(got) != 3 || got[2] != "Needs your OK" {
		t.Errorf("approver got %q", got)
	}

	// A phone the push service says is gone is forgotten.
	gone := newFakePhone(t, http.StatusGone)
	a.store.AddAdminPush(wes.ID, gone.sub())
	a.alertHeldRide(o, "Sam")
	a.push.wg.Wait()
	if n := a.store.AdminPushCount(wes.ID); n != 1 {
		t.Errorf("Wes has %d phones; the gone one should be forgotten", n)
	}
}

// Riders saying "I'm in" and waivers signed are alerts an admin turns on
// (they start off); waivers go to the board only. Each comes from the real
// form, once: saying "I'm in" again or signing twice the same day doesn't.
func TestAdminAlertsRidersAndWaivers(t *testing.T) {
	a, h, _ := testApp(t)
	adminLevels(t, a)
	wes, _ := a.store.AdminByEmail("wes@example.com")
	bea, _ := a.store.AdminByEmail("bea@example.com")
	wesPhone, beaPhone := newFakePhone(t, http.StatusCreated), newFakePhone(t, http.StatusCreated)
	a.store.AddAdminPush(wes.ID, wesPhone.sub())
	a.store.AddAdminPush(bea.ID, beaPhone.sub())
	sent := func(p *fakePhone) []pushMsg { a.push.wg.Wait(); return p.messages() }

	o, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 2).Format("2006-01-02"), Time: "08:00", Name: "Coffee Spin"})
	in := func(c *http.Cookie, name string) *http.Cookie {
		ct, body := form(url.Values{"ft": {oldToken(a)}, "rider_name": {name}})
		w := do(t, h, c, "POST", "/calendar/ride/"+o.Key+"/in", ct, body)
		a.push.wg.Wait()
		for _, k := range w.Result().Cookies() {
			if k.Name == riderCookie {
				return k
			}
		}
		return c
	}
	signRide := func(name string) {
		ct, b := form(rideWaiverForm(a, url.Values{"bike": {"own"}, "name": {name}, "signature": {name}}))
		if w := do(t, h, nil, "POST", "/ride-waiver", ct, b); w.Code != http.StatusSeeOther {
			t.Fatalf("ride waiver: %d", w.Code)
		}
		a.push.wg.Wait()
	}

	// Both start off.
	in(nil, "Sam Rider")
	signRide("Sam Newrider")
	if n, m := len(sent(wesPhone)), len(sent(beaPhone)); n+m != 0 {
		t.Fatalf("new kinds should start off: %d, %d", n, m)
	}
	if on, _ := a.store.AdminPushKinds(bea.ID, beaPhone.sub().Endpoint, nil); slices.Contains(on, "riders") || slices.Contains(on, "waivers") || !slices.Contains(on, "rides") {
		t.Errorf("kinds = %q", on)
	}
	for _, p := range []struct {
		id    int
		phone *fakePhone
	}{{wes.ID, wesPhone}, {bea.ID, beaPhone}} {
		on, _ := a.store.AdminPushKinds(p.id, p.phone.sub().Endpoint, []string{"rides", "riders", "waivers"})
		if !slices.Contains(on, "riders") || !slices.Contains(on, "waivers") || slices.Contains(on, "photos") {
			t.Errorf("kinds after turning on = %q", on)
		}
	}

	// I'm in: one alert per ride, saying how many are going.
	jo := in(nil, "Jo Rider")
	in(jo, "Jo Rider") // again from the same browser: not a new rider
	got := sent(beaPhone)
	if len(got) != 1 || got[0].Title != "Jo Rider is in: Coffee Spin" || !strings.HasSuffix(got[0].Body, "2 going") ||
		got[0].URL != "/admin/rides/"+o.Key+"#riders" || got[0].Tag != "in-"+o.Key {
		t.Fatalf("I'm in alert = %+v", got)
	}
	if n := len(sent(wesPhone)); n != 1 {
		t.Errorf("website admins get I'm in too: %d", n)
	}

	// A ride waiver: the board only, once a day.
	signRide("Kim Newrider")
	signRide("Kim Newrider")
	got = sent(beaPhone)
	if len(got) != 2 || got[1].Title != "Waiver signed: Kim Newrider" || got[1].Body != "Ride waiver, own bike" || !strings.HasPrefix(got[1].URL, "/admin/ride-waivers/") {
		t.Fatalf("ride waiver alert = %+v", got)
	}
	if n := len(sent(wesPhone)); n != 1 {
		t.Errorf("waivers are the board's: website admin got %d", n)
	}

	// A member's waiver from their own page.
	year := a.cfg.ThisYear()
	m, _, _ := a.store.Join(memberDetails{Name: "Pat Rider", Email: "pat@example.com", Plan: "individual"}, year)
	ct, body := form(url.Values{"t": {m.Token}, "waiver": {"yes"}, "signature": {"Pat Rider"}})
	if w := do(t, h, nil, "POST", "/membership/waiver", ct, body); w.Code != http.StatusSeeOther {
		t.Fatalf("member waiver: %d", w.Code)
	}
	got = sent(beaPhone)
	if len(got) != 3 || got[2].Title != "Waiver signed: Pat Rider" || got[2].Body != strconv.Itoa(year)+" waiver · Membership page" || !strings.HasPrefix(got[2].URL, "/admin/waivers/") {
		t.Fatalf("member waiver alert = %+v", got)
	}

	// Turned off again, they stop.
	a.store.AdminPushKinds(bea.ID, beaPhone.sub().Endpoint, []string{"rides"})
	in(nil, "Lee Rider")
	if n := len(sent(beaPhone)); n != 3 {
		t.Errorf("riders switched off, still got %d", n)
	}
}

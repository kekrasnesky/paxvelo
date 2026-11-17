package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"slices"
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
	if m := beaPhone.messages()[0]; m.URL != "/admin/calendar" || m.Icon != "/static/img/admin-192.png" {
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

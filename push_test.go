package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakePhone is a push service with one phone on it: it reads what the site
// sends, as the phone would.
type fakePhone struct {
	t      *testing.T
	priv   *ecdh.PrivateKey
	auth   []byte
	srv    *httptest.Server
	status int
	mu     sync.Mutex
	got    []pushMsg
	hdr    []http.Header
}

func newFakePhone(t *testing.T, status int) *fakePhone {
	p := &fakePhone{t: t, status: status, auth: make([]byte, 16)}
	p.priv, _ = ecdh.P256().GenerateKey(rand.Reader)
	rand.Read(p.auth)
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m pushMsg
		if err := json.Unmarshal(openPush(t, p.priv, p.auth, body), &m); err != nil {
			t.Errorf("phone couldn't read the message: %v", err)
		}
		p.mu.Lock()
		p.got, p.hdr = append(p.got, m), append(p.hdr, r.Header.Clone())
		p.mu.Unlock()
		w.WriteHeader(p.status)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakePhone) sub() PushSub {
	return PushSub{Endpoint: p.srv.URL + "/send/1", P256dh: b64.EncodeToString(p.priv.PublicKey().Bytes()), Auth: b64.EncodeToString(p.auth)}
}

func (p *fakePhone) messages() []pushMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pushMsg(nil), p.got...)
}

// A browser turns ride alerts on and off; only real push services' addresses
// are kept, with real keys, from this site.
func TestPushSubscribe(t *testing.T) {
	a, h, _ := testApp(t)
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	keys := map[string]string{"p256dh": b64.EncodeToString(priv.PublicKey().Bytes()), "auth": b64.EncodeToString(make([]byte, 16))}
	post := func(path string, v any, origin string) int {
		b, _ := json.Marshal(v)
		r := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	sub := func(endpoint string, k map[string]string) map[string]any {
		return map[string]any{"endpoint": endpoint, "keys": k}
	}
	fcm := "https://fcm.googleapis.com/fcm/send/abc123"
	if c := post("/push/subscribe", sub(fcm, keys), ""); c != 204 {
		t.Fatalf("subscribe = %d", c)
	}
	post("/push/subscribe", sub(fcm, keys), "")
	if n := a.store.PushCount(); n != 1 {
		t.Errorf("the same phone twice: %d", n)
	}
	for name, s := range map[string]map[string]any{
		"another site":    sub("https://evil.example/collect", keys),
		"plain http":      sub("http://fcm.googleapis.com/fcm/send/x", keys),
		"look-alike host": sub("https://fcm.googleapis.com.evil.example/x", keys),
		"a port":          sub("https://fcm.googleapis.com:8443/x", keys),
		"bad key":         sub("https://web.push.apple.com/x", map[string]string{"p256dh": "abc", "auth": keys["auth"]}),
		"short auth":      sub("https://web.push.apple.com/x", map[string]string{"p256dh": keys["p256dh"], "auth": "abc"}),
	} {
		if c := post("/push/subscribe", s, ""); c != 400 {
			t.Errorf("%s: %d, want 400", name, c)
		}
	}
	if c := post("/push/subscribe", sub("https://web.push.apple.com/QGv", keys), "https://evil.example"); c != 403 {
		t.Errorf("from another website: %d", c)
	}
	for _, ok := range []string{"https://web.push.apple.com/QGv", "https://updates.push.services.mozilla.com/wpush/v2/x", "https://wns2-bn3p.notify.windows.com/w/?token=x"} {
		if c := post("/push/subscribe", sub(ok, keys), ""); c != 204 {
			t.Errorf("%s: %d", ok, c)
		}
	}
	post("/push/unsubscribe", map[string]string{"endpoint": fcm}, "")
	if n := a.store.PushCount(); n != 3 {
		t.Errorf("after one unsubscribes: %d", n)
	}

	var k struct{ Key string }
	json.NewDecoder(do(t, h, nil, "GET", "/push/key", "", nil).Body).Decode(&k)
	if raw, err := decodeKey(k.Key); err != nil || len(raw) != 65 {
		t.Errorf("public key = %q", k.Key)
	}
	snap, _ := a.store.Snapshot()
	if strings.Contains(string(snap), "push_key") || strings.Contains(string(snap), "push_subs") {
		t.Error("the backup download shouldn't have the push key or the phones")
	}
	for _, p := range []string{"/calendar", "/calendar/ride/" + seriesKey(1, nextWeekday(a, 2))} {
		if !strings.Contains(do(t, h, nil, "GET", p, "", nil).Body.String(), `class="btn btn-quiet push-btn" type="button" hidden`) {
			t.Errorf("%s should have the (hidden) Ride alerts button", p)
		}
	}
}

// A leader cancelling a ride sends every phone with alerts on a message it
// can read (with the note), once; putting it back says so; "on" sends
// nothing; a phone the push service says is gone is forgotten.
func TestRideAlerts(t *testing.T) {
	a, h, _ := rideApp(t)
	a.store.SetClubPIN("8356")
	phone, gone := newFakePhone(t, http.StatusCreated), newFakePhone(t, http.StatusGone)
	a.store.AddPushSub(phone.sub())
	a.store.AddPushSub(gone.sub())
	o, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 1).Format("2006-01-02"), Time: "08:00", Name: "Coffee Spin", Leaders: []Leader{{Name: "Jane Doe"}}})
	status := func(st, note string) {
		ct, body := form(url.Values{"leader_name": {"Jane Doe"}, "pin": {"8356"}, "status": {st}, "note": {note}})
		do(t, h, nil, "POST", "/calendar/ride/"+o.Key+"/status", ct, body)
		a.push.wg.Wait()
	}

	status(statusOn, "Lights")
	if n := len(phone.messages()); n != 0 {
		t.Errorf("marking a ride on sent %d alerts", n)
	}
	status(statusCancel, "Rain")
	msgs := phone.messages()
	if len(msgs) != 1 || msgs[0].Title != "Cancelled: Coffee Spin" || !strings.HasSuffix(msgs[0].Body, "8:00 AM · Rain") || msgs[0].URL != "/calendar/ride/"+o.Key || msgs[0].Tag != o.Key {
		t.Fatalf("cancel alert: %+v", msgs)
	}
	hd := phone.hdr[0]
	if hd.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(hd.Get("Authorization"), "vapid t=") || hd.Get("Urgency") != "high" || hd.Get("Topic") != o.Key || hd.Get("TTL") == "" {
		t.Errorf("headers: %v", hd)
	}
	if a.store.PushCount() != 1 {
		t.Errorf("the gone phone should be forgotten: %d left", a.store.PushCount())
	}
	status(statusCancel, "Rain and wind")
	if n := len(phone.messages()); n != 1 {
		t.Errorf("a second cancel sent another alert: %d", n)
	}
	status(statusOpen, "")
	if msgs := phone.messages(); len(msgs) != 2 || msgs[1].Title != "Back on: Coffee Spin" {
		t.Errorf("put back: %+v", msgs)
	}

	// No alerts for a ride already gone by, or one waiting for approval.
	past, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, -2).Format("2006-01-02"), Name: "Old Ride"})
	a.alertRide(past, statusCancel, false)
	held, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 2).Format("2006-01-02"), Name: "Waiting", Held: true})
	a.alertRide(held, statusCancel, false)
	a.push.wg.Wait()
	if n := len(phone.messages()); n != 2 {
		t.Errorf("alerts for a past or waiting ride: %d", n)
	}
}

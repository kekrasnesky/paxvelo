package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func postJSON(h http.Handler, path string, v any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(v)
	r := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A phone picks Ride changes and Club news; only a phone the site has can be
// changed, and a phone from before topics gets both.
func TestPushTopics(t *testing.T) {
	a, h, _ := testApp(t)
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	keys := map[string]string{"p256dh": b64.EncodeToString(priv.PublicKey().Bytes()), "auth": b64.EncodeToString(make([]byte, 16))}
	ep := "https://fcm.googleapis.com/fcm/send/topics1"
	if w := postJSON(h, "/push/subscribe", map[string]any{"endpoint": ep, "keys": keys}); w.Code != 204 {
		t.Fatalf("subscribe = %d", w.Code)
	}
	topics := func(v map[string]any) (int, []string) {
		w := postJSON(h, "/push/topics", v)
		var out struct{ Topics []string }
		json.NewDecoder(w.Body).Decode(&out)
		return w.Code, out.Topics
	}
	if c, got := topics(map[string]any{"endpoint": ep}); c != 200 || strings.Join(got, ",") != "changes,news" {
		t.Errorf("a new phone gets both: %d %v", c, got)
	}
	if c, got := topics(map[string]any{"endpoint": ep, "topics": []string{"news", "bogus"}}); c != 200 || strings.Join(got, ",") != "news" {
		t.Errorf("news only: %d %v", c, got)
	}
	if _, ch, n := a.store.PushCounts(); ch != 0 || n != 1 {
		t.Errorf("counts: changes %d, news %d", ch, n)
	}
	// Subscribing again (same phone, keys refreshed) keeps the choice.
	postJSON(h, "/push/subscribe", map[string]any{"endpoint": ep, "keys": keys})
	if _, got := topics(map[string]any{"endpoint": ep}); strings.Join(got, ",") != "news" {
		t.Errorf("subscribing again lost the choice: %v", got)
	}
	// The push service gave the phone a new address (sw.js): the choice moves.
	ep2 := "https://fcm.googleapis.com/fcm/send/topics2"
	postJSON(h, "/push/subscribe", map[string]any{"endpoint": ep2, "keys": keys, "old": ep})
	if _, got := topics(map[string]any{"endpoint": ep2}); strings.Join(got, ",") != "news" || a.store.PushCount() != 1 {
		t.Errorf("new address: %v, %d phones", got, a.store.PushCount())
	}
	if c, _ := topics(map[string]any{"endpoint": ep, "topics": []string{"changes"}}); c != 404 {
		t.Errorf("the old address: %d, want 404", c)
	}
	if c, _ := topics(map[string]any{"endpoint": "https://fcm.googleapis.com/fcm/send/nobody", "topics": []string{}}); c != 404 {
		t.Errorf("a phone the site doesn't have: %d, want 404", c)
	}
	if c, _ := topics(map[string]any{"topics": []string{}}); c != 404 {
		t.Errorf("no address: %d, want 404", c)
	}
	r := httptest.NewRequest("POST", "/push/topics", strings.NewReader(`{"endpoint":"`+ep2+`","topics":[]}`))
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("from another website: %d", w.Code)
	}
	// Turning both off is kept (and not read back as "both").
	topics(map[string]any{"endpoint": ep2, "topics": []string{}})
	if _, got := topics(map[string]any{"endpoint": ep2}); len(got) != 0 {
		t.Errorf("both off: %v", got)
	}
	// A phone kept before topics existed gets both.
	var old PushSub
	json.Unmarshal([]byte(`{"endpoint":"x","p256dh":"y","auth":"z","added":"2026-11-01T00:00:00Z"}`), &old)
	if !old.Wants(topicChanges) || !old.Wants(topicNews) {
		t.Error("a phone from before topics should get both")
	}
}

// Ride changes go only to phones with Ride changes on; the board's alerts
// only to phones with Club news on.
func TestAlertTopics(t *testing.T) {
	a, h, _ := rideApp(t)
	both, changes, news := newFakePhone(t, http.StatusCreated), newFakePhone(t, http.StatusCreated), newFakePhone(t, http.StatusCreated)
	a.store.AddPushSub(both.sub(), nil, "")
	a.store.AddPushSub(changes.sub(), []string{topicChanges}, "")
	a.store.AddPushSub(news.sub(), []string{topicNews}, "")

	o, _ := a.store.AddRide(CalRide{Date: a.today().AddDate(0, 0, 1).Format("2006-01-02"), Time: "08:00", Name: "Coffee Spin"})
	a.alertRide(o, statusCancel, false)
	a.push.wg.Wait()
	if len(both.messages()) != 1 || len(changes.messages()) != 1 || len(news.messages()) != 0 {
		t.Errorf("ride change reached both %d, changes %d, news %d", len(both.messages()), len(changes.messages()), len(news.messages()))
	}

	_, board, _ := adminLevels(t, a)
	ct, body := form(url.Values{"title": {"Club meeting tonight"}, "body": {"7 pm at the library"}, "pick": {"/calendar"}})
	w := do(t, h, board, "POST", "/admin/email/alerts", ct, body)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "n=2") {
		t.Fatalf("send: %d %s %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	a.push.wg.Wait()
	if len(both.messages()) != 2 || len(changes.messages()) != 1 || len(news.messages()) != 1 {
		t.Errorf("club news reached both %d, changes %d, news %d", len(both.messages()), len(changes.messages()), len(news.messages()))
	}
	m := news.messages()[0]
	if m.Title != "Club meeting tonight" || m.Body != "7 pm at the library" || m.URL != "/calendar" || !strings.HasPrefix(m.Tag, "news-") {
		t.Errorf("message: %+v", m)
	}
	sent := a.store.PushAlerts()
	if len(sent) != 1 || sent[0].By != "Bea Board" || sent[0].Phones != 2 || sent[0].Reached != 2 || !sent[0].Done {
		t.Errorf("log: %+v", sent)
	}
	page := do(t, h, board, "GET", "/admin/email/alerts", "", nil).Body.String()
	for _, s := range []string{"Club meeting tonight", "2 of 2", "Send to 2 phones", "Bea Board"} {
		if !strings.Contains(page, s) {
			t.Errorf("the page should show %q", s)
		}
	}
	auto := do(t, h, board, "GET", "/admin/email/auto", "", nil).Body.String()
	if !strings.Contains(auto, "3 phones with alerts on · 2 Ride changes · 2 Club news") {
		t.Error("Automations should count phones per topic")
	}
}

// At most alertsPerDay alerts a day, counted on the server; one more is refused
// with a plain error and nothing is sent.
func TestAlertDailyCap(t *testing.T) {
	a, h, _ := testApp(t)
	phone := newFakePhone(t, http.StatusCreated)
	a.store.AddPushSub(phone.sub(), nil, "")
	_, board, _ := adminLevels(t, a)
	send := func(title string) *httptest.ResponseRecorder {
		ct, body := form(url.Values{"title": {title}})
		w := do(t, h, board, "POST", "/admin/email/alerts", ct, body)
		a.push.wg.Wait()
		return w
	}
	for i := range alertsPerDay {
		if w := send("Alert " + string(rune('A'+i))); w.Code != http.StatusSeeOther {
			t.Fatalf("alert %d: %d", i+1, w.Code)
		}
	}
	w := send("One too many")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "5 alerts have gone out today") {
		t.Errorf("fourth: %d", w.Code)
	}
	if n := len(phone.messages()); n != alertsPerDay {
		t.Errorf("phone got %d alerts", n)
	}
	// Yesterday's don't count.
	a.store.mu.Lock()
	for i := range a.store.d.PushAlerts {
		a.store.d.PushAlerts[i].At = a.store.d.PushAlerts[i].At.Add(-24 * time.Hour)
	}
	a.store.mu.Unlock()
	if w := send("Next day"); w.Code != http.StatusSeeOther {
		t.Errorf("a new day: %d", w.Code)
	}
	// With no phone on Club news nothing goes and nothing is counted.
	a.store.PushTopics(phone.sub().Endpoint, []string{topicChanges})
	if w := send("Nobody"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "No phones have Club news on") {
		t.Errorf("no phones: %d", w.Code)
	}
}

// Send an alert is for the board only.
func TestAlertBoardOnly(t *testing.T) {
	a, h, _ := testApp(t)
	a.store.AddPushSub(newFakePhone(t, http.StatusCreated).sub(), nil, "")
	web, board, _ := adminLevels(t, a)
	if loc := do(t, h, web, "GET", "/admin/email/alerts", "", nil).Header().Get("Location"); loc != "/admin?err=board-only" {
		t.Errorf("website admin opening the page went to %q", loc)
	}
	ct, body := form(url.Values{"title": {"Hi"}})
	if loc := do(t, h, web, "POST", "/admin/email/alerts", ct, body).Header().Get("Location"); loc != "/admin?err=board-only" {
		t.Errorf("website admin sending went to %q", loc)
	}
	if len(a.store.PushAlerts()) != 0 {
		t.Error("a website admin's alert was logged")
	}
	if w := do(t, h, board, "GET", "/admin/email/alerts", "", nil); w.Code != http.StatusOK {
		t.Errorf("board: %d", w.Code)
	}
	if strings.Contains(do(t, h, web, "GET", "/admin/email", "", nil).Body.String(), `href="/admin/email/alerts"`) {
		t.Error("website admins shouldn't see Send an alert in the menu")
	}
}

// An alert opens a page on this site, never another website; the form
// refuses anything else.
func TestAlertLink(t *testing.T) {
	site := "https://www.paxvelo.club"
	for in, want := range map[string]string{
		"":                             "",
		"/calendar":                    "/calendar",
		"/calendar/ride/r12":           "/calendar/ride/r12",
		"/rsvp/3?x=1#where":            "/rsvp/3?x=1#where",
		" /membership ":                "/membership",
		site + "/kit":                  "/kit",
		"HTTPS://WWW.PAXVELO.CLUB/kit": "/kit",
	} {
		if got, ok := alertLink(in, site); !ok || got != want {
			t.Errorf("%q = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{
		"https://evil.example/x", "http://www.paxvelo.club/x", "//evil.example/x", "/\\evil.example", "javascript:alert(1)",
		"calendar", "www.paxvelo.club/x", site + ".evil.example/x", site + "@evil.example/", "/a b", "/x\ny", "/x\x00", "mailto:a@b.c",
		"/" + strings.Repeat("a", 400),
	} {
		if got, ok := alertLink(bad, site); ok {
			t.Errorf("%q was let through as %q", bad, got)
		}
	}

	a, h, _ := testApp(t)
	phone := newFakePhone(t, http.StatusCreated)
	a.store.AddPushSub(phone.sub(), nil, "")
	_, board, _ := adminLevels(t, a)
	ct, body := form(url.Values{"title": {"Look"}, "pick": {"other"}, "path": {"https://evil.example/"}})
	w := do(t, h, board, "POST", "/admin/email/alerts", ct, body)
	a.push.wg.Wait()
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "a page on this site") || len(phone.messages()) != 0 {
		t.Errorf("outside link: %d, %d sent", w.Code, len(phone.messages()))
	}
	ct, body = form(url.Values{"title": {strings.Repeat("x", 61)}})
	if w := do(t, h, board, "POST", "/admin/email/alerts", ct, body); w.Code != http.StatusBadRequest {
		t.Errorf("long title: %d", w.Code)
	}
	ct, body = form(url.Values{"title": {"Hi"}, "body": {strings.Repeat("y", 181)}})
	if w := do(t, h, board, "POST", "/admin/email/alerts", ct, body); w.Code != http.StatusBadRequest {
		t.Errorf("long message: %d", w.Code)
	}
}

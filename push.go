package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Ride alerts: a phone notification when a leader or an admin cancels a ride
// coming up, or puts it back on. Anyone turns them on with the Ride alerts
// button on the calendar (on an iPhone, once the site is on the home
// screen). Nothing ties a phone to a member: the site keeps only the
// address its browser's push service gave it and the keys to encrypt for
// it (pushcrypto.go), and how many there are.

// PushSub is one phone (one browser) with ride alerts on.
type PushSub struct {
	Endpoint string    `json:"endpoint"`
	P256dh   string    `json:"p256dh"`
	Auth     string    `json:"auth"`
	Added    time.Time `json:"added"`
}

const maxPushSubs = 5000

var errTooManyPush = errors.New("too many phones have ride alerts on")

// pushHostOK says a host is one of the browsers' push services. A
// subscription naming any other address is refused, so the site never posts
// to a place a visitor chose.
func pushHostOK(host string) bool {
	switch host {
	case "fcm.googleapis.com", "android.googleapis.com":
		return true
	}
	for _, s := range []string{".push.apple.com", ".push.services.mozilla.com", ".notify.windows.com"} {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

func pushEndpointOK(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" && pushHostOK(u.Hostname()) && len(endpoint) <= 1024
}

// ---- Store ----

func (s *Store) PushPublicKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, err := parseVAPIDKey(s.d.PushKey)
	if err != nil {
		return ""
	}
	pub, _ := vapidPublic(k)
	return pub
}

func (s *Store) pushKey() (string, []PushSub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.PushKey, append([]PushSub(nil), s.d.PushSubs...)
}

func (s *Store) PushCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.d.PushSubs)
}

// AddPushSub keeps a phone's subscription, or updates its keys.
func (s *Store) AddPushSub(p PushSub) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.d.PushSubs {
		if x.Endpoint == p.Endpoint {
			if x.P256dh == p.P256dh && x.Auth == p.Auth {
				return nil
			}
			s.d.PushSubs[i].P256dh, s.d.PushSubs[i].Auth = p.P256dh, p.Auth
			return s.save()
		}
	}
	if len(s.d.PushSubs) >= maxPushSubs {
		return errTooManyPush
	}
	p.Added = time.Now().UTC()
	s.d.PushSubs = append(s.d.PushSubs, p)
	return s.save()
}

// RemovePushSubs drops phones by their address.
func (s *Store) RemovePushSubs(endpoints ...string) {
	if len(endpoints) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	gone := map[string]bool{}
	for _, e := range endpoints {
		gone[e] = true
	}
	kept := s.d.PushSubs[:0]
	for _, x := range s.d.PushSubs {
		if !gone[x.Endpoint] {
			kept = append(kept, x)
		}
	}
	s.d.PushSubs = kept
	if err := s.save(); err != nil {
		log.Printf("saving ride alerts: %v", err)
	}
}

// ---- Turning alerts on and off (site.js) ----

// pushKeyJSON is the key the browser subscribes with.
func (a *App) pushKeyJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"key": a.store.PushPublicKey()})
}

// pushSubscribe keeps the subscription the browser made.
func (a *App) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	if !a.limit.Allow("push:"+clientIP(r), 30, time.Hour) {
		http.Error(w, "Too many tries. Wait a bit.", http.StatusTooManyRequests)
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil || !pushEndpointOK(in.Endpoint) {
		http.Error(w, "That isn’t a phone’s push address.", http.StatusBadRequest)
		return
	}
	if k, err := decodeKey(in.Keys.P256dh); err != nil || len(k) != 65 || k[0] != 4 {
		http.Error(w, "Bad key.", http.StatusBadRequest)
		return
	}
	if k, err := decodeKey(in.Keys.Auth); err != nil || len(k) != 16 {
		http.Error(w, "Bad key.", http.StatusBadRequest)
		return
	}
	if err := a.store.AddPushSub(PushSub{Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth}); err != nil {
		http.Error(w, "Ride alerts are full right now.", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pushUnsubscribe forgets a phone.
func (a *App) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err == nil && in.Endpoint != "" {
		a.store.RemovePushSubs(in.Endpoint)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Sending ----

// pushMsg is what the service worker shows (sw.js).
type pushMsg struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"` // a newer alert with the same tag replaces it
}

// pushSender sends alerts. Client is for tests; nil is a client with a
// timeout.
type pushSender struct {
	Client *http.Client
	wg     sync.WaitGroup
}

func (p *pushSender) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

var topicRE = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// pushAll sends a message to every phone with alerts on, in the background.
// ttl is how long the push services keep trying a phone that's off.
func (a *App) pushAll(m pushMsg, ttl time.Duration) {
	a.push.wg.Add(1)
	go func() {
		defer a.push.wg.Done()
		sent, total, gone := a.pushNow(m, ttl)
		if total > 0 {
			log.Printf("ride alert %q: sent to %d of %d phones, %d gone", m.Title, sent, total, gone)
		}
	}()
}

// pushNow sends it and waits: how many went, out of how many, and how many
// phones the push services said are gone (they're forgotten).
func (a *App) pushNow(m pushMsg, ttl time.Duration) (sent, total, gone int) {
	keyS, subs := a.store.pushKey()
	key, err := parseVAPIDKey(keyS)
	if err != nil || len(subs) == 0 {
		return 0, len(subs), 0
	}
	body, _ := json.Marshal(m)
	ttlS := strconv.Itoa(max(60, int(ttl.Seconds())))
	topic := topicRE.ReplaceAllString(m.Tag, "")
	if len(topic) > 32 {
		topic = topic[:32]
	}
	var (
		mu      sync.Mutex
		dropped []string
		wg      sync.WaitGroup
		slots   = make(chan struct{}, 4)
		client  = a.push.client()
	)
	for _, sub := range subs {
		wg.Add(1)
		slots <- struct{}{}
		go func(sub PushSub) {
			defer func() { <-slots; wg.Done() }()
			code, err := sendPush(client, key, a.cfg.SiteURL, sub, body, ttlS, topic)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && code >= 200 && code < 300:
				sent++
			case code == http.StatusNotFound || code == http.StatusGone:
				dropped = append(dropped, sub.Endpoint)
			case err != nil:
				log.Printf("ride alert to %s: %v", hostOf(sub.Endpoint), err)
			default:
				log.Printf("ride alert to %s: %d", hostOf(sub.Endpoint), code)
			}
		}(sub)
	}
	wg.Wait()
	a.store.RemovePushSubs(dropped...)
	return sent, len(subs), len(dropped)
}

func hostOf(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil {
		return u.Host
	}
	return "?"
}

// sendPush posts one encrypted message to one phone's push service.
func sendPush(c *http.Client, k *ecdsa.PrivateKey, subject string, sub PushSub, body []byte, ttl, topic string) (int, error) {
	enc, err := encryptPush(body, sub.P256dh, sub.Auth)
	if err != nil {
		return 0, err
	}
	auth, err := vapidAuth(k, sub.Endpoint, subject, time.Now())
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest("POST", sub.Endpoint, bytes.NewReader(enc))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", ttl)
	req.Header.Set("Urgency", "high")
	if topic != "" {
		req.Header.Set("Topic", topic)
	}
	res, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	res.Body.Close()
	return res.StatusCode, nil
}

// ---- What sends them (ridestatus.go) ----

// alertRide sends a ride alert for a cancellation or a ride put back on: a
// ride on the calendar today or later, not one waiting for approval.
func (a *App) alertRide(o RideOcc, status string, wasCancelled bool) {
	if o.Held || o.Date < a.today().Format("2006-01-02") {
		return
	}
	when := o.ShortDate()
	if o.Time != "" {
		when += ", " + o.TimeLabel()
	}
	m := pushMsg{URL: "/calendar/ride/" + o.Key, Tag: o.Key, Body: when}
	switch {
	case status == statusCancel && !wasCancelled:
		m.Title = "Cancelled: " + o.Name
		if o.StatusNote != "" {
			m.Body += " · " + o.StatusNote
		}
	case status != statusCancel && wasCancelled:
		m.Title = "Back on: " + o.Name
	default:
		return
	}
	a.pushAll(m, a.alertTTL(o))
}

// alertTTL keeps an alert waiting for a phone that's off until the ride
// starts (at least an hour, at most two days).
func (a *App) alertTTL(o RideOcc) time.Duration {
	at, err := time.ParseInLocation("2006-01-02 15:04", o.Date+" "+or(o.Time, "23:59"), a.cfg.Location)
	if err != nil {
		return 24 * time.Hour
	}
	return min(max(time.Until(at), time.Hour), 48*time.Hour)
}

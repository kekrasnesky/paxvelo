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
	"slices"
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

// PushSub is one phone (one browser) with ride alerts on. Each phone picks
// what it gets: ride changes (a ride cancelled or put back on) and club news
// (alerts an admin sends, pushalerts.go). Both start on; the fields say what
// was turned off, so phones from before topics get both.
type PushSub struct {
	Endpoint  string    `json:"endpoint"`
	P256dh    string    `json:"p256dh"`
	Auth      string    `json:"auth"`
	Added     time.Time `json:"added"`
	NoChanges bool      `json:"no_changes,omitempty"`
	NoNews    bool      `json:"no_news,omitempty"`
}

// The topics a phone can choose.
const (
	topicChanges = "changes" // a ride cancelled or put back on
	topicNews    = "news"    // an alert an admin sends
)

// Wants says whether the phone gets a topic.
func (p PushSub) Wants(topic string) bool {
	switch topic {
	case topicChanges:
		return !p.NoChanges
	case topicNews:
		return !p.NoNews
	}
	return false
}

// Topics are the topics it gets, for the bell's switches.
func (p PushSub) Topics() []string {
	out := []string{}
	for _, t := range []string{topicChanges, topicNews} {
		if p.Wants(t) {
			out = append(out, t)
		}
	}
	return out
}

func (p *PushSub) setTopics(topics []string) {
	p.NoChanges, p.NoNews = !slices.Contains(topics, topicChanges), !slices.Contains(topics, topicNews)
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

// pushKey is the site's key and the phones that want topic ("" = every
// phone).
func (s *Store) pushKey(topic string) (string, []PushSub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var subs []PushSub
	for _, p := range s.d.PushSubs {
		if topic == "" || p.Wants(topic) {
			subs = append(subs, p)
		}
	}
	return s.d.PushKey, subs
}

func (s *Store) PushCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.d.PushSubs)
}

// PushCounts is how many phones have alerts on, and how many get each topic.
func (s *Store) PushCounts() (all, changes, news int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.d.PushSubs {
		if p.Wants(topicChanges) {
			changes++
		}
		if p.Wants(topicNews) {
			news++
		}
	}
	return len(s.d.PushSubs), changes, news
}

// AddPushSub keeps a phone's subscription, or updates its keys. topics nil
// keeps what the phone had (both, for a new one). old is the address the
// phone had before its push service changed it (sw.js): that phone's
// choices carry over, and the old address is forgotten.
func (s *Store) AddPushSub(p PushSub, topics []string, old string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old != "" && old != p.Endpoint {
		if i := slices.IndexFunc(s.d.PushSubs, func(x PushSub) bool { return x.Endpoint == old }); i >= 0 {
			p.NoChanges, p.NoNews = s.d.PushSubs[i].NoChanges, s.d.PushSubs[i].NoNews
			s.d.PushSubs = slices.Delete(s.d.PushSubs, i, i+1)
			topics = nil
			if j := slices.IndexFunc(s.d.PushSubs, func(x PushSub) bool { return x.Endpoint == p.Endpoint }); j >= 0 {
				s.d.PushSubs[j].NoChanges, s.d.PushSubs[j].NoNews = p.NoChanges, p.NoNews
			}
		}
	}
	if topics != nil {
		p.setTopics(topics)
	}
	for i, x := range s.d.PushSubs {
		if x.Endpoint == p.Endpoint {
			if topics == nil {
				p.NoChanges, p.NoNews = x.NoChanges, x.NoNews
			}
			p.Added = x.Added
			if x == p {
				return nil
			}
			s.d.PushSubs[i] = p
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

var errNoPushSub = errors.New("no phone with that address")

// PushTopics changes what a phone gets (topics nil: only reads it). Only a
// phone already kept can be changed.
func (s *Store) PushTopics(endpoint string, topics []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.d.PushSubs, func(x PushSub) bool { return x.Endpoint == endpoint })
	if endpoint == "" || i < 0 {
		return nil, errNoPushSub
	}
	p := &s.d.PushSubs[i]
	if topics != nil {
		was := *p
		p.setTopics(topics)
		if *p != was {
			if err := s.save(); err != nil {
				return nil, err
			}
		}
	}
	return p.Topics(), nil
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
		Topics []string `json:"topics"` // optional: what it gets
		Old    string   `json:"old"`    // optional: its address before (sw.js)
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
	sub := PushSub{Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth}
	if in.Old != "" && a.store.MoveAdminPush(in.Old, sub) { // the admin app's phone got a new address (adminpush.go)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.store.AddPushSub(sub, in.Topics, in.Old); err != nil {
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

// pushTopicsJSON reads or changes what a phone gets: {endpoint, topics}
// changes it, {endpoint} alone reads it. The answer is {topics}; a phone the
// site doesn't have is a 404 (site.js then turns alerts off on it).
func (a *App) pushTopicsJSON(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	if !a.limit.Allow("push-topics:"+clientIP(r), 60, time.Hour) {
		http.Error(w, "Too many tries. Wait a bit.", http.StatusTooManyRequests)
		return
	}
	var in struct {
		Endpoint string   `json:"endpoint"`
		Topics   []string `json:"topics"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	topics, err := a.store.PushTopics(in.Endpoint, in.Topics)
	if errors.Is(err, errNoPushSub) {
		http.Error(w, "This phone doesn’t have alerts on.", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "Couldn’t save that.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string][]string{"topics": topics})
}

// ---- Sending ----

// pushMsg is what the service worker shows (sw.js).
type pushMsg struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"` // a newer alert with the same tag replaces it
	Icon  string `json:"icon,omitempty"` // a picture on this site; empty = the club's app icon
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

// pushAll sends a message to every phone that wants topic, in the
// background. ttl is how long the push services keep trying a phone that's
// off. done, if not nil, gets the counts when it's finished.
func (a *App) pushAll(m pushMsg, ttl time.Duration, topic string, done func(sent, total int)) {
	a.push.wg.Add(1)
	go func() {
		defer a.push.wg.Done()
		sent, total, gone := a.pushNow(m, ttl, topic)
		if total > 0 {
			log.Printf("alert %q (%s): sent to %d of %d phones, %d gone", m.Title, topic, sent, total, gone)
		}
		if done != nil {
			done(sent, total)
		}
	}()
}

// pushNow sends it to the phones that want topic and waits: how many went,
// out of how many, and how many phones the push services said are gone
// (they're forgotten).
func (a *App) pushNow(m pushMsg, ttl time.Duration, topic string) (sent, total, gone int) {
	keyS, subs := a.store.pushKey(topic)
	sent, dropped := a.pushSend(keyS, subs, m, ttl)
	a.store.RemovePushSubs(dropped...)
	return sent, len(subs), len(dropped)
}

// pushSend sends m to subs, 4 at a time, and waits: how many went, and the
// phones the push services said are gone.
func (a *App) pushSend(keyS string, subs []PushSub, m pushMsg, ttl time.Duration) (sent int, dropped []string) {
	key, err := parseVAPIDKey(keyS)
	if err != nil || len(subs) == 0 {
		return 0, nil
	}
	body, _ := json.Marshal(m)
	ttlS := strconv.Itoa(max(60, int(ttl.Seconds())))
	tag := topicRE.ReplaceAllString(m.Tag, "") // the push service's Topic header
	if len(tag) > 32 {
		tag = tag[:32]
	}
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		slots  = make(chan struct{}, 4)
		client = a.push.client()
	)
	for _, sub := range subs {
		wg.Add(1)
		slots <- struct{}{}
		go func(sub PushSub) {
			defer func() { <-slots; wg.Done() }()
			code, err := sendPush(client, key, a.cfg.SiteURL, sub, body, ttlS, tag)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && code >= 200 && code < 300:
				sent++
			case code == http.StatusNotFound || code == http.StatusGone:
				dropped = append(dropped, sub.Endpoint)
			case err != nil:
				log.Printf("alert to %s: %v", hostOf(sub.Endpoint), err)
			default:
				log.Printf("alert to %s: %d", hostOf(sub.Endpoint), code)
			}
		}(sub)
	}
	wg.Wait()
	return sent, dropped
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

// alertRide sends the phones that want ride changes an alert for a cancellation or a ride put back on: a
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
	a.pushAll(m, a.alertTTL(o), topicChanges, nil)
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

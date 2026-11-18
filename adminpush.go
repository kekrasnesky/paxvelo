package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// Admin alerts: notifications on an admin's own phone from the admin app
// (adminapp.go), for what needs them: a spending request waiting for their
// answer or their own request answered, a ride added on the website waiting
// for approval, a loaner bike request, photos to review, and (board) a new
// member. Unlike the members' ride alerts each phone is tied to the admin
// who turned it on, so alerts follow their level and their role in a
// request; removing an admin forgets their phones. The admin app uses its
// own service worker (/admin/sw.js, the same script as /sw.js) so its
// subscription is separate from the club app's ride alerts in the same
// browser.

// AdminPushSub is one admin's phone with admin alerts on. Off lists the
// kinds they turned off; new kinds start on.
type AdminPushSub struct {
	PushSub
	Admin int      `json:"admin"`
	Off   []string `json:"off,omitempty"`
}

// adminAlertKinds are the kinds of admin alert, in the switches' order.
var adminAlertKinds = []struct{ Key, Label string }{
	{"requests", "Spending requests"},
	{"rides", "Rides and loaner bikes"},
	{"photos", "Photos to review"},
	{"members", "New members"},
}

// boardOnlyKinds are kinds only board members get.
var boardOnlyKinds = map[string]bool{"members": true}

func (p AdminPushSub) Wants(kind string) bool { return !slices.Contains(p.Off, kind) }

const maxAdminPush = 200

// ---- Store ----

// AddAdminPush keeps an admin's phone, or updates its keys. A phone that was
// someone else's (a shared phone, a new login) becomes this admin's.
func (s *Store) AddAdminPush(admin int, p PushSub) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.d.AdminPush {
		if x.Endpoint == p.Endpoint {
			p.Added = x.Added
			if x.Admin == admin && x.PushSub == p {
				return nil
			}
			off := x.Off
			if x.Admin != admin {
				off = nil
			}
			s.d.AdminPush[i] = AdminPushSub{PushSub: p, Admin: admin, Off: off}
			return s.save()
		}
	}
	if len(s.d.AdminPush) >= maxAdminPush {
		return errTooManyPush
	}
	p.Added = time.Now().UTC()
	s.d.AdminPush = append(s.d.AdminPush, AdminPushSub{PushSub: p, Admin: admin})
	return s.save()
}

// AdminPushKinds reads (kinds nil) or sets what an admin's phone gets. Only
// that admin's own phone can be read or changed.
func (s *Store) AdminPushKinds(admin int, endpoint string, on []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.d.AdminPush, func(x AdminPushSub) bool { return x.Endpoint == endpoint && x.Admin == admin })
	if endpoint == "" || i < 0 {
		return nil, errNoPushSub
	}
	p := &s.d.AdminPush[i]
	if on != nil {
		var off []string
		for _, k := range adminAlertKinds {
			if !slices.Contains(on, k.Key) {
				off = append(off, k.Key)
			}
		}
		if !slices.Equal(off, p.Off) {
			p.Off = off
			if err := s.save(); err != nil {
				return nil, err
			}
		}
	}
	out := []string{}
	for _, k := range adminAlertKinds {
		if p.Wants(k.Key) {
			out = append(out, k.Key)
		}
	}
	return out, nil
}

// RemoveAdminPush forgets phones by address; admin > 0 only forgets that
// admin's.
func (s *Store) RemoveAdminPush(admin int, endpoints ...string) {
	if len(endpoints) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.d.AdminPush)
	s.d.AdminPush = slices.DeleteFunc(s.d.AdminPush, func(x AdminPushSub) bool {
		return slices.Contains(endpoints, x.Endpoint) && (admin == 0 || x.Admin == admin)
	})
	if len(s.d.AdminPush) != n {
		if err := s.save(); err != nil {
			log.Printf("saving admin alerts: %v", err)
		}
	}
}

// MoveAdminPush gives an admin's phone its new address after the push
// service changed it (sw.js posts to /push/subscribe with the old one). It
// reports whether old was an admin's phone.
func (s *Store) MoveAdminPush(old string, p PushSub) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.d.AdminPush, func(x AdminPushSub) bool { return x.Endpoint == old })
	if i < 0 {
		return false
	}
	p.Added = s.d.AdminPush[i].Added
	s.d.AdminPush[i].PushSub = p
	if err := s.save(); err != nil {
		log.Printf("saving admin alerts: %v", err)
	}
	return true
}

// adminPushFor is the site's key and every admin phone, for sending.
func (s *Store) adminPushFor() (string, []AdminPushSub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.PushKey, slices.Clone(s.d.AdminPush)
}

// AdminPushCount is how many phones an admin has with alerts on.
func (s *Store) AdminPushCount(admin int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, x := range s.d.AdminPush {
		if x.Admin == admin {
			n++
		}
	}
	return n
}

// ---- Turning them on and off (the admin app's Alerts panel) ----

// adminPushIn is what the panel posts.
type adminPushIn struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Kinds []string `json:"kinds"`
}

func readAdminPush(w http.ResponseWriter, r *http.Request) (adminPushIn, bool) {
	var in adminPushIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return in, false
	}
	return in, true
}

// pushAdmin is the logged-in admin's own account; the setup password has
// none to tie a phone to.
func (a *App) pushAdmin(w http.ResponseWriter, r *http.Request) (AdminUser, bool) {
	u, ok := a.currentAdmin(r)
	if !ok || u.ID == 0 {
		http.Error(w, "Log in with your own account to turn on alerts.", http.StatusForbidden)
		return u, false
	}
	return u, true
}

// adminPushSubscribe ties the phone the browser subscribed to this admin.
func (a *App) adminPushSubscribe(w http.ResponseWriter, r *http.Request) {
	u, ok := a.pushAdmin(w, r)
	if !ok {
		return
	}
	in, ok := readAdminPush(w, r)
	if !ok {
		return
	}
	if !pushEndpointOK(in.Endpoint) {
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
	if err := a.store.AddAdminPush(u.ID, PushSub{Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth}); err != nil {
		http.Error(w, "Couldn’t turn alerts on.", http.StatusServiceUnavailable)
		return
	}
	a.adminPushKindsJSON(w, r, u, in.Endpoint, nil)
}

// adminPushUnsubscribe forgets this admin's phone.
func (a *App) adminPushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	u, ok := a.pushAdmin(w, r)
	if !ok {
		return
	}
	if in, ok := readAdminPush(w, r); ok && in.Endpoint != "" {
		a.store.RemoveAdminPush(u.ID, in.Endpoint)
		w.WriteHeader(http.StatusNoContent)
	}
}

// adminPushKinds reads ({endpoint}) or sets ({endpoint, kinds}) what this
// admin's phone gets. A phone that isn't theirs is a 404, and the panel then
// shows alerts as off.
func (a *App) adminPushKinds(w http.ResponseWriter, r *http.Request) {
	u, ok := a.pushAdmin(w, r)
	if !ok {
		return
	}
	if in, ok := readAdminPush(w, r); ok {
		a.adminPushKindsJSON(w, r, u, in.Endpoint, in.Kinds)
	}
}

func (a *App) adminPushKindsJSON(w http.ResponseWriter, r *http.Request, u AdminUser, endpoint string, kinds []string) {
	on, err := a.store.AdminPushKinds(u.ID, endpoint, kinds)
	if errors.Is(err, errNoPushSub) {
		http.Error(w, "This phone doesn’t have alerts on.", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "Couldn’t save that.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string][]string{"kinds": on})
}

// adminServiceWorker is /admin/sw.js: the site's service worker again, at an
// address whose scope is the admin portal, so the admin app has its own
// registration and its own push subscription.
func (a *App) adminServiceWorker(w http.ResponseWriter, r *http.Request) {
	a.serviceWorker(w, r)
}

// alertKinds are the switches this admin sees.
func (a *App) alertKinds(r *http.Request) []struct{ Key, Label string } {
	board := a.isBoard(r)
	var out []struct{ Key, Label string }
	for _, k := range adminAlertKinds {
		if board || !boardOnlyKinds[k.Key] {
			out = append(out, k)
		}
	}
	return out
}

// ---- Sending ----

// alertAdmins sends m, in the background, to the phones of admins who want
// kind and for whom who (nil = every admin) is true. Board-only kinds skip
// website admins.
func (a *App) alertAdmins(kind string, m pushMsg, ttl time.Duration, who func(AdminUser) bool) {
	keyS, all := a.store.adminPushFor()
	if len(all) == 0 {
		return
	}
	admins := map[int]AdminUser{}
	for _, u := range a.store.Admins() {
		admins[u.ID] = u
	}
	var subs []PushSub
	for _, p := range all {
		u, ok := admins[p.Admin]
		if !ok || !p.Wants(kind) || (who != nil && !who(u)) || (boardOnlyKinds[kind] && a.levelOf(u) < levelBoard) {
			continue
		}
		subs = append(subs, p.PushSub)
	}
	if len(subs) == 0 {
		return
	}
	m.Icon = "/static/img/admin-192.png"
	a.push.wg.Add(1)
	go func() {
		defer a.push.wg.Done()
		sent, gone := a.pushSend(keyS, subs, m, ttl)
		a.store.RemoveAdminPush(0, gone...)
		log.Printf("admin alert %q (%s): sent to %d of %d phones", m.Title, kind, sent, len(subs))
	}()
}

// alertActions are an alert's two buttons: do it (posting to the same
// route as the Needs you button, so the same level and answer checks apply)
// or look first (open URL).
func alertActions(label, post, done string) []pushAction {
	return []pushAction{{Action: "do", Title: label, Post: post, Done: done}, {Action: "open", Title: "Look first"}}
}

// alertHeldRide: a ride added on the website without the club PIN.
func (a *App) alertHeldRide(o RideOcc, by string) {
	a.alertAdmins("rides", pushMsg{
		Title: "Waiting for approval: " + o.Name,
		Body:  fmt.Sprintf("%s · added by %s", o.ShortDate(), or(by, "someone")),
		URL:   "/admin/rides/" + o.Key, Tag: "held-" + o.Key,
		Actions: alertActions("Approve", "/admin/rides/"+o.Key+"/approve", "Approved: "+o.Name),
	}, 48*time.Hour, nil)
}

// alertLoaner: someone asked for a loaner bike.
func (a *App) alertLoaner(q LoanerRequest) {
	id := strconv.Itoa(q.ID)
	a.alertAdmins("rides", pushMsg{
		Title: "Loaner bike request",
		Body:  fmt.Sprintf("%s · %s, %s · %s", or(q.Rider, q.Name), q.RideName, shortDate(q.RideDate), q.BikeLabel),
		URL:   "/admin/loaners#q" + id, Tag: "loaner-" + id,
		Actions: alertActions("Confirm", "/admin/loaners/"+id+"/confirm", "Confirmed: "+q.BikeLabel+" for "+q.RiderName()),
	}, 48*time.Hour, nil)
}

// alertPhotos: photos sent in for review. One alert, replaced by the next.
func (a *App) alertPhotos(n int, from string) {
	waiting := a.store.NewSubmitted()
	if waiting == 0 {
		return
	}
	body := fmt.Sprintf("%d from %s", n, or(from, "a member"))
	if waiting > n {
		body += fmt.Sprintf(" · %d waiting", waiting)
	}
	a.alertAdmins("photos", pushMsg{Title: "Photos to review", Body: body, URL: "/admin/submitted", Tag: "photos"}, 24*time.Hour, nil)
}

// alertRequestSent: a spending request waiting for these admins' answer.
func (a *App) alertRequestSent(q SpendRequest) {
	a.alertAdmins("requests", pushMsg{
		Title: "Needs your OK: " + money(q.Cents),
		Body:  fmt.Sprintf("%s · %s", q.By, q.What),
		URL:   fmt.Sprintf("%s#r%d", requestsPath, q.ID), Tag: "req-" + strconv.Itoa(q.ID),
		Actions: alertActions("Approve", fmt.Sprintf("%s/%d/approve", requestsPath, q.ID), "Approved: "+q.What),
	}, 72*time.Hour, func(u AdminUser) bool { return a.wantsAnswer(q, u) })
}

// alertRequestSettled: the sender's request was approved or not.
func (a *App) alertRequestSettled(q SpendRequest) {
	if q.ByID == 0 {
		return
	}
	m := pushMsg{Title: "Approved: " + q.What, Body: money(q.Cents), URL: fmt.Sprintf("%s#r%d", requestsPath, q.ID), Tag: "req-" + strconv.Itoa(q.ID)}
	if no := q.Rejection(); no != nil {
		m.Title, m.Body = "Not approved: "+q.What, no.Reason
	}
	a.alertAdmins("requests", m, 72*time.Hour, func(u AdminUser) bool { return u.ID == q.ByID })
}

// alertSignup: someone joined on the website (board).
func (a *App) alertSignup(m Member) {
	a.alertAdmins("members", pushMsg{
		Title: "New member: " + m.Name,
		Body:  m.PlanName(),
		URL:   fmt.Sprintf("/admin/members/%d", m.ID), Tag: "member-" + strconv.Itoa(m.ID),
	}, 72*time.Hour, nil)
}

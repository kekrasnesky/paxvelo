package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// "I'm in": anyone says they're coming to a group ride from its page, with
// their name, and the club's RSVPs move off Strava. No account: the
// browser keeps a random code in a cookie (pv_rider) so the page knows it's
// them, offers "Can't make it", and fills in their name next time. An email
// is optional, for word if the ride is cancelled or put back on. "First club
// ride" tells the club inbox and points them to the ride waiver.
//
// The ride page shows who's going as first name and last initial; emails are
// for admins only, and are dropped 30 days after the ride.

// RideRider is one person who said they're coming.
type RideRider struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Email  string    `json:"email,omitempty"`
	Group  string    `json:"group,omitempty"`
	First  bool      `json:"first,omitempty"` // their first club ride
	Device string    `json:"device,omitempty"`
	Added  time.Time `json:"added"`
}

// ShortName is how the public page names them: "Sam R.".
func (r RideRider) ShortName() string { return shortName(r.Name) }

const (
	maxRiders     = 100
	riderCookie   = "pv_rider"
	riderKeepDays = 30 // emails and device codes go this long after the ride
)

var (
	errRideFull = errors.New("this ride is full on the website")
	errClosed   = errors.New("this ride isn't taking riders")
)

// riderDevice is this browser's code, hashed with the site's secret so the
// data file never holds the cookie itself; "" when it has none.
func (a *App) riderDevice(r *http.Request) string {
	c, err := r.Cookie(riderCookie)
	if err != nil || len(c.Value) != 32 {
		return ""
	}
	return a.deviceHash(c.Value)
}

func (a *App) deviceHash(code string) string {
	sum := sha256.Sum256([]byte("rider\x00" + a.store.Secret() + "\x00" + code))
	return hex.EncodeToString(sum[:12])
}

// ensureRiderCookie gives the browser a code if it has none, and returns
// its hash.
func (a *App) ensureRiderCookie(w http.ResponseWriter, r *http.Request) string {
	if d := a.riderDevice(r); d != "" {
		return d
	}
	code := randHex(16)
	http.SetCookie(w, &http.Cookie{Name: riderCookie, Value: code, Path: "/calendar", MaxAge: 400 * 24 * 3600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: strings.HasPrefix(a.cfg.SiteURL, "https://")})
	return a.deviceHash(code)
}

// riderOf is this device's entry on a ride, if any.
func riderOf(c CalRide, device string) *RideRider {
	if device == "" {
		return nil
	}
	for i := range c.Riders {
		if c.Riders[i].Device == device {
			r := c.Riders[i]
			return &r
		}
	}
	return nil
}

// RiderByDevice is the newest entry this device made on any ride, to fill
// in the form.
func (s *Store) RiderByDevice(device string) (RideRider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best RideRider
	for _, c := range s.d.CalRides {
		for _, r := range c.Riders {
			if device != "" && r.Device == device && r.Added.After(best.Added) {
				best = r
			}
		}
	}
	return best, best.Name != ""
}

// canJoin says the ride takes "I'm in": a group ride on the calendar, not
// cancelled, not over.
func (a *App) canJoin(c CalRide) bool {
	return c.IsRide() && !c.Cancelled && !c.Held && c.Date >= a.today().Format("2006-01-02")
}

// imIn adds (or updates) this browser's rider on a ride.
func (a *App) imIn(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	if r.FormValue("website") != "" || !a.validFormToken(r.FormValue("ft")) {
		rideBack(w, r, key, "err=expired#going")
		return
	}
	name := cleanName(r.FormValue("rider_name"))
	if name == "" {
		rideBack(w, r, key, "err=ridername#going")
		return
	}
	email := ""
	if e := strings.TrimSpace(r.FormValue("email")); e != "" {
		var ok bool
		if email, ok = cleanEmail(e); !ok {
			rideBack(w, r, key, "err=email#going")
			return
		}
	}
	if !a.limit.Allow("imin:"+clientIP(r), 20, time.Hour) {
		rideBack(w, r, key, "err=limit#going")
		return
	}
	device := a.ensureRiderCookie(w, r)
	group, first := strings.TrimSpace(r.FormValue("group")), r.FormValue("first") == "1"
	isNew := false
	o, err := a.store.UpdateRide(key, func(c *CalRide) error {
		if !a.canJoin(*c) {
			return errClosed
		}
		if group != "" && !c.hasGroup(group) {
			group = ""
		}
		for i := range c.Riders {
			if c.Riders[i].Device == device {
				c.Riders[i].Name, c.Riders[i].Email, c.Riders[i].Group, c.Riders[i].First = name, email, group, first
				return nil
			}
		}
		if len(c.Riders) >= maxRiders {
			return errRideFull
		}
		isNew = true
		c.Riders = append(c.Riders, RideRider{ID: randHex(6), Name: name, Email: email, Group: group, First: first, Device: device, Added: time.Now().UTC()})
		return nil
	})
	switch {
	case errors.Is(err, errRideFull):
		rideBack(w, r, key, "err=full#going")
		return
	case errors.Is(err, errClosed):
		rideBack(w, r, key, "err=noride#going")
		return
	case err != nil:
		rideBack(w, r, key, "err="+or(rideErrCode(err), "noride")+"#going")
		return
	}
	if isNew && first {
		body := fmt.Sprintf("**%s** said they're coming to %s, their first club ride.", name, rideWhen(o))
		if group != "" {
			body += "\n\n- **Group:** " + group
		}
		if email != "" {
			body += "\n- **Email:** " + email
		}
		a.notifyRide("First ride: "+name+" on "+o.Name+", "+shortDate(o.Date), body, o, "")
	}
	if first {
		rideBack(w, r, key, "ok=infirst#going")
		return
	}
	rideBack(w, r, key, "ok=in#going")
}

// imOut takes this browser's rider off a ride.
func (a *App) imOut(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !sameOrigin(r) {
		http.Error(w, "Blocked a request from another website.", http.StatusForbidden)
		return
	}
	device := a.riderDevice(r)
	if device == "" {
		rideBack(w, r, key, "#going")
		return
	}
	a.store.UpdateRide(key, func(c *CalRide) error {
		c.Riders = dropRiders(c.Riders, func(x RideRider) bool { return x.Device == device })
		return nil
	})
	rideBack(w, r, key, "ok=out#going")
}

// removeRiderAdmin takes someone off a ride from its admin page.
func (a *App) removeRiderAdmin(w http.ResponseWriter, r *http.Request) {
	key, id := r.PathValue("key"), r.PathValue("id")
	a.store.UpdateRide(key, func(c *CalRide) error {
		c.Riders = dropRiders(c.Riders, func(x RideRider) bool { return x.ID == id })
		return nil
	})
	back(w, r, "/admin/rides/"+key+"?ok=rider-removed#riders")
}

func dropRiders(rs []RideRider, gone func(RideRider) bool) []RideRider {
	var out []RideRider
	for _, x := range rs {
		if !gone(x) {
			out = append(out, x)
		}
	}
	return out
}

// mailRiders tells the riders who left an email that their ride was
// cancelled or put back on (ridestatus.go).
func (a *App) mailRiders(o RideOcc, status string, wasCancelled bool) {
	if o.Date < a.today().Format("2006-01-02") {
		return
	}
	var subject, intro string
	switch {
	case status == statusCancel && !wasCancelled:
		subject, intro = "Cancelled: "+o.Name+", "+shortDate(o.Date), fmt.Sprintf("**%s** on %s is cancelled.", o.Name, rideWhen(o))
		if o.StatusNote != "" {
			intro += "\n\n" + o.StatusNote
		}
		intro += fmt.Sprintf("\n\n[Find another ride](%s/calendar)", a.cfg.SiteURL)
	case status != statusCancel && wasCancelled:
		subject, intro = "Back on: "+o.Name+", "+shortDate(o.Date), fmt.Sprintf("**%s** on %s is back on.\n\n[Ride details](%s)", o.Name, rideWhen(o), a.rideLink(o.Key))
	default:
		return
	}
	for _, rd := range o.Riders {
		if rd.Email == "" {
			continue
		}
		rd := rd
		body := "Hi " + strings.Fields(rd.Name + " there")[0] + ",\n\n" + intro + "\n\nYou got this because you said you're coming on the PaxVelo website."
		a.mailer.Go(func() {
			if err := a.mailer.SendDirect(mailRider, rd.Name, rd.Email, subject, body); err != nil {
				log.Printf("rider email %s: %v", rd.Email, err)
			}
		})
	}
}

// tidyRiders drops riders' emails and device codes from rides more than
// riderKeepDays ago; names and counts stay. At startup.
func (s *Store) tidyRiders(now time.Time) {
	cut := now.AddDate(0, 0, -riderKeepDays).Format("2006-01-02")
	for _, c := range s.d.CalRides {
		if c.Date >= cut {
			continue
		}
		for i := range c.Riders {
			c.Riders[i].Email, c.Riders[i].Device = "", ""
		}
	}
}

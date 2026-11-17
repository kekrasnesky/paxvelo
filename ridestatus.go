package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// A ride's status on the day: its leader (with the club PIN) or an admin
// says it's on, or cancels it, with a short note ("Rain", "Meet at the back
// lot"). The ride page shows it in a banner, and the home page's next rides,
// the calendar and the calendar feed show it too. A cancelled ride stays in
// the next rides until the day after, so riders see it's off instead of
// finding it gone.

const maxStatusNote = 120

// Ride statuses as the forms send them.
const (
	statusOn     = "on"
	statusCancel = "cancel"
	statusOpen   = "open" // neither: back on the calendar, not yet confirmed
)

// cleanStatusNote keeps a note to one short line.
func cleanStatusNote(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for utf8.RuneCountInString(s) > maxStatusNote {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimSpace(s)
}

// setStatus marks a ride on, cancelled, or neither.
func (c *CalRide) setStatus(status, note, by string, at time.Time) {
	switch status {
	case statusOn:
		c.On, c.Cancelled = true, false
	case statusCancel:
		c.On, c.Cancelled = false, true
	default:
		c.On, c.Cancelled, note, by, at = false, false, "", "", time.Time{}
	}
	c.StatusNote, c.StatusBy, c.StatusAt = cleanStatusNote(note), by, at.UTC()
	if at.IsZero() {
		c.StatusAt = time.Time{}
	}
}

// statusOf reads the status a form asks for: status=on|cancel|open, or the
// older cancel=1|0.
func statusOf(r *http.Request) string {
	switch s := r.FormValue("status"); s {
	case statusOn, statusCancel, statusOpen:
		return s
	}
	if r.FormValue("cancel") == "1" {
		return statusCancel
	}
	return statusOpen
}

// IsOn reports whether someone said the ride is going ahead, until its day
// is over.
func (o RideOcc) IsOn() bool {
	return o.On && !o.Cancelled && o.Date >= time.Now().In(clubTZ).Format("2006-01-02")
}

// cancelledLine is "Cancelled", with the note when there is one.
func (o RideOcc) cancelledLine() string {
	if o.StatusNote != "" {
		return "Cancelled · " + o.StatusNote
	}
	return "Cancelled"
}

// StatusWhen is when the status was set, in club time: "6:40 AM" on the
// ride's day, else "Mon 8:15 PM".
func (o RideOcc) StatusWhen() string {
	if o.StatusAt.IsZero() {
		return ""
	}
	t := o.StatusAt.In(clubTZ)
	if t.Format("2006-01-02") == o.Date {
		return t.Format("3:04 PM")
	}
	return t.Format("Mon 3:04 PM")
}

// StatusLine is the note, who set it and when, for the short lists:
// "Rain · Pat, 6:40 AM".
func (o RideOcc) StatusLine() string {
	var who []string
	if o.StatusBy != "" {
		who = append(who, o.StatusBy)
	}
	if w := o.StatusWhen(); w != "" {
		who = append(who, w)
	}
	parts := []string{}
	if o.StatusNote != "" {
		parts = append(parts, o.StatusNote)
	}
	if len(who) > 0 {
		parts = append(parts, strings.Join(who, ", "))
	}
	return strings.Join(parts, " · ")
}

// applyStatus sets a ride's status for a leader or an admin and emails the
// club about a cancellation. check, when set, can refuse the change.
func (a *App) applyStatus(key, status, note, by string, check func(c *CalRide) error) (RideOcc, error) {
	was, _ := a.store.Ride(key)
	o, err := a.store.UpdateRide(key, func(c *CalRide) error {
		if check != nil {
			if err := check(c); err != nil {
				return err
			}
		}
		c.setStatus(status, note, by, time.Now())
		return nil
	})
	if err != nil {
		return o, err
	}
	a.alertRide(o, status, was.Cancelled)  // phones with ride alerts on (push.go)
	a.mailRiders(o, status, was.Cancelled) // riders who said "I'm in" with an email (imin.go)
	if status == statusCancel && !was.Cancelled {
		body := fmt.Sprintf("%s cancelled %s on %s.", by, o.Name, rideWhen(o))
		if o.StatusNote != "" {
			body += "\n\nNote: " + o.StatusNote
		}
		a.notifyRide(fmt.Sprintf("%s on %s is cancelled", o.Name, shortDate(o.Date)), body, o, "")
	}
	return o, nil
}

// statusCode is the message a status change answers with.
func statusCode(status string) string {
	switch status {
	case statusOn:
		return "on"
	case statusCancel:
		return "cancelled"
	}
	return "restored"
}

// rideStatus is a leader's On / Cancel / Put back, with their name and the
// club PIN. /cancel (the older form) comes here too.
func (a *App) rideStatus(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	name, code, ok := a.leaderPost(w, r)
	if !ok {
		return
	}
	if code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	status, today := statusOf(r), a.today().Format("2006-01-02")
	_, err := a.applyStatus(key, status, r.FormValue("note"), name, func(c *CalRide) error {
		if _, leading := c.groupOf(name); !leading {
			return errNotYours
		}
		if c.Date < today {
			return errPastRide
		}
		return nil
	})
	if code := rideErrCode(err); code != "" {
		rideBack(w, r, key, "err="+code)
		return
	}
	rideBack(w, r, key, "ok="+statusCode(status))
}

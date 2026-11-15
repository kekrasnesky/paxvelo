package main

import (
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The club calendar as one address a phone's calendar subscribes to
// (/calendar/feed.ics): every ride and event from two weeks back to a year
// ahead, read again by the phone every so often, so new rides, changed times
// and cancellations show up by themselves. Nothing is kept about who
// subscribes. /calendar/subscribe has the buttons that add it to each app.

const (
	feedBack  = 14  // days of past rides the feed keeps
	feedAhead = 365 // days ahead
)

// rideEvent is one ride as the lines of a calendar event. A ride without a
// start time is an all-day event; one with a time lasts as long as its
// distance needs (RideOcc.Length).
//
// In the feed a cancelled ride stays, named "Cancelled: …": Google Calendar
// drops events marked STATUS:CANCELLED, and a ride that just disappears tells
// the rider less than one that says it's off.
func (a *App) rideEvent(o RideOcc, feed bool) []string {
	day, err := time.ParseInLocation("2006-01-02", o.Date, a.cfg.Location)
	if err != nil {
		return nil
	}
	link := a.cfg.SiteURL + "/calendar/ride/" + o.Key
	lines := []string{"BEGIN:VEVENT", "UID:" + o.Key + "@" + a.siteHost(), "DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z")}
	if t, err := time.ParseInLocation("2006-01-02 15:04", o.Date+" "+o.Time, a.cfg.Location); err == nil {
		lines = append(lines, "DTSTART:"+t.UTC().Format("20060102T150405Z"), "DTEND:"+t.Add(o.Length()).UTC().Format("20060102T150405Z"))
	} else {
		lines = append(lines, "DTSTART;VALUE=DATE:"+day.Format("20060102"), "DTEND;VALUE=DATE:"+day.AddDate(0, 0, 1).Format("20060102"))
	}
	name := o.Name
	if feed && o.Cancelled {
		name = "Cancelled: " + name
	}
	lines = append(lines, "SUMMARY:"+icsText(name))
	if p := o.Place; p != "" {
		lines = append(lines, "LOCATION:"+icsText(p))
	}
	desc := link
	if d := strings.TrimSpace(o.Details); d != "" {
		desc = d + "\n\n" + link
	}
	lines = append(lines, "DESCRIPTION:"+icsText(desc), "URL:"+link)
	if o.Cancelled && !feed {
		lines = append(lines, "STATUS:CANCELLED")
	}
	return append(lines, "END:VEVENT")
}

// siteHost is the site's address without the https://.
func (a *App) siteHost() string {
	return strings.TrimPrefix(strings.TrimPrefix(a.cfg.SiteURL, "https://"), "http://")
}

// writeICS sends calendar lines, folded the way calendar apps want them.
func writeICS(w http.ResponseWriter, file string, lines []string) {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(icsFold(l))
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="`+file+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(b.String()))
}

// calendarFeed is the whole club calendar for calendar apps to subscribe to.
// Like the calendar page, a weekly ride's past dates only show when the ride
// happened (it was on Strava, or someone signed up for it or changed it).
func (a *App) calendarFeed(w http.ResponseWriter, r *http.Request) {
	today := a.today()
	hidePast, todayS := a.store.HasHistory(), today.Format("2006-01-02")
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//PaxVelo//Club calendar//EN", "CALSCALE:GREGORIAN", "METHOD:PUBLISH",
		"X-WR-CALNAME:" + icsText(siteName), "NAME:" + icsText(siteName), "X-WR-CALDESC:" + icsText(siteName+" rides and events"),
		"X-WR-TIMEZONE:" + a.cfg.Location.String(), "REFRESH-INTERVAL;VALUE=DURATION:PT1H", "X-PUBLISHED-TTL:PT1H"}
	for _, o := range a.store.Rides(today.AddDate(0, 0, -feedBack), today.AddDate(0, 0, feedAhead)) {
		if hidePast && o.ID == 0 && o.SeriesID != 0 && o.Date < todayS {
			continue
		}
		lines = append(lines, a.rideEvent(o, true)...)
	}
	writeICS(w, "paxvelo.ics", append(lines, "END:VCALENDAR"))
}

// subApp is one calendar app's button on the Subscribe page.
type subApp struct {
	Label  string
	URL    template.URL // webcal:// isn't a link the templates know
	NewTab bool
}

// subView is the Subscribe page: the visitor's own app first.
type subView struct {
	Apps []subApp
	Feed string // the address, for any other app
}

// feedURL is the feed's address; webcal is the same address the way calendar
// apps open it to subscribe.
func (a *App) feedURL() string { return a.cfg.SiteURL + "/calendar/feed.ics" }

func (a *App) webcalURL() string { return "webcal://" + a.siteHost() + "/calendar/feed.ics" }

func (a *App) subscribeView(r *http.Request) *subView {
	apple := subApp{"Apple Calendar", template.URL(a.webcalURL()), false}
	google := subApp{"Google Calendar", template.URL("https://calendar.google.com/calendar/render?cid=" + url.QueryEscape(a.webcalURL())), true}
	outlook := subApp{"Outlook", template.URL("https://outlook.live.com/calendar/0/addfromweb?name=" + url.QueryEscape(siteName) + "&url=" + url.QueryEscape(a.feedURL())), true}
	v := &subView{Feed: a.feedURL(), Apps: []subApp{google, apple, outlook}}
	if appleDevice(r) {
		v.Apps = []subApp{apple, google, outlook}
	}
	return v
}

// subscribePage has a button for each calendar app. Emails link here
// ({calendar_link}), since email apps don't open webcal:// links.
func (a *App) subscribePage(w http.ResponseWriter, r *http.Request) {
	v := a.calBase(r)
	v.Sub = a.subscribeView(r)
	a.renderCal(w, http.StatusOK, "subscribe", "/calendar", "Subscribe to the Calendar", v)
}

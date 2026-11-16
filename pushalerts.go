package main

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Club news alerts: the board writes a phone notification (Email → Send an
// alert) and it goes to every phone with Club news on (push.go topics). A
// short log of what was sent is kept on the page; at most alertsPerDay a
// day, so the alerts stay worth having on.

// PushAlert is one alert an admin sent.
type PushAlert struct {
	ID      int       `json:"id"`
	At      time.Time `json:"at"`
	By      string    `json:"by"`
	Title   string    `json:"title"`
	Body    string    `json:"body,omitempty"`
	URL     string    `json:"url,omitempty"`
	Phones  int       `json:"phones"`  // phones with Club news on when it went
	Reached int       `json:"reached"` // phones the push services took it for
	Done    bool      `json:"done,omitempty"`
}

const (
	alertsPerDay   = 5
	alertsKept     = 50
	alertTitleMax  = 60
	alertBodyMax   = 180
	alertLinkMax   = 300
	alertKeepHours = 24 // how long the push services keep trying a phone that's off
)

var (
	errAlertCap    = errors.New(strconv.Itoa(alertsPerDay) + " alerts have gone out today. The next can go tomorrow.")
	errAlertPhones = errors.New("No phones have Club news on yet.")
)

// AddPushAlert logs an alert about to go, unless alertsPerDay already went
// today (club time).
func (s *Store) AddPushAlert(al PushAlert, loc *time.Location) (PushAlert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := al.At.In(loc).Format("2006-01-02")
	n := 0
	for _, x := range s.d.PushAlerts {
		if x.At.In(loc).Format("2006-01-02") == day {
			n++
		}
	}
	if n >= alertsPerDay {
		return al, errAlertCap
	}
	s.d.NextPushAlert++
	al.ID = s.d.NextPushAlert
	s.d.PushAlerts = append(s.d.PushAlerts, al)
	if len(s.d.PushAlerts) > alertsKept {
		s.d.PushAlerts = slices.Clone(s.d.PushAlerts[len(s.d.PushAlerts)-alertsKept:])
	}
	return al, s.save()
}

// FinishPushAlert records how many phones an alert reached.
func (s *Store) FinishPushAlert(id, reached int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.d.PushAlerts {
		if s.d.PushAlerts[i].ID == id {
			s.d.PushAlerts[i].Reached, s.d.PushAlerts[i].Done = reached, true
			s.save()
			return
		}
	}
}

// PushAlerts are the alerts sent, newest first.
func (s *Store) PushAlerts() []PushAlert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.d.PushAlerts)
	slices.Reverse(out)
	return out
}

// alertLink checks an alert's link: empty, or a page on this site. A full
// address on this site becomes its path; any other site is refused.
func alertLink(raw, siteURL string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	if base := strings.TrimRight(siteURL, "/"); base != "" && len(raw) > len(base) && strings.EqualFold(raw[:len(base)], base) && raw[len(base)] == '/' {
		raw = raw[len(base):]
	}
	if len(raw) > alertLinkMax || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\ \t\r\n") {
		return "", false
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") {
		return "", false
	}
	return u.String(), true
}

// ---- Email → Send an alert ----

type alertsView struct {
	Title, Body, Pick, Path string // the form, kept on an error
	News, Phones, Today     int    // phones with Club news on; with alerts on; alerts sent today
	Left, PerDay            int    // alerts left today; allowed a day
	Choices                 []eventChoice
	Sent                    []PushAlert
}

// alertChoices are the pages an alert can open: the calendar, and the rides
// and events of the next five weeks.
func (a *App) alertChoices() []eventChoice {
	today := a.today()
	out := []eventChoice{{"/calendar", "Calendar"}}
	for _, o := range a.store.Rides(today, today.AddDate(0, 0, 35)) {
		t, _ := time.Parse("2006-01-02", o.Date)
		label := t.Format("Mon Jan 2") + " · " + o.Name
		if o.Time != "" {
			label = t.Format("Mon Jan 2") + " " + o.TimeLabel() + " · " + o.Name
		}
		if o.Cancelled {
			label += " (cancelled)"
		}
		out = append(out, eventChoice{"/calendar/ride/" + o.Key, label})
	}
	return out
}

func (a *App) alertsPage(w http.ResponseWriter, r *http.Request) {
	a.renderAlerts(w, r, http.StatusOK, &alertsView{}, "")
}

func (a *App) renderAlerts(w http.ResponseWriter, r *http.Request, status int, av *alertsView, errMsg string) {
	av.Phones, _, av.News = a.store.PushCounts()
	av.Choices = a.alertChoices()
	av.Sent = a.store.PushAlerts()
	day := time.Now().In(a.cfg.Location).Format("2006-01-02")
	for _, x := range av.Sent {
		if x.At.In(a.cfg.Location).Format("2006-01-02") == day {
			av.Today++
		}
	}
	av.Left, av.PerDay = max(0, alertsPerDay-av.Today), alertsPerDay
	v := adminView{Title: "Send an alert", Tab: "alerts", Alerts: av, Error: errMsg}
	if r.URL.Query().Get("ok") == "alert-sent" {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		v.Flash = "Sending to " + strconv.Itoa(n) + " " + plural(n, "phone", "phones") + "."
	}
	a.renderAdmin(w, r, status, "alerts", v)
}

// sendAlert sends the board's alert to every phone with Club news on.
func (a *App) sendAlert(w http.ResponseWriter, r *http.Request) {
	av := &alertsView{Title: strings.TrimSpace(r.FormValue("title")), Body: strings.TrimSpace(r.FormValue("body")),
		Pick: r.FormValue("pick"), Path: strings.TrimSpace(r.FormValue("path"))}
	fail := func(msg string) { a.renderAlerts(w, r, http.StatusBadRequest, av, msg) }
	av.Body = strings.Join(strings.Fields(av.Body), " ")
	link := av.Pick
	if link == "other" {
		link = av.Path
	}
	switch {
	case av.Title == "":
		fail("Give the alert a title.")
		return
	case utf8.RuneCountInString(av.Title) > alertTitleMax:
		fail("The title is over " + strconv.Itoa(alertTitleMax) + " characters.")
		return
	case utf8.RuneCountInString(av.Body) > alertBodyMax:
		fail("The message is over " + strconv.Itoa(alertBodyMax) + " characters.")
		return
	}
	link, ok := alertLink(link, a.cfg.SiteURL)
	if !ok {
		fail("The link has to be a page on this site, like /calendar.")
		return
	}
	_, _, news := a.store.PushCounts()
	if news == 0 {
		fail(errAlertPhones.Error())
		return
	}
	who, _ := a.adminName(r)
	al, err := a.store.AddPushAlert(PushAlert{At: time.Now().UTC(), By: who, Title: av.Title, Body: av.Body, URL: link, Phones: news}, a.cfg.Location)
	if errors.Is(err, errAlertCap) {
		a.renderAlerts(w, r, http.StatusTooManyRequests, av, err.Error())
		return
	} else if err != nil {
		fail("Couldn’t save the alert. Try again.")
		return
	}
	m := pushMsg{Title: al.Title, Body: al.Body, URL: or(al.URL, "/"), Tag: "news-" + strconv.Itoa(al.ID)}
	a.pushAll(m, alertKeepHours*time.Hour, topicNews, func(sent, _ int) { a.store.FinishPushAlert(al.ID, sent) })
	back(w, r, "/admin/email/alerts?ok=alert-sent&n="+strconv.Itoa(news))
}

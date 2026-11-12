package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Loaner bikes: the club's few bikes that first-time riders can borrow for
// one group ride. Admins keep the list on Rides → Loaner bikes (size, the
// rider heights it fits, what bike it is, ready or not). Every group ride on
// the calendar shows the ready bikes, with the ones already asked for marked
// Reserved. A rider picks one, gives their name and email, and signs
// the loaner waiver (ridewaivers.go) on the same page, until 8 pm the day
// before the ride. The bike is held while the request waits; the club
// (RIDES_EMAIL) is emailed and admin Home shows it. An admin confirms or
// declines it, and the rider is emailed either way. Cancelling a confirmed
// one frees the bike.

const (
	loanWaiting   = "waiting"
	loanConfirmed = "confirmed"
	loanDeclined  = "declined"
	loanCancelled = "cancelled"

	loanerCutoffHour = 20 // requests close at 8 pm the day before the ride
	maxLoanerAsks    = 5  // requests an hour from one address
)

// LoanerBike is one bike in the loaner fleet.
type LoanerBike struct {
	ID    int    `json:"id"`
	Size  string `json:"size"`           // like "XS" or "XL"
	Fits  string `json:"fits,omitempty"` // rider heights, like 5'0"–5'3"
	Bike  string `json:"bike,omitempty"` // what it is: "2017 Trek Domane, 47 cm, rim brakes"
	Ready bool   `json:"ready,omitempty"`
}

// Label names the bike in a sentence.
func (b LoanerBike) Label() string {
	s := b.Size
	if b.Bike != "" {
		s += ", " + b.Bike
	}
	return s
}

// LoanerRequest is a rider asking to borrow a bike for one ride.
type LoanerRequest struct {
	ID        int    `json:"id"`
	Bike      int    `json:"bike"`
	BikeLabel string `json:"bike_label"` // the bike as it was when they asked
	RideKey   string `json:"ride_key"`
	RideName  string `json:"ride_name"`
	RideDate  string `json:"ride_date"`
	RideTime  string `json:"ride_time,omitempty"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Rider     string `json:"rider,omitempty"`  // a rider under 18 their parent asked for
	Waiver    int    `json:"waiver,omitempty"` // the ride waiver they signed with it

	Status   string    `json:"status"`
	Asked    time.Time `json:"asked"`
	Answered time.Time `json:"answered,omitzero"`
	By       string    `json:"by,omitempty"`     // the admin who answered
	Reason   string    `json:"reason,omitempty"` // why it was declined or cancelled
	IP       string    `json:"ip,omitempty"`
}

// Holds says whether the request keeps the bike for its ride.
func (q LoanerRequest) Holds() bool { return q.Status == loanWaiting || q.Status == loanConfirmed }

// RiderName is who rides the bike.
func (q LoanerRequest) RiderName() string { return or(q.Rider, q.Name) }

// RideLabel is the ride's day, like "Wed, Nov 12".
func (q LoanerRequest) RideLabel() string {
	t, err := time.Parse(time.DateOnly, q.RideDate)
	if err != nil {
		return q.RideDate
	}
	s := t.Format("Mon, Jan 2")
	if q.RideTime != "" {
		s += ", " + timeLabel(q.RideTime)
	}
	return s
}

// LoanerFleet is the bikes and the requests for them, in the data file.
type LoanerFleet struct {
	Bikes       []*LoanerBike    `json:"bikes,omitempty"`
	NextBike    int              `json:"next_bike,omitempty"`
	Requests    []*LoanerRequest `json:"requests,omitempty"`
	NextRequest int              `json:"next_request,omitempty"`
	Seeded      bool             `json:"seeded,omitempty"`
}

// seedLoaners starts the fleet with four sizes, once: the two bikes the club
// has, and medium and large to fill in when they're ready.
func (s *Store) seedLoaners() {
	f := &s.d.Loaners
	if f.Seeded {
		return
	}
	f.Seeded = true
	for _, b := range []LoanerBike{
		{Size: "XS", Fits: "5'0\"–5'3\"", Bike: "2017 Trek Domane, 47 cm, rim brakes", Ready: true},
		{Size: "M", Fits: "5'4\"–5'8\""},
		{Size: "L", Fits: "5'9\"–5'11\""},
		{Size: "XL", Fits: "6'0\"–6'4\"", Bike: "2018 Cannondale Synapse, aluminum, 61 cm, disc brakes", Ready: true},
	} {
		f.NextBike++
		b.ID = f.NextBike
		f.Bikes = append(f.Bikes, &b)
	}
}

// LoanerBikes lists the fleet in its order.
func (s *Store) LoanerBikes() []LoanerBike {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []LoanerBike
	for _, b := range s.d.Loaners.Bikes {
		out = append(out, *b)
	}
	return out
}

// bikeChoice is a ready bike on a ride's page, and whether it's taken.
type bikeChoice struct {
	LoanerBike
	Taken bool
}

// LoanerChoices lists the ready bikes for a ride, marking those already held.
func (s *Store) LoanerChoices(rideKey string) []bikeChoice {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []bikeChoice
	for _, b := range s.d.Loaners.Bikes {
		if b.Ready {
			out = append(out, bikeChoice{LoanerBike: *b, Taken: s.bikeHeld(b.ID, rideKey)})
		}
	}
	return out
}

// bikeHeld says whether a request holds a bike for a ride. Callers hold s.mu.
func (s *Store) bikeHeld(bike int, rideKey string) bool {
	for _, q := range s.d.Loaners.Requests {
		if q.Bike == bike && q.RideKey == rideKey && q.Holds() {
			return true
		}
	}
	return false
}

var (
	errBikeTaken    = errors.New("that bike is already asked for on this ride")
	errBikeNotReady = errors.New("that bike isn't ready")
	errLoanAnswered = errors.New("that request was already answered")
)

// RequestLoaner holds a ready bike for a ride, if nobody else has.
func (s *Store) RequestLoaner(q LoanerRequest) (LoanerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var bike *LoanerBike
	for _, b := range s.d.Loaners.Bikes {
		if b.ID == q.Bike {
			bike = b
		}
	}
	if bike == nil || !bike.Ready {
		return LoanerRequest{}, errBikeNotReady
	}
	if s.bikeHeld(bike.ID, q.RideKey) {
		return LoanerRequest{}, errBikeTaken
	}
	s.d.Loaners.NextRequest++
	q.ID, q.BikeLabel, q.Status, q.Asked = s.d.Loaners.NextRequest, bike.Label(), loanWaiting, time.Now().UTC()
	s.d.Loaners.Requests = append(s.d.Loaners.Requests, &q)
	return q, s.save()
}

// AnswerLoaner confirms, declines or cancels a request. A waiting one can be
// confirmed or declined; a waiting or confirmed one can be cancelled.
func (s *Store) AnswerLoaner(id int, status, by, reason string) (LoanerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.d.Loaners.Requests {
		if q.ID != id {
			continue
		}
		ok := q.Status == loanWaiting && (status == loanConfirmed || status == loanDeclined) || q.Holds() && status == loanCancelled
		if !ok {
			return *q, errLoanAnswered
		}
		q.Status, q.By, q.Reason, q.Answered = status, by, reason, time.Now().UTC()
		return *q, s.save()
	}
	return LoanerRequest{}, fmt.Errorf("no loaner request %d", id)
}

// LoanerRequests lists every request, newest first.
func (s *Store) LoanerRequests() []LoanerRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []LoanerRequest
	for i := len(s.d.Loaners.Requests) - 1; i >= 0; i-- {
		out = append(out, *s.d.Loaners.Requests[i])
	}
	return out
}

// WaitingLoaners counts the requests waiting for an answer for rides from
// today on.
func (s *Store) WaitingLoaners(today string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, q := range s.d.Loaners.Requests {
		if q.Status == loanWaiting && q.RideDate >= today {
			n++
		}
	}
	return n
}

// SaveLoanerBike adds a bike (ID 0) or changes one.
func (s *Store) SaveLoanerBike(b LoanerBike) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := &s.d.Loaners
	if b.ID == 0 {
		f.NextBike++
		b.ID = f.NextBike
		f.Bikes = append(f.Bikes, &b)
		return s.save()
	}
	for _, old := range f.Bikes {
		if old.ID == b.ID {
			*old = b
			return s.save()
		}
	}
	return fmt.Errorf("no loaner bike %d", b.ID)
}

var errBikeBooked = errors.New("that bike is held for a ride coming up")

// RemoveLoanerBike takes a bike off the list, unless a request holds it for
// a ride from today on.
func (s *Store) RemoveLoanerBike(id int, today string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.d.Loaners.Requests {
		if q.Bike == id && q.Holds() && q.RideDate >= today {
			return errBikeBooked
		}
	}
	n := len(s.d.Loaners.Bikes)
	s.d.Loaners.Bikes = slices.DeleteFunc(s.d.Loaners.Bikes, func(b *LoanerBike) bool { return b.ID == id })
	if len(s.d.Loaners.Bikes) == n {
		return nil
	}
	return s.save()
}

// ---- On the calendar ----

// loanerOpen says whether a ride takes loaner requests: a group ride on the
// road, on the calendar, not cancelled, until 8 pm the day before.
func (a *App) loanerOpen(o RideOcc) bool {
	if !o.IsRide() || o.Cancelled || o.Held || o.IsVirtual() {
		return false
	}
	day, err := time.ParseInLocation(time.DateOnly, o.Date, a.cfg.Location)
	if err != nil {
		return false
	}
	return time.Now().Before(day.Add(-time.Duration(24-loanerCutoffHour) * time.Hour))
}

// loanerPath is a ride's request page.
func loanerPath(key string) string { return "/calendar/ride/" + key + "/loaner" }

// loanerForm is a ride's request page: the ready bikes, and the loaner
// waiver to sign with the request.
func (a *App) loanerForm(w http.ResponseWriter, r *http.Request) {
	o, ok := a.store.Ride(r.PathValue("key"))
	if !ok || o.Held {
		a.site.notFound(w, r)
		return
	}
	d := rideWaiverData{Bike: bikeLoaner, BikeID: r.URL.Query().Get("bike"), Done: r.URL.Query().Get("asked") != ""}
	a.renderLoaner(w, http.StatusOK, o, d)
}

func (a *App) renderLoaner(w http.ResponseWriter, status int, o RideOcc, d rideWaiverData) {
	d.Ride, d.Action, d.Bike = &o, loanerPath(o.Key), bikeLoaner
	d.LoanerOpen = a.loanerOpen(o)
	if d.LoanerOpen {
		d.Bikes = a.store.LoanerChoices(o.Key)
	}
	d.Token, d.Today = a.formToken(), a.signedDate()
	w.Header().Set("X-Robots-Tag", "noindex")
	a.site.render(w, status, "ride-waiver", "/calendar", "Borrow a Club Bike — "+siteName, d)
}

// askLoaner takes a request: the bike, and the loaner
// waiver signed with it.
func (a *App) askLoaner(w http.ResponseWriter, r *http.Request) {
	o, ok := a.store.Ride(r.PathValue("key"))
	if !ok || o.Held {
		a.site.notFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSignatureForm)
	d, signed, minor, bot := a.rideWaiverForm(r)
	d.BikeID = r.PostFormValue("bike_id")
	if bot {
		d.Done = true
		a.renderLoaner(w, http.StatusOK, o, d)
		return
	}
	bikeID, _ := strconv.Atoi(d.BikeID)
	var picked *bikeChoice
	for _, c := range a.store.LoanerChoices(o.Key) {
		if c.ID == bikeID {
			picked = &c
		}
	}
	if d.Message == "" {
		switch {
		case !a.loanerOpen(o):
			d.Message = "Requests for this ride are closed."
		case picked == nil:
			d.Message = "Pick a bike."
		case picked.Taken:
			d.Message = "That bike is reserved for this ride. Pick another."
		case !a.limit.Allow("loaner:"+clientIP(r), maxLoanerAsks, time.Hour):
			d.Message = "Too many requests from here this hour. Try again later."
		}
	}
	if d.Message != "" {
		a.renderLoaner(w, http.StatusBadRequest, o, d)
		return
	}
	waiver, _, err := a.store.SignRideWaiver(signed, a.site.waiver.forRide(true, minor), a.cfg.Location)
	if err != nil {
		log.Printf("loaner waiver %s: %v", signed.Email, err)
		d.Message = "Something went wrong on our end. Try again in a minute."
		a.renderLoaner(w, http.StatusInternalServerError, o, d)
		return
	}
	q, err := a.store.RequestLoaner(LoanerRequest{Bike: bikeID, RideKey: o.Key, RideName: o.Name, RideDate: o.Date, RideTime: o.Time,
		Name: signed.Name, Email: signed.Email, Rider: signed.Rider, Waiver: waiver.ID, IP: clientIP(r)})
	if err != nil {
		d.Message = "That bike is reserved for this ride. Pick another."
		if !errors.Is(err, errBikeTaken) {
			log.Printf("loaner request %s: %v", signed.Email, err)
			d.Message = "Something went wrong on our end. Try again in a minute."
		}
		a.renderLoaner(w, http.StatusConflict, o, d)
		return
	}
	a.notifyLoaner(q)
	http.Redirect(w, r, loanerPath(o.Key)+"?asked=1", http.StatusSeeOther)
}

// ---- Emails ----

func (a *App) rideLink(key string) string { return a.cfg.SiteURL + "/calendar/ride/" + key }

// notifyLoaner tells the club a bike was asked for.
func (a *App) notifyLoaner(q LoanerRequest) {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** asked to borrow a club bike.\n\n", q.RiderName())
	b.WriteString("- **Ride:** " + q.RideName + ", " + q.RideLabel() + "\n")
	b.WriteString("- **Bike:** " + q.BikeLabel + "\n")
	if q.Rider != "" {
		b.WriteString("- **Parent or guardian:** " + q.Name + "\n")
	}
	b.WriteString("- **Email:** " + q.Email + "\n")
	fmt.Fprintf(&b, "\n[Confirm or decline](%s/admin/loaners)", a.cfg.SiteURL)
	a.mailer.Notify("Loaner bike request: "+q.RideName+", "+q.RideLabel(), b.String())
}

// mailLoanerAnswer tells the rider what happened to their request.
func (a *App) mailLoanerAnswer(q LoanerRequest) {
	var subject string
	var b strings.Builder
	ride := fmt.Sprintf("**%s**, %s", q.RideName, q.RideLabel())
	switch q.Status {
	case loanConfirmed:
		subject = "Your loaner bike is confirmed: " + q.RideName + ", " + q.RideLabel()
		fmt.Fprintf(&b, "Your club bike is set for %s.\n\n- **Bike:** %s\n\nBring a helmet and water, and come a few minutes early so we can fit the bike to you.\n\n[Ride details](%s)", ride, q.BikeLabel, a.rideLink(q.RideKey))
	case loanDeclined:
		subject = "About your loaner bike request: " + q.RideName
		fmt.Fprintf(&b, "Sorry, we can’t lend a club bike for %s.", ride)
		if q.Reason != "" {
			b.WriteString("\n\n" + q.Reason)
		}
		fmt.Fprintf(&b, "\n\n[Find another ride](%s/calendar)", a.cfg.SiteURL)
	case loanCancelled:
		subject = "Loaner bike cancelled: " + q.RideName + ", " + q.RideLabel()
		fmt.Fprintf(&b, "The club bike for %s is cancelled.", ride)
		if q.Reason != "" {
			b.WriteString("\n\n" + q.Reason)
		}
		fmt.Fprintf(&b, "\n\n[Find another ride](%s/calendar)", a.cfg.SiteURL)
	default:
		return
	}
	a.mailer.Go(func() {
		if err := a.mailer.SendDirect(mailLoaner, q.Name, q.Email, subject, b.String()); err != nil {
			log.Printf("loaner email %s: %v", q.Email, err)
		}
	})
}

// ---- Admin ----

func (a *App) loanersPage(w http.ResponseWriter, r *http.Request) {
	today := a.today().Format(time.DateOnly)
	v := adminView{Title: "Loaner bikes", Tab: "loaners", LoanerBikes: a.store.LoanerBikes()}
	for _, q := range a.store.LoanerRequests() {
		switch {
		case q.RideDate >= today && q.Status == loanWaiting:
			v.LoanersWaiting = append(v.LoanersWaiting, q)
		case q.RideDate >= today && q.Status == loanConfirmed:
			v.LoanersComing = append(v.LoanersComing, q)
		case len(v.LoanersPast) < 30:
			v.LoanersPast = append(v.LoanersPast, q)
		}
	}
	// Soonest ride first.
	soonest := func(x, y LoanerRequest) int { return strings.Compare(x.RideDate+x.RideTime, y.RideDate+y.RideTime) }
	slices.SortStableFunc(v.LoanersWaiting, soonest)
	slices.SortStableFunc(v.LoanersComing, soonest)
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "loaners", v)
}

// answerLoaner is Confirm, Decline or Cancel on a request.
func (a *App) answerLoaner(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		by, _ := a.adminName(r)
		q, err := a.store.AnswerLoaner(id, status, or(by, "an admin"), cleanTitle(r.FormValue("reason"), 300))
		if err != nil {
			back(w, r, "/admin/loaners?err=loaner-answered")
			return
		}
		log.Printf("loaner request %d (%s, %s) %s by %s", q.ID, q.RiderName(), q.RideDate, status, by)
		a.mailLoanerAnswer(q)
		back(w, r, fmt.Sprintf("/admin/loaners?ok=loaner-%s#q%d", status, q.ID))
	}
}

// saveLoanerBike adds a bike or saves one's row.
func (a *App) saveLoanerBike(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.FormValue("id"))
	b := LoanerBike{ID: id, Size: cleanTitle(r.FormValue("size"), 20), Fits: cleanTitle(r.FormValue("fits"), 40),
		Bike: cleanTitle(r.FormValue("bike"), 120), Ready: r.FormValue("ready") != ""}
	if b.Size == "" {
		back(w, r, "/admin/loaners?err=bike-size#bikes")
		return
	}
	if err := a.store.SaveLoanerBike(b); err != nil {
		back(w, r, "/admin/loaners?err=save#bikes")
		return
	}
	back(w, r, "/admin/loaners?ok=saved#bikes")
}

func (a *App) removeLoanerBike(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if err := a.store.RemoveLoanerBike(id, a.today().Format(time.DateOnly)); err != nil {
		back(w, r, "/admin/loaners?err=bike-booked#bikes")
		return
	}
	back(w, r, "/admin/loaners?ok=bike-removed#bikes")
}

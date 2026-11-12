package main

import (
	"encoding/csv"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Ride waivers, for people at their first group ride who aren't members (yet).
// The public page (/ride-waiver, and /loaner, which picks the club bike) asks
// for a name and email, whether they ride a club loaner bike or their own, and
// a signature, drawn or typed. Their own bike: they sign the League waiver
// (web/waiver.html). A loaner: the loaner bike agreement for one ride
// (web/waiver-loaner.html) and then the League waiver. A parent or guardian
// signing for a rider under 18 adds the League's release for parents and
// guardians. Each signing keeps the exact text signed, in the store's waiver
// texts like the yearly waivers.
//
// They're tied to a member by email: when they sign, if the address is on the
// list, and otherwise as soon as someone with it is added (joins on the
// website, is imported or added by hand). The admin's Waivers page lists them
// under First rides, and a member's page shows theirs.

const (
	bikeLoaner = "loaner"
	bikeOwn    = "own"

	rideWaiverPath = "/ride-waiver"
	maxRideWaivers = 10 // signings an hour from one address
)

// RideWaiver is one signed ride waiver.
type RideWaiver struct {
	ID int `json:"id"`
	// The signer: the rider, or for a rider under 18, the parent or guardian.
	Name     string `json:"name"`
	Email    string `json:"email"`
	MemberID int    `json:"member_id,omitempty"` // the member with that email, once there is one
	Bike     string `json:"bike"`                // bikeLoaner or bikeOwn
	Minor    bool   `json:"minor,omitempty"`
	Rider    string `json:"rider,omitempty"` // the rider under 18
	RiderAge int    `json:"rider_age,omitempty"`

	Signature string    `json:"signature"` // the name they typed to sign
	Drawing   []byte    `json:"drawing,omitempty"`
	Signed    time.Time `json:"signed"`
	Version   string    `json:"version"` // the text they signed, in the store's waiver texts
	IP        string    `json:"ip,omitempty"`
}

// RiderName is who rode.
func (w RideWaiver) RiderName() string { return or(w.Rider, w.Name) }

// Year is the year it was signed in, club time.
func (w RideWaiver) Year() int { return w.Signed.In(clubTZ).Year() }

// Loaner says whether they borrowed a club bike.
func (w RideWaiver) Loaner() bool { return w.Bike == bikeLoaner }

// BikeLabel says which bike, for the admin.
func (w RideWaiver) BikeLabel() string {
	if w.Loaner() {
		return "Club loaner bike"
	}
	return "Own bike"
}

// forRide is the text signed on the ride waiver page: the loaner agreement
// first for a club bike, the League waiver, and the release for parents and
// guardians for a rider under 18.
func (t waiverText) forRide(loaner, minor bool) waiverText {
	s := string(t.HTML)
	if loaner {
		s = string(t.Loaner) + "\n" + s
	}
	if minor {
		s += "\n" + string(t.Guardian)
	}
	return waiverText{HTML: template.HTML(s), Version: textVersion(s)}
}

// ---- Store ----

// SignRideWaiver records a ride waiver, adding the time, the text and the
// member with that email, if there is one. The same signer, rider and bike on
// the same day count once: it returns the earlier one and false.
func (s *Store) SignRideWaiver(w RideWaiver, text waiverText, loc *time.Location) (RideWaiver, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	day := now.In(loc).Format(time.DateOnly)
	for _, old := range s.d.RideWaivers {
		if old.Email == w.Email && old.Bike == w.Bike && strings.EqualFold(old.RiderName(), w.RiderName()) && old.Signed.In(loc).Format(time.DateOnly) == day {
			return *old, false, nil
		}
	}
	if s.d.WaiverTexts == nil {
		s.d.WaiverTexts = map[string]string{}
	}
	if _, ok := s.d.WaiverTexts[text.Version]; !ok {
		s.d.WaiverTexts[text.Version] = string(text.HTML)
	}
	s.d.NextRideWaiver++
	w.ID, w.Signed, w.Version, w.MemberID = s.d.NextRideWaiver, now, text.Version, 0
	if m := s.byEmail(w.Email); m != nil {
		w.MemberID = m.ID
	}
	s.d.RideWaivers = append(s.d.RideWaivers, &w)
	return w, true, s.save()
}

// linkRideWaivers ties ride waivers to the member with their email: someone
// who signed before joining, or whose member was deleted and added again.
// Callers hold s.mu.
func (s *Store) linkRideWaivers() {
	for _, w := range s.d.RideWaivers {
		if w.MemberID != 0 && s.byID(w.MemberID) != nil {
			continue
		}
		w.MemberID = 0
		if m := s.byEmail(w.Email); m != nil {
			w.MemberID = m.ID
		}
	}
}

// RideWaivers lists the ride waivers signed in a year, newest first.
func (s *Store) RideWaivers(year int, loc *time.Location) []RideWaiver {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RideWaiver
	for i := len(s.d.RideWaivers) - 1; i >= 0; i-- {
		if w := s.d.RideWaivers[i]; w.Signed.In(loc).Year() == year {
			out = append(out, *w)
		}
	}
	return out
}

// RideWaiverYears lists the years anyone signed a ride waiver in.
func (s *Store) RideWaiverYears(loc *time.Location) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var years []int
	for _, w := range s.d.RideWaivers {
		if y := w.Signed.In(loc).Year(); !slices.Contains(years, y) {
			years = append(years, y)
		}
	}
	return years
}

// RideWaiversOf lists a member's ride waivers, newest first.
func (s *Store) RideWaiversOf(memberID int) []RideWaiver {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RideWaiver
	for i := len(s.d.RideWaivers) - 1; i >= 0; i-- {
		if w := s.d.RideWaivers[i]; w.MemberID == memberID {
			out = append(out, *w)
		}
	}
	return out
}

// SignedRideWaiver returns a ride waiver and the text that was signed.
func (s *Store) SignedRideWaiver(id int) (RideWaiver, template.HTML, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.d.RideWaivers {
		if w.ID == id {
			return *w, template.HTML(s.d.WaiverTexts[w.Version]), true
		}
	}
	return RideWaiver{}, "", false
}

// DeleteRideWaiver removes a junk signing.
func (s *Store) DeleteRideWaiver(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.d.RideWaivers)
	s.d.RideWaivers = slices.DeleteFunc(s.d.RideWaivers, func(w *RideWaiver) bool { return w.ID == id })
	if len(s.d.RideWaivers) == n {
		return nil
	}
	return s.save()
}

// ---- The public page ----

type rideWaiverData struct {
	Token, Today               string
	Name, Email, Bike          string // as typed, to show again after a mistake
	Rider, RiderAge            string
	Signature                  string
	Agree, Guardian, SignMinor bool
	Message                    string
	Done                       bool
}

func (a *App) renderRideWaiver(w http.ResponseWriter, status int, d rideWaiverData) {
	d.Token, d.Today = a.formToken(), a.signedDate()
	w.Header().Set("X-Robots-Tag", "noindex")
	a.site.render(w, status, "ride-waiver", rideWaiverPath, "Ride Waiver — "+siteName, d)
}

func (a *App) rideWaiverPage(w http.ResponseWriter, r *http.Request) {
	d := rideWaiverData{Done: r.URL.Query().Get("signed") != ""}
	if b := r.URL.Query().Get("bike"); b == bikeLoaner || b == bikeOwn {
		d.Bike = b
	}
	a.renderRideWaiver(w, http.StatusOK, d)
}

// signRideWaiver records a ride waiver from the public page.
func (a *App) signRideWaiver(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSignatureForm)
	r.ParseForm()
	f := r.PostForm
	d := rideWaiverData{Name: f.Get("name"), Email: f.Get("email"), Bike: f.Get("bike"), Rider: f.Get("rider"), RiderAge: f.Get("rider_age"),
		Signature: f.Get("signature"), Agree: f.Get("waiver") != "", Guardian: f.Get("guardian") != ""}
	if f.Get("website") != "" { // the box only bots fill in
		d.Done = true
		a.renderRideWaiver(w, http.StatusOK, d)
		return
	}
	name, rider := cleanName(d.Name), cleanName(d.Rider)
	email, emailOK := cleanEmail(d.Email)
	minor := rider != "" || strings.TrimSpace(d.RiderAge) != "" || d.Guardian
	d.SignMinor = minor
	age, ageErr := strconv.Atoi(strings.TrimSpace(d.RiderAge))
	drawing, drawn := drawnSignature(r)
	switch {
	case !a.validFormToken(f.Get("ft")):
		d.Message = "This page expired. Reload it and try again."
	case len(strings.Fields(name)) < 2:
		d.Message = "Enter your first and last name."
	case !emailOK:
		d.Message = joinProblems["email"]
	case d.Bike != bikeLoaner && d.Bike != bikeOwn:
		d.Message = "Pick a club loaner bike or your own bike."
	case minor && len(strings.Fields(rider)) < 2:
		d.Message = "Enter the rider’s first and last name."
	case minor && (ageErr != nil || age < 1 || age > 17):
		d.Message = "Enter the rider’s age, under 18. Riders 18 and over sign for themselves."
	case minor && !d.Guardian:
		d.Message = "Tick the box to agree, as the rider’s parent or legal guardian."
	case !d.Agree:
		d.Message = "Tick the box to agree to the waiver."
	case !signedAs(d.Signature, name):
		d.Message = "To sign, type your first and last name as you entered them above."
	case !drawn:
		d.Message = joinProblems["drawing"]
	case !a.limit.Allow("ride-waiver:"+clientIP(r), maxRideWaivers, time.Hour): // counted once the form is right
		d.Message = "Too many waivers from here this hour. Try again later."
	}
	if d.Message != "" {
		a.renderRideWaiver(w, http.StatusBadRequest, d)
		return
	}
	signed := RideWaiver{Name: name, Email: email, Bike: d.Bike, Signature: cleanName(d.Signature), Drawing: drawing, IP: clientIP(r)}
	if minor {
		signed.Minor, signed.Rider, signed.RiderAge = true, rider, age
	}
	if _, _, err := a.store.SignRideWaiver(signed, a.site.waiver.forRide(d.Bike == bikeLoaner, minor), a.cfg.Location); err != nil {
		log.Printf("ride waiver %s: %v", email, err)
		d.Message = "Something went wrong on our end. Try again in a minute."
		a.renderRideWaiver(w, http.StatusInternalServerError, d)
		return
	}
	// Away from the form, so reloading doesn't send it again.
	http.Redirect(w, r, rideWaiverPath+"?signed=1", http.StatusSeeOther)
}

// ---- Admin ----

// adminRideWaiver shows one signed ride waiver, ready to print.
func (a *App) adminRideWaiver(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	signed, text, ok := a.store.SignedRideWaiver(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	v := adminView{Title: "Ride waiver: " + signed.RiderName(), Tab: "waivers", RideWaiver: &signed, WaiverHTML: text, CanManage: a.isBoard(r)}
	if m, ok := a.store.MemberByID(signed.MemberID); ok && signed.MemberID != 0 {
		v.RideMember = &m
	}
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "ride-waiver", v)
}

func (a *App) rideWaiverSignature(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	signed, _, ok := a.store.SignedRideWaiver(id)
	if !ok || len(signed.Drawing) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(signed.Drawing)
}

func (a *App) deleteRideWaiver(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	signed, _, ok := a.store.SignedRideWaiver(id)
	if !ok {
		back(w, r, "/admin/waivers?show=rides")
		return
	}
	if err := a.store.DeleteRideWaiver(id); err != nil {
		back(w, r, fmt.Sprintf("/admin/ride-waivers/%d?err=save", id))
		return
	}
	log.Printf("ride waiver %d (%s) deleted", id, signed.RiderName())
	back(w, r, fmt.Sprintf("/admin/waivers?year=%d&show=rides&ok=waiver-deleted", signed.Signed.In(a.cfg.Location).Year()))
}

// exportRideWaivers downloads a year's ride waivers as a spreadsheet.
func (a *App) exportRideWaivers(w http.ResponseWriter, r *http.Request) {
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil {
		year = a.cfg.ThisYear()
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="paxvelo-ride-waivers-%d.csv"`, year))
	cw := csv.NewWriter(w)
	cw.Write([]string{"Rider", "Under 18", "Age", "Bike", "Signed by", "Email", "Member", "Signed as", "Drawn signature", "Signed", "IP address", "Waiver version", "Link"})
	for _, s := range a.store.RideWaivers(year, a.cfg.Location) {
		minor, age, drawn, member := "No", "", "No", ""
		if s.Minor {
			minor, age = "Yes", strconv.Itoa(s.RiderAge)
		}
		if len(s.Drawing) > 0 {
			drawn = "Yes"
		}
		if m, ok := a.store.MemberByID(s.MemberID); ok && s.MemberID != 0 {
			member = m.Name
		}
		cw.Write([]string{s.RiderName(), minor, age, s.BikeLabel(), s.Name, s.Email, member, s.Signature, drawn, s.Signed.In(a.cfg.Location).Format("2006-01-02 15:04 MST"),
			s.IP, s.Version, fmt.Sprintf("%s/admin/ride-waivers/%d", a.cfg.SiteURL, s.ID)})
	}
	cw.Flush()
}

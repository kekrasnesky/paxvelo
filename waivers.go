package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"html/template"
	"image/png"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Insurance waivers. The club's insurance, through the League of American
// Bicyclists, needs every member to sign the League's waiver each year.
// Members sign on the join form, and in later years on their membership page.
// Every signing is kept for good, with the exact text that was signed, even
// if the member is deleted later: the admin's Waivers screen is the club's
// record of them.

// waiverText is the waiver members sign now, from web/waiver.html, and the
// release a parent or guardian adds for a rider under 18, from
// web/waiver-guardian.html.
type waiverText struct {
	HTML     template.HTML
	Version  string // a hash of the text, so each signed copy can show what was signed
	Guardian template.HTML
}

func loadWaiver() (waiverText, error) {
	read := func(name string) (string, error) {
		b, err := fs.ReadFile(webFS, name)
		s := string(b)
		if strings.HasPrefix(s, "<!--") {
			if _, rest, ok := strings.Cut(s, "-->"); ok {
				s = rest // the note for whoever edits the file
			}
		}
		return strings.TrimSpace(s), err
	}
	main, err := read("web/waiver.html")
	if err != nil {
		return waiverText{}, err
	}
	guardian, err := read("web/waiver-guardian.html")
	if err != nil {
		return waiverText{}, err
	}
	return waiverText{HTML: template.HTML(main), Version: textVersion(main), Guardian: template.HTML(guardian)}, nil
}

func textVersion(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// forMinor is the text a parent or guardian signs for a rider under 18: the
// waiver, then the release for parents and guardians.
func (t waiverText) forMinor() waiverText {
	s := string(t.HTML) + "\n" + string(t.Guardian)
	return waiverText{HTML: template.HTML(s), Version: textVersion(s)}
}

// Waiver is one signed waiver.
type Waiver struct {
	ID       int `json:"id"`
	MemberID int `json:"member_id"`
	Year     int `json:"year"` // the membership year it covers, January to December
	// The member's name and email on file when they signed. For a rider
	// under 18, the member is the parent or guardian who signed for them.
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Signature string    `json:"signature"` // the name they typed to sign
	Signed    time.Time `json:"signed"`
	Version   string    `json:"version"` // the text they signed, in the store's waiver texts
	Where     string    `json:"where"`   // "Join form" or "Membership page"
	IP        string    `json:"ip,omitempty"`
	// The signature they drew with a finger, stylus or mouse, a PNG. Empty
	// for a typed-only signature: without JavaScript, or for someone who
	// can't draw one.
	Drawing []byte `json:"drawing,omitempty"`
	// Who it covers: the member themselves, or a rider under 18 they signed
	// for as parent or guardian, with the rider's age and the guardian's phone.
	Rider         string `json:"rider,omitempty"`
	Minor         bool   `json:"minor,omitempty"`
	RiderAge      int    `json:"rider_age,omitempty"`
	GuardianPhone string `json:"guardian_phone,omitempty"`
}

// RiderName is who the waiver covers.
func (w Waiver) RiderName() string { return or(w.Rider, w.Name) }

// ---- Drawn signatures ----

const (
	maxSignature     = 64 << 10  // a drawn signature's PNG; a real one is a few KB
	maxSignatureForm = 256 << 10 // a form that carries one
)

// drawnSignature reads the signature drawn on a waiver form. ok is false
// when the form had the drawing box (its "drawn" field, which only the
// page's script adds) but no usable drawing came with it. With no box,
// the typed name is the signature, and it returns nil, true.
func drawnSignature(r *http.Request) (img []byte, ok bool) {
	if r.FormValue("drawn") == "" {
		return nil, true
	}
	return signatureImage(r.FormValue("signature_image"))
}

// signatureImage checks a drawn signature as the page sends it: a PNG data
// URL of a sensible size, with some ink on it.
func signatureImage(dataURL string) ([]byte, bool) {
	enc, ok := strings.CutPrefix(dataURL, "data:image/png;base64,")
	if !ok || base64.StdEncoding.DecodedLen(len(enc)) > maxSignature {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, false
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width < 100 || cfg.Height < 30 || cfg.Width > 1200 || cfg.Height > 400 {
		return nil, false
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, false
	}
	ink, bounds := 0, img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0x8000 {
				ink++
			}
		}
	}
	return b, ink >= 60 // a stray tap isn't a signature
}

// waiverSignature serves the signature drawn on a waiver, for its signed copy.
func (a *App) waiverSignature(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	signed, _, ok := a.store.SignedWaiver(id)
	if !ok || len(signed.Drawing) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(signed.Drawing)
}

// ---- Store ----

// SignWaiver records a signed waiver: w says whose, for which year, and how
// it was signed, and the store adds the member's name and email, the time,
// and the text. Each rider signs once a year: if they already have, it
// returns that waiver and false.
func (s *Store) SignWaiver(w Waiver, text waiverText) (Waiver, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID(w.MemberID)
	if m == nil {
		return Waiver{}, false, fmt.Errorf("no member %d", w.MemberID)
	}
	if !w.Minor {
		w.Rider = m.Name
	}
	for _, old := range s.d.Waivers {
		if old.MemberID == w.MemberID && old.Year == w.Year && old.Minor == w.Minor && (!w.Minor || strings.EqualFold(old.Rider, w.Rider)) {
			return *old, false, nil
		}
	}
	if s.d.WaiverTexts == nil {
		s.d.WaiverTexts = map[string]string{}
	}
	if _, ok := s.d.WaiverTexts[text.Version]; !ok {
		s.d.WaiverTexts[text.Version] = string(text.HTML)
	}
	s.d.NextWaiver++
	w.ID, w.Name, w.Email, w.Signed, w.Version = s.d.NextWaiver, m.Name, m.Email, time.Now().UTC(), text.Version
	s.d.Waivers = append(s.d.Waivers, &w)
	return w, true, s.save()
}

// Waivers lists every signed waiver for a year, newest first.
func (s *Store) Waivers(year int) []Waiver {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Waiver
	for i := len(s.d.Waivers) - 1; i >= 0; i-- {
		if w := s.d.Waivers[i]; w.Year == year {
			out = append(out, *w)
		}
	}
	return out
}

// WaiverYears lists the years anyone signed a waiver for, newest first.
func (s *Store) WaiverYears() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var years []int
	for _, w := range s.d.Waivers {
		if !slices.Contains(years, w.Year) {
			years = append(years, w.Year)
		}
	}
	slices.Sort(years)
	slices.Reverse(years)
	return years
}

// WaiverFor returns a member's own waiver for a year, if they've signed it.
func (s *Store) WaiverFor(memberID, year int) (Waiver, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.d.Waivers {
		if w.MemberID == memberID && w.Year == year && !w.Minor {
			return *w, true
		}
	}
	return Waiver{}, false
}

// MinorWaivers lists the waivers a member signed for riders under 18 for a year.
func (s *Store) MinorWaivers(memberID, year int) []Waiver {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Waiver
	for _, w := range s.d.Waivers {
		if w.MemberID == memberID && w.Year == year && w.Minor {
			out = append(out, *w)
		}
	}
	return out
}

// SignedWaiver returns a signed waiver and the text that was signed.
func (s *Store) SignedWaiver(id int) (Waiver, template.HTML, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.d.Waivers {
		if w.ID == id {
			return *w, template.HTML(s.d.WaiverTexts[w.Version]), true
		}
	}
	return Waiver{}, "", false
}

// ---- Membership page ----

// signWaiver signs this year's waiver from a member's own page. They sign
// with the name the club has for them.
func (a *App) signWaiver(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSignatureForm)
	r.ParseForm()
	t := r.PostFormValue("t")
	m, ok := a.store.MemberByToken(t)
	if !ok {
		a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
		return
	}
	signature := cleanName(r.PostFormValue("signature"))
	drawing, drawn := drawnSignature(r)
	v := a.membershipView(m, t)
	switch {
	case v.Waiver != nil:
	case r.PostFormValue("waiver") == "":
		v.WaiverMessage = joinProblems["waiver"]
	case !strings.EqualFold(signature, m.Name):
		v.WaiverMessage = fmt.Sprintf("To sign, type your name as the club has it: %s.", m.Name)
	case !drawn:
		v.WaiverMessage = joinProblems["drawing"]
	default:
		if _, _, err := a.store.SignWaiver(Waiver{MemberID: m.ID, Year: v.Year, Signature: signature, Drawing: drawing, Where: "Membership page", IP: clientIP(r)}, a.site.waiver); err != nil {
			log.Printf("sign waiver %s: %v", m.Email, err)
			v.WaiverMessage = "Something went wrong on our end. Try again in a minute."
		}
	}
	if v.WaiverMessage != "" {
		v.Signature, v.WaiverTicked = r.PostFormValue("signature"), r.PostFormValue("waiver") != ""
		a.renderMembership(w, http.StatusBadRequest, v)
		return
	}
	// Back to their page, so reloading it doesn't send the form again.
	http.Redirect(w, r, "/membership?t="+url.QueryEscape(t)+"&signed=1#waiver", http.StatusSeeOther)
}

// minorForm is what a parent or guardian typed to sign for a rider under 18.
type minorForm struct {
	First, Last, Age, Phone, Signature string
	Ticked                             bool
}

// signMinorWaiver signs this year's waiver for a rider under 18, from the
// membership page of their parent or guardian, who signs with the name the
// club has for them.
func (a *App) signMinorWaiver(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSignatureForm)
	r.ParseForm()
	t := r.PostFormValue("t")
	m, ok := a.store.MemberByToken(t)
	if !ok {
		a.renderMembership(w, http.StatusOK, membershipData{State: "invalid"})
		return
	}
	f := minorForm{First: r.PostFormValue("rider_first"), Last: r.PostFormValue("rider_last"), Age: r.PostFormValue("rider_age"),
		Phone: r.PostFormValue("guardian_phone"), Signature: r.PostFormValue("signature"), Ticked: r.PostFormValue("guardian") != ""}
	first, last, phone := cleanName(f.First), cleanName(f.Last), cleanPhone(f.Phone)
	age, err := strconv.Atoi(strings.TrimSpace(f.Age))
	drawing, drawn := drawnSignature(r)
	v := a.membershipView(m, t)
	rider := first + " " + last
	var already bool
	for _, s := range v.Minors {
		already = already || strings.EqualFold(s.Rider, rider)
	}
	switch {
	case first == "" || last == "":
		v.MinorMessage = "Enter the rider’s first and last name."
	case err != nil || age < 1 || age > 17:
		v.MinorMessage = "Enter the rider’s age, under 18. Riders 18 and over sign their own waiver."
	case already:
		v.MinorMessage = fmt.Sprintf("You’ve already signed %s’s %d waiver.", first, v.Year)
	case len(phoneDigits(phone)) != 10:
		v.MinorMessage = "Enter your 10-digit phone number, like 301-555-0123."
	case !f.Ticked:
		v.MinorMessage = "Tick the box to agree, as the rider’s parent or legal guardian."
	case !strings.EqualFold(cleanName(f.Signature), m.Name):
		v.MinorMessage = fmt.Sprintf("To sign, type your name as the club has it: %s.", m.Name)
	case !drawn:
		v.MinorMessage = joinProblems["drawing"]
	default:
		signed := Waiver{MemberID: m.ID, Year: v.Year, Signature: cleanName(f.Signature), Drawing: drawing, Where: "Membership page", IP: clientIP(r),
			Rider: rider, Minor: true, RiderAge: age, GuardianPhone: phone}
		if _, _, err := a.store.SignWaiver(signed, a.site.waiver.forMinor()); err != nil {
			log.Printf("sign minor waiver %s: %v", m.Email, err)
			v.MinorMessage = "Something went wrong on our end. Try again in a minute."
		}
	}
	if v.MinorMessage != "" {
		v.MinorForm = f
		a.renderMembership(w, http.StatusBadRequest, v)
		return
	}
	http.Redirect(w, r, "/membership?t="+url.QueryEscape(t)+"&minor=1#minors", http.StatusSeeOther)
}

// ---- Admin ----

// waiversPage is the club's record of signed waivers, a year at a time, and
// who hasn't signed that year's yet.
func (a *App) waiversPage(w http.ResponseWriter, r *http.Request) {
	thisYear := a.cfg.ThisYear()
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil {
		year = thisYear
	}
	v := adminView{Title: "Waivers", Tab: "waivers", Year: year, Show: r.URL.Query().Get("show"), Waivers: a.store.Waivers(year)}
	v.WaiverYears = a.store.WaiverYears()
	if !slices.Contains(v.WaiverYears, thisYear) {
		v.WaiverYears = append([]int{thisYear}, v.WaiverYears...)
	}
	if !slices.Contains(v.WaiverYears, year) {
		v.WaiverYears = append(v.WaiverYears, year)
	}
	signed := map[int]bool{}
	for _, w := range v.Waivers {
		if !w.Minor {
			signed[w.MemberID] = true
		}
	}
	for _, m := range a.store.Members() {
		if m.Active() && !signed[m.ID] {
			v.Unsigned = append(v.Unsigned, m)
		}
	}
	a.renderAdmin(w, r, http.StatusOK, "waivers", v)
}

// waiverPage shows one signed waiver, ready to print or save as a PDF.
func (a *App) waiverPage(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	signed, text, ok := a.store.SignedWaiver(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	a.renderAdmin(w, r, http.StatusOK, "waiver", adminView{Title: "Waiver: " + signed.RiderName(), Tab: "waivers", Waiver: &signed, WaiverHTML: text})
}

// exportWaivers downloads a year's signed waivers as a spreadsheet.
func (a *App) exportWaivers(w http.ResponseWriter, r *http.Request) {
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil {
		year = a.cfg.ThisYear()
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="paxvelo-waivers-%d.csv"`, year))
	cw := csv.NewWriter(w)
	cw.Write([]string{"Year", "Rider", "Under 18", "Age", "Member", "Email", "Guardian phone", "Signed as", "Drawn signature", "Signed", "Where", "IP address", "Waiver version", "Link"})
	for _, s := range a.store.Waivers(year) {
		minor, age, drawn := "No", "", "No"
		if len(s.Drawing) > 0 {
			drawn = "Yes"
		}
		if s.Minor {
			minor, age = "Yes", strconv.Itoa(s.RiderAge)
		}
		cw.Write([]string{strconv.Itoa(s.Year), s.RiderName(), minor, age, s.Name, s.Email, s.GuardianPhone, s.Signature, drawn, s.Signed.In(a.cfg.Location).Format("2006-01-02 15:04 MST"),
			s.Where, s.IP, s.Version, fmt.Sprintf("%s/admin/waivers/%d", a.cfg.SiteURL, s.ID)})
	}
	cw.Flush()
}

package main

import (
	"net/http"
	"strings"
)

// QR codes people scan with their phone camera (qr.go): printable signs for
// flyers and shop counters (Rides → Signs to print), and a full-screen code a
// leader holds up at the start so a newcomer signs the ride waiver on their
// own phone (/qr/ride-waiver, from the ride page).

// qrSign is one sign: where its code goes, and its words.
type qrSign struct {
	Key, Path, Title, Line string
}

var qrSigns = []qrSign{
	{"join", "/join", "Join PaxVelo", "Group rides in Southern Maryland"},
	{"ride-waiver", "/ride-waiver", "First ride with us?", "Sign the ride waiver"},
	{"calendar", "/calendar/subscribe", "Every club ride", "In your phone’s calendar"},
}

func signFor(key string) (qrSign, bool) {
	for _, s := range qrSigns {
		if s.Key == key {
			return s, true
		}
	}
	return qrSign{}, false
}

// Host is the address printed under the code, for anyone without a camera.
func (s qrSign) Host(siteURL string) string {
	return strings.TrimPrefix(strings.TrimPrefix(siteURL, "https://"), "http://") + s.Path
}

// qrPage is one code, filling a phone screen.
func (a *App) qrPage(w http.ResponseWriter, r *http.Request) {
	s, ok := signFor(r.PathValue("name"))
	if !ok {
		a.site.notFound(w, r)
		return
	}
	a.site.render(w, http.StatusOK, "qr", "", s.Title+" — "+siteName, struct {
		qrSign
		Address string
	}{s, s.Host(a.cfg.SiteURL)})
}

// signsPage is the admin's printable signs, one per sheet.
func (a *App) signsPage(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Signs to print", Tab: "signs", Signs: qrSigns}
	a.renderAdmin(w, r, http.StatusOK, "signs", v)
}

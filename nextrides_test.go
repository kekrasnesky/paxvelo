package main

import (
	"strings"
	"testing"
)

// The home page lists the next rides with where they start, a ride page
// links to directions and a calendar file, and rides sent through the
// calendar API don't say so on the public page.
func TestNextRidesAndCalendarFile(t *testing.T) {
	a, h, _ := testApp(t)
	date := a.today().AddDate(0, 0, 1).Format("2006-01-02")
	o, err := a.store.AddRide(CalRide{Date: date, Time: "08:30", Name: "Coffee Spin", Place: "Leonardtown Square",
		Details: "Flat, with a regroup; then coffee", AddedBy: "the calendar API"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.rebuildContent(); err != nil {
		t.Fatal(err)
	}
	home := do(t, h, nil, "GET", "/", "", nil).Body.String()
	for _, want := range []string{`class="next-rides"`, "Coffee Spin", `<span class="cal-place">Leonardtown Square</span>`, `href="/calendar/ride/` + o.Key + `"`, "Full ride calendar"} {
		if !strings.Contains(home, want) {
			t.Errorf("home page is missing %q", want)
		}
	}
	if cal := do(t, h, nil, "GET", "/calendar", "", nil).Body.String(); !strings.Contains(cal, `<span class="cal-place">Leonardtown Square</span>`) {
		t.Error("the calendar's upcoming rides should say where they start")
	}

	page := do(t, h, nil, "GET", "/calendar/ride/"+o.Key, "", nil).Body.String()
	for _, want := range []string{"maps/search/?api=1&amp;query=Leonardtown&#43;Square%2C&#43;Maryland", "/calendar/ride/" + o.Key + "/calendar.ics", "ride-share"} {
		if !strings.Contains(page, want) {
			t.Errorf("ride page is missing %q", want)
		}
	}
	if strings.Contains(page, "calendar API") {
		t.Error("the ride page shouldn't mention the calendar API")
	}

	w := do(t, h, nil, "GET", "/calendar/ride/"+o.Key+"/calendar.ics", "", nil)
	ics := w.Body.String()
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
		t.Errorf("calendar file type = %q", ct)
	}
	for _, want := range []string{"BEGIN:VEVENT\r\n", "SUMMARY:Coffee Spin\r\n", "LOCATION:Leonardtown Square\r\n", `Flat\, with a regroup\; then coffee`, "DTSTART:" + strings.ReplaceAll(date, "-", "") + "T", "END:VCALENDAR\r\n"} {
		if !strings.Contains(ics, want) {
			t.Errorf("calendar file is missing %q:\n%s", want, ics)
		}
	}
	for _, l := range strings.Split(ics, "\r\n") {
		if len(l) > 75 {
			t.Errorf("calendar file line longer than 75 bytes: %q", l)
		}
	}
	if w := do(t, h, nil, "GET", "/calendar/ride/r999/calendar.ics", "", nil); w.Code != 404 {
		t.Errorf("a missing ride's calendar file = %d, want 404", w.Code)
	}
}

func TestDirectionsSearch(t *testing.T) {
	for place, want := range map[string]string{
		"Solomons, MD":       "query=Solomons%2C+MD",
		"Myrtle Point Park":  "query=Myrtle+Point+Park%2C+Maryland",
		"Arlington Virginia": "query=Arlington+Virginia",
		"":                   "",
	} {
		got := RideOcc{CalRide: CalRide{Place: place}}.DirectionsFor(false)
		if !strings.HasSuffix(got, want) || (want == "") != (got == "") {
			t.Errorf("directions for %q = %q, want …%s", place, got, want)
		}
	}
}

func TestICSFold(t *testing.T) {
	l := "DESCRIPTION:" + strings.Repeat("é", 60)
	out := icsFold(l)
	if strings.ReplaceAll(strings.TrimSuffix(out, "\r\n"), "\r\n ", "") != l {
		t.Errorf("folding changed the text: %q", out)
	}
	for _, part := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if len(part) > 75 {
			t.Errorf("folded line is %d bytes", len(part))
		}
	}
}

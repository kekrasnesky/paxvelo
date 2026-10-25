package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const sampleEmail = `## Saturday ride

Hi {first_name}, join us for **coffee** after. See [the route](https://strava.com/r/1) or https://paxvelo.cc/rides.

![Photo](/media/abc.jpg)

- Bring lights
- Bring a **spare** tube

[[RSVP on Strava](https://strava.com/clubs/10197)]

Bad link: [click](javascript:alert(1)) <b>not bold</b>`

func TestEmailHTML(t *testing.T) {
	got := string(emailHTML(sampleEmail, "https://www.paxvelo.cc"))
	for _, want := range []string{
		`<h2 style="` + emailHeading + `">Saturday ride</h2>`,
		`<strong>coffee</strong>`,
		`<a href="https://strava.com/r/1" style="color:#9a2b29">the route</a>`,
		`<a href="https://paxvelo.cc/rides" style="color:#9a2b29">https://paxvelo.cc/rides</a>.`,
		`<img src="https://www.paxvelo.cc/media/abc.jpg" alt="Photo"`,
		`<li style="margin:0 0 6px">Bring a <strong>spare</strong> tube</li>`,
		`<a href="https://strava.com/clubs/10197" style="` + emailButton + `">RSVP on Strava</a>`,
		`Bad link: [click](javascript:alert(1)) &lt;b&gt;not bold&lt;/b&gt;`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s\nin:\n%s", want, got)
		}
	}
	if strings.Contains(got, `href="javascript`) {
		t.Error("javascript: links must not become links")
	}
}

func TestEmailText(t *testing.T) {
	got := emailText(sampleEmail, "https://www.paxvelo.cc")
	for _, want := range []string{
		"Saturday ride\n\nHi {first_name}, join us for coffee after. See the route (https://strava.com/r/1) or https://paxvelo.cc/rides.",
		"(photo)",
		"- Bring a spare tube",
		"RSVP on Strava: https://strava.com/clubs/10197",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q\nin:\n%s", want, got)
		}
	}
	if strings.Contains(got, "**") || strings.Contains(got, "##") {
		t.Errorf("marks should be taken out of the plain text:\n%s", got)
	}
}

// The preview shows the email as a member sees it: the club name in the
// banner, the formatting, and {first_name} filled in.
func TestEmailPreview(t *testing.T) {
	_, h, c := testApp(t)
	ct, body := form(url.Values{"subject": {"Ride this Saturday"}, "body": {"Hi {first_name},\n\n**See you there**"}})
	w := do(t, h, c, "POST", "/admin/email/preview", ct, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	got := w.Body.String()
	for _, want := range []string{"PAXVELO CYCLING CLUB", "Hi Pat,", "<strong>See you there</strong>", `src="/static/img/logo.png"`} {
		if !strings.Contains(got, want) {
			t.Errorf("preview missing %q", want)
		}
	}
	if w := do(t, h, nil, "POST", "/admin/email/preview", ct, strings.NewReader("")); w.Code == http.StatusOK {
		t.Error("the preview should need an admin login")
	}
}

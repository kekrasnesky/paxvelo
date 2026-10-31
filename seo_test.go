package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

var ldScript = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

// ldOf returns the structured data on a page, which must be valid JSON.
func ldOf(t *testing.T, page string) map[string]any {
	t.Helper()
	m := ldScript.FindStringSubmatch(page)
	if m == nil {
		return nil
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
		t.Fatalf("structured data isn't valid JSON: %v\n%s", err, m[1])
	}
	return v
}

// The home page says who the club is, the calendar lists rides as events,
// and each page has its own description.
func TestSearchEngineData(t *testing.T) {
	a, h, _ := testApp(t)

	home := do(t, h, nil, "GET", "/", "", nil).Body.String()
	if v := ldOf(t, home); v == nil || v["@type"] != "SportsClub" || v["url"] != a.cfg.SiteURL+"/" {
		t.Errorf("home page should describe the club: %v", v)
	}
	if !strings.Contains(home, "<title>PaxVelo Cycling Club · Group Rides in Southern Maryland</title>") {
		t.Error("home page title should say where the club rides")
	}

	cal := do(t, h, nil, "GET", "/calendar", "", nil).Body.String()
	if v := ldOf(t, cal); v != nil {
		events, _ := v["@graph"].([]any)
		if len(events) == 0 || events[0].(map[string]any)["@type"] != "SportsEvent" {
			t.Errorf("calendar should list rides as events: %v", v)
		}
	}
	if ldOf(t, do(t, h, nil, "GET", "/rides", "", nil).Body.String()) != nil {
		t.Error("only the home page and calendar carry structured data")
	}

	seen := map[string]string{}
	for _, p := range []string{"/", "/rides", "/bike-safety", "/committee", "/contact", "/join", "/calendar", "/newsletter"} {
		m := regexp.MustCompile(`<meta name="description" content="([^"]*)">`).FindStringSubmatch(do(t, h, nil, "GET", p, "", nil).Body.String())
		if m == nil {
			t.Errorf("%s has no description", p)
			continue
		}
		if other, dup := seen[m[1]]; dup {
			t.Errorf("%s has the same description as %s", p, other)
		}
		seen[m[1]] = p
	}
}

// Bing Webmaster Tools checks this file to verify the site.
func TestBingSiteAuth(t *testing.T) {
	_, h, _ := testApp(t)
	w := do(t, h, nil, "GET", "/BingSiteAuth.xml", "", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/xml") ||
		!strings.Contains(w.Body.String(), "<user>587A2EFD1A92F7085C35326E6D5A7059</user>") {
		t.Errorf("Bing verification file: %d %q\n%s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}

// The board page names each board member and their role in the structured
// data, so search tools can say who the president is.
func TestBoardInSearchData(t *testing.T) {
	a, h, _ := testApp(t)
	a.store.SetBoard([]BoardMember{{Name: "Pat Rider", Role: "President"}, {Name: "", Role: "Treasurer"}})
	if err := a.rebuildContent(); err != nil {
		t.Fatal(err)
	}
	v := ldOf(t, do(t, h, nil, "GET", "/committee", "", nil).Body.String())
	members, _ := v["member"].([]any)
	if len(members) != 1 {
		t.Fatalf("want the one named board member, got %v", v["member"])
	}
	p := members[0].(map[string]any)["member"].(map[string]any)
	if p["name"] != "Pat Rider" || p["jobTitle"] != "President, PaxVelo Cycling Club" {
		t.Errorf("board member: %v", p)
	}
}

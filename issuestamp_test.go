package main

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Drafts show exactly when they were started and last edited, to the
// second, and a copy counts as just started.
func TestIssueTimestamps(t *testing.T) {
	a, h, c := testApp(t)
	ct, body := form(url.Values{"month": {"11"}, "year": {"2026"}})
	do(t, h, c, "POST", "/admin/newsletter", ct, body)
	is := a.store.Issues()[0]
	if is.Created.IsZero() || !is.Created.Equal(is.Updated) {
		t.Fatalf("a new draft should record when it was started: %v / %v", is.Created, is.Updated)
	}
	stamp := regexp.MustCompile(`<time datetime="[^"]+">\w{3}, \w{3} \d{1,2}, \d{4}, \d{1,2}:\d{2}:\d{2} [AP]M \w+</time>`)
	list := do(t, h, c, "GET", "/admin/newsletter", "", nil).Body.String()
	if n := len(stamp.FindAllString(list, -1)); n < 2 || !strings.Contains(list, "#"+itoa(is.ID)) {
		t.Errorf("the issue list should show the started and edited times to the second (found %d)", n)
	}
	page := do(t, h, c, "GET", "/admin/newsletter/"+itoa(is.ID), "", nil).Body.String()
	if !stamp.MatchString(page) || !strings.Contains(page, "Issue #"+itoa(is.ID)) {
		t.Error("the issue page should show exact times")
	}
	w := do(t, h, c, "POST", "/admin/newsletter/"+itoa(is.ID)+"/copy", "", nil)
	if !strings.Contains(w.Header().Get("Location"), "ok=copied") {
		t.Fatalf("copy went to %q", w.Header().Get("Location"))
	}
	for _, x := range a.store.Issues() {
		if x.ID != is.ID && x.Created.Before(is.Created) {
			t.Error("a copy should count as started when it was copied")
		}
	}
}

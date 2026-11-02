package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Emailing an issue: the whole newsletter, its stories, photos, rides and
// buttons, goes out as an email to members, written in the simple marks
// emails use (emailfmt.go) and sent like any email to members (counted,
// paused on failures, in the sent log). The issue page previews it, sends a
// test to an admin, and sends it to a group of members.

// issueSubject is the email's subject unless the admin types another.
func issueSubject(is Issue) string { return "The " + newsName + ", " + is.Label() }

var mdLinkRE = regexp.MustCompile(`(!?)\[([^\]]*)\]\(\s*([^)\s]*)\s*\)`)

// newsLink resolves an address typed in an issue ({kit}, rides.md, /calendar)
// to one an email can use; "" if it can't be.
func (a *App) newsLink(raw string) string {
	u, err := a.site.md.url(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u
}

// emailBody turns a story's text, typed like the website's pages, into the
// email's marks: the same, with its links resolved.
func (a *App) emailBody(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	s = mdLinkRE.ReplaceAllStringFunc(s, func(m string) string {
		g := mdLinkRE.FindStringSubmatch(m)
		u := a.newsLink(g[3])
		if u == "" {
			return g[2]
		}
		return g[1] + "[" + g[2] + "](" + u + ")"
	})
	for k, v := range a.site.md.vars { // {strava}, {dues-family}, … in the text
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// issueEmail is the issue as an email body.
func (a *App) issueEmail(is Issue) string {
	var p []string
	add := func(s ...string) {
		for _, x := range s {
			if x = strings.TrimSpace(x); x != "" {
				p = append(p, x)
			}
		}
	}
	intro := "Hi {first_name}, here’s the " + is.Label() + " issue of the " + newsName + "."
	if is.Tagline != "" {
		intro += " " + is.Tagline
	}
	add(intro)
	if is.Cover != "" {
		add("![" + newsName + "](" + is.Cover + ")")
		if is.CoverCaption != "" {
			add("*" + is.CoverCaption + "*")
		}
	}
	story := func(b Block) {
		add("---")
		if b.Kicker != "" {
			add("^^ " + b.Kicker)
		}
		if b.Heading != "" {
			add("## " + b.Heading)
		}
		if b.Subhead != "" {
			add("*" + b.Subhead + "*")
		}
		if b.Photo != "" {
			add("![" + or(b.Caption, b.Heading) + "](" + b.Photo + ")")
			if b.VideoURL != "" {
				label := "Watch the video"
				if b.VideoLength != "" {
					label += " (" + b.VideoLength + ")"
				}
				add("[[" + label + "](" + b.VideoURL + ")]")
			}
			if b.Caption != "" {
				add("*" + b.Caption + "*")
			}
		}
		add(a.emailBody(b.Body))
		if u := a.newsLink(b.LinkURL); u != "" {
			add("[[" + or(b.LinkLabel, "Read more") + "](" + u + ")]")
		}
	}
	today := a.today().Format("2006-01-02")
	for _, b := range is.Blocks {
		switch b.Kind {
		case "big", "story", "banner":
			if b.Heading != "" || strings.TrimSpace(b.Body) != "" || b.Photo != "" {
				story(b)
			}
		case "rides":
			rv := a.issueRides(is)
			var lines []string
			for _, rd := range rv.Rides {
				if rd.Past {
					continue
				}
				line := "- **" + rd.Weekday + ", " + rd.Month + " " + rd.Day + "**"
				if rd.Time != "" {
					line += " · " + rd.Time
				}
				line += " · [" + rd.Name + "](/calendar/ride/" + rd.Key + ")"
				if rd.Place != "" {
					line += ", " + rd.Place
				}
				switch {
				case rd.Open == 1:
					line += ". Needs a leader."
				case rd.Open > 1:
					line += fmt.Sprintf(". Needs %d leaders.", rd.Open)
				}
				lines = append(lines, line)
			}
			if len(lines) == 0 {
				continue
			}
			add("---", "^^ On the calendar", "## "+or(b.Heading, "Upcoming rides"), strings.Join(lines, "\n"))
			if rv.Need > 0 {
				add("Rides that need a leader: open one and type your name to lead it.")
			}
			add("[[See the ride calendar](/calendar)]")
		case "event":
			o, ok := a.store.Ride(b.Event)
			if !ok || o.Held {
				continue
			}
			e := eventShown(o, today)
			kicker := e.Kind
			if e.Cancelled {
				kicker += " · Cancelled"
			}
			when := e.When
			if e.Place != "" {
				when += "\n" + e.Place
			}
			if e.Host != "" {
				when += "\n" + e.Host
			}
			add("---", "^^ "+kicker, "## "+e.Name, when)
			if strings.TrimSpace(b.Body) != "" {
				add(a.emailBody(b.Body))
			} else {
				add(e.Details)
			}
			add("[[More details](/calendar/ride/" + e.Key + ")]")
		case "gallery":
			if len(b.Photos) == 0 {
				continue
			}
			add("---", "^^ Photos")
			for i, ph := range b.Photos {
				if i == 4 {
					break
				}
				add("![Club photo](" + ph + ")")
			}
			if len(b.Photos) > 4 {
				add(fmt.Sprintf("[%d more photos in the newsletter online](%s)", len(b.Photos)-4, is.Path()))
			}
		case "share":
			add("---", "## "+or(b.Heading, shareHeading), a.emailBody(b.Body), "[["+or(b.LinkLabel, shareButton)+"](/share-photos)]")
		case "instagram":
			add("---", "## From our Instagram", "[[Follow @paxvelo](https://www.instagram.com/paxvelo/)]")
		}
	}
	add("---", "[[Read it on the website]("+is.Path()+")]", "See you on the road!\nPaxVelo")
	return strings.Join(p, "\n\n")
}

// issueEmailPreview shows the issue's email as a member sees it, for the
// preview on the issue page ({first_name} filled in for Pat).
func (a *App) issueEmailPreview(w http.ResponseWriter, r *http.Request) {
	is, ok := a.issueFromPath(w, r)
	if !ok {
		return
	}
	pat := Member{Name: "Pat Rider", Plan: "individual"}
	subject := or(strings.TrimSpace(r.URL.Query().Get("subject")), issueSubject(is))
	page, err := a.site.renderEmail(a.mailer.personalize(subject, pat), a.mailer.personalize(a.issueEmail(is), pat), memberLinks{Mine: "#", Unsub: "#"}, a.cfg.SiteURL)
	if err != nil {
		http.Error(w, "The preview couldn’t be made.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, page)
}

// testIssueEmail sends the issue's email to one address, marked [Test].
func (a *App) testIssueEmail(w http.ResponseWriter, r *http.Request) {
	is, ok := a.issueFromPath(w, r)
	if !ok {
		return
	}
	page := "/admin/newsletter/" + strconv.Itoa(is.ID)
	to, ok := cleanEmail(r.FormValue("test_to"))
	if !ok {
		back(w, r, page+"?err=test-to#email-it")
		return
	}
	subject := or(strings.TrimSpace(r.FormValue("subject")), issueSubject(is))
	if err := a.mailer.SendTest(to, subject, a.issueEmail(is)); err != nil {
		back(w, r, page+"?err=test-failed#email-it")
		return
	}
	back(w, r, page+"?ok=issue-tested&to="+url.QueryEscape(to)+"#email-it")
}

// sendIssueEmail emails a published issue to a group of members.
func (a *App) sendIssueEmail(w http.ResponseWriter, r *http.Request) {
	is, ok := a.issueFromPath(w, r)
	if !ok {
		return
	}
	page := "/admin/newsletter/" + strconv.Itoa(is.ID)
	if !is.IsPublished() {
		back(w, r, page+"?err=not-published#email-it")
		return
	}
	subject := or(strings.TrimSpace(r.FormValue("subject")), issueSubject(is))
	c, err := a.store.StartIssueCampaign(is.ID, subject, a.issueEmail(is), r.FormValue("audience"), a.cfg.ThisYear())
	if err != nil {
		back(w, r, page+"?err=nobody#email-it")
		return
	}
	a.mailer.Wake()
	back(w, r, page+"?ok=issue-sending&n="+strconv.Itoa(c.Total)+"#email-it")
}

// issueCampaigns are the times an issue was emailed to members, newest first.
func (a *App) issueCampaigns(id int) []Campaign {
	var out []Campaign
	for _, c := range a.store.Campaigns() {
		if c.Issue == id {
			out = append(out, c)
		}
	}
	return out
}

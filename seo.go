package main

import (
	"encoding/json"
	"html/template"
	"time"
)

// What search engines read: a description for each page that isn't a
// content file (those set theirs in the file's description: line), and
// structured data saying who the club is and when it rides.

var pageDescs = map[string]string{
	"/join":         "Join PaxVelo in Southern Maryland. Membership runs January to December, with shop and kit discounts and a say in the club.",
	"/calendar":     "Upcoming PaxVelo group rides in St. Mary’s County, Maryland, and who’s leading them.",
	"/newsletter":   "PaxVelo’s monthly newsletter: ride reports, photos, and club news from Southern Maryland.",
	"/share-photos": "Send PaxVelo your ride photos for the newsletter and gallery, straight from your phone.",
	"/gallery":      "Photos from PaxVelo’s group rides in Southern Maryland, taken by our members.",
}

// bingSiteAuth is Bing Webmaster Tools' verification file, served at /BingSiteAuth.xml.
const bingSiteAuth = "<?xml version=\"1.0\"?>\n<users>\n\t<user>587A2EFD1A92F7085C35326E6D5A7059</user>\n</users>"

const (
	clubEmail     = "PaxVeloClub@gmail.com"
	facebookGroup = "https://www.facebook.com/groups/112307228841445/"
)

// ldJSON writes structured data for a <script type="application/ld+json">.
// json.Marshal escapes < and >, so nothing in it can end the script early.
func ldJSON(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return template.JS(b)
}

type ld = map[string]any

func (s *Site) clubOrg() ld {
	return ld{
		"@type": "SportsClub", "name": siteName, "url": s.cfg.SiteURL + "/",
	}
}

// clubLD describes the club and who's on its board, for the home and board
// pages. Search engines and AI search tools read this to answer "who runs
// PaxVelo" or "who is <name>".
func (s *Site) clubLD(board []BoardMember) template.JS {
	org := s.clubOrg()
	org["@context"] = "https://schema.org"
	org["description"] = siteDescription + ". A nonprofit club with weekly group rides in St. Mary’s County, Maryland."
	org["logo"] = s.cfg.SiteURL + "/static/img/logo.png"
	org["image"] = s.cfg.SiteURL + "/static/img/og.jpg"
	org["email"] = clubEmail
	org["sport"] = "Cycling"
	org["nonprofitStatus"] = "Nonprofit501c3"
	org["address"] = ld{"@type": "PostalAddress", "addressRegion": "MD", "addressCountry": "US"}
	org["areaServed"] = ld{"@type": "AdministrativeArea", "name": "St. Mary’s County, Maryland"}
	org["sameAs"] = []string{s.cfg.StravaURL, instagramProfile, facebookGroup}
	var roles []ld
	for _, b := range board {
		if b.Name == "" || b.Role == "" {
			continue
		}
		roles = append(roles, ld{"@type": "OrganizationRole", "roleName": b.Role,
			"member": ld{"@type": "Person", "name": b.Name, "jobTitle": b.Role + ", " + siteName}})
	}
	if len(roles) > 0 {
		org["member"] = roles
	}
	return ldJSON(org)
}

// ridesLD lists upcoming rides as events, for the calendar. A ride without
// a start spot is left out: search engines need to know where an event is.
func (s *Site) ridesLD(rides []RideOcc) template.JS {
	var events []ld
	for _, o := range rides {
		place := o.Place
		if place == "" || o.Cancelled {
			continue
		}
		layout, at := "2006-01-02", o.Date
		if o.Time != "" {
			layout, at = "2006-01-02 15:04", o.Date+" "+o.Time
		}
		start, err := time.ParseInLocation(layout, at, s.cfg.Location)
		if err != nil {
			continue
		}
		startDate := start.Format("2006-01-02")
		if o.Time != "" {
			startDate = start.Format(time.RFC3339)
		}
		e := ld{
			"@type": "SportsEvent", "name": o.Name, "startDate": startDate, "sport": "Cycling",
			"eventStatus": "https://schema.org/EventScheduled", "eventAttendanceMode": "https://schema.org/OfflineEventAttendanceMode",
			"location": ld{"@type": "Place", "name": place,
				"address": ld{"@type": "PostalAddress", "streetAddress": place, "addressRegion": "MD", "addressCountry": "US"}},
			"organizer": s.clubOrg(), "url": s.cfg.SiteURL + "/calendar", "isAccessibleForFree": true,
		}
		if o.Details != "" {
			e["description"] = o.Details
		}
		events = append(events, e)
	}
	if len(events) == 0 {
		return ""
	}
	return ldJSON(ld{"@context": "https://schema.org", "@graph": events})
}

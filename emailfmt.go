package main

import (
	"html"
	"html/template"
	"regexp"
	"strings"
)

// Emails are typed in the admin as plain text with a few simple marks (the
// same ones the website's pages use, plus a button), so nobody needs HTML:
//
//	**bold words**              [words to click](https://…)
//	## A heading                - a list item
//	![Photo](/media/…jpg)       [[Button words](https://…)]
//
// A blank line starts a new paragraph, and plain web addresses become links.
// The toolbar above the message box types these marks for you. emailHTML
// turns the text into the HTML email (styles inline, since email apps ignore
// style sheets), and emailText into the plain-text copy sent alongside it.

var (
	paraBreak = regexp.MustCompile(`\n[ \t]*\n`)
	urlRe     = regexp.MustCompile(`https?://[^\s<>"']*[^\s<>"'.,;:!?)\]]`)
	// Every inline mark in one pattern, so they're found left to right:
	// button, photo, link, bold, then a plain web address.
	headingRe = regexp.MustCompile(`^#{1,3} +`)
	inlineRe  = regexp.MustCompile(`\[\[([^\]]+)\]\(([^)\s]+)\)\]|!\[([^\]]*)\]\(([^)\s]+)\)|\[([^\]]+)\]\(([^)\s]+)\)|\*\*(.+?)\*\*|` + urlRe.String())
)

const (
	emailLink    = `color:#9a2b29`
	emailButton  = `display:inline-block;padding:12px 22px;background:#9a2b29;color:#ffffff;font-weight:bold;text-decoration:none;border-radius:3px`
	emailPhoto   = `display:block;width:100%;max-width:552px;height:auto;margin:4px 0;border:0`
	emailHeading = `margin:8px 0 12px;font-size:20px;line-height:1.3;font-weight:bold;color:#9a2b29`
)

// emailHTML turns an email typed in the admin into the HTML body. Addresses
// starting with / (like an uploaded photo) are made full with base.
func emailHTML(s, base string) template.HTML {
	var b strings.Builder
	for _, para := range emailParas(s) {
		lines := strings.Split(para, "\n")
		switch {
		case len(lines) == 1 && headingRe.MatchString(para):
			b.WriteString(`<h2 style="` + emailHeading + `">` + emailInline(headingRe.ReplaceAllString(para, ""), base) + "</h2>\n")
		case allListItems(lines):
			b.WriteString(`<ul style="margin:0 0 16px;padding-left:22px">`)
			for _, l := range lines {
				b.WriteString(`<li style="margin:0 0 6px">` + emailInline(strings.TrimSpace(l[2:]), base) + "</li>")
			}
			b.WriteString("</ul>\n")
		default:
			for i, l := range lines {
				lines[i] = emailInline(l, base)
			}
			b.WriteString(`<p style="margin:0 0 16px">` + strings.Join(lines, "<br>") + "</p>\n")
		}
	}
	return template.HTML(b.String())
}

// emailText is the plain-text copy of the same email: marks are taken out,
// links are written as "words (address)", and photos as "(photo)".
func emailText(s, base string) string {
	var out []string
	for _, para := range emailParas(s) {
		lines := strings.Split(para, "\n")
		for i, l := range lines {
			if len(lines) == 1 {
				l = headingRe.ReplaceAllString(l, "")
			}
			lines[i] = inlineRe.ReplaceAllStringFunc(l, func(m string) string {
				g := inlineRe.FindStringSubmatch(m)
				switch {
				case g[1] != "":
					return g[1] + ": " + fullURL(g[2], base)
				case g[4] != "":
					return "(photo)"
				case g[5] != "":
					if u := fullURL(g[6], base); u != g[5] {
						return g[5] + " (" + u + ")"
					}
					return g[5]
				case g[7] != "":
					return g[7]
				}
				return m
			})
		}
		out = append(out, strings.Join(lines, "\n"))
	}
	return strings.Join(out, "\n\n")
}

func emailParas(s string) []string {
	var out []string
	for _, p := range paraBreak.Split(strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")), -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func allListItems(lines []string) bool {
	for _, l := range lines {
		if !strings.HasPrefix(l, "- ") {
			return false
		}
	}
	return true
}

// emailInline turns the marks within a line into HTML; everything else is
// shown exactly as typed.
func emailInline(s, base string) string {
	var b strings.Builder
	last := 0
	for _, m := range inlineRe.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(html.EscapeString(s[last:m[0]]))
		last = m[1]
		g := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return s[m[2*i]:m[2*i+1]]
		}
		whole := s[m[0]:m[1]]
		switch {
		case g(1) != "":
			b.WriteString(emailAnchor(g(2), base, emailButton, html.EscapeString(g(1)), whole))
		case g(4) != "":
			if u := fullURL(g(4), base); u != "" {
				b.WriteString(`<img src="` + html.EscapeString(u) + `" alt="` + html.EscapeString(or(g(3), "Photo")) + `" width="552" style="` + emailPhoto + `">`)
			} else {
				b.WriteString(html.EscapeString(whole))
			}
		case g(5) != "":
			b.WriteString(emailAnchor(g(6), base, emailLink, html.EscapeString(g(5)), whole))
		case g(7) != "":
			b.WriteString("<strong>" + emailInline(g(7), base) + "</strong>")
		default:
			b.WriteString(emailAnchor(whole, base, emailLink, html.EscapeString(whole), whole))
		}
	}
	b.WriteString(html.EscapeString(s[last:]))
	return b.String()
}

// emailAnchor makes a link, or shows what was typed if the address isn't one
// an email can safely link to.
func emailAnchor(u, base, style, label, typed string) string {
	full := fullURL(u, base)
	if full == "" {
		return html.EscapeString(typed)
	}
	return `<a href="` + html.EscapeString(full) + `" style="` + style + `">` + label + `</a>`
}

// fullURL allows web, email, and on-site addresses, making on-site ones full
// with base. Anything else (like javascript:) gives "".
func fullURL(u, base string) string {
	switch {
	case strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//"):
		return strings.TrimRight(base, "/") + u
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"), strings.HasPrefix(u, "mailto:"):
		return u
	}
	return ""
}

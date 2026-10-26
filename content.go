package main

import (
	"errors"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"log"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Page text lives in content/*.md so club volunteers can edit it on github.com
// without touching code. Each file is one page: rides.md is served at /rides,
// home.md at /. The format is a small, forgiving subset of Markdown, explained
// for non-programmers in content/README.md.
//
// Anything the renderer can't make sense of (a missing photo, an unknown
// {placeholder}, a typo in the settings at the top) is an error that names the
// file and line, so a broken edit fails the automatic check instead of going live.

type contentPage struct {
	Path, File string
	Title      string // the browser tab title
	Desc       string
	Menu       string // label in the Info menu; empty means not in the menu
	Order      int
	Wide       bool
	People     bool          // bullet lists starting with a bold name show as people cards
	Intro      template.HTML // the # heading and everything before the first ##
	Body       template.HTML // the ## sections
}

var (
	slugRE    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	commentRE = regexp.MustCompile(`(?s)<!--.*?-->`)
	headingRE = regexp.MustCompile(`^(#{1,3})\s+(.+?)\s*#*$`)
	bulletRE  = regexp.MustCompile(`^[-*+]\s+(.*)$`)
	numberRE  = regexp.MustCompile(`^\d+[.)]\s+(.*)$`)
	linkRE    = regexp.MustCompile(`(!?)\[([^\]]*)\]\(\s*([^)\s]*)\s*\)`)
	strongRE  = regexp.MustCompile(`\*\*(.+?)\*\*`)
	emRE      = regexp.MustCompile(`\*([^*\s][^*]*?)\*`)
	varRE     = regexp.MustCompile(`\{([a-z0-9-]+)\}`)
	widgetRE  = regexp.MustCompile(`^\{([a-z0-9-]+)\}$`)
	factRE    = regexp.MustCompile(`^\*\*([^*]+?)(?::\*\*|\*\*:)\s*(.*)$`)
	personRE  = regexp.MustCompile(`^(?:!\[[^\]]*\]\(\s*([^)\s]*)\s*\)\s*)?\*\*([^*]+)\*\*\s*[-–—,:]?\s*(.*)$`)
)

// loadContent reads every page in the content folder. A page edited in the
// admin (in edits, by file name) is used instead of the file, and pages added
// in the admin are included too.
func loadContent(content fs.FS, edits map[string]string, r *mdRenderer) ([]*contentPage, error) {
	files, err := fs.Glob(content, "*.md")
	if err != nil {
		return nil, err
	}
	var pages []*contentPage
	for _, file := range files {
		if strings.EqualFold(file, "README.md") {
			continue // the editing guide, not a page
		}
		src, edited := edits[file]
		if !edited {
			b, err := fs.ReadFile(content, file)
			if err != nil {
				return nil, err
			}
			src = string(b)
		}
		p, err := parsePage(file, src, r)
		if err != nil && edited {
			// A page edited in the admin that no longer builds (a change on GitHub
			// since removed a photo it uses, say) must not stop the whole site from
			// starting or publishing: show the GitHub version until it's fixed.
			log.Printf("page %s as edited in the admin no longer builds, showing the GitHub version: %v", file, err)
			var b []byte
			if b, err = fs.ReadFile(content, file); err == nil {
				p, err = parsePage(file, string(b), r)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("content/%s: %w", file, err)
		}
		pages = append(pages, p)
	}
	// Pages added in the admin, which have no file in the content folder.
	var added []string
	for file := range edits {
		if !slices.Contains(files, file) {
			added = append(added, file)
		}
	}
	sort.Strings(added)
	for _, file := range added {
		p, err := parsePage(file, edits[file], r)
		if err != nil {
			// Same for a page added in the admin: it has no GitHub version, so it's left out.
			log.Printf("page %s added in the admin no longer builds, leaving it out: %v", file, err)
			continue
		}
		pages = append(pages, p)
	}
	if len(pages) == 0 {
		return nil, errors.New("no pages found in the content folder")
	}
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].Order < pages[j].Order })
	return pages, nil
}

func parsePage(file, src string, r *mdRenderer) (*contentPage, error) {
	name := strings.TrimSuffix(file, ".md")
	if !slugRE.MatchString(name) {
		return nil, errors.New("file names can only use lowercase letters, numbers, and dashes (like bike-safety.md)")
	}
	p := &contentPage{File: file, Path: "/" + name, Desc: siteDescription}
	if name == "home" {
		p.Path = "/"
	}
	settings, body, bodyLine, err := splitFrontMatter(src)
	if err != nil {
		return nil, err
	}
	for _, s := range settings {
		switch s.key {
		case "title":
			p.Title = s.value
		case "description":
			p.Desc = s.value
		case "menu":
			p.Menu = s.value
		case "order":
			if p.Order, err = strconv.Atoi(s.value); err != nil {
				return nil, fmt.Errorf("line %d: order must be a whole number, like 2", s.line)
			}
		case "wide":
			p.Wide = isYes(s.value)
		case "people":
			p.People = isYes(s.value)
		default:
			return nil, fmt.Errorf("line %d: unknown setting %q (use title, description, menu, order, wide, or people)", s.line, s.key)
		}
	}
	heading, intro, rest, err := r.render(body, bodyLine, pageStyle{Home: p.Path == "/", People: p.People})
	if err != nil {
		return nil, err
	}
	if heading == "" {
		return nil, errors.New("the page needs a title line starting with # (like: # Our Rides)")
	}
	p.Intro, p.Body = intro, rest
	switch {
	case p.Path == "/" && p.Title == "":
		p.Title = siteName
	case p.Path == "/":
	case p.Title == "":
		p.Title = heading + " — " + siteName
	default:
		p.Title += " — " + siteName
	}
	return p, nil
}

type setting struct {
	key, value string
	line       int
}

// splitFrontMatter separates the optional settings block at the top of a page:
//
//	---
//	title: Our Rides
//	menu: Our Rides
//	---
func splitFrontMatter(src string) ([]setting, string, int, error) {
	src = strings.TrimPrefix(src, "\xef\xbb\xbf") // the invisible marker some editors add at the start
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, src, 1, nil
	}
	var out []setting
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			return out, strings.Join(lines[i+1:], "\n"), i + 2, nil
		}
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, "", 0, fmt.Errorf("line %d: settings look like \"title: Our Rides\"", i+1)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out = append(out, setting{strings.ToLower(strings.TrimSpace(k)), v, i + 1})
	}
	return nil, "", 0, errors.New("the settings at the top need a closing --- line")
}

func isYes(s string) bool {
	switch strings.ToLower(s) {
	case "yes", "true", "y", "on":
		return true
	}
	return false
}

// ---- Markdown ----

type mdRenderer struct {
	vars    map[string]string        // {strava}, {dues-family}, … in text and links
	widgets map[string]template.HTML // {membership-plans} on a line by itself
	photos  fs.FS                    // content/photos, to check photos exist
	version string                   // added to photo URLs so browsers pick up new ones
}

type mdBlock struct {
	kind  string // h1, h2, h3, p, ul, ol
	text  string
	items []string
	line  int
}

// pageStyle is what a page's layout changes about how its text is shown.
type pageStyle struct {
	Home   bool // sections are full-width bands
	People bool // bullet lists starting with a bold name become people cards
}

// render turns a page body into HTML. It returns the text of the # heading,
// everything before the first ## (the intro), and the ## sections. On the
// home page sections are full-width bands; elsewhere they sit in the article.
func (r *mdRenderer) render(src string, firstLine int, style pageStyle) (string, template.HTML, template.HTML, error) {
	home := style.Home
	// Hidden notes for editors: <!-- like this --> (keep line numbers right).
	src = commentRE.ReplaceAllStringFunc(src, func(c string) string { return strings.Repeat("\n", strings.Count(c, "\n")) })

	var blocks []*mdBlock
	var cur *mdBlock
	flush := func() { cur = nil }
	for i, raw := range strings.Split(src, "\n") {
		n := firstLine + i
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			flush()
		case headingRE.MatchString(line):
			m := headingRE.FindStringSubmatch(line)
			blocks = append(blocks, &mdBlock{kind: "h" + strconv.Itoa(len(m[1])), text: m[2], line: n})
			flush()
		case bulletRE.MatchString(line) || numberRE.MatchString(line):
			kind, item := "ul", ""
			if m := numberRE.FindStringSubmatch(line); m != nil {
				kind, item = "ol", m[1]
			} else {
				item = bulletRE.FindStringSubmatch(line)[1]
			}
			if cur == nil || cur.kind != kind {
				cur = &mdBlock{kind: kind, line: n}
				blocks = append(blocks, cur)
			}
			cur.items = append(cur.items, item)
		case cur != nil && (cur.kind == "ul" || cur.kind == "ol"):
			cur.items[len(cur.items)-1] += " " + line // a list item that wraps onto the next line
		case cur != nil:
			cur.text += " " + line
		default:
			cur = &mdBlock{kind: "p", text: line, line: n}
			blocks = append(blocks, cur)
		}
	}

	var intro, body strings.Builder
	out := &intro
	var heading string
	ids := map[string]int{}
	inSection, inCols, inArticle := false, false, false
	closeArticle := func() {
		if inArticle {
			out.WriteString("</article>\n")
			inArticle = false
		}
	}
	closeCols := func() {
		closeArticle()
		if inCols {
			out.WriteString("</div>\n")
			inCols = false
		}
	}
	sectionClass := "section"
	if home {
		sectionClass = "band"
	}
	prev := ""
	for _, b := range blocks {
		fail := func(format string, a ...any) error {
			return fmt.Errorf("line %d: %s", b.line, fmt.Sprintf(format, a...))
		}
		switch b.kind {
		case "h1":
			if heading != "" {
				return "", "", "", fail("a page can only have one title line starting with a single #. Use ## for section headings")
			}
			if inSection {
				return "", "", "", fail("the # title line must come before any ## section")
			}
			h, err := r.inline(b.text)
			if err != nil {
				return "", "", "", fail("%v", err)
			}
			heading = plainText(b.text)
			out.WriteString("<h1>" + h + "</h1>\n")
		case "h2":
			closeCols()
			if inSection {
				out.WriteString("</section>\n")
			}
			out = &body
			inSection = true
			h, err := r.inline(b.text)
			if err != nil {
				return "", "", "", fail("%v", err)
			}
			fmt.Fprintf(out, "<section class=\"%s\" id=\"%s\">\n<h2>%s</h2>\n", sectionClass, uniqueID(ids, b.text), h)
		case "h3":
			closeArticle()
			if !inCols {
				out.WriteString("<div class=\"cols\">\n")
				inCols = true
			}
			h, err := r.inline(b.text)
			if err != nil {
				return "", "", "", fail("%v", err)
			}
			fmt.Fprintf(out, "<article id=\"%s\">\n<h3>%s</h3>\n", uniqueID(ids, b.text), h)
			inArticle = true
		case "p":
			class := ""
			switch {
			case prev == "h1":
				class = "lead"
			case prev == "h2" && home:
				class = "intro"
			}
			h, err := r.paragraph(b.text, class)
			if err != nil {
				return "", "", "", fail("%v", err)
			}
			out.WriteString(h)
		case "ul", "ol":
			h, err := r.list(b.kind, b.items, style.People)
			if err != nil {
				return "", "", "", fail("%v", err)
			}
			out.WriteString(h)
		}
		prev = b.kind
	}
	closeCols()
	if inSection {
		out.WriteString("</section>\n")
	}
	return heading, template.HTML(intro.String()), template.HTML(body.String()), nil
}

// paragraph renders one paragraph. Three kinds of line get special treatment:
// a {widget} on its own, a photo on its own, and a line of nothing but links,
// which becomes a button (one link) or a row of links (several).
func (r *mdRenderer) paragraph(text, class string) (string, error) {
	attr := ""
	if class != "" {
		attr = ` class="` + class + `"`
	}
	if m := widgetRE.FindStringSubmatch(text); m != nil {
		if w, ok := r.widgets[m[1]]; ok {
			return string(w) + "\n", nil
		}
	}
	if m := linkRE.FindAllStringSubmatchIndex(text, -1); len(m) > 0 && strings.TrimSpace(linkRE.ReplaceAllString(text, "")) == "" {
		photos := 0
		for _, x := range m {
			if text[x[2]:x[3]] == "!" {
				photos++
			}
		}
		switch {
		case len(m) == 1 && photos == 1:
			return r.inline(text) // a photo on its own line
		case photos > 0:
			h, err := r.inline(text) // photos and links together: an ordinary paragraph
			return "<p" + attr + ">" + h + "</p>\n", err
		}
		var links []string
		for _, s := range linkRE.FindAllStringSubmatch(text, -1) {
			if s[1] == "!" {
				continue
			}
			href, err := r.url(s[3])
			if err != nil {
				return "", err
			}
			if href == "" {
				continue // a link whose setting is blank, like {donate}: leave it out
			}
			label, err := r.text(s[2])
			if err != nil {
				return "", err
			}
			links = append(links, anchor(href, label, len(m) == 1))
		}
		switch {
		case len(links) == 0:
			return "", nil
		case len(m) == 1:
			return "<p" + attr + ">" + links[0] + "</p>\n", nil
		default:
			return `<p class="more">` + strings.Join(links, "") + "</p>\n", nil
		}
	}
	h, err := r.inline(text)
	if err != nil {
		return "", err
	}
	return "<p" + attr + ">" + h + "</p>\n", nil
}

// list renders a numbered list, a plain bullet list, or one of two special
// bullet lists: facts ("- **Pace:** 20 mph") and, on pages with the people
// setting, people ("- **Jane Smith** President").
func (r *mdRenderer) list(kind string, items []string, people bool) (string, error) {
	var b strings.Builder
	if kind == "ol" {
		b.WriteString("<ol class=\"rules\">\n")
		for _, it := range items {
			h, err := r.inline(it)
			if err != nil {
				return "", err
			}
			b.WriteString("<li>" + h + "</li>\n")
		}
		b.WriteString("</ol>\n")
		return b.String(), nil
	}
	if all(items, factRE) {
		b.WriteString("<dl class=\"facts\">\n")
		for _, it := range items {
			m := factRE.FindStringSubmatch(it)
			dt, err := r.inline(m[1])
			if err != nil {
				return "", err
			}
			dd, err := r.inline(m[2])
			if err != nil {
				return "", err
			}
			b.WriteString("<div><dt>" + dt + "</dt><dd>" + dd + "</dd></div>\n")
		}
		b.WriteString("</dl>\n")
		return b.String(), nil
	}
	if people && all(items, personRE) {
		b.WriteString("<ul class=\"board\">\n")
		for _, it := range items {
			m := personRE.FindStringSubmatch(it)
			avatar := `<span class="avatar" aria-hidden="true"></span>`
			if m[1] != "" {
				src, err := r.url(m[1])
				if err != nil {
					return "", err
				}
				avatar = `<img class="avatar" src="` + html.EscapeString(src) + `" alt="" width="160" height="160" loading="lazy" decoding="async">`
			}
			name, err := r.inline(m[2])
			if err != nil {
				return "", err
			}
			role, err := r.inline(m[3])
			if err != nil {
				return "", err
			}
			b.WriteString("<li>" + avatar + "<strong>" + name + "</strong><span>" + role + "</span></li>\n")
		}
		b.WriteString("</ul>\n")
		return b.String(), nil
	}
	b.WriteString("<ul>\n")
	for _, it := range items {
		h, err := r.inline(it)
		if err != nil {
			return "", err
		}
		b.WriteString("<li>" + h + "</li>\n")
	}
	b.WriteString("</ul>\n")
	return b.String(), nil
}

func all(items []string, re *regexp.Regexp) bool {
	for _, it := range items {
		if !re.MatchString(it) {
			return false
		}
	}
	return true
}

// inline renders links, photos, **bold**, *italics*, and {placeholders}.
func (r *mdRenderer) inline(s string) (string, error) {
	var b strings.Builder
	last := 0
	for _, m := range linkRE.FindAllStringSubmatchIndex(s, -1) {
		t, err := r.text(s[last:m[0]])
		if err != nil {
			return "", err
		}
		b.WriteString(t)
		last = m[1]
		isImg, label, raw := s[m[2]:m[3]] == "!", s[m[4]:m[5]], s[m[6]:m[7]]
		href, err := r.url(raw)
		if err != nil {
			return "", err
		}
		if isImg {
			if href == "" {
				return "", fmt.Errorf("the photo %q has no file name", label)
			}
			fmt.Fprintf(&b, `<img src="%s" alt="%s" loading="lazy" decoding="async">`, html.EscapeString(href), html.EscapeString(label))
			continue
		}
		t, err = r.text(label)
		if err != nil {
			return "", err
		}
		if href == "" {
			b.WriteString(t) // blank setting: keep the words, drop the link
			continue
		}
		b.WriteString(anchor(href, t, false))
	}
	t, err := r.text(s[last:])
	if err != nil {
		return "", err
	}
	b.WriteString(t)
	return b.String(), nil
}

// text escapes plain text and applies **bold**, *italics*, and {placeholders}.
func (r *mdRenderer) text(s string) (string, error) {
	s = html.EscapeString(s)
	s = strongRE.ReplaceAllString(s, "<strong>$1</strong>")
	s = emRE.ReplaceAllString(s, "<em>$1</em>")
	var err error
	s = varRE.ReplaceAllStringFunc(s, func(v string) string {
		val, ok := r.vars[v[1:len(v)-1]]
		if !ok {
			if err == nil {
				err = unknownVar(v)
			}
			return v
		}
		return html.EscapeString(val)
	})
	return s, err
}

// url resolves a link or photo address. Blank means "leave the link out".
func (r *mdRenderer) url(raw string) (string, error) {
	var err error
	u := varRE.ReplaceAllStringFunc(raw, func(v string) string {
		val, ok := r.vars[v[1:len(v)-1]]
		if !ok && err == nil {
			err = unknownVar(v)
		}
		return val
	})
	if err != nil || u == "" {
		return "", err
	}
	lower := strings.ToLower(u)
	switch {
	case strings.HasPrefix(lower, "photos/"):
		if _, err := fs.Stat(r.photos, strings.TrimPrefix(u, "photos/")); err != nil {
			return "", fmt.Errorf("can't find the photo %s. Check the spelling (capital letters matter) and that it's in content/photos", u)
		}
		return "/" + u + "?v=" + r.version, nil
	case strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "http://"),
		strings.HasPrefix(lower, "mailto:"), strings.HasPrefix(lower, "tel:"),
		strings.HasPrefix(u, "/"), strings.HasPrefix(u, "#"):
		return u, nil
	case strings.Contains(u, ":"):
		return "", fmt.Errorf("links must start with https://, mailto:, or / (got %s)", u)
	}
	// A link to another page by its file name, like rides.md or rides#weekend-rides.
	page, frag, _ := strings.Cut(u, "#")
	page = strings.TrimSuffix(page, ".md")
	if strings.ContainsAny(page, "./") {
		return "", fmt.Errorf("web addresses need to start with https:// (got %s)", u)
	}
	if page == "home" {
		page = ""
	}
	if frag != "" {
		frag = "#" + frag
	}
	return "/" + page + frag, nil
}

func unknownVar(v string) error {
	return fmt.Errorf("unknown placeholder %s (see the list in content/README.md)", v)
}

// anchor builds a link. Links to other sites open in a new tab.
func anchor(href, label string, button bool) string {
	a := `<a href="` + html.EscapeString(href) + `"`
	if button {
		a += ` class="btn"`
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		a += ` target="_blank" rel="noopener"`
	}
	return a + ">" + label + "</a>"
}

// plainText strips Markdown marks, for titles and section ids.
func plainText(s string) string {
	s = linkRE.ReplaceAllString(s, "$2")
	return strings.TrimSpace(strings.NewReplacer("**", "", "*", "").Replace(s))
}

// uniqueID turns a heading into an address for links like /rides#weekend-rides.
func uniqueID(seen map[string]int, heading string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(plainText(heading)) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
			dash = false
		case c == '’' || c == '\'':
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	id := strings.TrimSuffix(b.String(), "-")
	if id == "" {
		id = "section"
	}
	seen[id]++
	if n := seen[id]; n > 1 {
		id += "-" + strconv.Itoa(n)
	}
	return id
}

package main

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Instagram posts on the home page and in newsletters. The home page shows the
// posts picked on the admin's Instagram tab, or with Instagram connected and
// none picked, the latest few; each newsletter issue can show up to three.
//
// With Instagram connected (instafeed.go), the site draws the posts itself
// from its own copies: all the same size, with videos that play on the page.
// A post it has no copy of falls back to Instagram's embed, where Instagram's
// script turns a link into the post (or it stays a plain link without it).

const instagramProfile = "https://www.instagram.com/paxvelo/"

const maxHomeInstagram = 6

var instagramPostRE = regexp.MustCompile(`^https?://(?:www\.)?instagram\.com/(?:[A-Za-z0-9_.]+/)?(p|reel|tv)/([A-Za-z0-9_-]+)`)

// parseInstagramPosts reads post links, one per line, and returns them in a
// standard form. Anything that isn't a post link is an error.
func parseInstagramPosts(text string, limit int) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := instagramPostRE.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("“%s” isn’t an Instagram post link. Open the post on Instagram, tap ⋯ → Copy link, and paste that", line)
		}
		u := "https://www.instagram.com/" + m[1] + "/" + m[2] + "/"
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	if len(out) > limit {
		return nil, fmt.Errorf("there’s room for %d Instagram posts here; remove %d", limit, len(out)-limit)
	}
	return out, nil
}

// instaRowTpl draws a row of posts. Instagram's script (and a frame per post)
// is far heavier than the rest of the page, so site.js loads it, only for a
// row with embeds, when the row is about to scroll into view.
var instaRowTpl = template.Must(template.New("").Parse(`<div class="insta{{if .Embeds}} insta-embeds{{end}}">
{{- range .Cards}}{{with .Post}}<figure class="ig">
  <div class="ig-media">{{if .Video}}<video controls playsinline preload="none" poster="/instagram/{{.Cover}}" src="/instagram/{{.Video}}" aria-label="Video from Instagram{{with .CaptionLine}}: {{.}}{{end}}"></video>
  {{- else}}<a href="{{.Link}}" target="_blank" rel="noopener"><img src="/instagram/{{.Cover}}" alt="{{with .CaptionLine}}{{.}}{{else}}Post from Instagram{{end}}" loading="lazy" decoding="async">{{if .IsVideo}}<span class="ig-play" aria-hidden="true"></span>{{end}}</a>{{end}}
  {{- if gt .Count 1}}<span class="ig-count">1 / {{.Count}}</span>{{end}}</div>
  <figcaption>{{with .CaptionLine}}<p>{{.}}</p>{{end}}<a href="{{.Link}}" target="_blank" rel="noopener">{{if and .IsVideo (not .Video)}}Watch on Instagram{{else}}View on Instagram{{end}}</a>{{with .DateLabel}} · {{.}}{{end}}</figcaption>
</figure>{{else}}<blockquote class="instagram-media" data-instgrm-permalink="{{.Link}}" data-instgrm-version="14"><a href="{{.Link}}" target="_blank" rel="noopener">View this post on Instagram</a></blockquote>{{end}}
{{- end}}</div>`))

// instagramRow is the markup for a row of posts: the picked ones, or the
// latest few from the feed when none are picked.
func instagramRow(picked []string, feed []InstaPost, latest int) template.HTML {
	cards := shownPosts(picked, feed, latest)
	if len(cards) == 0 {
		return ""
	}
	var b strings.Builder
	if err := instaRowTpl.Execute(&b, struct {
		Cards  []instaCard
		Embeds bool
	}{cards, usedEmbed(cards)}); err != nil {
		return ""
	}
	return template.HTML(b.String())
}

// instagramWidget is what {instagram} becomes on a page: the posts and a link
// to follow the club.
func instagramWidget(posts []string, feed []InstaPost) template.HTML {
	return instagramRow(posts, feed, instaLatest) + template.HTML(`<p><a class="btn" href="`+instagramProfile+`" target="_blank" rel="noopener">Follow @paxvelo on Instagram</a></p>`)
}

// ---- Admin ----

func (a *App) instagramAdmin(w http.ResponseWriter, r *http.Request) {
	v := a.instagramView()
	v.InstagramText = strings.Join(v.Instagram, "\n")
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "instagram", v)
}

func (a *App) instagramView() adminView {
	v := adminView{Title: "Instagram", Tab: "instagram", Instagram: a.store.Instagram(), InstaFeed: a.store.InstaFeed()}
	if l, ok := a.store.InstagramLink(); ok {
		v.InstagramLink = &l
	}
	return v
}

func (a *App) saveInstagram(w http.ResponseWriter, r *http.Request) {
	text := r.FormValue("posts")
	posts, err := parseInstagramPosts(text, maxHomeInstagram)
	if err != nil {
		v := a.instagramView()
		v.InstagramText, v.Error = text, "Not saved: "+err.Error()+"."
		a.renderAdmin(w, r, http.StatusOK, "instagram", v)
		return
	}
	if err := a.store.SetInstagram(posts); err != nil {
		back(w, r, "/admin/instagram?err=save")
		return
	}
	if _, ok := a.store.InstagramLink(); ok {
		// Download the newly picked posts before the pages are rebuilt.
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		a.syncInstagram(ctx)
		cancel()
	}
	if err := a.rebuildContent(); err != nil {
		back(w, r, "/admin/instagram?err=save")
		return
	}
	back(w, r, "/admin/instagram?ok=instagram")
}

// ---- Store ----

func (s *Store) Instagram() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.d.Instagram...)
}

func (s *Store) SetInstagram(posts []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Instagram = posts
	return s.save()
}

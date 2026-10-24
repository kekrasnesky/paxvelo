package main

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"regexp"
	"strings"
)

// Instagram posts are shown with Instagram's own embed: an admin pastes a
// post's link, and Instagram's script turns it into the full post (photo or
// video, caption, likes). Without the script (blocked, or slow to load) it
// stays a plain link to the post. The home page shows the posts picked on the
// admin's Instagram tab; each newsletter issue can show up to three of its own.
//
// Showing the latest posts automatically would need Instagram's Graph API (a
// Business account, a Facebook Page, and a Meta developer app); picking posts
// by hand needs none of that.

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

// instagramEmbeds is the markup for a row of embedded posts.
func instagramEmbeds(posts []string) template.HTML {
	if len(posts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="insta">`)
	for _, u := range posts {
		e := html.EscapeString(u)
		fmt.Fprintf(&b, `<blockquote class="instagram-media" data-instgrm-permalink="%s" data-instgrm-version="14"><a href="%s" target="_blank" rel="noopener">View this post on Instagram</a></blockquote>`, e, e)
	}
	b.WriteString(`</div><script async src="https://www.instagram.com/embed.js"></script>`)
	return template.HTML(b.String())
}

// instagramWidget is what {instagram} becomes on a page: the chosen posts and
// a link to follow the club.
func instagramWidget(posts []string) template.HTML {
	return instagramEmbeds(posts) + template.HTML(`<p><a class="btn" href="`+instagramProfile+`" target="_blank" rel="noopener">Follow @paxvelo on Instagram</a></p>`)
}

// ---- Admin ----

func (a *App) instagramAdmin(w http.ResponseWriter, r *http.Request) {
	v := adminView{Title: "Instagram", Tab: "instagram", Instagram: a.store.Instagram()}
	v.InstagramText = strings.Join(v.Instagram, "\n")
	v.Flash, v.Error = flashFrom(r)
	a.renderAdmin(w, r, http.StatusOK, "instagram", v)
}

func (a *App) saveInstagram(w http.ResponseWriter, r *http.Request) {
	text := r.FormValue("posts")
	posts, err := parseInstagramPosts(text, maxHomeInstagram)
	if err != nil {
		v := adminView{Title: "Instagram", Tab: "instagram", Instagram: a.store.Instagram(), InstagramText: text, Error: "Not saved: " + err.Error() + "."}
		a.renderAdmin(w, r, http.StatusOK, "instagram", v)
		return
	}
	if err := a.store.SetInstagram(posts); err != nil {
		back(w, r, "/admin/instagram?err=save")
		return
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

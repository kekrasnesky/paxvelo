package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The club's Instagram feed, through Behold (behold.so), which connects to
// Instagram with its own Meta app and hands out the latest posts as a JSON
// feed. Meta's own API needs a Meta developer app and a business portfolio
// holding the Instagram account, which Meta wouldn't let the club finish.
//
// An admin pastes the feed's link once. Every few hours the site reads it and
// downloads the posts it shows, so they appear in the site's own layout: all
// the same size, with videos that play on the page. Instagram's own embed
// can't do that, since its player won't start inside other websites. Each
// read of the feed counts toward Behold's monthly limit (1,200 on the free
// plan, which updates the feed once a day), so the site reads it 8 times a
// day. Posts the site hasn't downloaded (older ones, or a reel whose music
// Instagram won't hand out) fall back to Instagram's embed or a link.

// InstagramLink is the connected Instagram feed.
type InstagramLink struct {
	Feed       string     `json:"feed"` // the Behold feed's link
	Username   string     `json:"username"`
	LastSync   *time.Time `json:"last_sync,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	LastResult string     `json:"last_result,omitempty"`
}

// InstaPost is one of the club's Instagram posts.
type InstaPost struct {
	ID       string    `json:"id"`
	Code     string    `json:"code"` // the post's short code, from its link
	Kind     string    `json:"kind"` // IMAGE, VIDEO or CAROUSEL_ALBUM
	Caption  string    `json:"caption,omitempty"`
	Link     string    `json:"link"`
	Time     time.Time `json:"time"`
	Count    int       `json:"count,omitempty"`     // a carousel's photos and videos
	CoverURL string    `json:"cover_url,omitempty"` // on Instagram's servers, for downloading; these expire
	VideoURL string    `json:"video_url,omitempty"`
	Cover    string    `json:"cover,omitempty"` // downloaded copies, in the instagram folder
	Video    string    `json:"video,omitempty"`
	IsVideo  bool      `json:"is_video,omitempty"` // what's shown first is a video (a reel, or a carousel starting with one)
}

const (
	// How often the site reads the feed.
	instaSyncEvery = 3 * time.Hour
	// The home page shows this many of the latest posts when none are picked.
	instaLatest = 3
	// A video bigger than this stays on Instagram.
	maxInstaVideo = 100 << 20
)

var instaCodeRE = regexp.MustCompile(`instagram\.com/(?:[A-Za-z0-9_.]+/)?(?:p|reel|tv)/([A-Za-z0-9_-]+)`)

// instaCode is a post link's short code, which stays the same whichever
// form of the link was pasted.
func instaCode(link string) string {
	if m := instaCodeRE.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	return ""
}

func (a *App) instaDir() string { return filepath.Join(a.cfg.DataDir, "instagram") }

// ---- Store ----

func (s *Store) InstagramLink() (InstagramLink, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.InstagramLink == nil {
		return InstagramLink{}, false
	}
	return *s.d.InstagramLink, true
}

func (s *Store) SetInstagramLink(l *InstagramLink) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.InstagramLink = l
	if l == nil {
		s.d.InstaFeed = nil
	}
	return s.save()
}

func (s *Store) updateInstagramLink(change func(l *InstagramLink)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.InstagramLink != nil {
		change(s.d.InstagramLink)
		s.saveOrLog()
	}
}

// InstaFeed is the club's latest Instagram posts, newest first.
func (s *Store) InstaFeed() []InstaPost {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]InstaPost(nil), s.d.InstaFeed...)
}

// SetInstaFeed saves the latest posts, and reports whether anything the
// pages show changed.
func (s *Store) SetInstaFeed(feed []InstaPost) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	shown := func(f []InstaPost) string {
		var b strings.Builder
		for _, p := range f {
			fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%d\n", p.ID, p.Caption, p.Cover, p.Video, p.Kind, p.Count)
		}
		return b.String()
	}
	changed := shown(s.d.InstaFeed) != shown(feed)
	s.d.InstaFeed = feed
	return changed, s.save()
}

// instaWanted is every post a page shows: the home page's picks (or the
// latest few) and each newsletter issue's.
func (s *Store) instaWanted() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, l := range s.d.Instagram {
		want[instaCode(l)] = true
	}
	if len(s.d.Instagram) == 0 {
		for i := 0; i < len(s.d.InstaFeed) && i < instaLatest; i++ {
			want[s.d.InstaFeed[i].Code] = true
		}
	}
	for _, is := range s.d.Issues {
		for _, b := range is.Blocks {
			for _, l := range b.Posts {
				want[instaCode(l)] = true
			}
		}
	}
	return want
}

// ---- Connecting ----

var beholdFeedRE = regexp.MustCompile(`^/[A-Za-z0-9_-]{6,64}$`)

// instagramConnect saves the pasted feed link, once reading it works.
func (a *App) instagramConnect(w http.ResponseWriter, r *http.Request) {
	feed := strings.TrimRight(strings.TrimSpace(r.FormValue("feed")), "/")
	id, ok := strings.CutPrefix(feed, a.cfg.InstagramFeeds)
	if !ok || !beholdFeedRE.MatchString(id) {
		back(w, r, "/admin/instagram?err=igfeed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	f, err := readBehold(ctx, feed)
	if err != nil {
		log.Printf("instagram connect: %v", err)
		back(w, r, "/admin/instagram?err=igconnect")
		return
	}
	if err := a.store.SetInstagramLink(&InstagramLink{Feed: feed, Username: f.Username}); err != nil {
		back(w, r, "/admin/instagram?err=save")
		return
	}
	if err := a.syncInstagram(ctx); err != nil {
		back(w, r, "/admin/instagram?ok=igconnected-error")
		return
	}
	back(w, r, "/admin/instagram?ok=igconnected")
}

func (a *App) instagramDisconnect(w http.ResponseWriter, r *http.Request) {
	a.store.SetInstagramLink(nil)
	os.RemoveAll(a.instaDir())
	a.rebuildContent()
	back(w, r, "/admin/instagram?ok=igdisconnected")
}

func (a *App) instagramSyncNow(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := a.syncInstagram(ctx); err != nil {
		back(w, r, "/admin/instagram?err=igsync")
		return
	}
	back(w, r, "/admin/instagram?ok=igsynced")
}

// ---- Reading the feed ----

var instaClient = &http.Client{Timeout: 30 * time.Second}

// beholdFeed is what Behold's feed link answers.
type beholdFeed struct {
	Username string        `json:"username"`
	Posts    []beholdMedia `json:"posts"`
}

type beholdMedia struct {
	ID        string `json:"id"`
	Kind      string `json:"mediaType"`
	Caption   string `json:"caption"`
	Pruned    string `json:"prunedCaption"` // without the hashtags
	MediaURL  string `json:"mediaUrl"`      // a video's file, or the photo
	Thumbnail string `json:"thumbnailUrl"`  // a video's cover
	Permalink string `json:"permalink"`
	Timestamp string `json:"timestamp"`
	Sizes     struct {
		Large struct {
			MediaURL string `json:"mediaUrl"`
		} `json:"large"`
	} `json:"sizes"`
	Children []beholdMedia `json:"children"`
}

func readBehold(ctx context.Context, feed string) (*beholdFeed, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", feed, nil)
	resp, err := instaClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the feed answered %s", resp.Status)
	}
	var f beholdFeed
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&f); err != nil {
		return nil, fmt.Errorf("reading the feed: %w", err)
	}
	return &f, nil
}

// post turns what the feed says about a post into the site's record of it.
func (m beholdMedia) post() InstaPost {
	caption := m.Pruned
	if caption == "" {
		caption = m.Caption
	}
	p := InstaPost{ID: m.ID, Code: instaCode(m.Permalink), Kind: m.Kind, Caption: strings.TrimSpace(caption), Link: m.Permalink}
	p.Time, _ = time.Parse("2006-01-02T15:04:05-0700", m.Timestamp)
	first := m
	if m.Kind == "CAROUSEL_ALBUM" && len(m.Children) > 0 {
		first, p.Count = m.Children[0], len(m.Children)
	}
	// Behold's resized cover (on its own server) is smaller than Instagram's.
	p.CoverURL = first.Sizes.Large.MediaURL
	if first.Kind == "VIDEO" {
		// Instagram leaves out the video of a reel with music from its library.
		p.IsVideo, p.VideoURL = true, first.MediaURL
		if p.CoverURL == "" {
			p.CoverURL = cmp.Or(first.Thumbnail, m.Thumbnail)
		}
	} else if p.CoverURL == "" {
		p.CoverURL = first.MediaURL
	}
	return p
}

// syncInstagram reads the latest posts and downloads the ones the site shows.
func (a *App) syncInstagram(ctx context.Context) error {
	n, err := a.readInstagram(ctx)
	now := time.Now().UTC()
	a.store.updateInstagramLink(func(l *InstagramLink) {
		l.LastSync, l.LastError, l.LastResult = &now, "", fmt.Sprintf("%d posts", n)
		if err != nil {
			l.LastError, l.LastResult = err.Error(), ""
		}
	})
	if err != nil {
		log.Printf("instagram sync: %v", err)
	}
	return err
}

func (a *App) readInstagram(ctx context.Context) (int, error) {
	link, ok := a.store.InstagramLink()
	if !ok {
		return 0, errors.New("Instagram isn’t connected")
	}
	page, err := readBehold(ctx, link.Feed)
	if err != nil {
		return 0, err
	}
	if page.Username != "" && page.Username != link.Username {
		a.store.updateInstagramLink(func(l *InstagramLink) { l.Username = page.Username })
	}
	old := map[string]InstaPost{}
	for _, p := range a.store.InstaFeed() {
		old[p.ID] = p
	}
	var feed []InstaPost
	for _, m := range page.Posts {
		p := m.post()
		if p.Code == "" {
			continue
		}
		if o, ok := old[p.ID]; ok {
			p.Cover, p.Video = o.Cover, o.Video
		}
		feed = append(feed, p)
	}
	// Save first, so instaWanted sees the latest posts.
	if _, err := a.store.SetInstaFeed(feed); err != nil {
		return 0, err
	}
	a.downloadInsta(ctx, feed)
	changed, err := a.store.SetInstaFeed(feed)
	if err != nil {
		return 0, err
	}
	if changed || len(old) == 0 {
		if err := a.rebuildContent(); err != nil {
			log.Printf("instagram: rebuilding pages: %v", err)
		}
	}
	return len(feed), nil
}

// downloadInsta keeps a copy of each shown post's cover and video, since
// Instagram's own addresses for them expire, and removes copies no longer
// needed.
func (a *App) downloadInsta(ctx context.Context, feed []InstaPost) {
	dir := a.instaDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("instagram: %v", err)
		return
	}
	want := a.store.instaWanted()
	keep := map[string]bool{}
	for i := range feed {
		p := &feed[i]
		if !want[p.Code] {
			p.Cover, p.Video = "", ""
			continue
		}
		if p.Cover == "" && p.CoverURL != "" {
			name := p.ID + ".jpg"
			if u, err := url.Parse(p.CoverURL); err == nil && strings.HasSuffix(u.Path, ".webp") {
				name = p.ID + ".webp"
			}
			if err := fetchTo(ctx, p.CoverURL, filepath.Join(dir, name), 20<<20); err == nil {
				p.Cover = name
			} else {
				log.Printf("instagram: cover for %s: %v", p.Code, err)
			}
		}
		if p.Video == "" && p.VideoURL != "" {
			if err := fetchTo(ctx, p.VideoURL, filepath.Join(dir, p.ID+".mp4"), maxInstaVideo); err == nil {
				p.Video = p.ID + ".mp4"
			} else {
				log.Printf("instagram: video for %s: %v", p.Code, err)
			}
		}
		keep[p.Cover], keep[p.Video] = true, true
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !keep[e.Name()] {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// fetchTo downloads a file, at most limit bytes, without leaving a partial
// file behind.
func fetchTo(ctx context.Context, src, dst string, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, "GET", src, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("answered %s", resp.Status)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errors.New("too big")
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// instagramSyncLoop reads the feed every few hours while Instagram is connected.
func (a *App) instagramSyncLoop(ctx context.Context) {
	for {
		if _, ok := a.store.InstagramLink(); ok {
			c, cancel := context.WithTimeout(ctx, 5*time.Minute)
			a.syncInstagram(c)
			cancel()
		}
		select {
		case <-time.After(instaSyncEvery):
		case <-ctx.Done():
			return
		}
	}
}

// ---- Showing posts ----

// shownPosts is the posts for a row: the picked links, each matched to the
// feed when it's there, or the latest few when none are picked.
func shownPosts(picked []string, feed []InstaPost, latest int) []instaCard {
	byCode := map[string]InstaPost{}
	for _, p := range feed {
		byCode[p.Code] = p
	}
	var out []instaCard
	for _, l := range picked {
		c := instaCard{Link: l}
		if p, ok := byCode[instaCode(l)]; ok && p.Cover != "" {
			c.Post = &p
		}
		out = append(out, c)
	}
	if len(picked) == 0 {
		for _, p := range feed {
			if len(out) == latest {
				break
			}
			if p.Cover != "" {
				out = append(out, instaCard{Link: p.Link, Post: &p})
			}
		}
	}
	return out
}

// instaCard is one post in a row: drawn by the site when it's downloaded,
// otherwise Instagram's embed.
type instaCard struct {
	Link string
	Post *InstaPost
}

// CaptionLine is the caption's first line, cut to a tweet's length.
func (p InstaPost) CaptionLine() string {
	line, _, _ := strings.Cut(p.Caption, "\n")
	if r := []rune(line); len(r) > 140 {
		line = strings.TrimSpace(string(r[:140])) + "…"
	}
	return line
}

func (p InstaPost) DateLabel() string {
	if p.Time.IsZero() {
		return ""
	}
	return p.Time.Format("Jan 2, 2006")
}

// usedEmbed reports whether any of these posts falls back to Instagram's embed.
func usedEmbed(cards []instaCard) bool {
	return slices.ContainsFunc(cards, func(c instaCard) bool { return c.Post == nil })
}

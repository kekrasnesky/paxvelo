package main

import (
	"context"
	"crypto/hmac"
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
	"strconv"
	"strings"
	"time"
)

// The club's Instagram feed, through Instagram's API ("Instagram API with
// Unified Login", the only setup Meta offers new apps). An admin connects the
// @paxvelo account once (it must be a Business or Creator account) through
// the Meta app's login configuration; every hour the site reads the latest
// posts and downloads the ones it shows, so they appear in the site's own
// layout: all the same size, with videos that play on the page. Instagram's
// own embed can't do that, since its player won't start inside other websites.
//
// The login gives a key for the club's business portfolio that doesn't run
// out (the configuration's choice). Posts the site hasn't downloaded (older
// ones, or a reel whose music Instagram won't hand out) fall back to
// Instagram's embed or a link.

// InstagramLink is the connected Instagram account.
type InstagramLink struct {
	Username   string     `json:"username"`
	AccountID  string     `json:"account_id"`
	Token      string     `json:"token"`
	Expires    time.Time  `json:"expires,omitzero"` // zero: the key doesn't run out
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
	// How many recent posts the site keeps track of, so admins can pick from them.
	instaFeedSize = 30
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

func (a *App) instagramReady() bool {
	return a.cfg.InstagramAppID != "" && a.cfg.InstagramAppSecret != "" && a.cfg.InstagramConfigID != ""
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

func (a *App) instagramConnect(w http.ResponseWriter, r *http.Request) {
	if !a.instagramReady() {
		back(w, r, "/admin/instagram?err=noinstagram")
		return
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	q := url.Values{
		"client_id": {a.cfg.InstagramAppID}, "config_id": {a.cfg.InstagramConfigID}, "redirect_uri": {a.instagramRedirect()},
		"response_type": {"code"}, "override_default_response_type": {"true"}, "state": {ts + "." + sign(a.riderKey(), "instagram|"+ts)},
	}
	http.Redirect(w, r, a.cfg.InstagramAuth+"/dialog/oauth?"+q.Encode(), http.StatusSeeOther)
}

func (a *App) instagramRedirect() string { return a.cfg.SiteURL + "/admin/instagram/callback" }

func (a *App) instagramCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ts, sig, _ := strings.Cut(q.Get("state"), ".")
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || !hmac.Equal([]byte(sig), []byte(sign(a.riderKey(), "instagram|"+ts))) || time.Since(time.Unix(n, 0)) > 15*time.Minute {
		back(w, r, "/admin/instagram?err=igconnect")
		return
	}
	code := strings.TrimSuffix(q.Get("code"), "#_")
	if code == "" {
		back(w, r, "/admin/instagram?err=igdenied")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	link, err := a.instagramLogin(ctx, code)
	if err != nil {
		log.Printf("instagram connect: %v", err)
		back(w, r, "/admin/instagram?err=igconnect")
		return
	}
	if err := a.store.SetInstagramLink(link); err != nil {
		back(w, r, "/admin/instagram?err=save")
		return
	}
	if err := a.syncInstagram(ctx); err != nil {
		back(w, r, "/admin/instagram?ok=igconnected-error")
		return
	}
	back(w, r, "/admin/instagram?ok=igconnected")
}

// instagramLogin trades the code from the login for the club's key, and
// finds the Instagram account it was given.
func (a *App) instagramLogin(ctx context.Context, code string) (*InstagramLink, error) {
	q := url.Values{"client_id": {a.cfg.InstagramAppID}, "client_secret": {a.cfg.InstagramAppSecret},
		"redirect_uri": {a.instagramRedirect()}, "code": {code}}
	req, _ := http.NewRequestWithContext(ctx, "GET", a.cfg.InstagramAPI+instaVersion+"/oauth/access_token?"+q.Encode(), nil)
	var k struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := instaDo(req, &k); err != nil {
		return nil, fmt.Errorf("code: %w", err)
	}
	if k.AccessToken == "" {
		return nil, errors.New("Instagram sent no key")
	}
	link := &InstagramLink{Token: k.AccessToken}
	if k.ExpiresIn > 0 {
		link.Expires = time.Now().UTC().Add(time.Duration(k.ExpiresIn) * time.Second)
	}
	var accounts struct {
		Data []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := a.instaGet(ctx, link.Token, "/me/assigned_instagram_accounts?fields=id,username", &accounts); err != nil {
		return nil, fmt.Errorf("account: %w", err)
	}
	if len(accounts.Data) == 0 {
		return nil, errors.New("no Instagram account was shared")
	}
	link.AccountID, link.Username = accounts.Data[0].ID, accounts.Data[0].Username
	return link, nil
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

// instaVersion is the version of Meta's API the site was written for.
const instaVersion = "/v26.0"

func (a *App) instaGet(ctx context.Context, token, path string, out any) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", a.cfg.InstagramAPI+instaVersion+path+sep+"access_token="+url.QueryEscape(token), nil)
	return instaDo(req, out)
}

var instaClient = &http.Client{Timeout: 30 * time.Second}

func instaDo(req *http.Request, out any) error {
	resp, err := instaClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		if e.Error.Message != "" {
			return fmt.Errorf("Instagram answered %s: %s", resp.Status, e.Error.Message)
		}
		return fmt.Errorf("Instagram answered %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type instaMedia struct {
	ID        string `json:"id"`
	Kind      string `json:"media_type"`
	Caption   string `json:"caption"`
	MediaURL  string `json:"media_url"`
	Thumbnail string `json:"thumbnail_url"`
	Permalink string `json:"permalink"`
	Timestamp string `json:"timestamp"`
	Children  *struct {
		Data []instaMedia `json:"data"`
	} `json:"children"`
}

// post turns what Instagram says about a post into the site's record of it.
func (m instaMedia) post() InstaPost {
	p := InstaPost{ID: m.ID, Code: instaCode(m.Permalink), Kind: m.Kind, Caption: strings.TrimSpace(m.Caption), Link: m.Permalink}
	p.Time, _ = time.Parse("2006-01-02T15:04:05-0700", m.Timestamp)
	first := m
	if m.Kind == "CAROUSEL_ALBUM" && m.Children != nil && len(m.Children.Data) > 0 {
		first, p.Count = m.Children.Data[0], len(m.Children.Data)
	}
	if first.Kind == "VIDEO" {
		// Instagram leaves out the video of a reel with music from its library.
		p.IsVideo, p.CoverURL, p.VideoURL = true, first.Thumbnail, first.MediaURL
	} else {
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
	if !ok || !a.instagramReady() {
		return 0, errors.New("Instagram isn’t connected")
	}
	if link.AccountID == "" || (!link.Expires.IsZero() && time.Now().After(link.Expires)) {
		return 0, errors.New("Instagram’s key ran out. Connect Instagram again")
	}
	var page struct {
		Data []instaMedia `json:"data"`
	}
	fields := "id,media_type,caption,media_url,thumbnail_url,permalink,timestamp,children{media_type,media_url,thumbnail_url}"
	if err := a.instaGet(ctx, link.Token, "/"+link.AccountID+"/media?limit="+strconv.Itoa(instaFeedSize)+"&fields="+url.QueryEscape(fields), &page); err != nil {
		return 0, fmt.Errorf("reading the posts: %w", err)
	}
	old := map[string]InstaPost{}
	for _, p := range a.store.InstaFeed() {
		old[p.ID] = p
	}
	var feed []InstaPost
	for _, m := range page.Data {
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
			if err := fetchTo(ctx, p.CoverURL, filepath.Join(dir, p.ID+".jpg"), 20<<20); err == nil {
				p.Cover = p.ID + ".jpg"
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

// instagramSyncLoop reads the feed every hour while Instagram is connected.
func (a *App) instagramSyncLoop(ctx context.Context) {
	for {
		if _, ok := a.store.InstagramLink(); ok {
			c, cancel := context.WithTimeout(ctx, 5*time.Minute)
			a.syncInstagram(c)
			cancel()
		}
		select {
		case <-time.After(time.Hour):
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

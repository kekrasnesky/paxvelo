package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeInstagram stands in for Instagram's login, API, and file servers.
func fakeInstagram(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v26.0/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code") != "good-code" || q.Get("client_secret") != "ig-secret" || q.Get("redirect_uri") == "" {
			http.Error(w, `{"error":{"message":"bad code"}}`, http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "suat-1", "token_type": "bearer"})
	})
	mux.HandleFunc("GET /v26.0/me/assigned_instagram_accounts", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "17841", "username": "paxvelo"}}})
	})
	mux.HandleFunc("GET /v26.0/17841/media", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "suat-1" {
			http.Error(w, `{"error":{"message":"Invalid OAuth access token"}}`, http.StatusUnauthorized)
			return
		}
		cdn := srv.URL + "/cdn/"
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "1", "media_type": "VIDEO", "caption": "Thursday night ride\n#paxvelo", "permalink": "https://www.instagram.com/reel/REEL1/",
				"media_url": cdn + "1.mp4", "thumbnail_url": cdn + "1.jpg", "timestamp": "2026-10-20T22:10:00+0000"},
			// A reel with music from Instagram's library: no video file.
			{"id": "2", "media_type": "VIDEO", "caption": "Hill repeats", "permalink": "https://www.instagram.com/reel/REEL2/",
				"thumbnail_url": cdn + "2.jpg", "timestamp": "2026-10-18T12:00:00+0000"},
			{"id": "3", "media_type": "CAROUSEL_ALBUM", "caption": "Pizza after the ride", "permalink": "https://www.instagram.com/p/ALBUM3/",
				"media_url": cdn + "3.jpg", "timestamp": "2026-10-10T12:00:00+0000",
				"children": map[string]any{"data": []map[string]any{{"media_type": "IMAGE", "media_url": cdn + "3.jpg"}, {"media_type": "IMAGE", "media_url": cdn + "3b.jpg"}}}},
			{"id": "4", "media_type": "IMAGE", "caption": "Older photo", "permalink": "https://www.instagram.com/p/PHOTO4/",
				"media_url": cdn + "4.jpg", "timestamp": "2026-09-01T12:00:00+0000"},
		}})
	})
	mux.HandleFunc("GET /cdn/{file}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("file " + r.PathValue("file")))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestInstagramFeed(t *testing.T) {
	a, _, admin := testApp(t)
	ig := fakeInstagram(t)
	a.cfg.InstagramAppID, a.cfg.InstagramAppSecret, a.cfg.InstagramConfigID = "ig-app", "ig-secret", "cfg-9"
	a.cfg.InstagramAuth, a.cfg.InstagramAPI = ig.URL, ig.URL
	h := a.routes()

	// Connecting: the admin approves on Instagram, which sends them back here.
	w := do(t, h, admin, "POST", "/admin/instagram/connect", "", nil)
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Path != "/dialog/oauth" || u.Query().Get("config_id") != "cfg-9" || u.Query().Get("client_id") != "ig-app" {
		t.Fatalf("authorize: %s", u)
	}
	w = do(t, h, admin, "GET", "/admin/instagram/callback?code=good-code%23_&state="+url.QueryEscape(u.Query().Get("state")), "", nil)
	if !strings.Contains(w.Header().Get("Location"), "ok=igconnected") {
		t.Fatalf("callback: %s", w.Header().Get("Location"))
	}
	link, _ := a.store.InstagramLink()
	if link.Username != "paxvelo" || link.AccountID != "17841" || link.Token != "suat-1" || !link.Expires.IsZero() || link.LastError != "" {
		t.Errorf("link: %+v", link)
	}

	// With nothing picked, the home page shows the 3 latest posts, drawn by the site.
	home := do(t, h, nil, "GET", "/", "", nil).Body.String()
	for _, want := range []string{
		`<video controls playsinline preload="none" poster="/instagram/1.jpg" src="/instagram/1.mp4"`,
		`<img src="/instagram/2.jpg"`, `class="ig-play"`, "Watch on Instagram", // the reel without its video
		`<span class="ig-count">1 / 2</span>`, "Thursday night ride", "Follow @paxvelo",
	} {
		if !strings.Contains(home, want) {
			t.Errorf("home page is missing %s", want)
		}
	}
	if strings.Contains(home, "Older photo") || strings.Contains(home, `class="insta insta-embeds"`) || strings.Contains(home, "#paxvelo") {
		t.Error("home page should show only the 3 latest posts, drawn by the site, with the caption's first line")
	}
	if w := do(t, h, nil, "GET", "/instagram/1.mp4", "", nil); w.Body.String() != "file 1.mp4" {
		t.Errorf("video: %d %q", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(a.instaDir(), "4.jpg")); err == nil {
		t.Error("a post the site doesn't show shouldn't be downloaded")
	}

	// Picking posts: one from the feed, and an older one the site falls back to Instagram's embed for.
	ct, body := form(url.Values{"posts": {"https://www.instagram.com/p/PHOTO4/\nhttps://www.instagram.com/p/OLDONE/"}})
	do(t, h, admin, "POST", "/admin/instagram", ct, body)
	home = do(t, h, nil, "GET", "/", "", nil).Body.String()
	if !strings.Contains(home, `<img src="/instagram/4.jpg"`) || !strings.Contains(home, `data-instgrm-permalink="https://www.instagram.com/p/OLDONE/"`) ||
		!strings.Contains(home, `class="insta insta-embeds"`) || strings.Contains(home, "Thursday night ride") {
		t.Errorf("picked posts on the home page:\n%s", home)
	}
	if _, err := os.Stat(filepath.Join(a.instaDir(), "1.mp4")); err == nil {
		t.Error("a video no longer shown should be removed")
	}
	if page := do(t, h, admin, "GET", "/admin/instagram", "", nil).Body.String(); !strings.Contains(page, "@paxvelo") || !strings.Contains(page, "Hill repeats") {
		t.Error("the admin tab should show the account and the latest posts")
	}

	// A key that ran out asks for connecting again.
	a.store.updateInstagramLink(func(l *InstagramLink) { l.Expires = time.Now().Add(-time.Hour) })
	if err := a.syncInstagram(t.Context()); err == nil || !strings.Contains(err.Error(), "Connect Instagram again") {
		t.Errorf("sync with a key that ran out: %v", err)
	}

	// Disconnecting goes back to Instagram's embed.
	do(t, h, admin, "POST", "/admin/instagram/disconnect", "", nil)
	if home = do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(home, `data-instgrm-permalink="https://www.instagram.com/p/PHOTO4/"`) {
		t.Error("after disconnecting, picked posts should use Instagram's embed")
	}
}

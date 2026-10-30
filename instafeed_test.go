package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeInstagram stands in for Instagram's login, API, and file servers.
func fakeInstagram(t *testing.T, refreshed *atomic.Int32) *httptest.Server {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("code") != "good-code" || r.FormValue("client_secret") != "ig-secret" || r.FormValue("grant_type") != "authorization_code" {
			http.Error(w, `{"error":{"message":"bad code"}}`, http.StatusBadRequest)
			return
		}
		// The shape Instagram's documentation shows: inside "data".
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"access_token": "short", "user_id": "17841"}}})
	})
	mux.HandleFunc("GET /access_token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "short" || r.URL.Query().Get("grant_type") != "ig_exchange_token" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "long-1", "token_type": "bearer", "expires_in": 5184000})
	})
	mux.HandleFunc("GET /refresh_access_token", func(w http.ResponseWriter, r *http.Request) {
		refreshed.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"access_token": "long-2", "token_type": "bearer", "expires_in": 5184000})
	})
	mux.HandleFunc("GET /v23.0/me", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": "17841", "username": "paxvelo"})
	})
	mux.HandleFunc("GET /v23.0/me/media", func(w http.ResponseWriter, r *http.Request) {
		if tok := r.URL.Query().Get("access_token"); tok != "long-1" && tok != "long-2" {
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
	var refreshed atomic.Int32
	ig := fakeInstagram(t, &refreshed)
	a.cfg.InstagramAppID, a.cfg.InstagramAppSecret = "ig-app", "ig-secret"
	a.cfg.InstagramAuth, a.cfg.InstagramTokenAPI, a.cfg.InstagramAPI = ig.URL, ig.URL, ig.URL
	h := a.routes()

	// Connecting: the admin approves on Instagram, which sends them back here.
	w := do(t, h, admin, "POST", "/admin/instagram/connect", "", nil)
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Query().Get("scope") != "instagram_business_basic" || u.Query().Get("client_id") != "ig-app" {
		t.Fatalf("authorize: %s", u)
	}
	w = do(t, h, admin, "GET", "/admin/instagram/callback?code=good-code%23_&state="+url.QueryEscape(u.Query().Get("state")), "", nil)
	if !strings.Contains(w.Header().Get("Location"), "ok=igconnected") {
		t.Fatalf("callback: %s", w.Header().Get("Location"))
	}
	link, _ := a.store.InstagramLink()
	if link.Username != "paxvelo" || link.Token != "long-1" || time.Until(link.Expires) < 59*24*time.Hour || link.LastError != "" {
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

	// The key is renewed two weeks before it runs out.
	a.store.updateInstagramLink(func(l *InstagramLink) {
		l.Expires, l.Renewed = time.Now().Add(10*24*time.Hour), time.Now().Add(-50*24*time.Hour)
	})
	if err := a.syncInstagram(t.Context()); err != nil || refreshed.Load() != 1 {
		t.Fatalf("sync: %v, refreshed %d times", err, refreshed.Load())
	}
	if l, _ := a.store.InstagramLink(); l.Token != "long-2" || time.Until(l.Expires) < 59*24*time.Hour {
		t.Errorf("after renewing: %+v", l)
	}

	// Disconnecting goes back to Instagram's embed.
	do(t, h, admin, "POST", "/admin/instagram/disconnect", "", nil)
	if home = do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(home, `data-instgrm-permalink="https://www.instagram.com/p/PHOTO4/"`) {
		t.Error("after disconnecting, picked posts should use Instagram's embed")
	}
}

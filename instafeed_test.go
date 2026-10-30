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
)

// fakeBehold stands in for Behold's feed server and the files it points to.
func fakeBehold(t *testing.T, reads *atomic.Int32) *httptest.Server {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /feed-abc123", func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		cdn := srv.URL + "/cdn/"
		large := func(name string) map[string]any {
			return map[string]any{"large": map[string]any{"mediaUrl": cdn + name, "width": 1000, "height": 1000}}
		}
		json.NewEncoder(w).Encode(map[string]any{"username": "paxvelo", "posts": []map[string]any{
			{"id": "1", "mediaType": "VIDEO", "isReel": true, "caption": "Thursday night ride\n#paxvelo", "prunedCaption": "Thursday night ride at [paxvelo.club](https://paxvelo.club)",
				"permalink": "https://www.instagram.com/reel/REEL1/", "mediaUrl": cdn + "1.mp4", "thumbnailUrl": cdn + "1t.jpg",
				"sizes": large("1.webp"), "timestamp": "2026-10-20T22:10:00+0000"},
			// A reel with music from Instagram's library: no video file.
			{"id": "2", "mediaType": "VIDEO", "isReel": true, "caption": "Hill repeats", "permalink": "https://www.instagram.com/reel/REEL2/",
				"thumbnailUrl": cdn + "2.jpg", "timestamp": "2026-10-18T12:00:00+0000"},
			{"id": "3", "mediaType": "CAROUSEL_ALBUM", "caption": "Pizza after the ride", "permalink": "https://www.instagram.com/p/ALBUM3/",
				"mediaUrl": cdn + "3.jpg", "sizes": large("3.webp"), "timestamp": "2026-10-10T12:00:00+0000",
				"children": []map[string]any{{"id": "3a", "mediaType": "IMAGE", "mediaUrl": cdn + "3.jpg", "sizes": large("3.webp")}, {"id": "3b", "mediaType": "IMAGE", "mediaUrl": cdn + "3b.jpg"}}},
			{"id": "4", "mediaType": "IMAGE", "caption": "Older photo", "permalink": "https://www.instagram.com/p/PHOTO4/",
				"mediaUrl": cdn + "4.jpg", "sizes": large("4.webp"), "timestamp": "2026-09-01T12:00:00+0000"},
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
	var reads atomic.Int32
	feeds := fakeBehold(t, &reads)
	a.cfg.InstagramFeeds = feeds.URL
	h := a.routes()

	if page := do(t, h, admin, "GET", "/admin/instagram", "", nil).Body.String(); !strings.Contains(page, `name="feed"`) {
		t.Error("the Instagram tab should ask for the feed link")
	}

	// Only a Behold feed link is taken.
	ct, body := form(url.Values{"feed": {"https://example.com/feed-abc123"}})
	if w := do(t, h, admin, "POST", "/admin/instagram/connect", ct, body); !strings.Contains(w.Header().Get("Location"), "err=igfeed") {
		t.Fatalf("a link elsewhere: %s", w.Header().Get("Location"))
	}
	ct, body = form(url.Values{"feed": {feeds.URL + "/feed-abc123/"}})
	if w := do(t, h, admin, "POST", "/admin/instagram/connect", ct, body); !strings.Contains(w.Header().Get("Location"), "ok=igconnected") {
		t.Fatalf("connect: %s", w.Header().Get("Location"))
	}
	link, _ := a.store.InstagramLink()
	if link.Username != "paxvelo" || link.Feed != feeds.URL+"/feed-abc123" || link.LastError != "" {
		t.Errorf("link: %+v", link)
	}

	// With nothing picked, the home page shows the 3 latest posts, drawn by the site.
	home := do(t, h, nil, "GET", "/", "", nil).Body.String()
	for _, want := range []string{
		`<video controls playsinline preload="none" poster="/instagram/1.webp" src="/instagram/1.mp4"`,
		`<img src="/instagram/2.jpg"`, `class="ig-play"`, "Watch on Instagram", // the reel without its video
		`<span class="ig-count">1 / 2</span>`, "Thursday night ride at paxvelo.club<", "Follow @paxvelo",
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
	ct, body = form(url.Values{"posts": {"https://www.instagram.com/p/PHOTO4/\nhttps://www.instagram.com/p/OLDONE/"}})
	do(t, h, admin, "POST", "/admin/instagram", ct, body)
	home = do(t, h, nil, "GET", "/", "", nil).Body.String()
	if !strings.Contains(home, `<img src="/instagram/4.webp"`) || !strings.Contains(home, `data-instgrm-permalink="https://www.instagram.com/p/OLDONE/"`) ||
		!strings.Contains(home, `class="insta insta-embeds"`) || strings.Contains(home, "Thursday night ride") {
		t.Errorf("picked posts on the home page:\n%s", home)
	}
	if _, err := os.Stat(filepath.Join(a.instaDir(), "1.mp4")); err == nil {
		t.Error("a video no longer shown should be removed")
	}
	if page := do(t, h, admin, "GET", "/admin/instagram", "", nil).Body.String(); !strings.Contains(page, "@paxvelo") || !strings.Contains(page, "Hill repeats") {
		t.Error("the admin tab should show the account and the latest posts")
	}

	// Each read of the feed counts toward Behold's monthly limit, so the site reads it sparingly.
	if n := reads.Load(); n > 4 {
		t.Errorf("the feed was read %d times", n)
	}

	// Disconnecting goes back to Instagram's embed.
	do(t, h, admin, "POST", "/admin/instagram/disconnect", "", nil)
	if home = do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(home, `data-instgrm-permalink="https://www.instagram.com/p/PHOTO4/"`) {
		t.Error("after disconnecting, picked posts should use Instagram's embed")
	}
}

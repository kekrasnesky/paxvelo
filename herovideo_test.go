package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What the home page plays: each shape's own video; phones fall back to the
// computer one; nothing when it's off or there are none.
func TestHeroVideoView(t *testing.T) {
	comp := &HeroFile{File: "/media/video/c.mp4", Poster: "/media/video/c.jpg", Type: "video/mp4; codecs=\"av01.0.08M.08\""}
	phone := &HeroFile{File: "/media/video/p.mp4", Poster: "/media/video/p.jpg", Type: "video/mp4; codecs=\"av01.0.08M.08\""}
	if v := (HeroVideo{Computer: comp, Phone: phone}).view(); v == nil || v.Video != comp.File || v.Phone != phone.File || v.PhonePoster != phone.Poster {
		t.Errorf("both: %+v", v)
	}
	if v := (HeroVideo{Computer: comp}).view(); v == nil || v.Phone != comp.File || v.PhonePoster != comp.Poster {
		t.Errorf("phones play the computer one: %+v", v)
	}
	if v := (HeroVideo{Phone: phone}).view(); v == nil || v.Video != "" || v.Phone != phone.File {
		t.Errorf("phone only: %+v", v)
	}
	if v := (HeroVideo{}).view(); v != nil {
		t.Errorf("none: %+v", v)
	}
	if v := (HeroVideo{Computer: comp, Off: true}).view(); v != nil {
		t.Errorf("off: %+v", v)
	}
}

func TestAV1Type(t *testing.T) {
	for c, want := range map[clipInfo]string{
		{Profile: "Main", Level: 8, PixFmt: "yuv420p"}:     `av01.0.08M.08`,
		{Profile: "Main", Level: 9, PixFmt: "yuv420p10le"}: `av01.0.09M.10`,
		{Profile: "High", Level: 12, PixFmt: "yuv444p"}:    `av01.1.12M.08`,
		{Profile: "Main", Level: -99, PixFmt: "yuv420p"}:   `av01.0.08M.08`,
	} {
		if got := av1Type(c); got != `video/mp4; codecs="`+want+`"` {
			t.Errorf("%+v: %s, want %s", c, got, want)
		}
	}
}

func TestVideoWarnings(t *testing.T) {
	good := clipInfo{W: 1920, H: 1080, Secs: 15}
	if w := videoWarnings(good, 15*3000*1000/8, 1920, 1080); len(w) != 0 {
		t.Errorf("a good one: %v", w)
	}
	w := videoWarnings(clipInfo{W: 3840, H: 2160, Secs: 90, Transfer: "smpte2084"}, 90*8000*1000/8, 1920, 1080)
	if len(w) != 4 || !strings.Contains(w[0], "Bigger than 1920×1080") || !strings.Contains(w[1], "8,000 kb/s") || !strings.Contains(w[2], "90 seconds") || !strings.HasPrefix(w[3], "HDR") {
		t.Errorf("4K, fast, long, HDR: %q", w)
	}
	if w := videoWarnings(clipInfo{W: 1280, H: 720, Secs: 15}, 1000, 1920, 1080); len(w) != 1 || !strings.HasPrefix(w[0], "Smaller") {
		t.Errorf("small: %q", w)
	}
}

func videoUpload(t *testing.T, file string) (string, *bytes.Buffer) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	w, _ := mw.CreateFormFile("video", filepath.Base(file))
	w.Write(data)
	mw.Close()
	return mw.FormDataContentType(), &b
}

// makeVideo makes a short test video with sound and camera details, or
// skips the test when this ffmpeg can't.
func makeVideo(t *testing.T, ffmpeg, out, size, codec string) {
	t.Helper()
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=30", "-f", "lavfi", "-i", "sine",
		"-t", "4", "-pix_fmt", "yuv420p", "-metadata", "title=Secret", "-metadata", "location=+38.29-076.63/"}
	if codec == "av1" {
		args = append(args, "-c:v", "libsvtav1", "-preset", "12", "-b:v", "1000k")
	} else {
		args = append(args, "-c:v", "libx264", "-preset", "ultrafast")
	}
	if _, err := runTool(ffmpeg, append(args, "-c:a", "aac", "-shortest", out)...); err != nil {
		t.Skipf("this ffmpeg can't make a %s test video: %v", codec, err)
	}
}

// A finished AV1 video goes live as it is: the same picture, not compressed
// again, without sound or camera details, with a still; the wrong codec or
// shape is turned away.
func TestHeroVideoUpload(t *testing.T) {
	ffmpeg, ffprobe := videoTools()
	if ffmpeg == "" {
		t.Skip("ffmpeg isn't installed")
	}
	a, h, admin := testApp(t)
	dir := t.TempDir()
	wide, tall, h264 := filepath.Join(dir, "wide.mp4"), filepath.Join(dir, "Gabe Test.mp4"), filepath.Join(dir, "h264.mp4")
	makeVideo(t, ffmpeg, wide, "1920x1080", "av1")
	makeVideo(t, ffmpeg, tall, "720x1280", "av1")
	makeVideo(t, ffmpeg, h264, "1920x1080", "h264")
	send := func(kind, file string) string {
		ct, b := videoUpload(t, file)
		return do(t, h, admin, "POST", "/admin/home-video/"+kind, ct, b).Header().Get("Location")
	}

	loc := send("computer", h264)
	if !strings.Contains(loc, "err=video-codec") {
		t.Errorf("H.264: %s", loc)
	}
	// The message shows in that video's own box, where the page scrolls to.
	page := do(t, h, admin, "GET", strings.Split(loc, "#")[0], "", nil).Body.String() // browsers don't send the #
	if i, j := strings.Index(page, `id="video-computer"`), strings.Index(page, "isn’t AV1"); i < 0 || j < i || strings.Count(page, "isn’t AV1") != 1 {
		t.Errorf("error in the computer box: %d %d", i, j)
	}
	if loc := send("computer", tall); !strings.Contains(loc, "err=video-wide") {
		t.Errorf("tall for computers: %s", loc)
	}
	if loc := send("phone", wide); !strings.Contains(loc, "err=video-tall") {
		t.Errorf("wide for phones: %s", loc)
	}
	if v := a.store.HeroVideo(); v.Computer != nil || v.Phone != nil {
		t.Fatalf("nothing kept: %+v", v)
	}

	if loc := send("computer", wide); !strings.Contains(loc, "ok=video-added") {
		t.Fatalf("computer: %s", loc)
	}
	if loc := send("phone", tall); !strings.Contains(loc, "ok=video-added") {
		t.Fatalf("phone: %s", loc)
	}
	v := a.store.HeroVideo()
	c, p := v.Computer, v.Phone
	if c == nil || p == nil || c.W != 1920 || c.H != 1080 || p.W != 720 || p.H != 1280 || c.Poster == "" || !strings.Contains(c.Type, "av01.") {
		t.Fatalf("stored: %+v %+v", c, p)
	}
	if len(c.Warn) != 0 || len(p.Warn) != 1 || !strings.Contains(p.Warn[0], "Smaller than 1080×1920") {
		t.Errorf("warnings: %q %q", c.Warn, p.Warn)
	}
	served := filepath.Join(a.videoDir(), filepath.Base(c.File))
	// The very same picture: its checksum matches the upload's.
	sum := func(f string) string {
		out, err := runTool(ffmpeg, "-hide_banner", "-loglevel", "error", "-i", f, "-map", "0:v", "-c", "copy", "-f", "md5", "-")
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if sum(served) != sum(wide) {
		t.Error("the picture was changed")
	}
	out, _ := runTool(ffprobe, "-v", "error", "-show_entries", "stream=codec_type:format_tags:stream_tags", "-of", "default", served)
	if s := string(out); strings.Contains(s, "audio") || strings.Contains(s, "Secret") || strings.Contains(s, "location") || strings.Contains(s, "+38.29") {
		t.Errorf("sound or camera details kept:\n%s", s)
	}
	if left, _ := os.ReadDir(a.videoUploadDir()); len(left) != 0 {
		t.Errorf("uploads left: %v", left)
	}

	home := do(t, h, nil, "GET", "/", "", nil).Body.String()
	for _, want := range []string{`src="` + p.File + `"`, `media="(max-width: 767px)"`, `src="` + c.File + `"`, `media="(min-width: 768px)"`, `codecs=&#34;av01.`, `srcset="` + c.Poster + `"`} {
		if !strings.Contains(home, want) {
			t.Errorf("home page should have %s", want)
		}
	}
	page = do(t, h, admin, "GET", "/admin/home-photos", "", nil).Body.String()
	for _, want := range []string{"Computer video", "Phone video", "AV1 · 1920×1080", "AV1 · 1080×1920", "Gabe Test.mp4", "1920×1080 · 4 s", "Smaller than 1080×1920"} {
		if !strings.Contains(page, want) {
			t.Errorf("admin page should show %q", want)
		}
	}

	// Replacing one deletes the old files; removing deletes its own.
	if loc := send("computer", wide); !strings.Contains(loc, "ok=video-added") {
		t.Fatalf("replace: %s", loc)
	}
	if _, err := os.Stat(served); err == nil {
		t.Error("the replaced video is deleted")
	}
	ct, b := form(nil)
	do(t, h, admin, "POST", "/admin/home-video/phone/remove", ct, b)
	if v := a.store.HeroVideo(); v.Phone != nil || v.Computer == nil {
		t.Errorf("removed: %+v", v)
	}
	if _, err := os.Stat(filepath.Join(a.videoDir(), filepath.Base(p.File))); err == nil {
		t.Error("the removed video is deleted")
	}
}

// At startup, files no video uses (older versions' clips and loops, half-done
// uploads) are deleted; the videos in use stay.
func TestCleanVideoFiles(t *testing.T) {
	a, _, _ := testApp(t)
	for _, d := range []string{a.videoDir(), a.videoUploadDir()} {
		os.MkdirAll(d, 0o755)
	}
	for _, n := range []string{"computer-1.mp4", "computer-1.jpg", "clip-1-aa.mp4", "home-1.mp4", "home-2-av1.mp4"} {
		os.WriteFile(filepath.Join(a.videoDir(), n), []byte("x"), 0o644)
	}
	os.WriteFile(filepath.Join(a.videoUploadDir(), "raw-1.mp4"), []byte("x"), 0o600)
	a.store.SetHeroFile("computer", &HeroFile{File: "/media/video/computer-1.mp4", Poster: "/media/video/computer-1.jpg"})
	a.cleanVideoFiles()
	for n, want := range map[string]bool{"computer-1.mp4": true, "computer-1.jpg": true, "clip-1-aa.mp4": false, "home-1.mp4": false, "home-2-av1.mp4": false} {
		if _, err := os.Stat(filepath.Join(a.videoDir(), n)); (err == nil) != want {
			t.Errorf("%s kept %v, want %v", n, err == nil, want)
		}
	}
	if _, err := os.Stat(a.videoUploadDir()); err == nil {
		t.Error("half-done uploads are deleted")
	}
}

// An older version's data (clips and a loop made from them) loads without
// a video.
func TestOldHeroVideoData(t *testing.T) {
	var v struct {
		HeroVideo HeroVideo `json:"hero_video"`
	}
	old := `{"hero_video":{"clips":[{"id":1,"name":"Club rides","raw":"","file":"clip-1.mp4","thumb":"/media/video/clip-1.jpg","secs":15,"w":1080,"h":1920}],"next_clip":1,"seeded":true,` +
		`"video":"/media/video/home-1.mp4","phone":"/media/video/home-1-phone.mp4","video_av1":"/media/video/home-1-av1.mp4","phone_av1":"/media/video/home-1-phone-av1.mp4",` +
		`"poster":"/media/video/home-1.jpg","phone_poster":"/media/video/home-1-phone.jpg","dirty":true,"error":"x","off":false}}`
	if err := json.Unmarshal([]byte(old), &v); err != nil {
		t.Fatal(err)
	}
	if v.HeroVideo.view() != nil {
		t.Errorf("old data plays nothing: %+v", v.HeroVideo)
	}
	// And the site starts with a data file like that.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "paxvelo.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(dir)
	if err != nil {
		t.Fatalf("an older data file won't load: %v", err)
	}
	if hv := s.HeroVideo(); hv.Computer != nil || hv.Phone != nil {
		t.Errorf("loaded: %+v", hv)
	}
}

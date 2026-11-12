package main

import (
	"bytes"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The loop: each clip fades into the next a second before it ends, the last
// fades into the first second of the first, and the result starts after it.
func TestLoopFilter(t *testing.T) {
	f := loopFilter([]float64{9, 7, 12})
	for _, want := range []string{
		"[0:v]settb=AVTB,setpts=PTS-STARTPTS,split[c0][head];[head]trim=0:1.000",
		"[c0][c1]xfade=transition=fade:duration=1.000:offset=8.000[x1]",
		"[x1][c2]xfade=transition=fade:duration=1.000:offset=14.000[x2]",
		"[x2][h]xfade=transition=fade:duration=1.000:offset=25.000[x3]",
		"[x3]trim=start=1.000,setpts=PTS-STARTPTS,format=yuv420p[v]",
	} {
		if !strings.Contains(f, want) {
			t.Errorf("filter should have %q:\n%s", want, f)
		}
	}
	if one := loopFilter([]float64{5}); !strings.Contains(one, "[c0][h]xfade=transition=fade:duration=1.000:offset=4.000[x1]") {
		t.Errorf("one clip loops into itself:\n%s", one)
	}
}

// What the home page plays: the made video, the built-in one until there is
// one, and nothing when it's off or every clip is gone.
func TestHeroVideoView(t *testing.T) {
	made := HeroVideo{Seeded: true, Clips: []*HeroClip{{ID: 1}}, Video: "/media/video/a.mp4", Phone: "/media/video/a-phone.mp4", Poster: "/media/video/a.jpg"}
	if v := made.view(true, "1"); v == nil || v.Video != "/media/video/a.mp4" || v.Phone != "/media/video/a-phone.mp4" {
		t.Errorf("made: %+v", v)
	}
	if v := (HeroVideo{}).view(true, "1"); v == nil || v.Video != "/photos/hero.mp4?v=1" {
		t.Errorf("before seeding, the built-in video plays: %+v", v)
	}
	if v := (HeroVideo{Seeded: true, Clips: []*HeroClip{{ID: 1}}}).view(true, "1"); v == nil || !strings.HasPrefix(v.Video, "/photos/") {
		t.Errorf("until it's made, the built-in one plays: %+v", v)
	}
	if v := (HeroVideo{}).view(false, "1"); v != nil {
		t.Errorf("no built-in video: %+v", v)
	}
	if v := (HeroVideo{Seeded: true}).view(true, "1"); v != nil {
		t.Errorf("every clip removed: %+v", v)
	}
	made.Off = true
	if v := made.view(true, "1"); v != nil {
		t.Errorf("off: %+v", v)
	}
}

func clipUpload(t *testing.T, files map[string][]byte) (string, *bytes.Buffer) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for name, data := range files {
		w, err := mw.CreateFormFile("clips", name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	mw.Close()
	return mw.FormDataContentType(), &b
}

// Admins upload clips, put them in order, remove them and switch the video
// off. Uploads wait in a folder that isn't served.
func TestHeroClipsAdmin(t *testing.T) {
	a, h, admin := testApp(t)
	a.vid.started = true // no background job in this test
	ct, b := clipUpload(t, map[string][]byte{"GX011193.MP4": []byte("first"), "beach.mov": []byte("second")})
	w := do(t, h, admin, "POST", "/admin/home-video/clips", ct, b)
	if !strings.Contains(w.Header().Get("Location"), "ok=video-added") {
		t.Fatalf("upload: %d %s", w.Code, w.Header().Get("Location"))
	}
	v := a.store.HeroVideo()
	if len(v.Clips) != 2 || !v.Dirty {
		t.Fatalf("clips %+v", v)
	}
	for _, c := range v.Clips {
		if c.Raw == "" || c.Ready() {
			t.Errorf("an upload waits to be prepared: %+v", c)
		}
		if _, err := os.Stat(filepath.Join(a.videoUploadDir(), c.Raw)); err != nil {
			t.Errorf("upload kept: %v", err)
		}
		if strings.HasPrefix(a.videoUploadDir(), a.mediaDir()) {
			t.Error("uploads aren't in the served media folder")
		}
	}
	first, second := v.Clips[0], v.Clips[1]

	ct, b = clipUpload(t, map[string][]byte{"notes.txt": []byte("hi")})
	if w := do(t, h, admin, "POST", "/admin/home-video/clips", ct, b); !strings.Contains(w.Header().Get("Location"), "err=video-type") {
		t.Errorf("only videos: %s", w.Header().Get("Location"))
	}

	ct2, b2 := form(map[string][]string{"dir": {"down"}})
	do(t, h, admin, "POST", "/admin/home-video/clips/"+strconv.Itoa(first.ID)+"/move", ct2, b2)
	if v := a.store.HeroVideo(); v.Clips[0].ID != second.ID {
		t.Errorf("moved: %+v", v.Clips)
	}

	page := do(t, h, admin, "GET", "/admin/home-photos", "", nil).Body.String()
	for _, want := range []string{`id="video"`, "Top video", first.Name, second.Name, "Preparing…", `name="clips"`} {
		if !strings.Contains(page, want) {
			t.Errorf("Home photos should show %q", want)
		}
	}

	ct2, b2 = form(nil)
	do(t, h, admin, "POST", "/admin/home-video/clips/"+strconv.Itoa(first.ID)+"/remove", ct2, b2)
	if v := a.store.HeroVideo(); len(v.Clips) != 1 || v.Clips[0].ID != second.ID {
		t.Errorf("removed: %+v", v.Clips)
	}

	ct2, b2 = form(nil) // the switch sends on="" to turn it off
	do(t, h, admin, "POST", "/admin/home-video/switch", ct2, b2)
	if !a.store.HeroVideo().Off || strings.Contains(do(t, h, nil, "GET", "/", "", nil).Body.String(), "<video") {
		t.Error("switched off, the home page has no video")
	}
	ct2, b2 = form(map[string][]string{"on": {"1"}})
	do(t, h, admin, "POST", "/admin/home-video/switch", ct2, b2)
	if a.store.HeroVideo().Off {
		t.Error("switched back on")
	}

	if w := do(t, h, nil, "GET", "/admin/home-video/status", "", nil); w.Code == http.StatusOK {
		t.Error("the status is for admins")
	}
}

// With ffmpeg there, uploads are prepared and joined into the loop, and the
// home page plays it.
func TestHeroVideoMade(t *testing.T) {
	ffmpeg, ffprobe := videoTools()
	if ffmpeg == "" {
		t.Skip("ffmpeg isn't installed")
	}
	a, h, _ := testApp(t)
	a.vid.started = true
	a.store.updateHeroVideo(func(v *HeroVideo) error { v.Seeded = true; return nil }) // skip the big built-in clip
	if err := os.MkdirAll(a.videoUploadDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for i, src := range []string{"testsrc2=size=640x480:rate=25", "mandelbrot=size=640x360:rate=60"} {
		raw := "test" + strconv.Itoa(i) + ".mp4"
		if _, err := runTool(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", src, "-t", "4",
			"-pix_fmt", "yuv420p", filepath.Join(a.videoUploadDir(), raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := a.store.AddHeroClip("Clip "+strconv.Itoa(i), raw); err != nil {
			t.Fatal(err)
		}
	}
	a.videoWork()
	v := a.store.HeroVideo()
	if v.Video == "" || v.Phone == "" || v.Dirty || v.Error != "" {
		t.Fatalf("video not made: %+v", v)
	}
	for _, c := range v.Clips {
		if !c.Ready() || c.Raw != "" || c.Thumb == "" || math.Abs(c.Secs-4) > 0.2 {
			t.Errorf("clip %+v", c)
		}
	}
	if entries, _ := os.ReadDir(a.videoUploadDir()); len(entries) != 0 {
		t.Errorf("uploads are deleted once prepared: %d left", len(entries))
	}
	file := filepath.Join(a.videoDir(), strings.TrimPrefix(v.Video, "/media/video/"))
	secs, err := clipLength(ffprobe, file)
	if err != nil || math.Abs(secs-6) > 0.25 { // 4 + 4, less a fade between and one back to the start
		t.Errorf("length %.2f, %v", secs, err)
	}
	if page := do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(page, `src="`+v.Video+`"`) || !strings.Contains(page, `src="`+v.Phone+`"`) {
		t.Error("the home page plays the made video")
	}
	if w := do(t, h, nil, "GET", v.Video, "", nil); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "video/mp4" {
		t.Errorf("served: %d %s", w.Code, w.Header().Get("Content-Type"))
	}

	// Removing one remakes it from the other; the old files go.
	a.store.RemoveHeroClip(v.Clips[0].ID)
	a.dropClipFiles(*v.Clips[0])
	a.videoWork()
	v2 := a.store.HeroVideo()
	if v2.Video == v.Video || v2.Video == "" {
		t.Errorf("remade: %+v", v2)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("the old video is deleted")
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The video at the top of the home page, made from clips admins add on
// Website → Home photos → Top video. They upload clips straight from a phone
// or a GoPro, put them in order and remove them; the site does the rest in
// the background with ffmpeg (deploy/setup.sh installs it): each upload is
// shrunk to a 1280×720, 30 fps copy without sound or the camera's GPS track
// (prepareClip), then all the clips are joined into one loop (buildVideo),
// each fading into the next over a second and the last fading back into the
// first, starting just after that fade so the browser's loop has no cut.
// A lighter 960×540 copy goes to phones, and a still of the first frame shows
// while it loads.
//
// The video built into the site (content/photos/hero.mp4) becomes the first
// clip the first time, so the home page looks the same until someone changes
// the list. With no clips, or the video switched off, the home page shows
// the top photo.

const (
	maxHeroClips   = 8
	maxClipUpload  = 2 << 30          // one upload, in case a phone sends 4K
	clipSeconds    = 30               // the most of each clip used
	minClipSeconds = 3.0              // a clip has to outlast its fades
	heroFade       = 1.0              // seconds each clip fades into the next
	videoTimeout   = 30 * time.Minute // one ffmpeg run, on a small server
)

// HeroClip is one clip in the home page video.
type HeroClip struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`             // the file name it was uploaded with
	Raw    string  `json:"raw,omitempty"`    // the upload, in the private uploads folder, until it's prepared
	File   string  `json:"file,omitempty"`   // the prepared copy, in the video folder
	Thumb  string  `json:"thumb,omitempty"`  // a still, under /media/
	Secs   float64 `json:"secs,omitempty"`   // how long the prepared copy runs
	Failed string  `json:"failed,omitempty"` // why it couldn't be prepared
}

// Ready says whether the clip is prepared and can go in the video.
func (c HeroClip) Ready() bool { return c.File != "" && c.Failed == "" }

// SecsLabel is its length, like "9 s".
func (c HeroClip) SecsLabel() string { return fmt.Sprintf("%.0f s", c.Secs) }

// HeroVideo is the home page video: its clips, and the files made from them.
type HeroVideo struct {
	Clips    []*HeroClip `json:"clips,omitempty"`
	NextClip int         `json:"next_clip,omitempty"`
	Seeded   bool        `json:"seeded,omitempty"` // the built-in video became the first clip
	Off      bool        `json:"off,omitempty"`    // show the top photo instead
	// The video made from the clips, under /media/; empty until it's made
	// (the built-in one shows meanwhile).
	Video  string `json:"video,omitempty"`
	Phone  string `json:"phone,omitempty"`
	Poster string `json:"poster,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"` // the clips changed since it was made
	Error  string `json:"error,omitempty"` // why the last try to make it failed
}

// heroVideoView is what the home page plays.
type heroVideoView struct{ Video, Phone, Poster string }

// ---- Store ----

func (s *Store) HeroVideo() HeroVideo {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.d.HeroVideo
	v.Clips = nil
	for _, c := range s.d.HeroVideo.Clips {
		cc := *c
		v.Clips = append(v.Clips, &cc)
	}
	return v
}

// updateHeroVideo changes the video under the store's lock and saves it.
func (s *Store) updateHeroVideo(f func(v *HeroVideo) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := f(&s.d.HeroVideo); err != nil {
		return err
	}
	return s.save()
}

var errTooManyClips = fmt.Errorf("the video can have up to %d clips", maxHeroClips)

// AddHeroClip adds an uploaded clip at the end, waiting to be prepared.
func (s *Store) AddHeroClip(name, raw string) (HeroClip, error) {
	var out HeroClip
	err := s.updateHeroVideo(func(v *HeroVideo) error {
		if len(v.Clips) >= maxHeroClips {
			return errTooManyClips
		}
		v.NextClip++
		c := &HeroClip{ID: v.NextClip, Name: name, Raw: raw}
		v.Clips, v.Dirty, v.Error = append(v.Clips, c), true, ""
		out = *c
		return nil
	})
	return out, err
}

// MoveHeroClip moves a clip one place earlier (by -1) or later (by 1).
func (s *Store) MoveHeroClip(id, by int) error {
	return s.updateHeroVideo(func(v *HeroVideo) error {
		i := slices.IndexFunc(v.Clips, func(c *HeroClip) bool { return c.ID == id })
		j := i + by
		if i < 0 || j < 0 || j >= len(v.Clips) {
			return nil
		}
		v.Clips[i], v.Clips[j] = v.Clips[j], v.Clips[i]
		v.Dirty, v.Error = true, ""
		return nil
	})
}

// RemoveHeroClip takes a clip out and returns it, so its files can go.
func (s *Store) RemoveHeroClip(id int) (HeroClip, bool, error) {
	var out HeroClip
	var found bool
	err := s.updateHeroVideo(func(v *HeroVideo) error {
		i := slices.IndexFunc(v.Clips, func(c *HeroClip) bool { return c.ID == id })
		if i < 0 {
			return nil
		}
		out, found = *v.Clips[i], true
		v.Clips = slices.Delete(v.Clips, i, i+1)
		v.Dirty, v.Error = true, ""
		return nil
	})
	return out, found, err
}

// SetHeroVideoOff switches the video off (the top photo shows) or on.
func (s *Store) SetHeroVideoOff(off bool) error {
	return s.updateHeroVideo(func(v *HeroVideo) error { v.Off = off; return nil })
}

// heroVideoView picks what the home page plays: the video made from the
// clips, or the built-in one until it's made, or nothing when it's switched
// off or every clip was removed. builtin says whether the site has one.
func (v HeroVideo) view(builtin bool, version string) *heroVideoView {
	switch {
	case v.Off:
		return nil
	case v.Video != "":
		return &heroVideoView{Video: v.Video, Phone: or(v.Phone, v.Video), Poster: v.Poster}
	case (!v.Seeded || len(v.Clips) > 0) && builtin:
		q := "?v=" + version
		return &heroVideoView{Video: "/photos/hero.mp4" + q, Phone: "/photos/hero-phone.mp4" + q, Poster: "/photos/hero-video.webp" + q}
	}
	return nil
}

// ---- ffmpeg ----

// videoTools are ffmpeg and ffprobe, or "" when they aren't installed. The
// FFMPEG environment variable can name ffmpeg (ffprobe sits beside it).
func videoTools() (ffmpeg, ffprobe string) {
	if p := os.Getenv("FFMPEG"); p != "" {
		dir, base := filepath.Split(p)
		return p, filepath.Join(dir, strings.Replace(base, "ffmpeg", "ffprobe", 1))
	}
	ffmpeg, _ = exec.LookPath("ffmpeg")
	ffprobe, _ = exec.LookPath("ffprobe")
	if ffmpeg == "" || ffprobe == "" {
		return "", ""
	}
	return ffmpeg, ffprobe
}

var errNoFFmpeg = errors.New("ffmpeg isn't installed on the server")

// runTool runs ffmpeg or ffprobe, at low priority where the server has
// nice, so the site stays quick while a video is made.
func runTool(prog string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), videoTimeout)
	defer cancel()
	if nice, err := exec.LookPath("nice"); err == nil && runtime.GOOS != "windows" {
		args, prog = append([]string{"-n", "15", prog}, args...), nice
	}
	out, err := exec.CommandContext(ctx, prog, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return out, fmt.Errorf("%v: %s", err, msg)
	}
	return out, nil
}

// clipLength is how long a video file runs, in seconds.
func clipLength(ffprobe, file string) (float64, error) {
	out, err := runTool(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", file)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// clipFilter shapes every clip the same: 30 fps, 1280×720 filling the frame.
const clipFilter = "fps=30,scale=1280:720:force_original_aspect_ratio=increase:flags=lanczos,crop=1280:720,setsar=1,format=yuv420p"

// prepareArgs shrinks an upload to the copy the video is made from: no
// sound, subtitles, data tracks (a GoPro's GPS) or metadata, at most
// clipSeconds long. ffmpeg turns clips filmed upside down the right way up.
func prepareArgs(in, out string) []string {
	return []string{"-hide_banner", "-loglevel", "error", "-y", "-i", in, "-t", strconv.Itoa(clipSeconds),
		"-map", "0:v:0", "-an", "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1",
		"-vf", clipFilter, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-movflags", "+faststart", out}
}

// loopFilter joins clips of these lengths into one loop: each fades into the
// next over heroFade, the last fades into the start of the first, and the
// result starts heroFade in, just after that, so the end runs straight into
// the start. Input i is clip i; the output is [v].
func loopFilter(secs []float64) string {
	var b strings.Builder
	n := len(secs)
	for i := range secs {
		fmt.Fprintf(&b, "[%d:v]settb=AVTB,setpts=PTS-STARTPTS", i)
		if i == 0 {
			b.WriteString(",split[c0][head];[head]trim=0:" + ffNum(heroFade) + ",setpts=PTS-STARTPTS[h];")
		} else {
			fmt.Fprintf(&b, "[c%d];", i)
		}
	}
	prev, total := "c0", secs[0]
	for i := 1; i <= n; i++ {
		next := "h"
		if i < n {
			next = fmt.Sprintf("c%d", i)
		}
		out := fmt.Sprintf("x%d", i)
		fmt.Fprintf(&b, "[%s][%s]xfade=transition=fade:duration=%s:offset=%s[%s];", prev, next, ffNum(heroFade), ffNum(total-heroFade), out)
		if i < n {
			total += secs[i] - heroFade
		}
		prev = out
	}
	fmt.Fprintf(&b, "[%s]trim=start=%s,setpts=PTS-STARTPTS,format=yuv420p[v]", prev, ffNum(heroFade))
	return b.String()
}

func ffNum(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }

// ---- The background job ----

// videoJobs runs one ffmpeg job at a time, in the background, so a page
// never waits for a video.
type videoJobs struct {
	mu      sync.Mutex
	busy    bool
	kick    chan struct{}
	started bool
}

func (a *App) videoDir() string { return filepath.Join(a.mediaDir(), "video") }

// videoUploadDir holds uploads until they're prepared. It isn't served: a
// camera's file can carry where it was filmed.
func (a *App) videoUploadDir() string { return filepath.Join(a.cfg.DataDir, "video-uploads") }

// kickVideo starts the job if there's anything to do.
func (a *App) kickVideo() {
	j := &a.vid
	j.mu.Lock()
	if !j.started {
		j.started, j.kick = true, make(chan struct{}, 1)
		go a.videoLoop()
	}
	j.mu.Unlock()
	select {
	case j.kick <- struct{}{}:
	default:
	}
}

// VideoBusy says whether a clip is being prepared or the video made.
func (a *App) VideoBusy() bool {
	a.vid.mu.Lock()
	defer a.vid.mu.Unlock()
	return a.vid.busy
}

func (a *App) setVideoBusy(b bool) {
	a.vid.mu.Lock()
	a.vid.busy = b
	a.vid.mu.Unlock()
}

func (a *App) videoLoop() {
	for range a.vid.kick {
		a.setVideoBusy(true)
		a.videoWork()
		a.setVideoBusy(false)
	}
}

// videoWork prepares clips waiting for it, then makes the video if the clips
// changed and every one is ready.
func (a *App) videoWork() {
	ffmpeg, ffprobe := videoTools()
	if ffmpeg == "" {
		return
	}
	a.seedHeroVideo()
	for {
		var todo *HeroClip
		for _, c := range a.store.HeroVideo().Clips {
			if c.Raw != "" && c.Failed == "" {
				todo = c
				break
			}
		}
		if todo == nil {
			break
		}
		a.prepareClip(ffmpeg, ffprobe, *todo)
	}
	v := a.store.HeroVideo()
	if !v.Dirty {
		return
	}
	for _, c := range v.Clips {
		if !c.Ready() {
			return // a failed clip waits to be removed
		}
	}
	err := a.buildVideo(ffmpeg, v)
	if err != nil {
		log.Printf("home video: %v", err)
		a.store.updateHeroVideo(func(hv *HeroVideo) error { hv.Error = "The video couldn’t be made: " + err.Error(); return nil })
		return
	}
	if err := a.rebuildContent(); err != nil {
		log.Printf("home video: %v", err)
	}
}

// seedHeroVideo makes the built-in video the first clip, once.
func (a *App) seedHeroVideo() {
	if a.store.HeroVideo().Seeded {
		return
	}
	raw := ""
	if b, err := fs.ReadFile(a.site.photos, "hero.mp4"); err == nil {
		if err := os.MkdirAll(a.videoUploadDir(), 0o700); err == nil {
			raw = "builtin-" + randHex(4) + ".mp4"
			if os.WriteFile(filepath.Join(a.videoUploadDir(), raw), b, 0o600) != nil {
				raw = ""
			}
		}
	}
	a.store.updateHeroVideo(func(v *HeroVideo) error {
		if v.Seeded {
			return nil
		}
		v.Seeded = true
		if raw != "" && len(v.Clips) < maxHeroClips {
			v.NextClip++
			v.Clips = append([]*HeroClip{{ID: v.NextClip, Name: "Club rides", Raw: raw}}, v.Clips...)
			// The built-in video already plays, so it doesn't need making again
			// until the list changes.
		}
		return nil
	})
}

// prepareClip shrinks one upload and takes a still from it.
func (a *App) prepareClip(ffmpeg, ffprobe string, c HeroClip) {
	dir := a.videoDir()
	in := filepath.Join(a.videoUploadDir(), c.Raw)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("home video: %v", err)
		return
	}
	base := fmt.Sprintf("clip-%d-%s", c.ID, randHex(4))
	file, thumb := base+".mp4", base+".jpg"
	fail := func(why string) {
		log.Printf("home video clip %d (%s): %s", c.ID, c.Name, why)
		os.Remove(filepath.Join(dir, file))
		os.Remove(filepath.Join(dir, thumb))
		a.store.updateHeroVideo(func(v *HeroVideo) error {
			for _, x := range v.Clips {
				if x.ID == c.ID {
					x.Failed = why
				}
			}
			return nil
		})
	}
	if _, err := runTool(ffmpeg, prepareArgs(in, filepath.Join(dir, file))...); err != nil {
		fail("That file isn’t a video ffmpeg can read.")
		return
	}
	secs, err := clipLength(ffprobe, filepath.Join(dir, file))
	if err != nil {
		fail("That file isn’t a video ffmpeg can read.")
		return
	}
	if secs < minClipSeconds {
		fail(fmt.Sprintf("Too short: clips need at least %.0f seconds.", minClipSeconds))
		return
	}
	runTool(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-ss", "1", "-i", filepath.Join(dir, file), "-frames:v", "1", "-vf", "scale=320:-2", "-q:v", "4", filepath.Join(dir, thumb))
	gone := false
	a.store.updateHeroVideo(func(v *HeroVideo) error {
		i := slices.IndexFunc(v.Clips, func(x *HeroClip) bool { return x.ID == c.ID })
		if i < 0 {
			gone = true // removed while it was being prepared
			return nil
		}
		x := v.Clips[i]
		x.Raw, x.File, x.Secs = "", file, secs
		if _, err := os.Stat(filepath.Join(dir, thumb)); err == nil {
			x.Thumb = "/media/video/" + thumb
		}
		return nil
	})
	os.Remove(in)
	if gone {
		os.Remove(filepath.Join(dir, file))
		os.Remove(filepath.Join(dir, thumb))
	}
}

// buildVideo joins the prepared clips into the loop, for computers and
// phones, with a still for while it loads, and swaps them in.
func (a *App) buildVideo(ffmpeg string, v HeroVideo) error {
	dir := a.videoDir()
	if len(v.Clips) == 0 {
		old := v
		a.store.updateHeroVideo(func(hv *HeroVideo) error {
			hv.Video, hv.Phone, hv.Poster, hv.Dirty, hv.Error = "", "", "", false, ""
			return nil
		})
		a.dropVideoFiles(old)
		return nil
	}
	base := "home-" + time.Now().UTC().Format("20060102-150405") + "-" + randHex(3)
	video, phone, poster := base+".mp4", base+"-phone.mp4", base+".jpg"
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	var secs []float64
	for _, c := range v.Clips {
		args = append(args, "-i", filepath.Join(dir, c.File))
		secs = append(secs, c.Secs)
	}
	args = append(args, "-filter_complex", loopFilter(secs), "-map", "[v]", "-an", "-map_metadata", "-1",
		"-c:v", "libx264", "-preset", "medium", "-crf", "28", "-maxrate", "2000k", "-bufsize", "4000k",
		"-profile:v", "high", "-level", "4.0", "-movflags", "+faststart", filepath.Join(dir, video))
	cleanup := func() {
		for _, f := range []string{video, phone, poster} {
			os.Remove(filepath.Join(dir, f))
		}
	}
	if _, err := runTool(ffmpeg, args...); err != nil {
		cleanup()
		return err
	}
	if _, err := runTool(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", filepath.Join(dir, video),
		"-vf", "scale=960:540:flags=lanczos,format=yuv420p", "-c:v", "libx264", "-preset", "medium", "-crf", "30",
		"-maxrate", "1100k", "-bufsize", "2200k", "-profile:v", "high", "-level", "4.0", "-movflags", "+faststart", filepath.Join(dir, phone)); err != nil {
		cleanup()
		return err
	}
	runTool(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", filepath.Join(dir, video), "-frames:v", "1", "-q:v", "4", filepath.Join(dir, poster))
	if _, err := os.Stat(filepath.Join(dir, poster)); err != nil {
		poster = ""
	}
	// Made from the clips as they were: if the list changed meanwhile, it
	// stays dirty and the next round makes it again.
	var old HeroVideo
	a.store.updateHeroVideo(func(hv *HeroVideo) error {
		old = *hv
		same := len(hv.Clips) == len(v.Clips)
		for i := range hv.Clips {
			same = same && i < len(v.Clips) && hv.Clips[i].ID == v.Clips[i].ID
		}
		hv.Video, hv.Phone, hv.Error = "/media/video/"+video, "/media/video/"+phone, ""
		hv.Poster = ""
		if poster != "" {
			hv.Poster = "/media/video/" + poster
		}
		hv.Dirty = !same
		return nil
	})
	a.dropVideoFiles(old)
	if a.store.HeroVideo().Dirty {
		a.kickVideo()
	}
	return nil
}

// dropVideoFiles deletes a made video's files once another replaces it.
func (a *App) dropVideoFiles(v HeroVideo) {
	for _, u := range []string{v.Video, v.Phone, v.Poster} {
		if name, ok := strings.CutPrefix(u, "/media/video/"); ok && name != "" && !strings.ContainsAny(name, `/\`) {
			os.Remove(filepath.Join(a.videoDir(), name))
		}
	}
}

// dropClipFiles deletes a removed clip's files.
func (a *App) dropClipFiles(c HeroClip) {
	thumb, _ := strings.CutPrefix(c.Thumb, "/media/video/")
	for _, name := range []string{c.File, thumb} {
		if name != "" && !strings.ContainsAny(name, `/\`) {
			os.Remove(filepath.Join(a.videoDir(), name))
		}
	}
	if c.Raw != "" && !strings.ContainsAny(c.Raw, `/\`) {
		os.Remove(filepath.Join(a.videoUploadDir(), c.Raw))
	}
}

// ---- Admin ----

var videoTypes = []string{".mp4", ".mov", ".m4v", ".webm", ".mkv", ".avi", ".3gp"}

// addHeroClips takes uploaded clips, streamed straight to disk: a phone's
// video can be hundreds of megabytes, and a slow connection takes minutes.
func (a *App) addHeroClips(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(time.Hour))
	rc.SetWriteDeadline(time.Now().Add(time.Hour))
	r.Body = http.MaxBytesReader(w, r.Body, maxHeroClips*maxClipUpload)
	reply := func(code string) {
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"go": "/admin/home-photos?" + code + "#video"})
			return
		}
		back(w, r, "/admin/home-photos?"+code+"#video")
	}
	mr, err := r.MultipartReader()
	if err != nil {
		reply("err=video-upload")
		return
	}
	if err := os.MkdirAll(a.videoUploadDir(), 0o700); err != nil {
		reply("err=save")
		return
	}
	added := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			reply("err=video-upload")
			return
		}
		if part.FormName() != "clips" || part.FileName() == "" {
			continue
		}
		name := path.Base(strings.ReplaceAll(part.FileName(), `\`, "/"))
		ext := strings.ToLower(path.Ext(name))
		if !slices.Contains(videoTypes, ext) {
			reply("err=video-type")
			return
		}
		raw := "raw-" + randHex(8) + ext
		f, err := os.OpenFile(filepath.Join(a.videoUploadDir(), raw), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			reply("err=save")
			return
		}
		_, err = io.Copy(f, io.LimitReader(part, maxClipUpload+1))
		f.Close()
		if err != nil {
			os.Remove(filepath.Join(a.videoUploadDir(), raw))
			reply("err=video-upload")
			return
		}
		if _, err := a.store.AddHeroClip(cleanTitle(strings.TrimSuffix(name, path.Ext(name)), 80), raw); err != nil {
			os.Remove(filepath.Join(a.videoUploadDir(), raw))
			if errors.Is(err, errTooManyClips) {
				reply("err=video-many")
			} else {
				reply("err=save")
			}
			a.kickVideo()
			return
		}
		added++
	}
	if added == 0 {
		reply("err=video-none")
		return
	}
	a.kickVideo()
	reply("ok=video-added")
}

func (a *App) moveHeroClip(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	by := 1
	if r.FormValue("dir") == "up" {
		by = -1
	}
	if err := a.store.MoveHeroClip(id, by); err != nil {
		back(w, r, "/admin/home-photos?err=save#video")
		return
	}
	a.kickVideo()
	back(w, r, "/admin/home-photos?ok=video-changed#video")
}

func (a *App) removeHeroClip(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	c, ok, err := a.store.RemoveHeroClip(id)
	if err != nil {
		back(w, r, "/admin/home-photos?err=save#video")
		return
	}
	if ok && (c.Raw == "" || c.Failed != "") { // one being prepared is cleaned up when it's done
		a.dropClipFiles(c)
	}
	a.kickVideo()
	back(w, r, "/admin/home-photos?ok=video-changed#video")
}

func (a *App) switchHeroVideo(w http.ResponseWriter, r *http.Request) {
	if err := a.store.SetHeroVideoOff(r.FormValue("on") == ""); err != nil || a.rebuildContent() != nil {
		back(w, r, "/admin/home-photos?err=save#video")
		return
	}
	back(w, r, "/admin/home-photos?ok=saved#video")
}

// heroVideoStatus tells the open admin page when the job is done.
func (a *App) heroVideoStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]bool{"busy": a.VideoBusy() || a.videoWaiting()})
}

// videoWaiting says whether clips are waiting to be prepared or the video to
// be made, with ffmpeg there to do it.
func (a *App) videoWaiting() bool {
	if ff, _ := videoTools(); ff == "" {
		return false
	}
	v := a.store.HeroVideo()
	waiting := false
	for _, c := range v.Clips {
		if c.Failed != "" {
			return false // nothing more happens until it's removed
		}
		waiting = waiting || c.Raw != ""
	}
	return waiting || v.Dirty && v.Error == ""
}

// heroVideoAdmin is what the Top video section shows.
type heroVideoAdmin struct {
	HeroVideo
	Now    *heroVideoView // what the home page plays now
	Busy   bool
	FFmpeg bool
	Max    int
}

func (a *App) heroVideoAdmin() heroVideoAdmin {
	v := a.store.HeroVideo()
	ff, _ := videoTools()
	return heroVideoAdmin{HeroVideo: v, Now: v.view(a.site.hasBuiltinVideo(), a.site.version), Busy: a.VideoBusy() || a.videoWaiting(), FFmpeg: ff != "", Max: maxHeroClips}
}

// hasBuiltinVideo says whether the site has content/photos/hero.mp4.
func (s *Site) hasBuiltinVideo() bool {
	_, err := fs.Stat(s.photos, "hero.mp4")
	return err == nil
}

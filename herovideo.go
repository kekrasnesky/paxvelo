package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The video at the top of the home page (Website → Home photos → Top video):
// the board uploads two finished videos made in an editor, one for computers
// (wide) and one for phones (tall), in AV1. The site never compresses them
// again. It only copies the picture into a clean MP4: no sound, no metadata
// or camera GPS track, and the index at the front so it starts at once. It
// takes a still of the first frame too. Browsers that can't play AV1 (older
// iPhones and Macs) show that still, and so does anyone saving data. People
// who turn motion off get the top photo.
//
// With no phone video, phones play the computer one cut to fit. With no
// computer video, computers show the top photo.

const (
	maxVideoUpload = 1 << 30         // one upload
	videoTimeout   = 5 * time.Minute // one ffmpeg or ffprobe run
	bestVideoRate  = 3000            // kbit/s: plenty for a background loop
	maxVideoRate   = 4500            // above this, a warning
	maxVideoSecs   = 60              // longer, a warning
)

// heroKind is one of the two videos, with the size it looks best at.
type heroKind struct {
	Kind, Label string
	W, H        int
}

var heroKinds = []heroKind{
	{"computer", "Computer video", 1920, 1080},
	{"phone", "Phone video", 1080, 1920},
}

// HeroFile is one uploaded video, as served.
type HeroFile struct {
	File   string   `json:"file"`             // under /media/video/
	Poster string   `json:"poster,omitempty"` // its first frame, the same
	Type   string   `json:"type"`             // the <source> type, with the codec
	Name   string   `json:"name,omitempty"`   // as uploaded
	W      int      `json:"w"`
	H      int      `json:"h"`
	Secs   float64  `json:"secs"`
	Size   int64    `json:"size"`
	Warn   []string `json:"warn,omitempty"` // things worth fixing, shown on the admin page
}

// Rate is its data rate in kbit/s.
func (f HeroFile) Rate() int {
	if f.Secs <= 0 {
		return 0
	}
	return int(math.Round(float64(f.Size) * 8 / 1000 / f.Secs))
}

// Info is what the admin page says about it: "1920×1080 · 15 s · 2,690 kb/s · 5.0 MB".
func (f HeroFile) Info() string {
	return fmt.Sprintf("%d×%d · %.0f s · %s kb/s · %.1f MB", f.W, f.H, f.Secs, thousands(f.Rate()), float64(f.Size)/1e6)
}

// Tall says it's taller than wide.
func (f HeroFile) Tall() bool { return f.H > f.W }

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// HeroVideo is the home page video: the computer and phone versions. Their
// keys aren't "computer" and "phone": older versions kept a file's address
// under "phone", and a data file with it has to keep loading.
type HeroVideo struct {
	Computer *HeroFile `json:"computer_video,omitempty"`
	Phone    *HeroFile `json:"phone_video,omitempty"`
	Off      bool      `json:"off,omitempty"` // show the top photo instead
}

func (v HeroVideo) file(kind string) *HeroFile {
	if kind == "phone" {
		return v.Phone
	}
	return v.Computer
}

func (v *HeroVideo) set(kind string, f *HeroFile) {
	if kind == "phone" {
		v.Phone = f
	} else {
		v.Computer = f
	}
}

// heroVideoView is what the home page plays. An empty Video means computers
// don't get one (only phones do).
type heroVideoView struct {
	Video, VideoType, Poster      string
	Phone, PhoneType, PhonePoster string
}

// view picks what the home page plays, or nil for the top photo.
func (v HeroVideo) view() *heroVideoView {
	if v.Off || v.Computer == nil && v.Phone == nil {
		return nil
	}
	hv := &heroVideoView{}
	if c := v.Computer; c != nil {
		hv.Video, hv.VideoType, hv.Poster = c.File, c.Type, c.Poster
	}
	p := v.Phone
	if p == nil {
		p = v.Computer
	}
	if p != nil {
		hv.Phone, hv.PhoneType, hv.PhonePoster = p.File, p.Type, p.Poster
	}
	return hv
}

// ---- Store ----

func (s *Store) HeroVideo() HeroVideo {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.d.HeroVideo
	for _, f := range []**HeroFile{&v.Computer, &v.Phone} {
		if *f != nil {
			c := **f
			c.Warn = slices.Clone(c.Warn)
			*f = &c
		}
	}
	return v
}

func (s *Store) updateHeroVideo(f func(v *HeroVideo) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := f(&s.d.HeroVideo); err != nil {
		return err
	}
	return s.save()
}

// SetHeroFile puts in (or with nil takes out) one of the two videos and
// returns the one it replaced, so its files can go.
func (s *Store) SetHeroFile(kind string, f *HeroFile) (*HeroFile, error) {
	var old *HeroFile
	err := s.updateHeroVideo(func(v *HeroVideo) error {
		old = v.file(kind)
		v.set(kind, f)
		return nil
	})
	return old, err
}

// SetHeroVideoOff switches the video off (the top photo shows) or on.
func (s *Store) SetHeroVideoOff(off bool) error {
	return s.updateHeroVideo(func(v *HeroVideo) error { v.Off = off; return nil })
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

// runTool runs ffmpeg or ffprobe, at low priority where the server has nice.
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

// clipInfo is what ffprobe says about a video file's picture: its codec, its
// size as it plays (turned the right way up), how long it runs and how its
// colors are coded.
type clipInfo struct {
	Codec    string // av1, h264, hevc…
	Profile  string // Main, High, Professional (AV1)
	Level    int    // AV1: seq_level_idx
	PixFmt   string // yuv420p, yuv420p10le…
	Transfer string // arib-std-b67 (HLG) or smpte2084 (PQ) is HDR
	W, H     int
	Secs     float64
}

// HDR says the colors are HDR (iPhones film in it).
func (c clipInfo) HDR() bool { return c.Transfer == "arib-std-b67" || c.Transfer == "smpte2084" }

func probeClip(ffprobe, file string) (clipInfo, error) {
	out, err := runTool(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries",
		"stream=codec_name,profile,level,pix_fmt,width,height,color_transfer:stream_side_data=rotation:format=duration", "-of", "json", file)
	if err != nil {
		return clipInfo{}, err
	}
	var p struct {
		Streams []struct {
			Codec         string `json:"codec_name"`
			Profile       string `json:"profile"`
			Level         int    `json:"level"`
			PixFmt        string `json:"pix_fmt"`
			Width, Height int
			Transfer      string `json:"color_transfer"`
			SideData      []struct {
				Rotation float64 `json:"rotation"`
			} `json:"side_data_list"`
		}
		Format struct {
			Duration string
		}
	}
	if err := json.Unmarshal(out, &p); err != nil || len(p.Streams) == 0 {
		return clipInfo{}, fmt.Errorf("no video in it")
	}
	st := p.Streams[0]
	info := clipInfo{Codec: st.Codec, Profile: st.Profile, Level: st.Level, PixFmt: st.PixFmt, Transfer: st.Transfer, W: st.Width, H: st.Height}
	for _, sd := range st.SideData {
		if r := int(math.Abs(sd.Rotation)) % 180; r == 90 { // filmed sideways: plays turned
			info.W, info.H = info.H, info.W
		}
	}
	info.Secs, err = strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil || info.W == 0 || info.H == 0 {
		return clipInfo{}, fmt.Errorf("no video in it")
	}
	return info, nil
}

// av1Type is the <source> type for an AV1 video, with the codec spelled out
// (av01.profile.levelTier.bitDepth), so a browser that can't play this one
// skips it instead of fetching it.
func av1Type(c clipInfo) string {
	profile := map[string]int{"High": 1, "Professional": 2}[c.Profile]
	level := c.Level
	if level < 0 || level > 31 {
		level = 8 // 4.0, what 1080p30 needs
	}
	depth := 8
	if strings.Contains(c.PixFmt, "10") {
		depth = 10
	} else if strings.Contains(c.PixFmt, "12") {
		depth = 12
	}
	return fmt.Sprintf(`video/mp4; codecs="av01.%d.%02dM.%02d"`, profile, level, depth)
}

// videoWarnings are the things worth fixing in an upload; none stop it.
func videoWarnings(c clipInfo, size int64, bestW, bestH int) []string {
	var w []string
	switch {
	case c.W*c.H > bestW*bestH:
		w = append(w, fmt.Sprintf("Bigger than %d×%d: slower to load, and no sharper.", bestW, bestH))
	case c.W*c.H < bestW*bestH:
		w = append(w, fmt.Sprintf("Smaller than %d×%d: it may look soft.", bestW, bestH))
	}
	if f := (HeroFile{Size: size, Secs: c.Secs}); f.Rate() > maxVideoRate {
		w = append(w, fmt.Sprintf("%s kb/s: slow on phone data; about %s is plenty.", thousands(f.Rate()), thousands(bestVideoRate)))
	}
	if c.Secs > maxVideoSecs {
		w = append(w, fmt.Sprintf("%.0f seconds: a big download; 10 to 30 is plenty.", c.Secs))
	}
	if c.HDR() {
		w = append(w, "HDR: it may look brighter than the page. Export it in SDR (Rec.709).")
	}
	return w
}

// ---- Files ----

// videoDir holds the videos as served, under /media/video/.
func (a *App) videoDir() string { return filepath.Join(a.mediaDir(), "video") }

// videoUploadDir holds uploads until they're copied: it isn't served, since
// camera files carry GPS.
func (a *App) videoUploadDir() string { return filepath.Join(a.cfg.DataDir, "video-uploads") }

// dropHeroFile deletes a video's files once it's replaced or removed.
func (a *App) dropHeroFile(f *HeroFile) {
	if f == nil {
		return
	}
	for _, u := range []string{f.File, f.Poster} {
		if name, ok := strings.CutPrefix(u, "/media/video/"); ok && name != "" && !strings.ContainsAny(name, `/\`) {
			os.Remove(filepath.Join(a.videoDir(), name))
		}
	}
}

// cleanVideoFiles, at startup, deletes files no video uses: half-done
// uploads, and the files of older versions of this feature.
func (a *App) cleanVideoFiles() {
	v := a.store.HeroVideo()
	keep := map[string]bool{}
	for _, f := range []*HeroFile{v.Computer, v.Phone} {
		if f != nil {
			keep[path.Base(f.File)], keep[path.Base(f.Poster)] = true, true
		}
	}
	if entries, err := os.ReadDir(a.videoDir()); err == nil {
		for _, e := range entries {
			if !keep[e.Name()] && !e.IsDir() {
				os.Remove(filepath.Join(a.videoDir(), e.Name()))
				log.Printf("home video: removed %s, not in use", e.Name())
			}
		}
	}
	os.RemoveAll(a.videoUploadDir())
}

// ---- Admin ----

var videoTypes = []string{".mp4", ".mov", ".m4v", ".webm", ".mkv"}

// uploadHeroVideo takes one finished video, streamed straight to disk (a
// slow connection can take minutes), checks it, copies its picture into a
// clean MP4 without compressing it again, and puts it live.
func (a *App) uploadHeroVideo(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	i := slices.IndexFunc(heroKinds, func(k heroKind) bool { return k.Kind == kind })
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	best := heroKinds[i]
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(time.Hour))
	rc.SetWriteDeadline(time.Now().Add(time.Hour))
	r.Body = http.MaxBytesReader(w, r.Body, maxVideoUpload+(1<<20))
	reply := func(code string) {
		to := "/admin/home-photos?" + code + "&slot=" + kind + "#video-" + kind
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"go": to})
			return
		}
		back(w, r, to)
	}
	ffmpeg, ffprobe := videoTools()
	if ffmpeg == "" {
		reply("err=video-noff")
		return
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
	var raw, name string
	defer func() {
		if raw != "" {
			os.Remove(raw)
		}
	}()
	for raw == "" {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			reply("err=video-upload")
			return
		}
		if part.FormName() != "video" || part.FileName() == "" {
			continue
		}
		name = path.Base(strings.ReplaceAll(part.FileName(), `\`, "/"))
		ext := strings.ToLower(path.Ext(name))
		if !slices.Contains(videoTypes, ext) {
			reply("err=video-type")
			return
		}
		raw = filepath.Join(a.videoUploadDir(), "raw-"+randHex(8)+ext)
		f, err := os.OpenFile(raw, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			raw = ""
			reply("err=save")
			return
		}
		_, err = io.Copy(f, part)
		f.Close()
		if err != nil {
			reply("err=video-upload")
			return
		}
	}
	if raw == "" {
		reply("err=video-none")
		return
	}
	info, err := probeClip(ffprobe, raw)
	switch {
	case err != nil:
		reply("err=video-bad")
		return
	case info.Codec != "av1":
		reply("err=video-codec")
		return
	case kind == "computer" && info.H > info.W:
		reply("err=video-wide")
		return
	case kind == "phone" && info.W > info.H:
		reply("err=video-tall")
		return
	}
	if err := os.MkdirAll(a.videoDir(), 0o755); err != nil {
		reply("err=save")
		return
	}
	base := kind + "-" + time.Now().UTC().Format("20060102-150405") + "-" + randHex(3)
	file, poster := base+".mp4", base+".jpg"
	out := filepath.Join(a.videoDir(), file)
	// The picture only, as it is: no sound, subtitles, data tracks (GPS),
	// chapters or metadata; the index at the front so it starts at once.
	if _, err := runTool(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", raw, "-map", "0:v:0", "-c", "copy",
		"-map_metadata", "-1", "-map_metadata:s:v", "-1", "-map_chapters", "-1", "-movflags", "+faststart", "-f", "mp4", out); err != nil {
		log.Printf("home video: copying %s: %v", name, err)
		os.Remove(out)
		reply("err=video-bad")
		return
	}
	st, err := os.Stat(out)
	if err != nil {
		reply("err=save")
		return
	}
	hf := &HeroFile{File: "/media/video/" + file, Type: av1Type(info), Name: cleanTitle(name, 80), W: info.W, H: info.H, Secs: info.Secs, Size: st.Size()}
	hf.Warn = videoWarnings(info, st.Size(), best.W, best.H)
	if _, err := runTool(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", out, "-frames:v", "1", "-q:v", "3", filepath.Join(a.videoDir(), poster)); err == nil {
		hf.Poster = "/media/video/" + poster
	} else {
		log.Printf("home video: still of %s: %v", name, err)
	}
	old, err := a.store.SetHeroFile(kind, hf)
	if err != nil {
		a.dropHeroFile(hf)
		reply("err=save")
		return
	}
	a.dropHeroFile(old)
	if err := a.rebuildContent(); err != nil {
		log.Printf("home video: %v", err)
	}
	reply("ok=video-added")
}

func (a *App) removeHeroVideo(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if !slices.ContainsFunc(heroKinds, func(k heroKind) bool { return k.Kind == kind }) {
		http.NotFound(w, r)
		return
	}
	old, err := a.store.SetHeroFile(kind, nil)
	if err != nil {
		back(w, r, "/admin/home-photos?err=save#video")
		return
	}
	a.dropHeroFile(old)
	if err := a.rebuildContent(); err != nil {
		log.Printf("home video: %v", err)
	}
	back(w, r, "/admin/home-photos?ok=video-removed#video")
}

func (a *App) switchHeroVideo(w http.ResponseWriter, r *http.Request) {
	if err := a.store.SetHeroVideoOff(r.FormValue("on") == ""); err != nil || a.rebuildContent() != nil {
		back(w, r, "/admin/home-photos?err=save#video")
		return
	}
	back(w, r, "/admin/home-photos?ok=saved#video")
}

// heroVideoAdmin is what the Top video section shows.
type heroVideoAdmin struct {
	Off    bool
	FFmpeg bool
	Slots  []heroVideoSlot
}

// heroVideoSlot is one of the two videos, with the size it looks best at,
// and what the last upload to it said.
type heroVideoSlot struct {
	Kind, Label, Best string
	File              *HeroFile
	Flash, Error      string
}

// slotMessage moves the page's message into the slot an upload went to, so
// it shows where the page scrolls to.
func (hv *heroVideoAdmin) slotMessage(r *http.Request, flash, err *string) {
	for i := range hv.Slots {
		if s := &hv.Slots[i]; s.Kind == r.FormValue("slot") {
			s.Flash, s.Error, *flash, *err = *flash, *err, "", ""
		}
	}
}

func (a *App) heroVideoAdmin() heroVideoAdmin {
	v := a.store.HeroVideo()
	ff, _ := videoTools()
	hv := heroVideoAdmin{Off: v.Off, FFmpeg: ff != ""}
	for _, k := range heroKinds {
		hv.Slots = append(hv.Slots, heroVideoSlot{Kind: k.Kind, Label: k.Label,
			Best: fmt.Sprintf("AV1 · %d×%d · about %s kb/s", k.W, k.H, thousands(bestVideoRate)), File: v.file(k.Kind)})
	}
	return hv
}

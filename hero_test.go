package main

import (
	"bytes"
	"image"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An admin can replace the home page's top photo, frame and describe it, and
// go back to the original.
func TestHeroPhoto(t *testing.T) {
	a, h, admin := testApp(t)
	a.rebuildContent()
	if home := do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(home, `src="/photos/hero.webp`) {
		t.Fatal("the home page starts with the built-in top photo")
	}
	if w := do(t, h, admin, "GET", "/admin/home-photos", "", nil); !strings.Contains(w.Body.String(), "Top photo") {
		t.Fatal("the Home photos tab has a Top photo box")
	}

	form := func(fields map[string]string, file bool) (string, *bytes.Buffer) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		if file {
			fw, _ := mw.CreateFormFile("hero_photo", "climb.jpg")
			fw.Write(encodeJPEG(t, halves(400, 200, 255)))
		}
		mw.Close()
		return mw.FormDataContentType(), &buf
	}
	ct, body := form(map[string]string{"hero_alt": "Riders on the climb", "hero_focus": "30% 40%"}, true)
	if w := do(t, h, admin, "POST", "/admin/home-hero", ct, body); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "ok=hero") {
		t.Fatalf("upload: %d %s", w.Code, w.Header().Get("Location"))
	}
	hero := a.store.Hero()
	if !strings.HasPrefix(hero.Photo, "/media/") || hero.Alt != "Riders on the climb" || hero.Focus != "" {
		t.Fatalf("after upload (a new photo starts centered): %+v", hero)
	}
	ct, body = form(map[string]string{"hero_alt": "Riders on the climb", "hero_focus": "30% 40% 1.5"}, false)
	do(t, h, admin, "POST", "/admin/home-hero", ct, body)
	home := do(t, h, nil, "GET", "/", "", nil).Body.String()
	if !strings.Contains(home, `src="`+hero.Photo+`"`) || !strings.Contains(home, `alt="Riders on the climb"`) || !strings.Contains(home, "object-position:30% 40%") || strings.Contains(home, "hero-phone.webp") {
		t.Fatal("the home page shows the uploaded photo, framed, for phones too")
	}

	// Phones get a small square copy cut where the framing points, made again
	// when the framing changes; the old one is deleted.
	hero = a.store.Hero()
	if !strings.HasSuffix(hero.Phone, "-phone.jpg") || !strings.Contains(home, `srcset="`+hero.Phone+`"`) || !strings.Contains(home, "@media (min-width:768px)") {
		t.Fatalf("phone copy: %+v", hero)
	}
	phoneFile := func(u string) string { return filepath.Join(a.mediaDir(), filepath.Base(u)) }
	f, _ := os.Open(phoneFile(hero.Phone))
	cfg, _, err := image.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != cfg.Height {
		t.Fatalf("phone copy is square: %v %dx%d", err, cfg.Width, cfg.Height)
	}
	old := hero.Phone
	ct, body = form(map[string]string{"hero_alt": "Riders on the climb", "hero_focus": "80% 50%"}, false)
	do(t, h, admin, "POST", "/admin/home-hero", ct, body)
	if p := a.store.Hero().Phone; p == old || p == "" {
		t.Error("new framing, new phone copy")
	}
	if _, err := os.Stat(phoneFile(old)); !os.IsNotExist(err) {
		t.Error("the old phone copy is deleted")
	}

	ct, body = form(map[string]string{"builtin": "on"}, false)
	do(t, h, admin, "POST", "/admin/home-hero", ct, body)
	if home := do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(home, `src="/photos/hero.webp`) || a.store.Hero() != (HeroPhoto{}) {
		t.Fatal("back to the original photo")
	}
}

// The phone copy is the square the framing picks: the left end, the right
// end, or zoomed in.
func TestHeroPhoneCrop(t *testing.T) {
	src := encodeJPEG(t, halves(400, 200, 255)) // red left half, blue right half
	for focus, want := range map[string]string{"0% 50%": "red", "100% 50%": "blue", "10% 50% 2": "red"} {
		b := heroPhoneCrop(src, focus)
		img, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		r, _, bl, _ := img.At(img.Bounds().Dx()/2, img.Bounds().Dy()/2).RGBA()
		got := "red"
		if bl > r {
			got = "blue"
		}
		if img.Bounds().Dx() != img.Bounds().Dy() || got != want {
			t.Errorf("%s: %dx%d %s, want square %s", focus, img.Bounds().Dx(), img.Bounds().Dy(), got, want)
		}
	}
	if b := heroPhoneCrop(src, "10% 50% 2"); b != nil {
		if img, _, _ := image.Decode(bytes.NewReader(b)); img.Bounds().Dx() != 100 {
			t.Errorf("zoomed in twice keeps half the square: %d", img.Bounds().Dx())
		}
	}
}

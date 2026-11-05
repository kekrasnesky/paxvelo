package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
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

	ct, body = form(map[string]string{"builtin": "on"}, false)
	do(t, h, admin, "POST", "/admin/home-hero", ct, body)
	if home := do(t, h, nil, "GET", "/", "", nil).Body.String(); !strings.Contains(home, `src="/photos/hero.webp`) || a.store.Hero() != (HeroPhoto{}) {
		t.Fatal("back to the original photo")
	}
}

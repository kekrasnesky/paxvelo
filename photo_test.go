package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"testing"
)

// halves makes a w×h photo, red on the left half and blue on the right, with
// noise so it compresses like a real photo.
func halves(w, h int, alpha uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(1))
	for y := range h {
		for x := range w {
			n := uint8(r.Intn(40))
			c := color.NRGBA{200 + n, n, n, alpha}
			if x >= w/2 {
				c = color.NRGBA{n, n, 200 + n, alpha}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withOrientation adds an EXIF block saying which way up the photo goes,
// the way a phone does.
func withOrientation(b []byte, o byte) []byte {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, o, 0, 0, 0, 0, 0, 0}
	seg := append([]byte("Exif\x00\x00"), tiff...)
	n := len(seg) + 2
	app1 := append([]byte{0xFF, 0xE1, byte(n >> 8), byte(n)}, seg...)
	return append(append(append([]byte{}, b[:2]...), app1...), b[2:]...)
}

func decoded(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestShrinkPhotoLarge(t *testing.T) {
	in := encodeJPEG(t, halves(3000, 1500, 255))
	out, ext := shrinkPhoto(in, ".jpg")
	if ext != ".jpg" || len(out) >= len(in) {
		t.Fatalf("got %s, %d bytes from %d", ext, len(out), len(in))
	}
	if b := decoded(t, out).Bounds(); b.Dx() != 2000 || b.Dy() != 1000 {
		t.Fatalf("size %v, want 2000×1000", b.Size())
	}
}

func TestShrinkPhotoTurnsSideways(t *testing.T) {
	in := withOrientation(encodeJPEG(t, halves(3000, 1000, 255)), 6)
	if o := jpegOrientation(in); o != 6 {
		t.Fatalf("orientation %d, want 6", o)
	}
	img := decoded(t, mustShrink(t, in, ".jpg"))
	b := img.Bounds()
	if b.Dx() != 666 || b.Dy() != 2000 {
		t.Fatalf("size %v, want 666×2000 (turned upright)", b.Size())
	}
	// Turned a quarter clockwise, the red left half is now on top.
	if r, _, bl, _ := img.At(333, 100).RGBA(); r < bl {
		t.Errorf("top should be red")
	}
	if r, _, bl, _ := img.At(333, 1900).RGBA(); bl < r {
		t.Errorf("bottom should be blue")
	}
}

func TestShrinkPhotoKeepsSeeThroughPNG(t *testing.T) {
	// A smooth see-through graphic, like a logo. (Noise, as in halves, can
	// come out bigger once shrunk, and then the original is kept.)
	img := image.NewNRGBA(image.Rect(0, 0, 2400, 1200))
	for y := range 1200 {
		for x := range 2400 {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x / 10), 0, uint8(y / 5), 128})
		}
	}
	var in bytes.Buffer
	png.Encode(&in, img)
	out, ext := shrinkPhoto(in.Bytes(), ".png")
	if ext != ".png" {
		t.Fatalf("got %s, want a PNG", ext)
	}
	if b := decoded(t, out).Bounds(); b.Dx() != 2000 {
		t.Fatalf("width %d, want 2000", b.Dx())
	}
}

func TestShrinkPhotoLeavesSmallAndOtherPhotos(t *testing.T) {
	small := encodeJPEG(t, halves(800, 600, 255))
	if out, _ := shrinkPhoto(small, ".jpg"); !bytes.Equal(out, small) {
		t.Error("a small photo should be kept as is")
	}
	webp := []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
	if out, ext := shrinkPhoto(webp, ".webp"); !bytes.Equal(out, webp) || ext != ".webp" {
		t.Error("WebP should be kept as is")
	}
	if out, _ := shrinkPhoto([]byte("not a photo"), ".jpg"); string(out) != "not a photo" {
		t.Error("a broken photo should be kept as is")
	}
}

func mustShrink(t *testing.T, b []byte, ext string) []byte {
	t.Helper()
	out, _ := shrinkPhoto(b, ext)
	if bytes.Equal(out, b) {
		t.Fatal("photo wasn't shrunk")
	}
	return out
}

// A photo small enough to keep as it is still loses the details the camera
// added, like where it was taken, but the picture itself is untouched.
func TestSmallPhotoLosesCameraDetails(t *testing.T) {
	plain := encodeJPEG(t, halves(800, 600, 255))
	tagged := withOrientation(plain, 1)
	if !bytes.Contains(tagged, []byte("Exif")) {
		t.Fatal("test photo has no EXIF block")
	}
	out, ext := shrinkPhoto(tagged, ".jpg")
	if ext != ".jpg" || bytes.Contains(out, []byte("Exif")) {
		t.Error("the EXIF block should be removed")
	}
	if !bytes.Equal(out, plain) {
		t.Error("only the EXIF block should change")
	}
	// A sideways photo is turned upright rather than losing its orientation.
	sideways := withOrientation(encodeJPEG(t, halves(800, 600, 255)), 6)
	out, _ = shrinkPhoto(sideways, ".jpg")
	if b := decoded(t, out).Bounds(); b.Dx() != 600 || b.Dy() != 800 || bytes.Contains(out, []byte("Exif")) {
		t.Errorf("a sideways photo should be turned upright and clean: %v", b)
	}
}

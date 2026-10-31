package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
)

// Photos straight from a phone are often 3–6 MB, which makes a newsletter
// slow to load on a phone signal. Uploaded JPG and PNG photos are shrunk to
// at most 1600 pixels on their long side and saved as JPG (a PNG with
// see-through parts stays a PNG). A busy photo (grass, gravel, crowds) can
// still be large at that size, so it steps down to 1400, then 1200 pixels,
// until it's within photoBudget. That's sharp across a laptop screen. Re-saving also drops the camera's hidden details,
// like where the photo was taken. WebP and GIF photos keep their size: Go
// can't write WebP, and a GIF may be animated. A WebP still loses its hidden
// details; a GIF has none worth worrying about.
const (
	photoMaxSide   = 1600
	photoBudget    = 350 << 10  // a photo over this steps down to a smaller size
	photoKeepBytes = 250 << 10  // a photo this small that fits is kept as is
	photoMaxPixels = 60_000_000 // larger than any camera photo; kept as is
	photoQuality   = 80
)

// photoSides are the sizes a photo tries, largest first, to fit photoBudget.
var photoSides = []int{photoMaxSide, 1400, 1200}

// shrinkPhoto returns a smaller copy of the photo b and its file extension,
// or b and ext unchanged when it's already small or can't be shrunk.
func shrinkPhoto(b []byte, ext string) ([]byte, string) {
	switch ext {
	case ".webp":
		return stripWebPMetadata(b), ext
	case ".jpg", ".png":
	default:
		return b, ext
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > photoMaxPixels {
		return stripMetadata(b, ext), ext
	}
	turn := 1
	if ext == ".jpg" {
		turn = jpegOrientation(b)
	}
	if max(cfg.Width, cfg.Height) <= photoMaxSide && len(b) <= photoKeepBytes && turn == 1 {
		return stripMetadata(b, ext), ext // small enough to keep, but not with the camera's details
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return stripMetadata(b, ext), ext
	}
	var out bytes.Buffer
	outExt := ".jpg"
	for _, side := range photoSides {
		img := orient(downscale(src, side), turn)
		out.Reset()
		if !img.Opaque() {
			outExt = ".png"
			err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, img)
			break // see-through graphics are small already
		}
		if err = jpeg.Encode(&out, img, &jpeg.Options{Quality: photoQuality}); err != nil || out.Len() <= photoBudget {
			break
		}
	}
	if err != nil || out.Len() >= len(b) && turn == 1 {
		return stripMetadata(b, ext), ext
	}
	return out.Bytes(), outExt
}

// stripMetadata drops a JPG's or PNG's hidden details (see below).
func stripMetadata(b []byte, ext string) []byte {
	if ext == ".png" {
		return stripPNGMetadata(b)
	}
	return stripJPEGMetadata(b)
}

// downscale returns src as RGBA, shrunk (never enlarged) so its long side is
// at most maxSide. Each new pixel is the average of the pixels it covers,
// which keeps fine detail smooth instead of jagged.
func downscale(src image.Image, maxSide int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	rgba := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	if max(sw, sh) <= maxSide {
		return rgba
	}
	dw, dh := sw*maxSide/max(sw, sh), sh*maxSide/max(sw, sh)
	dw, dh = max(dw, 1), max(dh, 1)
	cols, rows := spans(sw, dw), spans(sh, dh)

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	row := make([]float32, dw*4) // one source row, shrunk across
	acc := make([]float32, dw*4)
	for y, ws := range rows {
		clear(acc)
		for _, w := range ws {
			clear(row)
			line := rgba.Pix[w.i*rgba.Stride:]
			for x, cs := range cols {
				for _, c := range cs {
					p := line[c.i*4 : c.i*4+4]
					for k := range 4 {
						row[x*4+k] += c.w * float32(p[k])
					}
				}
			}
			for k, v := range row {
				acc[k] += w.w * v
			}
		}
		out := dst.Pix[y*dst.Stride:]
		for k, v := range acc {
			out[k] = uint8(min(v+0.5, 255))
		}
	}
	return dst
}

type weight struct {
	i int
	w float32
}

// spans lists, for each of the n new pixels along one side, the old pixels
// (of size along that side) it covers and how much of each, adding up to 1.
func spans(size, n int) [][]weight {
	out := make([][]weight, n)
	step := float64(size) / float64(n)
	for j := range out {
		lo, hi := float64(j)*step, float64(j+1)*step
		for i := int(lo); i < size && float64(i) < hi; i++ {
			cover := min(hi, float64(i+1)) - max(lo, float64(i))
			if cover > 0 {
				out[j] = append(out[j], weight{i, float32(cover / step)})
			}
		}
	}
	return out
}

// stripJPEGMetadata returns the JPEG without the details a camera adds: the
// EXIF block (which holds where the photo was taken), XMP, IPTC and comments.
// The picture itself isn't touched. A photo with none of these, or one that
// can't be read, is returned as is.
func stripJPEGMetadata(b []byte) []byte {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return b
	}
	out := append(make([]byte, 0, len(b)), b[:2]...)
	p, dropped := 2, false
	for p+4 <= len(b) && b[p] == 0xFF {
		marker := b[p+1]
		n := int(binary.BigEndian.Uint16(b[p+2:]))
		if marker == 0xDA {
			break // the image data starts here
		}
		if n < 2 || p+2+n > len(b) {
			return b
		}
		if marker == 0xE1 || marker == 0xED || marker == 0xFE { // EXIF/XMP, IPTC, comment
			dropped = true
		} else {
			out = append(out, b[p:p+2+n]...)
		}
		p += 2 + n
	}
	if !dropped {
		return b
	}
	return append(out, b[p:]...)
}

// jpegOrientation reads which way up the camera says the photo goes (the
// EXIF orientation, 1 to 8), or 1 if it doesn't say. Phones save photos
// sideways and set this, and re-saving drops it, so orient applies it.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	for p := 2; p+4 <= len(b) && b[p] == 0xFF; {
		marker := b[p+1]
		n := int(binary.BigEndian.Uint16(b[p+2:]))
		if marker == 0xDA || n < 2 || p+2+n > len(b) {
			break // the image data starts; no orientation came first
		}
		seg := b[p+4 : p+2+n]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return exifOrientation(seg[6:])
		}
		p += 2 + n
	}
	return 1
}

func exifOrientation(t []byte) int {
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := int(order.Uint32(t[4:]))
	if ifd < 8 || ifd+2 > len(t) {
		return 1
	}
	count := int(order.Uint16(t[ifd:]))
	for e := ifd + 2; e+12 <= len(t) && count > 0; e, count = e+12, count-1 {
		if order.Uint16(t[e:]) == 0x0112 {
			if o := int(order.Uint16(t[e+8:])); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

// orient turns and flips img to match an EXIF orientation.
func orient(img *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return img
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			copy(dst.Pix[dy*dst.Stride+dx*4:][:4], img.Pix[y*img.Stride+x*4:][:4])
		}
	}
	return dst
}

// smallerCopies returns copies of a gallery photo (already shrunk by
// shrinkPhoto) at each of galleryWidths that's narrower than the photo, keyed
// by width, and its size. The grid loads the small ones and the viewer the
// full one. A photo Go can't read (WebP, GIF) or one with see-through parts
// gets none.
func smallerCopies(b []byte) (copies map[int][]byte, w, h int) {
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, 0, 0
	}
	w, h = src.Bounds().Dx(), src.Bounds().Dy()
	full := downscale(src, max(w, h))
	if !full.Opaque() {
		return nil, w, h
	}
	copies = map[int][]byte{}
	for _, side := range galleryWidths {
		if side >= w {
			continue
		}
		var out bytes.Buffer
		if jpeg.Encode(&out, downscale(full, side*max(w, h)/w), &jpeg.Options{Quality: 78}) == nil {
			copies[side] = out.Bytes()
		}
	}
	return copies, w, h
}

// stripPNGMetadata drops a PNG's text and EXIF chunks, where a camera or
// editor can leave where the photo was taken.
func stripPNGMetadata(b []byte) []byte {
	if len(b) < 8 || string(b[1:4]) != "PNG" {
		return b
	}
	out := append(make([]byte, 0, len(b)), b[:8]...)
	for p := 8; p+12 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[p:]))
		if n < 0 || p+12+n > len(b) {
			return b
		}
		switch string(b[p+4 : p+8]) {
		case "eXIf", "tEXt", "zTXt", "iTXt":
		default:
			out = append(out, b[p:p+12+n]...)
		}
		p += 12 + n
	}
	return out
}

// stripWebPMetadata drops a WebP's EXIF and XMP chunks, and the flags saying
// it has them.
func stripWebPMetadata(b []byte) []byte {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return b
	}
	out := append(make([]byte, 0, len(b)), b[:12]...)
	p, dropped := 12, false
	for p+8 <= len(b) {
		n := int(binary.LittleEndian.Uint32(b[p+4:]))
		end := p + 8 + n + n%2 // chunks are padded to an even length
		if n < 0 || end > len(b) {
			return b
		}
		switch id := string(b[p : p+4]); id {
		case "EXIF", "XMP ":
			dropped = true
		default:
			c := append([]byte{}, b[p:end]...)
			if id == "VP8X" && len(c) > 8 {
				c[8] &^= 0x0C // the EXIF and XMP flags
			}
			out = append(out, c...)
		}
		p = end
	}
	if !dropped || p != len(b) {
		return b
	}
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	return out
}

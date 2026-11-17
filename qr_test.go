package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The phone-screen code and the admin's signs show codes for the site's
// pages; a ride page links its leader to the waiver code.
func TestQRPages(t *testing.T) {
	a, h, _ := rideApp(t)
	p := do(t, h, nil, "GET", "/qr/ride-waiver", "", nil)
	if p.Code != 200 || !strings.Contains(p.Body.String(), `<svg xmlns="http://www.w3.org/2000/svg" class="qr"`) || !strings.Contains(p.Body.String(), "/ride-waiver</p>") {
		t.Errorf("/qr/ride-waiver = %d", p.Code)
	}
	if do(t, h, nil, "GET", "/qr/nope", "", nil).Code != 404 {
		t.Error("an unknown code should be not found")
	}
	if !strings.Contains(do(t, h, nil, "GET", "/calendar/ride/"+seriesKey(1, nextWeekday(a, 2)), "", nil).Body.String(), `href="/qr/ride-waiver"`) {
		t.Error("a ride page should link to the waiver code")
	}
	rec := httptest.NewRecorder()
	a.setSession(rec, "Board Member", 3600)
	signs := do(t, h, rec.Result().Cookies()[0], "GET", "/admin/signs", "", nil).Body.String()
	if strings.Count(signs, `class="qr"`) != 3 || !strings.Contains(signs, "Join PaxVelo") {
		t.Error("the signs page should have three codes")
	}
	if !localSite("http://localhost:8101") || localSite("https://www.paxvelo.club") {
		t.Error("localSite is wrong")
	}
}

// Format and version words match the standard's tables.
func TestQRFormatWords(t *testing.T) {
	// ISO/IEC 18004 Table C.1, level M, masks 0–7.
	want := []int{0b101010000010010, 0b101000100100101, 0b101111001111100, 0b101101101001011,
		0b100010111111001, 0b100000011001110, 0b100111110010111, 0b100101010100000}
	for m, w := range want {
		if got := qrFormatBits(m); got != w {
			t.Errorf("format M/%d = %015b, want %015b", m, got, w)
		}
	}
	// Table D.1: version 7 is 0x07C94, version 10 is 0x0A4D3.
	for v, w := range map[int]int{7: 0x07C94, 10: 0x0A4D3} {
		rem := v
		for range 12 {
			rem = rem<<1 ^ (rem>>11)*0x1F25
		}
		if got := v<<12 | rem; got != w {
			t.Errorf("version %d word = %05X, want %05X", v, got, w)
		}
	}
}

// Every block with its error correction is a codeword: the polynomial
// evaluates to zero at each root α^i.
func TestQRReedSolomon(t *testing.T) {
	data := qrDataBits(4, []byte("https://www.paxvelo.club/ride-waiver"))
	if len(data) != qrDataCodewords(4) {
		t.Fatalf("data codewords = %d", len(data))
	}
	ec := rsRemainder(data[:32], 18)
	msg := append(append([]byte{}, data[:32]...), ec...)
	for i := range 18 {
		var acc byte
		for _, c := range msg { // Horner at α^i
			acc = gfMul(acc, gfExp[i]) ^ c
		}
		if acc != 0 {
			t.Fatalf("syndrome %d = %d", i, acc)
		}
	}
	// The bit stream starts with byte mode and the length.
	if data[0]>>4 != 0b0100 || (data[0]&0x0F)<<4|data[1]>>4 != byte(len("https://www.paxvelo.club/ride-waiver")) {
		t.Errorf("header %08b %08b", data[0], data[1])
	}
}

// Codes come out the right size, with the finder patterns in their corners
// and the timing patterns alternating.
func TestQRLayout(t *testing.T) {
	for text, v := range map[string]int{"https://www.paxvelo.club/join": 3, "https://www.paxvelo.club/calendar/subscribe": 4, strings.Repeat("x", 200): 10} {
		q, err := qrEncode(text)
		if err != nil {
			t.Fatal(err)
		}
		if q.version != v || q.size != 17+4*v {
			t.Errorf("%d bytes: version %d, want %d", len(text), q.version, v)
		}
		for _, c := range [][2]int{{0, 0}, {q.size - 7, 0}, {0, q.size - 7}} {
			for i := range 7 {
				if !q.dark[c[1]][c[0]+i] || !q.dark[c[1]+6][c[0]+i] || !q.dark[c[1]+i][c[0]] || q.dark[c[1]+1][c[0]+1] || !q.dark[c[1]+3][c[0]+3] {
					t.Fatalf("finder at %v is wrong", c)
				}
			}
		}
		for i := 8; i < q.size-8; i++ {
			if q.dark[6][i] != (i%2 == 0) || q.dark[i][6] != (i%2 == 0) {
				t.Fatal("timing pattern is wrong")
			}
		}
		if !q.dark[q.size-8][8] {
			t.Error("dark module missing")
		}
	}
	if _, err := qrEncode(strings.Repeat("x", 300)); err == nil {
		t.Error("300 bytes should be too long")
	}
	// For checking with a phone or a reader: QR_OUT=dir go test -run QRLayout
	if dir := os.Getenv("QR_OUT"); dir != "" {
		for name, text := range map[string]string{"join": "https://www.paxvelo.club/join", "waiver": "https://www.paxvelo.club/ride-waiver",
			"calendar": "https://www.paxvelo.club/calendar/subscribe", "long": "https://www.paxvelo.club/" + strings.Repeat("abc", 60)} {
			q, _ := qrEncode(text)
			os.WriteFile(dir+"/"+name+".svg", []byte(q.SVG(name)), 0o644)
		}
	}
}

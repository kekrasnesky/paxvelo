package main

import (
	"errors"
	"fmt"
	"html/template"
	"strings"
)

// QR codes, drawn by the site itself (no outside code): for the printable
// signs (Join, Ride waiver, the calendar) and the waiver code a leader shows
// on their phone at the start. Byte mode, error correction level M (about
// 15% of the code can be smudged or covered), versions 1 to 10, which holds
// up to 213 bytes: plenty for the site's addresses. Built after ISO/IEC 18004.

type qrCode struct {
	size    int
	dark    [][]bool
	isFunc  [][]bool
	version int
}

// Level M, versions 1–10: error correction codewords per block, and the
// blocks as (count, data codewords) groups.
var qrBlocksM = [11]struct {
	ec     int
	groups [][2]int
}{
	{},
	{10, [][2]int{{1, 16}}},
	{16, [][2]int{{1, 28}}},
	{26, [][2]int{{1, 44}}},
	{18, [][2]int{{2, 32}}},
	{24, [][2]int{{2, 43}}},
	{16, [][2]int{{4, 27}}},
	{18, [][2]int{{4, 31}}},
	{22, [][2]int{{2, 38}, {2, 39}}},
	{22, [][2]int{{3, 36}, {2, 37}}},
	{26, [][2]int{{4, 43}, {1, 44}}},
}

var qrAlign = [11][]int{nil, nil, {6, 18}, {6, 22}, {6, 26}, {6, 30}, {6, 34}, {6, 22, 38}, {6, 24, 42}, {6, 26, 46}, {6, 28, 50}}

func qrDataCodewords(v int) int {
	n := 0
	for _, g := range qrBlocksM[v].groups {
		n += g[0] * g[1]
	}
	return n
}

var errQRTooLong = errors.New("too long for a QR code here")

// qrEncode makes the smallest code (versions 1–10, level M) for text.
func qrEncode(text string) (*qrCode, error) {
	data := []byte(text)
	v := 1
	for ; v <= 10; v++ {
		ccBits := 8
		if v >= 10 {
			ccBits = 16
		}
		if 4+ccBits+8*len(data) <= qrDataCodewords(v)*8 {
			break
		}
	}
	if v > 10 {
		return nil, errQRTooLong
	}
	codewords := qrInterleave(v, qrDataBits(v, data))
	q := newQR(v)
	q.placeData(codewords)
	best, bestScore := -1, 0
	for m := range 8 {
		q.applyMask(m)
		q.drawFormat(m)
		if s := q.penalty(); best < 0 || s < bestScore {
			best, bestScore = m, s
		}
		q.applyMask(m) // XOR again undoes it
	}
	q.applyMask(best)
	q.drawFormat(best)
	return q, nil
}

// qrDataBits is the byte-mode segment, ended and padded to fill the
// version's data codewords.
func qrDataBits(v int, data []byte) []byte {
	var bits []bool
	put := func(val, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, val>>i&1 == 1)
		}
	}
	put(0b0100, 4) // byte mode
	if v >= 10 {
		put(len(data), 16)
	} else {
		put(len(data), 8)
	}
	for _, b := range data {
		put(int(b), 8)
	}
	capBits := qrDataCodewords(v) * 8
	put(0, min(4, capBits-len(bits))) // terminator
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	out := make([]byte, 0, capBits/8)
	for i := 0; i < len(bits); i += 8 {
		var b byte
		for j := range 8 {
			if bits[i+j] {
				b |= 0x80 >> j
			}
		}
		out = append(out, b)
	}
	for pad := byte(0xEC); len(out) < capBits/8; pad ^= 0xEC ^ 0x11 {
		out = append(out, pad)
	}
	return out
}

// ---- Reed–Solomon over GF(256), polynomial x^8+x^4+x^3+x^2+1 ----

var gfExp, gfLog = func() (exp [512]byte, log [256]int) {
	x := 1
	for i := range 255 {
		exp[i] = byte(x)
		log[x] = i
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		exp[i] = exp[i-255]
	}
	return
}()

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[gfLog[a]+gfLog[b]]
}

// rsGenerator is the product of (x − α^i) for i < n, highest power first
// (its leading 1 left out).
func rsGenerator(n int) []byte {
	g := []byte{1}
	for i := range n {
		next := make([]byte, len(g)+1)
		for j, c := range g {
			next[j] ^= c
			next[j+1] ^= gfMul(c, gfExp[i])
		}
		g = next
	}
	return g[1:]
}

// rsRemainder is the error correction codewords for data.
func rsRemainder(data []byte, n int) []byte {
	gen := rsGenerator(n)
	rem := make([]byte, n)
	for _, d := range data {
		f := d ^ rem[0]
		copy(rem, rem[1:])
		rem[n-1] = 0
		for i, g := range gen {
			rem[i] ^= gfMul(g, f)
		}
	}
	return rem
}

// qrInterleave splits the data into blocks, adds each block's error
// correction, and interleaves them as the code is read.
func qrInterleave(v int, data []byte) []byte {
	spec := qrBlocksM[v]
	var blocks, ecs [][]byte
	at := 0
	for _, g := range spec.groups {
		for range g[0] {
			b := data[at : at+g[1]]
			at += g[1]
			blocks = append(blocks, b)
			ecs = append(ecs, rsRemainder(b, spec.ec))
		}
	}
	var out []byte
	for i := 0; ; i++ {
		any := false
		for _, b := range blocks {
			if i < len(b) {
				out, any = append(out, b[i]), true
			}
		}
		if !any {
			break
		}
	}
	for i := range spec.ec {
		for _, e := range ecs {
			out = append(out, e[i])
		}
	}
	return out
}

// ---- The grid ----

func newQR(v int) *qrCode {
	n := 17 + 4*v
	q := &qrCode{size: n, version: v, dark: make([][]bool, n), isFunc: make([][]bool, n)}
	for i := range n {
		q.dark[i], q.isFunc[i] = make([]bool, n), make([]bool, n)
	}
	// Timing patterns.
	for i := range n {
		q.set(6, i, i%2 == 0)
		q.set(i, 6, i%2 == 0)
	}
	// Finder patterns with their white separators.
	for _, c := range [][2]int{{3, 3}, {n - 4, 3}, {3, n - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := c[0]+dx, c[1]+dy
				if x < 0 || y < 0 || x >= n || y >= n {
					continue
				}
				d := max(abs(dx), abs(dy))
				q.set(x, y, d != 2 && d != 4)
			}
		}
	}
	// Alignment patterns, except where they'd sit on a finder.
	al := qrAlign[v]
	for i, ax := range al {
		for j, ay := range al {
			if (i == 0 && j == 0) || (i == 0 && j == len(al)-1) || (i == len(al)-1 && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					q.set(ax+dx, ay+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	q.drawFormat(0) // reserves the format areas; drawn again with the mask chosen
	if v >= 7 {
		rem := v
		for range 12 {
			rem = rem<<1 ^ (rem>>11)*0x1F25
		}
		bits := v<<12 | rem
		for i := range 18 {
			bit := bits>>i&1 == 1
			a, b := n-11+i%3, i/3
			q.set(a, b, bit)
			q.set(b, a, bit)
		}
	}
	return q
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// set marks a function module (not data); x is the column, y the row.
func (q *qrCode) set(x, y int, dark bool) {
	q.dark[y][x], q.isFunc[y][x] = dark, true
}

// qrFormatBits is the 15-bit format word for level M and a mask.
func qrFormatBits(mask int) int {
	data := 0b00<<3 | mask // M is 00
	rem := data
	for range 10 {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	return (data<<10 | rem) ^ 0x5412
}

func (q *qrCode) drawFormat(mask int) {
	bits, n := qrFormatBits(mask), q.size
	bit := func(i int) bool { return bits>>i&1 == 1 }
	for i := range 6 {
		q.set(8, i, bit(i))
	}
	q.set(8, 7, bit(6))
	q.set(8, 8, bit(7))
	q.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		q.set(14-i, 8, bit(i))
	}
	for i := range 8 {
		q.set(n-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		q.set(8, n-15+i, bit(i))
	}
	q.set(8, n-8, true) // the dark module
}

// placeData lays the codewords out in two-column zigzags from the bottom
// right, skipping the vertical timing pattern.
func (q *qrCode) placeData(cw []byte) {
	n, i := q.size, 0
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := range n {
			for j := range 2 {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = n - 1 - vert
				}
				if q.isFunc[y][x] {
					continue
				}
				if i < len(cw)*8 {
					q.dark[y][x] = cw[i>>3]>>(7-i&7)&1 == 1
					i++
				}
			}
		}
	}
}

func qrMask(m, x, y int) bool {
	switch m {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	}
	return ((x+y)%2+x*y%3)%2 == 0
}

func (q *qrCode) applyMask(m int) {
	for y := range q.size {
		for x := range q.size {
			if !q.isFunc[y][x] && qrMask(m, x, y) {
				q.dark[y][x] = !q.dark[y][x]
			}
		}
	}
}

// penalty scores a masked code the standard's way (lower reads better):
// runs of one color, 2×2 blocks, finder-like patterns, and dark/light
// balance.
func (q *qrCode) penalty() int {
	n, score := q.size, 0
	line := func(get func(i int) bool) {
		run := 1
		for i := 1; i <= n; i++ {
			if i < n && get(i) == get(i-1) {
				run++
				continue
			}
			if run >= 5 {
				score += 3 + run - 5
			}
			run = 1
		}
		for i := 0; i+11 <= n; i++ { // 1011101 with four light on one side
			p := [11]bool{}
			for k := range 11 {
				p[k] = get(i + k)
			}
			core := p[4] && !p[5] && p[6] && p[7] && p[8] && !p[9] && p[10]
			if core && !p[0] && !p[1] && !p[2] && !p[3] {
				score += 40
			}
			core = p[0] && !p[1] && p[2] && p[3] && p[4] && !p[5] && p[6]
			if core && !p[7] && !p[8] && !p[9] && !p[10] {
				score += 40
			}
		}
	}
	for y := range n {
		line(func(i int) bool { return q.dark[y][i] })
	}
	for x := range n {
		line(func(i int) bool { return q.dark[i][x] })
	}
	darkN := 0
	for y := range n {
		for x := range n {
			if q.dark[y][x] {
				darkN++
			}
			if x+1 < n && y+1 < n {
				c := q.dark[y][x]
				if q.dark[y][x+1] == c && q.dark[y+1][x] == c && q.dark[y+1][x+1] == c {
					score += 3
				}
			}
		}
	}
	k := abs(darkN*20-n*n*10) / (n * n) // steps of 5% away from half dark
	return score + k*10
}

// SVG draws the code with the quiet zone around it, scaling to its box.
func (q *qrCode) SVG(label string) string {
	n := q.size + 8
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" class="qr" viewBox="0 0 %d %d" role="img" aria-label="%s" shape-rendering="crispEdges"><rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n, template.HTMLEscapeString(label), n, n)
	for y := range q.size {
		for x := range q.size {
			if q.dark[y][x] {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+4, y+4)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

// qrSVG is the template function: a code for a web address.
func qrSVG(text, label string) template.HTML {
	q, err := qrEncode(text)
	if err != nil {
		return ""
	}
	return template.HTML(q.SVG(label))
}

package playback

// Export watermark (WAVE8-DETECT-EVIDENCE.md, Stream D). The macOS ffmpeg
// bundle has no drawtext (no freetype), so the text is rendered here, in Go,
// to a semi-transparent PNG strip that ffmpeg composites with `overlay`.
// The glyphs are a small built-in 5x7 bitmap font (no font files, no
// third-party licenses); lowercase renders as uppercase and anything outside
// the table as '?'.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
)

const (
	glyphW, glyphH = 5, 7
	// WatermarkScale is the pixel size of one font dot in the export strip.
	WatermarkScale = 2
	// MaxWatermarkText bounds the rendered text (runes).
	MaxWatermarkText = 120
)

// font5x7 rows are top to bottom, bit 4 = leftmost column.
var font5x7 = map[rune][glyphH]uint8{
	' ': {},
	'A': {0b01110, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'B': {0b11110, 0b10001, 0b10001, 0b11110, 0b10001, 0b10001, 0b11110},
	'C': {0b01110, 0b10001, 0b10000, 0b10000, 0b10000, 0b10001, 0b01110},
	'D': {0b11110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b11110},
	'E': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b11111},
	'F': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b10000},
	'G': {0b01110, 0b10001, 0b10000, 0b10111, 0b10001, 0b10001, 0b01111},
	'H': {0b10001, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'I': {0b01110, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'J': {0b00111, 0b00010, 0b00010, 0b00010, 0b00010, 0b10010, 0b01100},
	'K': {0b10001, 0b10010, 0b10100, 0b11000, 0b10100, 0b10010, 0b10001},
	'L': {0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b11111},
	'M': {0b10001, 0b11011, 0b10101, 0b10101, 0b10001, 0b10001, 0b10001},
	'N': {0b10001, 0b11001, 0b10101, 0b10011, 0b10001, 0b10001, 0b10001},
	'O': {0b01110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'P': {0b11110, 0b10001, 0b10001, 0b11110, 0b10000, 0b10000, 0b10000},
	'Q': {0b01110, 0b10001, 0b10001, 0b10001, 0b10101, 0b10010, 0b01101},
	'R': {0b11110, 0b10001, 0b10001, 0b11110, 0b10100, 0b10010, 0b10001},
	'S': {0b01111, 0b10000, 0b10000, 0b01110, 0b00001, 0b00001, 0b11110},
	'T': {0b11111, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100},
	'U': {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'V': {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01010, 0b00100},
	'W': {0b10001, 0b10001, 0b10001, 0b10101, 0b10101, 0b11011, 0b10001},
	'X': {0b10001, 0b10001, 0b01010, 0b00100, 0b01010, 0b10001, 0b10001},
	'Y': {0b10001, 0b10001, 0b01010, 0b00100, 0b00100, 0b00100, 0b00100},
	'Z': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b10000, 0b11111},
	'0': {0b01110, 0b10001, 0b10011, 0b10101, 0b11001, 0b10001, 0b01110},
	'1': {0b00100, 0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'2': {0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0b01000, 0b11111},
	'3': {0b11110, 0b00001, 0b00001, 0b01110, 0b00001, 0b00001, 0b11110},
	'4': {0b00010, 0b00110, 0b01010, 0b10010, 0b11111, 0b00010, 0b00010},
	'5': {0b11111, 0b10000, 0b11110, 0b00001, 0b00001, 0b10001, 0b01110},
	'6': {0b00110, 0b01000, 0b10000, 0b11110, 0b10001, 0b10001, 0b01110},
	'7': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b01000, 0b01000},
	'8': {0b01110, 0b10001, 0b10001, 0b01110, 0b10001, 0b10001, 0b01110},
	'9': {0b01110, 0b10001, 0b10001, 0b01111, 0b00001, 0b00010, 0b01100},
	'-': {0, 0, 0, 0b11111, 0, 0, 0},
	'+': {0, 0b00100, 0b00100, 0b11111, 0b00100, 0b00100, 0},
	':': {0, 0b00100, 0b00100, 0, 0b00100, 0b00100, 0},
	'.': {0, 0, 0, 0, 0, 0b01100, 0b01100},
	',': {0, 0, 0, 0, 0b01100, 0b00100, 0b01000},
	'_': {0, 0, 0, 0, 0, 0, 0b11111},
	'/': {0b00001, 0b00001, 0b00010, 0b00100, 0b01000, 0b10000, 0b10000},
	'|': {0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100},
	'(': {0b00010, 0b00100, 0b01000, 0b01000, 0b01000, 0b00100, 0b00010},
	')': {0b01000, 0b00100, 0b00010, 0b00010, 0b00010, 0b00100, 0b01000},
	'#': {0b01010, 0b01010, 0b11111, 0b01010, 0b11111, 0b01010, 0b01010},
	'@': {0b01110, 0b10001, 0b10111, 0b10101, 0b10110, 0b10000, 0b01110},
	'?': {0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0, 0b00100},
}

// WatermarkText normalizes text for the strip: upper-cased, unknown glyphs
// (and control characters) replaced by '?', capped at MaxWatermarkText runes.
func WatermarkText(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.ToUpper(s) {
		if n >= MaxWatermarkText {
			break
		}
		if _, ok := font5x7[r]; !ok {
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// WatermarkSize is the pixel size of the strip RenderWatermark makes for
// text at the given scale.
func WatermarkSize(text string, scale int) (w, h int) {
	if scale < 1 {
		scale = 1
	}
	n := len([]rune(WatermarkText(text)))
	pad := 3 * scale
	if n == 0 {
		return 2 * pad, glyphH*scale + 2*pad
	}
	return 2*pad + (n*(glyphW+1)-1)*scale, glyphH*scale + 2*pad
}

// RenderWatermark draws text white on a semi-transparent black strip and
// returns it as a PNG. Output is deterministic for a given text and scale.
func RenderWatermark(text string, scale int) ([]byte, error) {
	if scale < 1 {
		scale = 1
	}
	text = WatermarkText(text)
	w, h := WatermarkSize(text, scale)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	bg := color.NRGBA{0, 0, 0, 150}
	fg := color.NRGBA{255, 255, 255, 235}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, bg)
		}
	}
	pad := 3 * scale
	for i, r := range []rune(text) {
		rows := font5x7[r]
		ox := pad + i*(glyphW+1)*scale
		for gy := 0; gy < glyphH; gy++ {
			for gx := 0; gx < glyphW; gx++ {
				if rows[gy]&(1<<(glyphW-1-gx)) == 0 {
					continue
				}
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						img.SetNRGBA(ox+gx*scale+dx, pad+gy*scale+dy, fg)
					}
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

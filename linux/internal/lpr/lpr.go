// Package lpr reads licence plates (WAVE16-DETECTION.md, stream D): PaddleOCR
// text detection (the DB head) finds text boxes in a vehicle crop, and the
// PaddleOCR CRNN recognizer with CTC decoding reads each box. Both models run
// through the same purego ONNX Runtime binding object detection uses. The
// plate is the best box whose shape and text look like a plate.
package lpr

import (
	"errors"
	"image"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Plate-shaped text: what a box and its text must look like to be kept.
const (
	MinAspect     = 1.5 // box width / height
	MaxAspect     = 7.0
	MinBoxHeight  = 12  // pixels in the crop the reader was given
	MinChars      = 4   // after normalization
	MaxChars      = 8   //
	MinConfidence = 0.6 // mean character confidence
)

// DB post-processing (PaddleOCR's defaults for the DB head).
const (
	DetMaxSide   = 640 // longer side of the detector input
	detThresh    = 0.3 // probability map binarization
	detBoxThresh = 0.5 // mean probability inside a component
	detUnclip    = 1.5 // DB unclip ratio
	detMinSide   = 3   // components thinner than this (map pixels) are noise
	maxBoxes     = 32  // recognized per crop, biggest first
)

// Recognizer input (PP-OCRv4 rec): height 48, width by aspect, at least 320.
const (
	RecHeight   = 48
	recMinWidth = 320
	recMaxWidth = 960
)

// Result is one plate read.
type Result struct {
	Text       string          // normalized, A–Z0–9
	Confidence float64         // mean character confidence of Text
	Box        image.Rectangle // in the input image's coordinates
	Raw        string          // what the recognizer read, before normalization
}

// Reader reads the plate in a vehicle crop. Implementations are safe for
// concurrent use. A nil Result with a nil error means no plate was found.
type Reader interface {
	Read(img image.Image) (*Result, error)
	// DetModel and RecModel name the models (file SHA-256 prefixes).
	DetModel() string
	RecModel() string
}

// Net runs one network: a float32 tensor of shape in, the first output and
// its shape out. *ort.Session satisfies it; tests use fakes.
type Net interface {
	Run(input []float32, shape []int64) ([]float32, []int64, error)
}

// Engine is the production Reader: a detector Net, a recognizer Net and the
// recognizer's dictionary.
type Engine struct {
	det, rec     Net
	dict         []string // index i+1 of the recognizer output (0 is the CTC blank)
	detID, recID string
	mu           sync.Mutex // one read at a time: bounds CPU next to the video work
}

// NewEngine wires the two networks. dict is the recognizer's character list
// (one entry per line of plate_rec.txt); a trailing space class is added as
// PaddleOCR does.
func NewEngine(det, rec Net, dict []string, detID, recID string) (*Engine, error) {
	if det == nil || rec == nil {
		return nil, errors.New("no model")
	}
	if len(dict) == 0 {
		return nil, errors.New("empty dictionary")
	}
	return &Engine{det: det, rec: rec, dict: append(append([]string{}, dict...), " "), detID: detID, recID: recID}, nil
}

func (e *Engine) DetModel() string { return e.detID }
func (e *Engine) RecModel() string { return e.recID }

// ParseDict reads a PaddleOCR dictionary: one character per line.
func ParseDict(raw string) []string {
	raw = strings.TrimPrefix(raw, "\ufeff")
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Read implements Reader.
func (e *Engine) Read(img image.Image) (*Result, error) {
	if img == nil || img.Bounds().Dx() < 8 || img.Bounds().Dy() < 8 {
		return nil, nil
	}
	px := toBGR(img)
	e.mu.Lock()
	defer e.mu.Unlock()
	// The whole crop first; when it holds no plate, the lower part of the
	// vehicle in two overlapping halves, each scaled up to the detector's
	// size (a small plate in a wide crop is too thin for the detector).
	for _, region := range regions(px.w, px.h) {
		boxes, err := e.detect(px, region)
		if err != nil {
			return nil, err
		}
		best, err := e.bestRead(px, boxes)
		if err != nil || best != nil {
			if best != nil {
				best.Box = best.Box.Add(img.Bounds().Min)
			}
			return best, err
		}
	}
	return nil, nil
}

// regions are where Read looks: the whole w×h crop, then the left and right
// 60 % of its lower 70 %.
func regions(w, h int) []image.Rectangle {
	all := image.Rect(0, 0, w, h)
	top := h * 3 / 10
	lw := w * 6 / 10
	tiles := []image.Rectangle{image.Rect(0, top, lw, h), image.Rect(w-lw, top, w, h)}
	out := []image.Rectangle{all}
	for _, t := range tiles {
		if t.Dx() >= 16 && t.Dy() >= 16 {
			out = append(out, t)
		}
	}
	return out
}

// bestRead recognizes the plate-shaped boxes and returns the best plate.
func (e *Engine) bestRead(px *bgr, boxes []image.Rectangle) (*Result, error) {
	var best *Result
	bestH, bestDigit := 0, false
	for _, b := range boxes {
		w, h := b.Dx(), b.Dy()
		if h < MinBoxHeight {
			continue
		}
		if a := float64(w) / float64(h); a < MinAspect || a > MaxAspect {
			continue
		}
		raw, conf, err := e.recognize(px, b)
		if err != nil {
			return nil, err
		}
		text, c := normalizeRead(raw, conf)
		if len(text) < MinChars || len(text) > MaxChars || c < MinConfidence {
			continue
		}
		// The plate number almost always has a digit (badges, state names
		// and dealer frames rarely do) and is the biggest plate-shaped text
		// on the plate; the confidence breaks ties.
		dig := hasDigit(text)
		if best == nil || better(dig, h, c, bestDigit, bestH, best.Confidence) {
			best, bestH, bestDigit = &Result{Text: text, Confidence: c, Box: b, Raw: string(raw)}, h, dig
		}
	}
	return best, nil
}

func hasDigit(s string) bool { return strings.ContainsAny(s, "0123456789") }

// better ranks candidate a over b: a digit first, then height, then confidence.
func better(aDigit bool, aH int, aC float64, bDigit bool, bH int, bC float64) bool {
	if aDigit != bDigit {
		return aDigit
	}
	if aH != bH {
		return aH > bH
	}
	return aC > bC
}

// Normalize upper-cases s and keeps only A–Z and 0–9.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeRead normalizes a read character by character and averages the
// confidence of the characters it keeps.
func normalizeRead(raw []rune, conf []float64) (string, float64) {
	var b strings.Builder
	var sum float64
	n := 0
	for i, r := range raw {
		u := []rune(strings.ToUpper(string(r)))
		if len(u) != 1 {
			continue
		}
		c := u[0]
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
			sum += conf[i]
			n++
		}
	}
	if n == 0 {
		return "", 0
	}
	return b.String(), sum / float64(n)
}

// MARK: - Images

// bgr is a packed 8-bit image, row-major, B G R per pixel (PaddleOCR's
// channel order: its models were trained on OpenCV images).
type bgr struct {
	w, h int
	px   []uint8
}

func toBGR(img image.Image) *bgr {
	b := img.Bounds()
	out := &bgr{w: b.Dx(), h: b.Dy(), px: make([]uint8, 3*b.Dx()*b.Dy())}
	switch src := img.(type) {
	case *image.YCbCr:
		for y := 0; y < out.h; y++ {
			for x := 0; x < out.w; x++ {
				c := src.YCbCrAt(b.Min.X+x, b.Min.Y+y)
				r, g, bl, _ := c.RGBA()
				i := 3 * (y*out.w + x)
				out.px[i], out.px[i+1], out.px[i+2] = uint8(bl>>8), uint8(g>>8), uint8(r>>8)
			}
		}
	default:
		for y := 0; y < out.h; y++ {
			for x := 0; x < out.w; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				i := 3 * (y*out.w + x)
				out.px[i], out.px[i+1], out.px[i+2] = uint8(bl>>8), uint8(g>>8), uint8(r>>8)
			}
		}
	}
	return out
}

// resizeCHW resamples rect of src (bilinear, pixel centres aligned) to ow×oh
// into dst (CHW, stride dstW per row, dstW >= ow), mapping each 8-bit value
// v of channel c through norm(c, v).
func resizeCHW(src *bgr, rect image.Rectangle, ow, oh, dstW int, dst []float32, norm func(c int, v float64) float32) {
	w, h := rect.Dx(), rect.Dy()
	plane := oh * dstW
	sx, sy := float64(w)/float64(ow), float64(h)/float64(oh)
	for y := 0; y < oh; y++ {
		fy := math.Max(0, (float64(y)+0.5)*sy-0.5)
		y0 := int(fy)
		if y0 > h-1 {
			y0 = h - 1
		}
		y1 := min(y0+1, h-1)
		dy := fy - float64(y0)
		for x := 0; x < ow; x++ {
			fx := math.Max(0, (float64(x)+0.5)*sx-0.5)
			x0 := int(fx)
			if x0 > w-1 {
				x0 = w - 1
			}
			x1 := min(x0+1, w-1)
			dx := fx - float64(x0)
			r0 := 3 * ((rect.Min.Y+y0)*src.w + rect.Min.X)
			r1 := 3 * ((rect.Min.Y+y1)*src.w + rect.Min.X)
			for c := 0; c < 3; c++ {
				p00 := float64(src.px[r0+3*x0+c])
				p01 := float64(src.px[r0+3*x1+c])
				p10 := float64(src.px[r1+3*x0+c])
				p11 := float64(src.px[r1+3*x1+c])
				v := (p00*(1-dx)+p01*dx)*(1-dy) + (p10*(1-dx)+p11*dx)*dy
				dst[c*plane+y*dstW+x] = norm(c, v)
			}
		}
	}
}

// MARK: - Detection (DB)

// PaddleOCR's det normalization, applied in BGR order as PaddleOCR does.
var (
	detMean = [3]float64{0.485, 0.456, 0.406}
	detStd  = [3]float64{0.229, 0.224, 0.225}
)

// DetSize is the detector input for a w×h crop: the longer side scaled to
// DetMaxSide (up or down), each side rounded to a multiple of 32.
func DetSize(w, h int) (int, int) {
	r := float64(DetMaxSide) / float64(max(w, h))
	round32 := func(v float64) int { return max(32, int(math.Round(v/32))*32) }
	return round32(float64(w) * r), round32(float64(h) * r)
}

// detect returns the text boxes inside region of src, in src coordinates,
// biggest first.
func (e *Engine) detect(src *bgr, region image.Rectangle) ([]image.Rectangle, error) {
	dw, dh := DetSize(region.Dx(), region.Dy())
	in := make([]float32, 3*dw*dh)
	resizeCHW(src, region, dw, dh, dw, in, func(c int, v float64) float32 {
		return float32((v/255 - detMean[c]) / detStd[c])
	})
	out, shape, err := e.det.Run(in, []int64{1, 3, int64(dh), int64(dw)})
	if err != nil {
		return nil, err
	}
	if len(shape) != 4 || shape[2] != int64(dh) || shape[3] != int64(dw) || len(out) < dw*dh {
		return nil, errors.New("plate detector output is not a probability map of the input's size")
	}
	boxes := dbBoxes(out[:dw*dh], dw, dh)
	sx, sy := float64(region.Dx())/float64(dw), float64(region.Dy())/float64(dh)
	rects := make([]image.Rectangle, 0, len(boxes))
	for _, b := range boxes {
		r := image.Rect(int(math.Floor(b[0]*sx)), int(math.Floor(b[1]*sy)), int(math.Ceil(b[2]*sx)), int(math.Ceil(b[3]*sy))).
			Add(region.Min).Intersect(region)
		if !r.Empty() {
			rects = append(rects, r)
		}
	}
	rects = mergeLine(rects)
	sort.SliceStable(rects, func(i, j int) bool { return rects[i].Dx()*rects[i].Dy() > rects[j].Dx()*rects[j].Dy() })
	if len(rects) > maxBoxes {
		rects = rects[:maxBoxes]
	}
	return rects, nil
}

// dbBoxes thresholds the probability map, takes 8-connected components and
// returns each one's unclipped bounding box [x1, y1, x2, y2] in map pixels.
func dbBoxes(prob []float32, w, h int) [][4]float64 {
	label := make([]int32, w*h)
	var out [][4]float64
	stack := make([]int, 0, 256)
	next := int32(0)
	for start := range prob {
		if prob[start] <= detThresh || label[start] != 0 {
			continue
		}
		next++
		label[start] = next
		stack = append(stack[:0], start)
		minX, minY, maxX, maxY := w, h, -1, -1
		var sum float64
		n := 0
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := p%w, p/w
			minX, maxX = min(minX, x), max(maxX, x)
			minY, maxY = min(minY, y), max(maxY, y)
			sum += float64(prob[p])
			n++
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if nx < 0 || ny < 0 || nx >= w || ny >= h {
						continue
					}
					q := ny*w + nx
					if label[q] == 0 && prob[q] > detThresh {
						label[q] = next
						stack = append(stack, q)
					}
				}
			}
		}
		bw, bh := float64(maxX-minX+1), float64(maxY-minY+1)
		if bw < detMinSide || bh < detMinSide || sum/float64(n) < detBoxThresh {
			continue
		}
		// DB unclip: grow by area × ratio / perimeter on every side.
		d := bw * bh * detUnclip / (2 * (bw + bh))
		out = append(out, [4]float64{
			math.Max(0, float64(minX)-d), math.Max(0, float64(minY)-d),
			math.Min(float64(w), float64(maxX+1)+d), math.Min(float64(h), float64(maxY+1)+d),
		})
	}
	return out
}

// mergeLine joins boxes that sit side by side on one text line (similar
// height and centre, a gap under one text height): the detector often splits
// "71 AMX" or "GN·PR·77" into words, and a plate reads as one line.
func mergeLine(rs []image.Rectangle) []image.Rectangle {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Min.X < rs[j].Min.X })
	used := make([]bool, len(rs))
	var out []image.Rectangle
	for i := range rs {
		if used[i] {
			continue
		}
		cur := rs[i]
		used[i] = true
		for grew := true; grew; {
			grew = false
			for j := range rs {
				if used[j] || !sameLine(cur, rs[j]) {
					continue
				}
				cur = cur.Union(rs[j])
				used[j] = true
				grew = true
			}
		}
		out = append(out, cur)
	}
	return out
}

func sameLine(a, b image.Rectangle) bool {
	ha, hb := float64(a.Dy()), float64(b.Dy())
	if math.Min(ha, hb) < 0.7*math.Max(ha, hb) {
		return false
	}
	ca, cb := float64(a.Min.Y+a.Max.Y)/2, float64(b.Min.Y+b.Max.Y)/2
	if math.Abs(ca-cb) > 0.25*math.Max(ha, hb) {
		return false
	}
	gap := float64(max(a.Min.X, b.Min.X) - min(a.Max.X, b.Max.X))
	return gap < 0.8*math.Max(ha, hb)
}

// MARK: - Recognition (CRNN + CTC)

// recognize reads the text in box r of src: the characters and each one's
// probability.
func (e *Engine) recognize(src *bgr, r image.Rectangle) ([]rune, []float64, error) {
	w := int(math.Ceil(float64(RecHeight) * float64(r.Dx()) / float64(r.Dy())))
	w = max(1, min(w, recMaxWidth))
	padW := max(w, recMinWidth)
	in := make([]float32, 3*RecHeight*padW) // the padding stays 0 (mid-gray after normalization)
	resizeCHW(src, r, w, RecHeight, padW, in, func(_ int, v float64) float32 { return float32((v/255 - 0.5) / 0.5) })
	out, shape, err := e.rec.Run(in, []int64{1, 3, RecHeight, int64(padW)})
	if err != nil {
		return nil, nil, err
	}
	if len(shape) != 3 || shape[0] != 1 {
		return nil, nil, errors.New("plate recognizer output is not [1, steps, classes]")
	}
	steps, classes := int(shape[1]), int(shape[2])
	if classes != len(e.dict)+1 && classes != len(e.dict) {
		return nil, nil, errors.New("plate recognizer output does not match the dictionary (" +
			strconv.Itoa(classes) + " classes, " + strconv.Itoa(len(e.dict)-1) + " characters)")
	}
	text, conf := ctcDecode(out, steps, classes, e.dict)
	return text, conf, nil
}

// ctcDecode is greedy CTC: the best class per step, repeats collapsed,
// blanks (class 0) dropped. Class k > 0 is dict[k-1].
func ctcDecode(out []float32, steps, classes int, dict []string) ([]rune, []float64) {
	var text []rune
	var conf []float64
	prev := -1
	for t := 0; t < steps; t++ {
		row := out[t*classes : (t+1)*classes]
		best, p := 0, row[0]
		for k := 1; k < classes; k++ {
			if row[k] > p {
				best, p = k, row[k]
			}
		}
		if best != 0 && best != prev && best-1 < len(dict) {
			for _, r := range dict[best-1] {
				text = append(text, r)
				conf = append(conf, float64(p))
			}
		}
		prev = best
	}
	return text, conf
}

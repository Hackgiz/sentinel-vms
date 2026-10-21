package lpr

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDet answers a probability map with one bright rectangle at box
// (normalized to the input), or a broken shape.
type fakeDet struct {
	boxes [][4]float64
	bad   bool
	err   error
	calls int
}

func (f *fakeDet) Run(in []float32, shape []int64) ([]float32, []int64, error) {
	f.calls++
	if f.err != nil {
		return nil, nil, f.err
	}
	h, w := int(shape[2]), int(shape[3])
	if f.bad {
		return make([]float32, 10), []int64{1, 10}, nil
	}
	out := make([]float32, w*h)
	for _, b := range f.boxes {
		for y := int(b[1] * float64(h)); y < int(b[3]*float64(h)); y++ {
			for x := int(b[0] * float64(w)); x < int(b[2]*float64(w)); x++ {
				out[y*w+x] = 0.9
			}
		}
	}
	return out, []int64{1, 1, int64(h), int64(w)}, nil
}

// fakeRec reads text with probability p per character, separated by
// blanks, over a fixed number of steps.
type fakeRec struct {
	text    string
	p       float32
	classes int // 0 = dict+2
	dict    []string
	widths  []int64
}

func (f *fakeRec) Run(in []float32, shape []int64) ([]float32, []int64, error) {
	f.widths = append(f.widths, shape[3])
	classes := f.classes
	if classes == 0 {
		classes = len(f.dict) + 2
	}
	steps := 40
	out := make([]float32, steps*classes)
	for t := 0; t < steps; t++ {
		out[t*classes] = 0.5 // blank
	}
	for i, r := range f.text {
		t := 2*i + 1
		k := len(f.dict) + 1 // space
		for j, d := range f.dict {
			if d == string(r) {
				k = j + 1
			}
		}
		out[t*classes] = 0
		out[t*classes+k] = f.p
	}
	return out, []int64{1, int64(steps), int64(classes)}, nil
}

var testDict = ParseDict("0\n1\n2\n3\n4\n5\n6\n7\n8\n9\nA\nB\nC\nD\nE\nF\nG\nH\nI\nJ\nK\nL\nM\nN\nO\nP\nQ\nR\nS\nT\nU\nV\nW\nX\nY\nZ\na\nb\n-\n")

func gray(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 128
	}
	return img
}

func engine(t *testing.T, det Net, rec Net) *Engine {
	t.Helper()
	e, err := NewEngine(det, rec, testDict, "sha256:det", "sha256:rec")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"ab-12 3": "AB123", "GN·PR·77": "GNPR77", " 71 amx ": "71AMX", "Ünï": "N", "": ""} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	text, c := normalizeRead([]rune("a-1 b"), []float64{0.9, 0.1, 0.7, 0.1, 0.5})
	if text != "A1B" || c < 0.699 || c > 0.701 {
		t.Fatalf("normalizeRead = %q %v (dropped characters must not count)", text, c)
	}
}

func TestParseDict(t *testing.T) {
	d := ParseDict("\ufeff0\r\n1\r\n\r\nA\n")
	if strings.Join(d, ",") != "0,1,A" {
		t.Fatalf("dict %q", d)
	}
}

func TestCTCDecode(t *testing.T) {
	dict := []string{"A", "B", " "}
	// steps: A A blank A B B blank → "AAB"
	rows := [][]float32{{0, 1, 0, 0}, {0, .8, 0, 0}, {1, 0, 0, 0}, {0, .6, 0, 0}, {0, 0, .9, 0}, {0, 0, .7, 0}, {1, 0, 0, 0}}
	var out []float32
	for _, r := range rows {
		out = append(out, r...)
	}
	text, conf := ctcDecode(out, len(rows), 4, dict)
	if string(text) != "AAB" || len(conf) != 3 || conf[0] != 1 || conf[1] != float64(float32(.6)) {
		t.Fatalf("ctc = %q %v", string(text), conf)
	}
}

func TestDetSize(t *testing.T) {
	for _, c := range []struct{ w, h, ww, wh int }{{640, 480, 640, 480}, {1280, 720, 640, 352}, {200, 100, 640, 320}, {100, 400, 160, 640}, {10, 2000, 32, 640}} {
		if w, h := DetSize(c.w, c.h); w != c.ww || h != c.wh || w%32 != 0 || h%32 != 0 || max(w, h) > DetMaxSide {
			t.Errorf("DetSize(%d,%d) = %d,%d want %d,%d", c.w, c.h, w, h, c.ww, c.wh)
		}
	}
}

func TestDBBoxesUnclip(t *testing.T) {
	w, h := 100, 50
	prob := make([]float32, w*h)
	for y := 20; y < 30; y++ { // a 40×10 component
		for x := 10; x < 50; x++ {
			prob[y*w+x] = 0.8
		}
	}
	prob[0] = 0.9 // a 1-pixel speck: dropped
	boxes := dbBoxes(prob, w, h)
	if len(boxes) != 1 {
		t.Fatalf("boxes %v", boxes)
	}
	d := 40.0 * 10 * detUnclip / (2 * 50) // 6
	b := boxes[0]
	if b[0] != 10-d || b[1] != 20-d || b[2] != 50+d || b[3] != 30+d {
		t.Fatalf("unclipped box %v, want grown by %v", b, d)
	}
	// Weak components (mean below the box threshold) go.
	for i := range prob {
		if prob[i] > 0 {
			prob[i] = 0.35
		}
	}
	if boxes := dbBoxes(prob, w, h); len(boxes) != 0 {
		t.Fatalf("weak boxes kept: %v", boxes)
	}
}

func TestMergeLine(t *testing.T) {
	words := []image.Rectangle{image.Rect(100, 50, 130, 70), image.Rect(10, 50, 40, 70), image.Rect(50, 51, 90, 71), image.Rect(10, 90, 60, 98)}
	got := mergeLine(words)
	if len(got) != 2 || got[0] != image.Rect(10, 50, 130, 71) || got[1] != image.Rect(10, 90, 60, 98) {
		t.Fatalf("merged %v", got)
	}
	// A far-away word on the same line stays apart.
	if got := mergeLine([]image.Rectangle{image.Rect(0, 0, 30, 20), image.Rect(80, 0, 110, 20)}); len(got) != 2 {
		t.Fatalf("far words merged: %v", got)
	}
}

func TestReadPicksThePlate(t *testing.T) {
	// 400×200 crop: a plate-shaped box [0.35,0.6]-[0.65,0.75] → 120×30 px
	// (aspect 4) before unclip.
	det := &fakeDet{boxes: [][4]float64{{0.35, 0.6, 0.65, 0.75}}}
	rec := &fakeRec{text: "ab-123", p: 0.9, dict: testDict}
	r, err := engine(t, det, rec).Read(gray(400, 200))
	if err != nil || r == nil {
		t.Fatalf("read %v %v", r, err)
	}
	if r.Text != "AB123" || r.Raw != "ab-123" || r.Confidence < 0.89 {
		t.Fatalf("result %+v", r)
	}
	if !r.Box.In(image.Rect(0, 0, 400, 200)) || r.Box.Dy() < 30 {
		t.Fatalf("box %v", r.Box)
	}
	if rec.widths[0] != recMinWidth {
		t.Fatalf("recognizer input width %d, want padded to %d", rec.widths[0], recMinWidth)
	}
	if det.calls != 1 {
		t.Fatalf("detector ran %d times; the first pass found the plate", det.calls)
	}
}

func TestReadFilters(t *testing.T) {
	cases := []struct {
		name string
		box  [4]float64
		text string
		p    float32
	}{
		{"too tall (aspect < 1.5)", [4]float64{0.4, 0.3, 0.5, 0.7}, "AB123", 0.9},
		{"too wide (aspect > 7)", [4]float64{0.0, 0.6, 1.0, 0.65}, "AB123", 0.9},
		{"too short (< 12 px)", [4]float64{0.4, 0.6, 0.47, 0.62}, "AB123", 0.9},
		{"too few characters", [4]float64{0.35, 0.6, 0.65, 0.75}, "A-1", 0.9},
		{"too many characters", [4]float64{0.35, 0.6, 0.65, 0.75}, "ABC123456", 0.9},
		{"low confidence", [4]float64{0.35, 0.6, 0.65, 0.75}, "AB123", 0.55},
	}
	for _, c := range cases {
		det := &fakeDet{boxes: [][4]float64{c.box}}
		r, err := engine(t, det, &fakeRec{text: c.text, p: c.p, dict: testDict}).Read(gray(400, 200))
		if err != nil || r != nil {
			t.Errorf("%s: got %+v %v, want no plate", c.name, r, err)
		}
		if det.calls != 3 {
			t.Errorf("%s: detector ran %d times, want the whole crop and two tiles", c.name, det.calls)
		}
	}
	// Exactly at the confidence bar: kept.
	r, _ := engine(t, &fakeDet{boxes: [][4]float64{{0.35, 0.6, 0.65, 0.75}}}, &fakeRec{text: "AB12", p: MinConfidence, dict: testDict}).Read(gray(400, 200))
	if r == nil || r.Text != "AB12" {
		t.Fatalf("at the bar: %+v", r)
	}
}

func TestReadPrefersDigitsThenHeight(t *testing.T) {
	// Two boxes; the recognizer reads the same text for both, so check
	// the ranking helper directly and through Read.
	if !better(true, 10, 0.6, false, 40, 0.99) || better(false, 40, 0.99, true, 10, 0.6) {
		t.Fatal("a read with a digit must beat a taller one without")
	}
	if !better(true, 30, 0.7, true, 20, 0.99) || !better(true, 20, 0.9, true, 20, 0.8) {
		t.Fatal("height, then confidence")
	}
	det := &fakeDet{boxes: [][4]float64{{0.1, 0.1, 0.4, 0.2}, {0.4, 0.6, 0.8, 0.8}}}
	r, _ := engine(t, det, &fakeRec{text: "AB123", p: 0.9, dict: testDict}).Read(gray(400, 200))
	if r == nil || r.Box.Min.Y < 80 { // the lower box (y 120–160, grown by the unclip), not the upper one (y 20–40)
		t.Fatalf("picked %+v, want the taller box", r)
	}
}

func TestReadErrorsAndSmallImages(t *testing.T) {
	boom := errors.New("boom")
	if _, err := engine(t, &fakeDet{err: boom}, &fakeRec{dict: testDict}).Read(gray(100, 100)); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if r, err := engine(t, &fakeDet{}, &fakeRec{dict: testDict}).Read(gray(4, 100)); r != nil || err != nil {
		t.Fatal("a sliver is no plate and no error")
	}
	if r, err := engine(t, &fakeDet{}, &fakeRec{dict: testDict}).Read(nil); r != nil || err != nil {
		t.Fatal("nil image")
	}
}

func TestVerify(t *testing.T) {
	closed := 0
	closer := func() { closed++ }
	if r, reason := Verify(&fakeDet{}, &fakeRec{dict: testDict}, testDict, "d", "r", closer); r == nil || reason != "" || closed != 0 {
		t.Fatalf("good models: %v %q", r, reason)
	}
	if r, reason := Verify(&fakeDet{bad: true}, &fakeRec{dict: testDict}, testDict, "d", "r", closer); r != nil || !strings.Contains(reason, "detection model failed a test run") || closed != 1 {
		t.Fatalf("bad det: %v %q", r, reason)
	}
	if r, reason := Verify(&fakeDet{}, &fakeRec{dict: testDict, classes: 7}, testDict, "d", "r", closer); r != nil || !strings.Contains(reason, "does not match the dictionary") || closed != 2 {
		t.Fatalf("dictionary mismatch: %v %q", r, reason)
	}
	if r, reason := Verify(&fakeDet{}, &fakeRec{}, nil, "d", "r", closer); r != nil || !strings.Contains(reason, "empty dictionary") {
		t.Fatalf("empty dictionary: %v %q", r, reason)
	}
}

func TestOpenReasons(t *testing.T) {
	for _, env := range []string{DetModelEnv, RecModelEnv, DictEnv} {
		t.Setenv(env, "")
	}
	dir := t.TempDir()
	r, reason := Open(dir, "/nope/libonnxruntime.so")
	if r != nil || !strings.Contains(reason, filepath.Join(dir, "models", "plate_det.onnx")) {
		t.Fatalf("no det: %q", reason)
	}
	if Missing(dir) == "" {
		t.Fatal("Missing says installed")
	}
	_ = os.MkdirAll(filepath.Join(dir, "models"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "models", "plate_det.onnx"), []byte("x"), 0o644)
	if _, reason = Open(dir, "x"); !strings.Contains(reason, "plate_rec.onnx") {
		t.Fatalf("no rec: %q", reason)
	}
	_ = os.WriteFile(filepath.Join(dir, "models", "plate_rec.onnx"), []byte("x"), 0o644)
	if _, reason = Open(dir, "x"); !strings.Contains(reason, "plate_rec.txt") {
		t.Fatalf("no dict: %q", reason)
	}
	_ = os.WriteFile(filepath.Join(dir, "models", "plate_rec.txt"), []byte("A\n"), 0o644)
	if Missing(dir) != "" {
		t.Fatal("all three files are there")
	}
	if _, reason = Open(dir, ""); !strings.Contains(reason, "ONNX Runtime library not found") {
		t.Fatalf("no runtime: %q", reason)
	}
	if _, reason = Open(dir, filepath.Join(dir, "missing.so")); !strings.Contains(reason, "ONNX Runtime unavailable") {
		t.Fatalf("bad runtime: %q", reason)
	}
	// Overrides win.
	t.Setenv(DetModelEnv, "/elsewhere/det.onnx")
	if _, reason = Open(dir, "x"); !strings.Contains(reason, "/elsewhere/det.onnx") {
		t.Fatalf("override: %q", reason)
	}
	if id, err := ModelID(filepath.Join(dir, "models", "plate_rec.txt")); err != nil || !strings.HasPrefix(id, "sha256:") || len(id) != 23 {
		t.Fatalf("model id %q %v", id, err)
	}
}

func TestWarmupImageAndRegions(t *testing.T) {
	img := WarmupImage()
	if img.Bounds().Dx() != 200 || img.At(40, 40) != (color.RGBA{20, 20, 20, 255}) {
		t.Fatal("warm-up image")
	}
	rs := regions(400, 200)
	if len(rs) != 3 || rs[0] != image.Rect(0, 0, 400, 200) || rs[1] != image.Rect(0, 60, 240, 200) || rs[2] != image.Rect(160, 60, 400, 200) {
		t.Fatalf("regions %v", rs)
	}
	if rs := regions(20, 20); len(rs) != 1 {
		t.Fatalf("tiny crop regions %v", rs)
	}
}

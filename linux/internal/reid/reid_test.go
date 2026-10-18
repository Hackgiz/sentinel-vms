package reid

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	v := []float32{0, 1, -1, 0.5, float32(math.Pi), -1e-7, 3.4e38}
	b := Encode(v)
	if len(b) != 4*len(v) {
		t.Fatalf("len = %d", len(b))
	}
	// Little-endian float32: 1.0 is 00 00 80 3f.
	if !bytes.Equal(b[4:8], []byte{0, 0, 0x80, 0x3f}) {
		t.Fatalf("1.0 encoded as % x", b[4:8])
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := range v {
		if got[i] != v[i] {
			t.Fatalf("[%d] = %v, want %v", i, got[i], v[i])
		}
	}
	if _, err := Decode([]byte{1, 2, 3}); err == nil {
		t.Fatal("ragged bytes must fail")
	}
	if v, err := Decode(nil); err != nil || len(v) != 0 {
		t.Fatalf("empty: %v %v", v, err)
	}
}

func TestNormalize(t *testing.T) {
	v, err := Normalize([]float32{3, 4})
	if err != nil || math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("got %v %v", v, err)
	}
	var sum float64
	w, _ := Normalize([]float32{1, 2, 3, 4, 5, -6, 7})
	for _, f := range w {
		sum += float64(f) * float64(f)
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Fatalf("norm² = %v", sum)
	}
	for _, bad := range [][]float32{nil, {0, 0, 0}, {1, float32(math.NaN())}, {float32(math.Inf(1)), 1}} {
		if _, err := Normalize(bad); !errors.Is(err, ErrBadVector) {
			t.Errorf("%v: err = %v", bad, err)
		}
	}
}

func TestCosineRanking(t *testing.T) {
	unit := func(v ...float32) []float32 { u, _ := Normalize(v); return u }
	q := unit(1, 0, 0)
	if c := Cosine(q, q); math.Abs(c-1) > 1e-9 {
		t.Fatalf("self = %v", c)
	}
	if c := Cosine(q, unit(0, 1, 0)); math.Abs(c) > 1e-9 {
		t.Fatalf("orthogonal = %v", c)
	}
	if Cosine(q, []float32{1, 0}) != -1 {
		t.Fatal("dimension mismatch must score -1")
	}

	r := NewRanker(q, 3, 0.1)
	r.Add("far", unit(0, 1, 0))       // 0: under minScore
	r.Add("opposite", unit(-1, 0, 0)) // -1
	r.Add("mid", unit(1, 1, 0))       // 0.707
	r.Add("close", unit(10, 1, 0))    // 0.995
	r.Add("same", unit(2, 0, 0))      // 1
	r.Add("near", unit(3, 1, 0))      // 0.949
	r.Add("wrongdim", []float32{1, 0})
	r.Add("low", unit(1, 2, 0)) // 0.447: worse than the 3 kept
	hits := r.Hits()
	var ids []string
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	if strings.Join(ids, ",") != "same,close,near" {
		t.Fatalf("ranking = %v", hits)
	}
	if hits[0].Score < hits[1].Score || hits[1].Score < hits[2].Score {
		t.Fatalf("not sorted: %v", hits)
	}
}

func TestModelIDFollowsFileContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "reid.onnx")
	_ = os.WriteFile(p, []byte("model one"), 0o644)
	a, err := ModelID(p)
	if err != nil || !strings.HasPrefix(a, "sha256:") || len(a) != len("sha256:")+16 {
		t.Fatalf("id = %q %v", a, err)
	}
	_ = os.WriteFile(p, []byte("model two"), 0o644)
	b, _ := ModelID(p)
	if a == b {
		t.Fatal("a replaced model must get a new id")
	}
	_ = os.WriteFile(p, []byte("model one"), 0o644)
	if c, _ := ModelID(p); c != a {
		t.Fatal("same bytes, same id")
	}
}

func TestModelPathAndOpenDegrade(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ModelEnv, "")
	p, ok := ModelPath(dir)
	if ok || p != filepath.Join(dir, "models", "reid.onnx") {
		t.Fatalf("%s %v", p, ok)
	}
	if e, reason := Open(dir, "/nonexistent/libonnxruntime.so"); e != nil || !strings.Contains(reason, "no appearance model") {
		t.Fatalf("no model: %v %q", e, reason)
	}
	_ = os.MkdirAll(filepath.Join(dir, "models"), 0o755)
	_ = os.WriteFile(p, []byte("not onnx"), 0o644)
	if e, reason := Open(dir, ""); e != nil || !strings.Contains(reason, "ONNX Runtime") {
		t.Fatalf("no runtime: %v %q", e, reason)
	}
	if e, reason := Open(dir, filepath.Join(dir, "missing.so")); e != nil || !strings.Contains(reason, "ONNX Runtime unavailable") {
		t.Fatalf("bad runtime: %v %q", e, reason)
	}
	custom := filepath.Join(dir, "custom.onnx")
	_ = os.WriteFile(custom, []byte("x"), 0o644)
	t.Setenv(ModelEnv, custom)
	if p, ok := ModelPath(dir); !ok || p != custom {
		t.Fatalf("env: %s %v", p, ok)
	}
}

type fakeNet struct {
	out    []float32
	err    error
	gotLen int
	input  []float32
}

func (f *fakeNet) Run(in []float32) ([]float32, error) {
	f.gotLen, f.input = len(in), in
	return f.out, f.err
}

func TestVerifyAndModelEmbedder(t *testing.T) {
	net := &fakeNet{out: []float32{0, 3, 4}}
	closed := 0
	e, reason := Verify(net, "sha256:abc", func() { closed++ })
	if e == nil || reason != "" || closed != 0 {
		t.Fatalf("good net: %v %q", e, reason)
	}
	if net.gotLen != 3*InputH*InputW {
		t.Fatalf("input len %d", net.gotLen)
	}
	if e.Model() != "sha256:abc" {
		t.Fatal(e.Model())
	}
	v, err := e.Embed(solid(40, 90, color.RGBA{200, 10, 10, 255}))
	if err != nil || math.Abs(float64(v[1])-0.6) > 1e-6 || math.Abs(float64(v[2])-0.8) > 1e-6 {
		t.Fatalf("embed: %v %v", v, err)
	}
	// Broken outputs degrade with a reason and close the session.
	for name, n := range map[string]*fakeNet{
		"zero":  {out: []float32{0, 0}},
		"nan":   {out: []float32{float32(math.NaN()), 1}},
		"error": {err: errors.New("shape mismatch")},
		"huge":  {out: make([]float32, MaxDim+1)},
	} {
		closed = 0
		if e, reason := Verify(n, "id", func() { closed++ }); e != nil || !strings.Contains(reason, "test run") || closed != 1 {
			t.Errorf("%s: %v %q closed=%d", name, e, reason, closed)
		}
	}
	if e, reason := Verify(nil, "id", nil); e != nil || reason == "" {
		t.Fatal("nil net must fail")
	}
}

func TestPreprocessLayoutAndNormalization(t *testing.T) {
	// Left half pure red, right half pure blue: channel planes and the
	// ImageNet normalization must land where the model expects them.
	img := image.NewRGBA(image.Rect(0, 0, 64, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 64; x++ {
			if x < 32 {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.Set(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	in := Preprocess(img)
	if len(in) != 3*InputH*InputW {
		t.Fatalf("len %d", len(in))
	}
	plane := InputH * InputW
	at := func(c, y, x int) float32 { return in[c*plane+y*InputW+x] }
	want := func(c int, v float32) float32 { return (v - imagenetMean[c]) / imagenetStd[c] }
	near := func(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-4 }
	if !near(at(0, 10, 5), want(0, 1)) || !near(at(1, 10, 5), want(1, 0)) || !near(at(2, 10, 5), want(2, 0)) {
		t.Fatalf("left pixel = %v %v %v", at(0, 10, 5), at(1, 10, 5), at(2, 10, 5))
	}
	if !near(at(0, 200, 120), want(0, 0)) || !near(at(2, 200, 120), want(2, 1)) {
		t.Fatalf("right pixel = %v %v", at(0, 200, 120), at(2, 200, 120))
	}
}

func TestCropTakesTheBoxNotTheFrame(t *testing.T) {
	// A 200×100 frame: gray everywhere except a green person box at
	// [0.5,0.2]-[0.7,0.9] outlined in blue like detect.FrameJPEG does.
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{128, 128, 128, 255})
		}
	}
	box := [4]float64{0.5, 0.2, 0.7, 0.9}
	for y := 20; y < 90; y++ {
		for x := 100; x < 140; x++ {
			c := color.RGBA{0, 200, 0, 255}
			if x < 102 || x >= 138 || y < 22 || y >= 88 {
				c = color.RGBA{0x1a, 0x7f, 0xd4, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95})
	crop, err := CropJPEG(buf.Bytes(), box)
	if err != nil {
		t.Fatal(err)
	}
	if b := crop.Bounds(); b.Dx() != 36 || b.Dy() != 66 || b.Min != image.Pt(102, 22) {
		t.Fatalf("crop bounds %v", b)
	}
	// The crop's average is green, not frame gray, and carries no outline blue.
	var gs, rs, n float64
	b := crop.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, _, _ := crop.At(x, y).RGBA()
			rs += float64(r >> 8)
			gs += float64(g >> 8)
			n++
		}
	}
	if gs/n < 170 || rs/n > 40 {
		t.Fatalf("crop is not the box: mean r=%v g=%v", rs/n, gs/n)
	}

	if _, err := CropJPEG(buf.Bytes(), [4]float64{0.1, 0.1, 0.12, 0.13}); !errors.Is(err, ErrTooSmall) {
		t.Fatalf("tiny box: %v", err)
	}
	if _, err := CropJPEG([]byte("not a jpeg"), box); err == nil {
		t.Fatal("bad jpeg must fail")
	}
}

func TestParseBox(t *testing.T) {
	if b, err := ParseBox("[0.1,0.2,0.5,0.9]"); err != nil || b != [4]float64{0.1, 0.2, 0.5, 0.9} {
		t.Fatalf("%v %v", b, err)
	}
	if b, err := ParseBox("[-0.1,0.2,1.5,0.9]"); err != nil || b[0] != 0 || b[2] != 1 {
		t.Fatalf("clamp: %v %v", b, err)
	}
	for _, bad := range []string{"", "null", "[1,2]", "[0.5,0.5,0.4,0.9]", "[0,0,0,0]", "junk"} {
		if _, err := ParseBox(bad); !errors.Is(err, ErrNoBox) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func solid(w, h int, c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

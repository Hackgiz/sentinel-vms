package faces

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"math"
	"os"
	"strings"
	"testing"

	"sentinel-linux/internal/reid"
)

// unitPair returns two unit vectors whose cosine is exactly cos.
func unitPair(cos float64) ([]float32, []float32) {
	a := []float32{1, 0}
	b := []float32{float32(cos), float32(math.Sqrt(1 - cos*cos))}
	return a, b
}

func TestMatchThresholdBoundary(t *testing.T) {
	if MatchThreshold != 0.363 {
		t.Fatalf("MatchThreshold = %v; SFace's published cosine threshold is 0.363", MatchThreshold)
	}
	if !Matches(0.363) || !Matches(0.3631) || Matches(0.3629) || Matches(-1) {
		t.Fatal("Matches must be cosine >= 0.363")
	}
	// Through Best, with vectors right at and right below the threshold.
	q, at := unitPair(0.3632) // float32 rounding: stay a hair above
	if _, sim, ok := Best(q, []Enrolled{{PersonID: "p", Vec: at}}); !ok || math.Abs(sim-0.3632) > 1e-6 {
		t.Fatalf("at threshold: ok=%v sim=%v", ok, sim)
	}
	_, below := unitPair(0.3625)
	if _, _, ok := Best(q, []Enrolled{{PersonID: "p", Vec: below}}); ok {
		t.Fatal("below threshold matched")
	}
}

func TestBestPicksTheClosestPerson(t *testing.T) {
	q := []float32{1, 0, 0}
	gallery := []Enrolled{
		{PersonID: "ann", Vec: []float32{0.6, 0.8, 0}},
		{PersonID: "bob", Vec: []float32{0.9, 0.435889, 0}},
		{PersonID: "ann", Vec: []float32{0.95, 0.31225, 0}}, // Ann's second face is the closest
		{PersonID: "odd", Vec: []float32{1, 0}},             // another model's length: skipped
	}
	id, sim, ok := Best(q, gallery)
	if !ok || id != "ann" || math.Abs(sim-0.95) > 1e-6 {
		t.Fatalf("Best = %s %v %v", id, sim, ok)
	}
	if _, _, ok := Best(q, nil); ok {
		t.Fatal("empty gallery matched")
	}
}

func TestFitSimilarityRecoversTransform(t *testing.T) {
	// dst = rotate 30°, scale 1.7, shift (12, -4) of src.
	a, b := 1.7*math.Cos(math.Pi/6), 1.7*math.Sin(math.Pi/6)
	var src, dst [5][2]float64
	for i, p := range [5][2]float64{{10, 20}, {50, 21}, {30, 40}, {14, 60}, {47, 61}} {
		src[i] = p
		dst[i] = [2]float64{a*p[0] - b*p[1] + 12, b*p[0] + a*p[1] - 4}
	}
	tr := fitSimilarity(src, dst)
	if math.Abs(tr.a-a) > 1e-9 || math.Abs(tr.b-b) > 1e-9 || math.Abs(tr.tx-12) > 1e-9 || math.Abs(tr.ty+4) > 1e-9 {
		t.Fatalf("fit = %+v", tr)
	}
	x, y := tr.inverse(dst[2][0], dst[2][1])
	if math.Abs(x-src[2][0]) > 1e-9 || math.Abs(y-src[2][1]) > 1e-9 {
		t.Fatalf("inverse = %v,%v", x, y)
	}
}

func TestAlignPutsLandmarksOnTheTemplate(t *testing.T) {
	// A 112×112 image whose landmarks already sit on the template aligns to
	// itself (identity transform).
	img := image.NewRGBA(image.Rect(0, 0, alignSize, alignSize))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 7)
	}
	m := toRGB(img)
	in := alignInput(m, alignRef)
	plane := alignSize * alignSize
	for _, p := range [][2]int{{0, 0}, {56, 71}, {111, 111}, {38, 51}} {
		for c := 0; c < 3; c++ {
			want := float32(m.px[3*(p[1]*alignSize+p[0])+c])
			if got := in[c*plane+p[1]*alignSize+p[0]]; math.Abs(float64(got-want)) > 0.01 {
				t.Fatalf("pixel %v channel %d = %v want %v", p, c, got, want)
			}
		}
	}
}

func TestDetInputLetterboxesBGR(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 200, 100, 50, 255
	}
	in, scale := detInput(toRGB(img), 32, 32)
	if scale != 0.5 {
		t.Fatalf("scale %v", scale)
	}
	plane := 32 * 32
	// Content (32×16 top-left) is BGR; the rest is zero padding.
	if in[0] != 50 || in[plane] != 100 || in[2*plane] != 200 {
		t.Fatalf("content pixel BGR = %v %v %v", in[0], in[plane], in[2*plane])
	}
	if in[20*32+5] != 0 || in[plane+31*32+31] != 0 {
		t.Fatal("padding not zero")
	}
}

// yunetTensors builds YuNet outputs for a 64×64 input with one strong face
// anchor at stride 8, cell (row 3, col 4), plus a weaker overlapping one at
// the neighbouring cell that NMS must drop, plus a low-score anchor.
func yunetTensors() [][]float32 {
	const in = 64
	var outs [][]float32
	for _, s := range yunetStrides {
		n := (in / s) * (in / s)
		cls, obj, bbox, kps := make([]float32, n), make([]float32, n), make([]float32, 4*n), make([]float32, 10*n)
		if s == 8 {
			set := func(i int, score float32, dx float32) {
				cls[i], obj[i] = score, score
				bbox[4*i], bbox[4*i+1] = 0.5+dx, 0.5
				bbox[4*i+2], bbox[4*i+3] = float32(math.Log(2)), float32(math.Log(3)) // 16×24 px
				for k := 0; k < 5; k++ {
					kps[10*i+2*k], kps[10*i+2*k+1] = 0.1*float32(k), 0.2*float32(k)
				}
			}
			cols := in / s
			set(3*cols+4, 0.98, 0)
			set(3*cols+5, 0.95, -0.9) // nearly the same box, lower score
			set(7*cols+0, 0.5, 0)     // below detScoreMin
		}
		outs = append(outs, cls, obj, bbox, kps)
	}
	return outs
}

func TestDecodeYuNet(t *testing.T) {
	// The image was 128×64, scaled by 0.5 into the 64×64 canvas.
	fs := decodeYuNet(yunetTensors(), 64, 64, 0.5, 128, 64)
	if len(fs) != 1 {
		t.Fatalf("faces = %d, want 1 after threshold and NMS: %+v", len(fs), fs)
	}
	f := fs[0]
	// Centre (4.5·8, 3.5·8) = (36, 28) input px; 16×24 → x 28..44, y 16..40;
	// the content is 64×32 input px.
	want := [4]float64{28.0 / 64, 16.0 / 32, 44.0 / 64, 40.0 / 32}
	want[3] = 1 // clamped
	for i := range want {
		if math.Abs(f.Box[i]-want[i]) > 1e-6 {
			t.Fatalf("box = %v want %v", f.Box, want)
		}
	}
	if math.Abs(f.Score-0.98) > 1e-6 {
		t.Fatalf("score = %v (sqrt(cls·obj))", f.Score)
	}
	// Landmark 2: ((0.2+4)·8, (0.4+3)·8) = (33.6, 27.2) input px.
	if math.Abs(f.Landmarks[2][0]-33.6/64) > 1e-6 || math.Abs(f.Landmarks[2][1]-27.2/32) > 1e-6 {
		t.Fatalf("landmark = %v", f.Landmarks[2])
	}
	// 128×64 image: the box is 32×(clamped) px.
	if got := f.MinSidePx(128, 64); math.Abs(got-32) > 1e-6 {
		t.Fatalf("MinSidePx = %v", got)
	}
}

// fakeDet returns fixed tensors; fakeRec returns a fixed vector.
type fakeDet struct {
	outs [][]float32
	err  error
}

func (f *fakeDet) Size() (int, int) { return 64, 64 }
func (f *fakeDet) Run(in []float32) ([][]float32, error) {
	if len(in) != 3*64*64 {
		return nil, errFake
	}
	return f.outs, f.err
}

type fakeRec struct {
	out []float32
	err error
}

func (f *fakeRec) Run(in []float32) ([]float32, error) {
	if len(in) != 3*alignSize*alignSize {
		return nil, errFake
	}
	return f.out, f.err
}

func TestVerifyWarmsUpAndRejectsBrokenModels(t *testing.T) {
	vec := make([]float32, Dim)
	for i := range vec {
		vec[i] = float32(i%7) - 3
	}
	closed := 0
	closer := func() { closed++ }
	e, reason := Verify(&fakeDet{outs: yunetTensors()}, &fakeRec{out: vec}, "sha256:d", "sha256:r", closer)
	if e == nil || reason != "" || closed != 0 {
		t.Fatalf("Verify: %v %q closed=%d", e, reason, closed)
	}
	if e.DetectModel() != "sha256:d" || e.EmbedModel() != "sha256:r" {
		t.Fatal("model ids")
	}
	img, face := SyntheticFace(120)
	v, err := e.Embed(img, face)
	if err != nil || len(v) != Dim {
		t.Fatalf("Embed: %v %v", len(v), err)
	}
	if n := reid.Cosine(v, v); math.Abs(n-1) > 1e-5 {
		t.Fatalf("not unit length: %v", n)
	}
	fs, err := e.Find(img)
	if err != nil || len(fs) != 1 {
		t.Fatalf("Find: %v %v", fs, err)
	}

	cases := []struct {
		name string
		det  DetNet
		rec  RecNet
		want string
	}{
		{"detector fails", &fakeDet{err: errFake}, &fakeRec{out: vec}, "face detection model failed a test run"},
		{"detector outputs", &fakeDet{outs: yunetTensors()[:4]}, &fakeRec{out: vec}, "face detection model failed a test run"},
		{"recognizer fails", &fakeDet{outs: yunetTensors()}, &fakeRec{err: errFake}, "face recognition model failed a test run"},
		{"zero vector", &fakeDet{outs: yunetTensors()}, &fakeRec{out: make([]float32, Dim)}, "face recognition model failed a test run"},
		{"wrong length", &fakeDet{outs: yunetTensors()}, &fakeRec{out: vec[:64]}, "returned 64 values"},
	}
	for _, c := range cases {
		closed = 0
		e, reason := Verify(c.det, c.rec, "sha256:d", "sha256:r", closer)
		if e != nil || !strings.Contains(reason, c.want) || closed != 1 {
			t.Errorf("%s: %v %q closed=%d", c.name, e, reason, closed)
		}
	}
}

func TestOpenSaysWhichModelIsMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DetectModelEnv, "")
	t.Setenv(EmbedModelEnv, "")
	if e, reason := Open(dir, "/nope/libonnxruntime.so"); e != nil || !strings.Contains(reason, "face_det.onnx") || !strings.Contains(reason, "face_rec.onnx") {
		t.Fatalf("both missing: %q", reason)
	}
	det := dir + "/det.onnx"
	if err := writeFile(det, "x"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DetectModelEnv, det)
	if _, reason := Open(dir, "/nope/libonnxruntime.so"); !strings.HasPrefix(reason, "face recognition model not installed") || !strings.Contains(reason, "face_rec.onnx") {
		t.Fatalf("rec missing: %q", reason)
	}
	t.Setenv(EmbedModelEnv, det)
	if _, reason := Open(dir, ""); reason != "ONNX Runtime library not found" {
		t.Fatalf("no runtime: %q", reason)
	}
	if _, reason := Open(dir, "/nope/libonnxruntime.so"); !strings.Contains(reason, "ONNX Runtime unavailable") {
		t.Fatalf("bad runtime: %q", reason)
	}
}

func TestCropJPEGIsSmall(t *testing.T) {
	img, face := SyntheticFace(400)
	b, err := CropJPEG(img, face)
	if err != nil {
		t.Fatal(err)
	}
	c, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if c.Bounds().Dx() != CropMax || c.Bounds().Dy() != CropMax {
		t.Fatalf("crop %v, want %d px square", c.Bounds(), CropMax)
	}
	small, sf := SyntheticFace(60)
	b, _ = CropJPEG(small, sf)
	c, _ = jpeg.Decode(bytes.NewReader(b))
	if want := int(math.Ceil(1.3 * 0.84 * 60)); c.Bounds().Dx() != want {
		t.Fatalf("small crop %v, want %d px: never upscaled", c.Bounds(), want)
	}
}

// errFake is returned by test nets.
var errFake = errors.New("fake net failure")

func writeFile(p, s string) error { return os.WriteFile(p, []byte(s), 0o644) }

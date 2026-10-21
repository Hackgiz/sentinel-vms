package semantic

import (
	"errors"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeImageNet struct {
	n    int
	err  error
	last []float32
}

func (f *fakeImageNet) Run(px []float32) ([]float32, error) {
	f.last = px
	if f.err != nil {
		return nil, f.err
	}
	out := make([]float32, f.n)
	for i := range out {
		out[i] = 1
	}
	return out, nil
}

type fakeTextNet struct {
	out  []float32
	last []int64
}

func (f *fakeTextNet) Run(ids []int64) ([]float32, error) {
	f.last = ids
	return f.out, nil
}

func miniTokenizer(t *testing.T) *Tokenizer {
	t.Helper()
	tok, err := LoadTokenizer("testdata/clip_mini_vocab.json", "testdata/clip_mini_merges.txt")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestVerifyAndEmbed(t *testing.T) {
	tok := miniTokenizer(t)
	img := &fakeImageNet{n: Dim}
	txtOut := make([]float32, Dim)
	txtOut[3] = 2
	txt := &fakeTextNet{out: txtOut}
	e, reason := Verify(img, txt, tok, "sha256:abc", nil)
	if e == nil {
		t.Fatal(reason)
	}
	if e.Model() != "sha256:abc" {
		t.Fatal(e.Model())
	}
	if len(img.last) != 3*ImageSize*ImageSize || len(txt.last) != ContextLen {
		t.Fatalf("warm-up shapes %d %d", len(img.last), len(txt.last))
	}
	v, err := e.EmbedText("a photo of a cat")
	if err != nil || len(v) != Dim || v[3] != 1 {
		t.Fatalf("text %v %v", v[:4], err)
	}
	if txt.last[0] != 49406 || txt.last[5] != 2368 {
		t.Fatalf("ids %v", txt.last[:8])
	}
	iv, err := e.EmbedImage(image.NewRGBA(image.Rect(0, 0, 50, 30)))
	if err != nil || math.Abs(float64(iv[0])-1/math.Sqrt(Dim)) > 1e-6 {
		t.Fatalf("image %v %v", iv[:2], err)
	}
	if _, err := e.EmbedImage(image.NewRGBA(image.Rect(0, 0, 0, 0))); !errors.Is(err, ErrEmptyImage) {
		t.Fatalf("empty image: %v", err)
	}
}

func TestVerifyFailsAndCloses(t *testing.T) {
	tok := miniTokenizer(t)
	closed := 0
	closer := func() { closed++ }
	good := make([]float32, Dim)
	good[0] = 1
	cases := []struct {
		img  ImageNet
		txt  TextNet
		want string
	}{
		{&fakeImageNet{n: 768}, &fakeTextNet{out: good}, "image model failed a test run"},
		{&fakeImageNet{n: Dim, err: errors.New("boom")}, &fakeTextNet{out: good}, "boom"},
		{&fakeImageNet{n: Dim}, &fakeTextNet{out: make([]float32, Dim)}, "text model failed a test run"},
		{&fakeImageNet{n: Dim}, &fakeTextNet{out: []float32{float32(math.NaN())}}, "text model failed"},
	}
	for i, c := range cases {
		if e, reason := Verify(c.img, c.txt, tok, "m", closer); e != nil || !strings.Contains(reason, c.want) {
			t.Errorf("case %d: %v %q", i, e, reason)
		}
	}
	if closed != len(cases) {
		t.Fatalf("closer ran %d times", closed)
	}
}

func TestOpenSaysWhatIsMissing(t *testing.T) {
	for _, env := range []string{ImageModelEnv, TextModelEnv, VocabEnv, MergesEnv} {
		t.Setenv(env, "")
	}
	dir := t.TempDir()
	if e, reason := Open(dir, "/nonexistent/libonnxruntime"); e != nil || !strings.Contains(reason, "image model") || !strings.Contains(reason, filepath.Join(dir, "models", ImageModelFile)) {
		t.Fatalf("no files: %v %q", e, reason)
	}
	_ = os.MkdirAll(filepath.Join(dir, "models"), 0o755)
	for _, f := range []string{ImageModelFile, TextModelFile, VocabFile} {
		_ = os.WriteFile(filepath.Join(dir, "models", f), []byte("x"), 0o644)
	}
	if _, reason := Open(dir, "/x"); !strings.Contains(reason, "tokenizer merges") {
		t.Fatalf("merges missing: %q", reason)
	}
	_ = os.WriteFile(filepath.Join(dir, "models", MergesFile), []byte("x"), 0o644)
	if _, reason := Open(dir, ""); reason != "ONNX Runtime library not found" {
		t.Fatalf("no runtime: %q", reason)
	}
	if _, reason := Open(dir, "/x"); !strings.Contains(reason, "tokenizer files could not be read") {
		t.Fatalf("bad tokenizer: %q", reason)
	}
	// The overrides win over <data>/models.
	t.Setenv(ImageModelEnv, filepath.Join(dir, "elsewhere.onnx"))
	if _, reason := Open(dir, "/x"); !strings.Contains(reason, "elsewhere.onnx") {
		t.Fatalf("override: %q", reason)
	}
}

func TestPreprocessCropsTheCentre(t *testing.T) {
	// 448×224: left half red, right half blue, a green centre band 112 px
	// wide. Shorter side is already 224, so the crop is columns 112..336:
	// the green band fills the middle half of the input.
	img := image.NewRGBA(image.Rect(0, 0, 448, 224))
	for y := 0; y < 224; y++ {
		for x := 0; x < 448; x++ {
			c := color.RGBA{255, 0, 0, 255}
			if x >= 224 {
				c = color.RGBA{0, 0, 255, 255}
			}
			if x >= 168 && x < 280 {
				c = color.RGBA{0, 255, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	px := Preprocess(img)
	plane := ImageSize * ImageSize
	at := func(c, x, y int) float32 { return px[c*plane+y*ImageSize+x]*clipStd[c] + clipMean[c] }
	if g := at(1, 112, 100); math.Abs(float64(g-1)) > 1e-3 {
		t.Fatalf("centre green = %v", g)
	}
	if r := at(0, 10, 100); math.Abs(float64(r-1)) > 1e-3 {
		t.Fatalf("left red = %v", r)
	}
	if b := at(2, 220, 100); math.Abs(float64(b-1)) > 1e-3 {
		t.Fatalf("right blue = %v", b)
	}
	// Portrait input: shorter side scaled up to 224, output always 224².
	if n := len(Preprocess(image.NewGray(image.Rect(0, 0, 30, 90)))); n != 3*plane {
		t.Fatal(n)
	}
}

// The real CLIP models through the bundled ONNX Runtime: "a photo of a
// cat" must rank a cat snapshot above a car one. Needs SENTINEL_ORT_LIB
// and the model files (see realFiles), plus test_cat.jpg and test_car.jpg
// in linux/.models-cache (COCO val2017 000000039769 and Wikimedia Commons
// 2019_Toyota_Corolla_Icon_Tech_VVT-i_Hybrid_1.8.jpg at 640 px wide).
func TestRealModelCatAboveCar(t *testing.T) {
	f := realFiles(t)
	lib := os.Getenv("SENTINEL_ORT_LIB")
	if lib == "" {
		t.Skip("SENTINEL_ORT_LIB not set: point it at libonnxruntime (e.g. dist/sentinel-*/lib/libonnxruntime.dylib) to run the real CLIP test")
	}
	dir := filepath.Dir(f.Image)
	catJPG, err1 := os.ReadFile(filepath.Join(dir, "test_cat.jpg"))
	carJPG, err2 := os.ReadFile(filepath.Join(dir, "test_car.jpg"))
	if err1 != nil || err2 != nil {
		t.Skipf("test_cat.jpg / test_car.jpg not in %s", dir)
	}
	for k, v := range map[string]string{ImageModelEnv: f.Image, TextModelEnv: f.Text, VocabEnv: f.Vocab, MergesEnv: f.Merges} {
		t.Setenv(k, v)
	}
	e, reason := Open(t.TempDir(), lib)
	if e == nil {
		t.Fatal(reason)
	}
	if !strings.HasPrefix(e.Model(), "sha256:") || len(e.Model()) != 23 {
		t.Fatalf("model id %q", e.Model())
	}
	embed := func(b []byte) []float32 {
		img, err := DecodeJPEG(b)
		if err != nil {
			t.Fatal(err)
		}
		v, err := e.EmbedImage(img)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cat, car := embed(catJPG), embed(carJPG)
	for _, q := range []struct{ text, want string }{{"a photo of a cat", "cat"}, {"a red car parked on the street", "car"}} {
		tv, err := e.EmbedText(q.text)
		if err != nil {
			t.Fatal(err)
		}
		sCat, sCar := Cosine(tv, cat), Cosine(tv, car)
		t.Logf("%q: cat %.4f, car %.4f", q.text, sCat, sCar)
		if (q.want == "cat") != (sCat > sCar) {
			t.Errorf("%q ranked the wrong snapshot first (cat %.4f, car %.4f)", q.text, sCat, sCar)
		}
		if q.want == "cat" && sCat < 0.2 {
			t.Errorf("cat score %.4f is under the default minScore 0.2", sCat)
		}
	}
}

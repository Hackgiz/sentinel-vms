package lpr

import (
	"image"
	_ "image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealPlates runs the real PaddleOCR models over the photos in testdata
// (vehicle crops from public-domain and CC0 photos; the expected plate is
// the file name, see testdata/SOURCES.txt). It needs the ONNX Runtime
// library (SENTINEL_ORT_LIB) and the three model files, from the
// SENTINEL_PLATE_* overrides or linux/.models-cache; it skips otherwise.
func TestRealPlates(t *testing.T) {
	lib := os.Getenv("SENTINEL_ORT_LIB")
	if lib == "" {
		t.Skip("real plate models: set SENTINEL_ORT_LIB to the ONNX Runtime library")
	}
	cache, _ := filepath.Abs(filepath.Join("..", "..", ".models-cache"))
	for env, name := range map[string]string{DetModelEnv: DetModelFile, RecModelEnv: RecModelFile, DictEnv: DictFile} {
		if os.Getenv(env) == "" {
			t.Setenv(env, filepath.Join(cache, name))
		}
	}
	if m := Missing(t.TempDir()); m != "" {
		t.Skipf("real plate models: %s is missing (download plate_det.onnx, plate_rec.onnx, plate_rec.txt into linux/.models-cache)", m)
	}
	r, reason := Open(t.TempDir(), lib)
	if r == nil {
		t.Fatalf("models did not open: %s", reason)
	}
	// Known misses: TK71691 is a Polish plate whose blue EU band reads as
	// a leading "E".
	known := map[string]bool{"TK71691": true}
	files, _ := filepath.Glob(filepath.Join("testdata", "*.jpg"))
	if len(files) < 3 {
		t.Fatalf("want at least three plate photos, have %d", len(files))
	}
	pass := 0
	for _, f := range files {
		want := strings.TrimSuffix(filepath.Base(f), ".jpg")
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(fh)
		fh.Close()
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.Read(img)
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		got := ""
		if res != nil {
			got = res.Text
			t.Logf("%s: read %q (raw %q, confidence %.2f, box %v)", want, res.Text, res.Raw, res.Confidence, res.Box)
		} else {
			t.Logf("%s: no plate found", want)
		}
		switch {
		case got == want:
			pass++
		case known[want]:
			t.Logf("%s: known miss (read %q)", want, got)
		default:
			t.Errorf("%s: read %q", want, got)
		}
	}
	t.Logf("%d of %d plates read exactly", pass, len(files))
}

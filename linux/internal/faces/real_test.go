package faces

import (
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sentinel-linux/internal/reid"
)

// realModels points the loader at the downloaded models: the environment
// overrides, else linux/.models-cache (git-ignored). It skips the test when
// the ONNX Runtime library (SENTINEL_ORT_LIB) or a model is missing.
func realModels(t *testing.T) (dataDir, lib string) {
	t.Helper()
	lib = os.Getenv("SENTINEL_ORT_LIB")
	if lib == "" {
		t.Skip("set SENTINEL_ORT_LIB to the ONNX Runtime library to run the real face models")
	}
	cache, _ := filepath.Abs(filepath.Join("..", "..", ".models-cache"))
	if os.Getenv(DetectModelEnv) == "" {
		t.Setenv(DetectModelEnv, filepath.Join(cache, "face_detection_yunet_2023mar.onnx"))
	}
	if os.Getenv(EmbedModelEnv) == "" {
		t.Setenv(EmbedModelEnv, filepath.Join(cache, "face_recognition_sface_2021dec.onnx"))
	}
	dataDir = t.TempDir()
	if m := Missing(dataDir); m != "" {
		t.Skip(m + "; download the OpenCV Zoo YuNet and SFace models into linux/.models-cache")
	}
	return dataDir, lib
}

func TestRealModelsLoadAndWarmUp(t *testing.T) {
	dataDir, lib := realModels(t)
	e, reason := Open(dataDir, lib)
	if e == nil {
		t.Fatalf("Open: %s", reason)
	}
	if !strings.HasPrefix(e.DetectModel(), "sha256:") || !strings.HasPrefix(e.EmbedModel(), "sha256:") {
		t.Fatalf("model ids %q %q", e.DetectModel(), e.EmbedModel())
	}
	blank := image.NewGray(image.Rect(0, 0, 400, 300))
	if fs, err := e.Find(blank); err != nil || len(fs) != 0 {
		t.Fatalf("blank frame: %v %v", fs, err)
	}
}

// TestRealRecognition runs both models on real photos: every *.jpg in
// SENTINEL_FACE_SAMPLES named <person>_<anything>.jpg (one face each). Pairs
// of the same person must match, pairs of different people must not. The
// photos are not checked in; the report lists the public-domain ones used.
func TestRealRecognition(t *testing.T) {
	dataDir, lib := realModels(t)
	dir := os.Getenv("SENTINEL_FACE_SAMPLES")
	if dir == "" {
		t.Skip("set SENTINEL_FACE_SAMPLES to a folder of <person>_<n>.jpg photos")
	}
	e, reason := Open(dataDir, lib)
	if e == nil {
		t.Fatalf("Open: %s", reason)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jpg"))
	sort.Strings(files)
	type sample struct {
		person, name string
		vec          []float32
	}
	var samples []sample
	for _, f := range files {
		r, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(r)
		r.Close()
		if err != nil {
			t.Fatal(f, err)
		}
		fs, err := e.Find(img)
		if err != nil || len(fs) == 0 {
			t.Fatalf("%s: no face found (%v)", filepath.Base(f), err)
		}
		b := img.Bounds()
		if fs[0].MinSidePx(b.Dx(), b.Dy()) < MinFacePx {
			t.Fatalf("%s: face too small", filepath.Base(f))
		}
		v, err := e.Embed(img, fs[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CropJPEG(img, fs[0]); err != nil {
			t.Fatal(err)
		}
		base := filepath.Base(f)
		t.Logf("%s: %d face(s), best score %.3f box %.3f", base, len(fs), fs[0].Score, fs[0].Box)
		samples = append(samples, sample{person: strings.SplitN(base, "_", 2)[0], name: base, vec: v})
	}
	if len(samples) < 2 {
		t.Skip("need at least two photos")
	}
	for i := range samples {
		for j := i + 1; j < len(samples); j++ {
			a, b := samples[i], samples[j]
			sim := reid.Cosine(a.vec, b.vec)
			same := a.person == b.person
			t.Logf("%-50s %-50s same=%v cosine=%.3f match=%v", a.name, b.name, same, sim, Matches(sim))
			if same != Matches(sim) {
				t.Errorf("%s vs %s: same person %v but cosine %.3f (threshold %.3f)", a.name, b.name, same, sim, MatchThreshold)
			}
		}
	}
}

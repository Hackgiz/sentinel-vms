package audiodetect

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRealYAMNet runs the classifier on the real YAMNet ONNX export (wave
// 16) over labelled clips. Needs SENTINEL_ORT_LIB, SENTINEL_AUDIO_MODEL and
// SENTINEL_AUDIO_SAMPLES: a directory of raw 16 kHz mono float32 clips named
// `<class>__<anything>.f32`, where <class> is one of the esc50 names below
// (ffmpeg -i in.wav -ac 1 -ar 16000 -f f32le out.f32). Skipped otherwise.
func TestRealYAMNet(t *testing.T) {
	lib, model, dir := os.Getenv("SENTINEL_ORT_LIB"), os.Getenv(ModelEnv), os.Getenv("SENTINEL_AUDIO_SAMPLES")
	if lib == "" || model == "" || dir == "" {
		t.Skip("set SENTINEL_ORT_LIB, SENTINEL_AUDIO_MODEL and SENTINEL_AUDIO_SAMPLES to run against the real model")
	}
	c, reason := OpenClassifier(t.TempDir(), lib)
	if c == nil {
		t.Fatal(reason)
	}
	if _, ok := c.(*ModelClassifier).Model.(WaveformModel); !ok && strings.Contains(strings.ToLower(model), "yamnet") {
		t.Fatal("the YAMNet export must open with the waveform layout")
	}
	want := map[string]string{
		"dog": LabelDogBark, "siren": LabelSiren, "car_horn": LabelCarHorn, "crying_baby": LabelBabyCry,
		"glass_breaking": LabelGlass,
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.f32"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no .f32 clips in " + dir)
	}
	right, total, falsePos, negatives := 0, 0, 0, 0
	for _, f := range files {
		class := strings.SplitN(filepath.Base(f), "__", 2)[0]
		samples := readF32(t, f)
		got := map[string]float64{}
		for off := 0; off+WindowSamples <= len(samples); off += WindowSamples / 2 {
			hits, err := c.Classify(samples[off : off+WindowSamples])
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range hits {
				got[h.Label] = math.Max(got[h.Label], h.Score)
			}
		}
		label, positive := want[class]
		switch {
		case positive:
			total++
			if got[label] > 0 {
				right++
			}
		default:
			negatives++
			if len(got) > 0 {
				falsePos++
			}
		}
		t.Logf("%-40s %v", filepath.Base(f), got)
	}
	t.Logf("recall %d/%d clips, false positives %d/%d negative clips", right, total, falsePos, negatives)
	if total > 0 && float64(right)/float64(total) < 0.6 {
		t.Errorf("recall %d/%d is below 60%%", right, total)
	}
}

func readF32(t *testing.T, path string) []float32 {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}

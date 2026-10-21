package audiodetect

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeModel struct {
	scores  []float32
	err     error
	gotLen  int
	gotCall int
}

func (f *fakeModel) Scores(patch []float32) ([]float32, error) {
	f.gotLen = len(patch)
	f.gotCall++
	return f.scores, f.err
}

func scoresWith(n int, kv map[int]float32) []float32 {
	s := make([]float32, n)
	for i, v := range kv {
		s[i] = v
	}
	return s
}

func TestMapScoresPerClassThresholds(t *testing.T) {
	rules := []ClassRule{
		{Label: LabelScream, Indices: []int{11}, Threshold: 0.5},
		{Label: LabelGunshot, Indices: []int{421, 422}, Threshold: 0.4},
		{Label: LabelGlass, Indices: []int{435}, Threshold: 0.4},
	}
	// Scream below its 0.5, gunshot via the second index at 0.45, glass out of range.
	hits := MapScores(scoresWith(430, map[int]float32{11: 0.49, 422: 0.45}), rules)
	if len(hits) != 1 || hits[0].Label != LabelGunshot || hits[0].Score != 0.45 {
		t.Fatalf("hits = %+v", hits)
	}
	hits = MapScores(scoresWith(521, map[int]float32{11: 0.5, 435: 0.95}), rules)
	if len(hits) != 2 {
		t.Fatalf("hits = %+v", hits)
	}
	if b := BestHit(hits); b.Label != LabelGlass {
		t.Fatalf("best = %+v", b)
	}
	if BestHit(nil) != nil {
		t.Fatal("best of nothing")
	}
	tie := []ClassHit{{LabelAlarm, 0.7}, {LabelGunshot, 0.7}}
	if BestHit(tie).Label != LabelGunshot {
		t.Fatal("tie must prefer gunshot")
	}
}

func TestModelClassifierWithFake(t *testing.T) {
	fm := &fakeModel{scores: scoresWith(521, map[int]float32{11: 0.8})}
	c, err := NewModelClassifier(fm, nil)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := c.Classify(make([]float32, WindowSamples))
	if err != nil || len(hits) != 1 || hits[0].Label != LabelScream {
		t.Fatalf("hits %+v err %v", hits, err)
	}
	if fm.gotLen != MelFrames*MelBands {
		t.Fatalf("model got %d values, want %d", fm.gotLen, MelFrames*MelBands)
	}
	if _, err := c.Classify(make([]float32, 100)); err == nil {
		t.Fatal("short window accepted")
	}
	fm.err = errors.New("bad")
	if _, err := c.Classify(make([]float32, WindowSamples)); err == nil {
		t.Fatal("model error swallowed")
	}
	if _, err := NewModelClassifier(nil, nil); err == nil {
		t.Fatal("nil model accepted")
	}
}

func TestLoadClassRules(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "a.json")
	os.WriteFile(good, []byte(`[{"label":"Gunshot","indices":[3],"threshold":0.3}]`), 0o600)
	rules, err := LoadClassRules(good)
	if err != nil || len(rules) != 1 || rules[0].Indices[0] != 3 {
		t.Fatalf("%+v %v", rules, err)
	}
	for name, body := range map[string]string{
		"nothreshold": `[{"label":"X","indices":[1]}]`,
		"noindices":   `[{"label":"X","threshold":0.5}]`,
		"junk":        `nope`,
	} {
		p := filepath.Join(dir, name+".json")
		os.WriteFile(p, []byte(body), 0o600)
		if _, err := LoadClassRules(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := LoadClassRules(filepath.Join(dir, "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file err = %v", err)
	}
}

func TestOpenClassifierDegradesWithReason(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ModelEnv, "")
	c, reason := OpenClassifier(dir, "")
	if c != nil || reason == "" {
		t.Fatalf("missing model: %v %q", c, reason)
	}
	// A model file that is not a model, with no runtime: still a reason, no panic.
	os.MkdirAll(filepath.Join(dir, "models"), 0o755)
	os.WriteFile(filepath.Join(dir, "models", "audio.onnx"), []byte("not onnx"), 0o600)
	c, reason = OpenClassifier(dir, "")
	if c != nil || reason == "" {
		t.Fatalf("no runtime: %v %q", c, reason)
	}
	c, reason = OpenClassifier(dir, filepath.Join(dir, "libonnxruntime-missing.so"))
	if c != nil || reason == "" {
		t.Fatalf("bad runtime: %v %q", c, reason)
	}
	// SENTINEL_AUDIO_MODEL wins over the data dir.
	t.Setenv(ModelEnv, filepath.Join(dir, "elsewhere.onnx"))
	if p, ok := ModelPath(dir); ok || filepath.Base(p) != "elsewhere.onnx" {
		t.Fatalf("env path = %q %v", p, ok)
	}
}

type fakeWaveModel struct {
	fakeModel
	gotSamples int
}

func (f *fakeWaveModel) ScoresWaveform(samples []float32) ([]float32, error) {
	f.gotSamples = len(samples)
	return f.scores, f.err
}

// A model with the front end built in (the YAMNet export) gets the raw
// window, never a mel patch; the new wave 16 labels map from it.
func TestModelClassifierWaveformAndNewLabels(t *testing.T) {
	fm := &fakeWaveModel{fakeModel: fakeModel{scores: scoresWith(521, map[int]float32{70: 0.8, 390: 0.75, 302: 0.5, 20: 0.45})}}
	c, err := NewModelClassifier(fm, nil)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := c.Classify(make([]float32, WindowSamples))
	if err != nil || fm.gotSamples != WindowSamples || fm.gotCall != 0 {
		t.Fatalf("waveform path: %d samples, %d patch calls, %v", fm.gotSamples, fm.gotCall, err)
	}
	got := map[string]float64{}
	for _, h := range hits {
		got[h.Label] = h.Score
	}
	if got[LabelDogBark] != 0.8 || got[LabelSiren] != 0.75 || got[LabelCarHorn] != 0.5 || got[LabelBabyCry] != 0.45 || len(got) != 4 {
		t.Fatalf("hits %v", got)
	}
	if b := BestHit([]ClassHit{{LabelAlarm, 0.9}, {LabelSiren, 0.9}}); b.Label != LabelSiren {
		t.Fatal("a siren tie must name the siren")
	}
}

func TestMaxOverFrames(t *testing.T) {
	got, err := maxOverFrames([]float32{0.1, 0.9, 0.5, 0.2, 0.3, 0.7}, []int64{3, 2})
	if err != nil || len(got) != 2 || got[0] != 0.5 || got[1] != 0.9 {
		t.Fatalf("%v %v", got, err)
	}
	if got, _ := maxOverFrames([]float32{0.4, 0.6}, []int64{1, 2}); got[1] != 0.6 {
		t.Fatal("single frame")
	}
	for _, dims := range [][]int64{nil, {0}, {4}} {
		if _, err := maxOverFrames([]float32{1, 2, 3}, dims); err == nil {
			t.Errorf("dims %v accepted", dims)
		}
	}
}

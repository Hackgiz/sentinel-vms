package audiodetect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sentinel-linux/internal/ort"
)

// ModelEnv names the model file; without it <data>/models/audio.onnx is used.
const ModelEnv = "SENTINEL_AUDIO_MODEL"

// ModelPath returns where the optional classifier model is expected and
// whether the file exists.
func ModelPath(dataDir string) (path string, exists bool) {
	path = os.Getenv(ModelEnv)
	if path == "" {
		path = filepath.Join(dataDir, "models", "audio.onnx")
	}
	fi, err := os.Stat(path)
	return path, err == nil && fi.Mode().IsRegular()
}

// onnxScores runs a YAMNet-class ONNX network through ONNX Runtime (the
// same purego binding object detection uses). WAVE8(B): TODO confirm the
// input layout against a real model: this feeds one log-mel patch of shape
// [1, 96, 64] and reads the first output as per-class scores.
type onnxScores struct{ sess *ort.Session }

func (o *onnxScores) Scores(patch []float32) ([]float32, error) {
	out, _, err := o.sess.Run(patch, []int64{1, MelFrames, MelBands})
	return out, err
}

// OpenClassifier loads the optional model. It returns (nil, reason) when
// the classifier is unavailable (no ONNX Runtime, no model file, a model
// that fails to load or run); the caller keeps the baseline detector and
// surfaces the reason. It never panics on a bad model.
func OpenClassifier(dataDir, ortLib string) (c Classifier, reason string) {
	defer func() {
		if r := recover(); r != nil {
			c, reason = nil, fmt.Sprintf("classifier failed to start: %v", r)
		}
	}()
	model, ok := ModelPath(dataDir)
	if !ok {
		return nil, "no audio model (" + model + "): loudness detection only"
	}
	if ortLib == "" {
		return nil, "ONNX Runtime library not found: loudness detection only"
	}
	rt, err := ort.Load(ortLib)
	if err != nil {
		return nil, "ONNX Runtime unavailable: " + err.Error()
	}
	sess, err := rt.NewSession(model, 1)
	if err != nil {
		return nil, "audio model could not be loaded: " + err.Error()
	}
	rules := DefaultClassRules
	if side, err := LoadClassRules(strings.TrimSuffix(model, filepath.Ext(model)) + ".classes.json"); err == nil {
		rules = side
	} else if !errors.Is(err, os.ErrNotExist) {
		sess.Close()
		return nil, "audio class map rejected: " + err.Error()
	}
	mc, err := NewModelClassifier(&onnxScores{sess: sess}, rules)
	if err != nil {
		sess.Close()
		return nil, err.Error()
	}
	// Warm-up on silence proves the input/output shapes before cameras use it.
	if _, err := mc.Classify(make([]float32, WindowSamples)); err != nil {
		sess.Close()
		return nil, "audio model failed a test run: " + err.Error()
	}
	return mc, ""
}

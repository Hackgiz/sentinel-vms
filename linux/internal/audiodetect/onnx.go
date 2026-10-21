package audiodetect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sentinel-linux/internal/ort"

	"sentinel-linux/internal/modelpath"
)

// ModelEnv names the model file; without it <data>/models/audio.onnx is used.
const ModelEnv = "SENTINEL_AUDIO_MODEL"

// ModelPath returns where the optional classifier model is expected and
// whether the file exists.
func ModelPath(dataDir string) (path string, exists bool) {
	path = os.Getenv(ModelEnv)
	if path == "" {
		path = modelpath.Find(dataDir, "audio.onnx")
	}
	fi, err := os.Stat(path)
	return path, err == nil && fi.Mode().IsRegular()
}

// ONNX input layouts, probed in order when the model opens (wave 16 checked
// them against the tf2onnx export of Google's YAMNet, which takes a raw
// waveform `waveform: float32[N]` and returns `output_0: float32[frames,
// 521]`, one frame per 0.96 s patch with a 0.48 s hop, the log-mel front end
// built into the graph):
//
//   - waveform [N]        (the YAMNet export)
//   - waveform [1, N]     (exports with a batch axis)
//   - log-mel [1, 96, 64] (patch models without a front end; ours feeds them)
//
// The first output is read as class scores; any leading axes (batch, frames)
// are reduced by taking each class's maximum.
type onnxWaveform struct {
	sess  *ort.Session
	batch bool // [1, N] instead of [N]
}

func (o *onnxWaveform) ScoresWaveform(samples []float32) ([]float32, error) {
	shape := []int64{int64(len(samples))}
	if o.batch {
		shape = []int64{1, int64(len(samples))}
	}
	out, dims, err := o.sess.Run(samples, shape)
	if err != nil {
		return nil, err
	}
	return maxOverFrames(out, dims)
}

// Scores is never called for a waveform model (ModelClassifier prefers
// ScoresWaveform); it exists so the type is a ScoreModel.
func (o *onnxWaveform) Scores([]float32) ([]float32, error) {
	return nil, errors.New("waveform model fed a mel patch")
}

type onnxMel struct{ sess *ort.Session }

func (o *onnxMel) Scores(patch []float32) ([]float32, error) {
	out, dims, err := o.sess.Run(patch, []int64{1, MelFrames, MelBands})
	if err != nil {
		return nil, err
	}
	return maxOverFrames(out, dims)
}

// maxOverFrames reduces a [..., classes] output to one score per class.
func maxOverFrames(out []float32, dims []int64) ([]float32, error) {
	if len(dims) == 0 || dims[len(dims)-1] <= 0 || len(out) == 0 || len(out)%int(dims[len(dims)-1]) != 0 {
		return nil, fmt.Errorf("unexpected audio model output shape %v", dims)
	}
	n := int(dims[len(dims)-1])
	if len(out) == n {
		return out, nil
	}
	best := append([]float32(nil), out[:n]...)
	for off := n; off < len(out); off += n {
		for i, v := range out[off : off+n] {
			if v > best[i] {
				best[i] = v
			}
		}
	}
	return best, nil
}

// probeLayout picks the first input layout the session accepts.
func probeLayout(sess *ort.Session) (ScoreModel, error) {
	silence := make([]float32, WindowSamples)
	var errs []string
	for _, m := range []ScoreModel{&onnxWaveform{sess: sess}, &onnxWaveform{sess: sess, batch: true}, &onnxMel{sess: sess}} {
		var err error
		if wm, ok := m.(WaveformModel); ok {
			_, err = wm.ScoresWaveform(silence)
		} else {
			_, err = m.Scores(make([]float32, MelFrames*MelBands))
		}
		if err == nil {
			return m, nil
		}
		errs = append(errs, err.Error())
	}
	return nil, errors.New(strings.Join(errs, "; "))
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
	sm, err := probeLayout(sess)
	if err != nil {
		sess.Close()
		return nil, "audio model failed a test run: " + err.Error()
	}
	mc, err := NewModelClassifier(sm, rules)
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

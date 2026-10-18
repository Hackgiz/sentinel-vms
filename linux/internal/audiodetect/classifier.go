package audiodetect

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
)

// Labels the classifier can assign (replacing "Loud noise").
const (
	LabelGlass   = "Glass break"
	LabelScream  = "Scream"
	LabelGunshot = "Gunshot"
	LabelAlarm   = "Alarm"
)

// ClassHit is one recognized sound in a window.
type ClassHit struct {
	Label string
	Score float64
}

// Classifier recognizes sounds in one 1 s, 16 kHz mono window. It is the
// seam the ONNX model sits behind; tests use a fake. Implementations must be
// safe for concurrent use (one is shared by every camera).
type Classifier interface {
	Classify(window []float32) ([]ClassHit, error)
}

// BestHit returns the highest-scoring hit (ties: gunshot, glass, scream,
// alarm), or nil.
func BestHit(hits []ClassHit) *ClassHit {
	var best *ClassHit
	for i := range hits {
		h := &hits[i]
		if best == nil || h.Score > best.Score || (h.Score == best.Score && labelRank(h.Label) < labelRank(best.Label)) {
			best = h
		}
	}
	return best
}

func labelRank(l string) int {
	switch l {
	case LabelGunshot:
		return 0
	case LabelGlass:
		return 1
	case LabelScream:
		return 2
	}
	return 3
}

// ScoreModel runs the network: a log-mel patch (MelFrames x MelBands,
// row-major) in, one score per model class out.
type ScoreModel interface {
	Scores(patch []float32) ([]float32, error)
}

// ClassRule maps model class indices to one label with its own threshold.
// The score of a rule is the maximum over its indices.
type ClassRule struct {
	Label     string  `json:"label"`
	Indices   []int   `json:"indices"`
	Threshold float64 `json:"threshold"`
}

// DefaultClassRules maps YAMNet's 521 AudioSet classes (indices of
// yamnet_class_map.csv). WAVE8(B): TODO verify these indices against a real
// model; they come from memory of that file. A model whose classes differ
// ships a `<model>.classes.json` sidecar with the same shape as this list.
var DefaultClassRules = []ClassRule{
	{Label: LabelScream, Indices: []int{11}, Threshold: 0.5},             // Screaming
	{Label: LabelGunshot, Indices: []int{421, 422, 423}, Threshold: 0.4}, // Gunshot/gunfire, machine gun, fusillade
	{Label: LabelGlass, Indices: []int{435, 437}, Threshold: 0.4},        // Glass, Shatter
	{Label: LabelAlarm, Indices: []int{382, 393, 394}, Threshold: 0.5},   // Alarm, smoke detector, fire alarm
}

// LoadClassRules reads a sidecar holding a JSON array of ClassRule.
func LoadClassRules(path string) ([]ClassRule, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules []ClassRule
	if err := json.Unmarshal(b, &rules); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, r := range rules {
		if r.Label == "" || len(r.Indices) == 0 || r.Threshold <= 0 || r.Threshold > 1 {
			return nil, fmt.Errorf("%s: rule %q needs a label, indices and a threshold in (0,1]", path, r.Label)
		}
	}
	return rules, nil
}

// MapScores applies the rules to raw model scores.
func MapScores(scores []float32, rules []ClassRule) []ClassHit {
	var hits []ClassHit
	for _, r := range rules {
		var best float64
		for _, i := range r.Indices {
			if i >= 0 && i < len(scores) && float64(scores[i]) > best {
				best = float64(scores[i])
			}
		}
		if best >= r.Threshold {
			hits = append(hits, ClassHit{Label: r.Label, Score: math.Round(best*100) / 100})
		}
	}
	return hits
}

// ModelClassifier is the production Classifier: log-mel front end, a
// ScoreModel, then the class rules.
type ModelClassifier struct {
	Model ScoreModel
	Rules []ClassRule
	mel   *MelSpectrogram
	sem   sync.Mutex // one inference at a time: bounds CPU across cameras
}

// NewModelClassifier wires a model with rules (nil = DefaultClassRules).
func NewModelClassifier(m ScoreModel, rules []ClassRule) (*ModelClassifier, error) {
	if m == nil {
		return nil, errors.New("no model")
	}
	if rules == nil {
		rules = DefaultClassRules
	}
	return &ModelClassifier{Model: m, Rules: rules, mel: NewMelSpectrogram()}, nil
}

// Classify implements Classifier on the newest PatchSamples of the window.
func (c *ModelClassifier) Classify(window []float32) ([]ClassHit, error) {
	if len(window) < PatchSamples {
		return nil, fmt.Errorf("window of %d samples, need %d", len(window), PatchSamples)
	}
	patch := c.mel.Patch(window[len(window)-PatchSamples:])
	c.sem.Lock()
	scores, err := c.Model.Scores(patch)
	c.sem.Unlock()
	if err != nil {
		return nil, err
	}
	return MapScores(scores, c.Rules), nil
}

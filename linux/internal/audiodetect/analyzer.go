// Package audiodetect raises events for loud or distinctive sounds. Each
// enabled camera gets one ffmpeg that decodes the camera's audio to 16 kHz
// mono float32 PCM (tap.go). The Analyzer slides a 1 s window along it every
// 0.5 s and applies two detectors:
//
//   - the baseline, always available: RMS loudness in dBFS against a rolling
//     noise floor (needs no model file);
//   - an optional classifier (classifier.go, onnx.go) that recognizes glass
//     breaking, screams, gunshots and alarms and replaces the baseline label.
package audiodetect

import (
	"math"
	"sort"
	"time"
)

const (
	SampleRate    = 16000
	WindowSamples = SampleRate     // 1 s analysis window
	HopSamples    = SampleRate / 2 // 0.5 s hop

	// DefaultThresholdDB is how far above the noise floor a window must be.
	DefaultThresholdDB = 25
	MinThresholdDB     = 6
	MaxThresholdDB     = 60

	// ConsecutiveWindows over the threshold needed to fire the baseline.
	ConsecutiveWindows = 2
	// Debounce is the quiet time after an event, per camera.
	Debounce = 30 * time.Second

	// Noise floor: the FloorPercentile of the last floorWindows window levels
	// (60 s of hops), once at least floorWarmup (10 s) have been seen.
	floorWindows    = 120
	floorWarmup     = 20
	FloorPercentile = 0.2
	// MinFloorDB keeps digital silence from making every click an event.
	MinFloorDB = -80.0
	// SilenceDB is what an all-zero window reads as.
	SilenceDB = -120.0

	// The classifier only runs on windows at least classifyGateDB above the
	// floor (or during warm-up), to bound CPU on quiet cameras.
	classifyGateDB = 10.0

	// BaselineLabel is the label of a baseline event.
	BaselineLabel = "Loud noise"
)

// RMSdBFS is the root-mean-square level of x in decibels relative to full
// scale (a full-scale sine reads about -3 dBFS, samples are -1..1).
func RMSdBFS(x []float32) float64 {
	if len(x) == 0 {
		return SilenceDB
	}
	var sum float64
	for _, v := range x {
		sum += float64(v) * float64(v)
	}
	rms := math.Sqrt(sum / float64(len(x)))
	if rms <= 0 {
		return SilenceDB
	}
	return math.Max(SilenceDB, 20*math.Log10(rms))
}

// NoiseFloor tracks the ambient level: a low percentile of recent window
// levels, so a few loud windows do not lift it.
type NoiseFloor struct {
	ring []float64
	next int
	full bool
}

// Add records one window level.
func (n *NoiseFloor) Add(db float64) {
	if n.ring == nil {
		n.ring = make([]float64, floorWindows)
	}
	n.ring[n.next] = db
	n.next++
	if n.next == len(n.ring) {
		n.next, n.full = 0, true
	}
}

func (n *NoiseFloor) count() int {
	if n.full {
		return len(n.ring)
	}
	return n.next
}

// Ready reports whether enough windows were seen to trust the floor.
func (n *NoiseFloor) Ready() bool { return n.count() >= floorWarmup }

// Floor returns the current floor in dBFS (not below MinFloorDB).
func (n *NoiseFloor) Floor() float64 {
	c := n.count()
	if c == 0 {
		return MinFloorDB
	}
	tmp := append([]float64(nil), n.ring[:c]...)
	sort.Float64s(tmp)
	f := tmp[int(FloorPercentile*float64(c-1))]
	return math.Max(f, MinFloorDB)
}

// Event is one detected sound.
type Event struct {
	CameraID string
	Kind     string // always "audio"
	Label    string // "Loud noise", or a classifier label (classifier.go)
	// Score is the classifier's confidence (0..1), or for a baseline event
	// the excess over the floor as a fraction of 60 dB.
	Score float64
	DB    float64 // window level, dBFS
	Floor float64 // noise floor at the time, dBFS
	At    time.Time
}

// Analyzer turns a PCM stream into events. It is not safe for concurrent
// use; the tap feeds it from one goroutine. Time is stream time: Start plus
// samples consumed, so tests (and a stalled reader) are deterministic.
type Analyzer struct {
	CameraID    string
	ThresholdDB float64 // 0 = DefaultThresholdDB
	Start       time.Time
	// Classifier is optional; a failing one is dropped for the camera and
	// the reason kept in ClassifierErr (baseline keeps working).
	Classifier    Classifier
	ClassifierErr error

	floor    NoiseFloor
	buf      []float32 // unconsumed samples; buf[:WindowSamples] is the next window
	consumed int64     // samples before buf[0]
	over     int       // consecutive windows over threshold
	lastFire time.Time
}

func (a *Analyzer) threshold() float64 {
	if a.ThresholdDB <= 0 {
		return DefaultThresholdDB
	}
	return a.ThresholdDB
}

// Feed appends PCM and returns the events fired by the windows it completes.
func (a *Analyzer) Feed(pcm []float32) []Event {
	a.buf = append(a.buf, pcm...)
	var out []Event
	for len(a.buf) >= WindowSamples {
		win := a.buf[:WindowSamples]
		at := a.Start.Add(time.Duration(a.consumed+WindowSamples) * time.Second / SampleRate)
		if ev, ok := a.window(win, at); ok {
			out = append(out, ev)
		}
		a.buf = a.buf[HopSamples:]
		a.consumed += HopSamples
	}
	if cap(a.buf) > 4*WindowSamples && len(a.buf) < WindowSamples {
		a.buf = append([]float32(nil), a.buf...) // release the grown backing array
	}
	return out
}

// window processes one analysis window ending at at.
func (a *Analyzer) window(win []float32, at time.Time) (Event, bool) {
	db := RMSdBFS(win)
	ready := a.floor.Ready()
	floor := a.floor.Floor()
	over := db - floor
	a.floor.Add(db)

	// Classifier: only audible windows (or while the floor is warming up).
	var hit *ClassHit
	if a.Classifier != nil && (!ready || over >= classifyGateDB) {
		hits, err := a.Classifier.Classify(win)
		if err != nil {
			a.ClassifierErr, a.Classifier = err, nil
		} else {
			hit = BestHit(hits)
		}
	}

	if !ready {
		return Event{}, false // learning the room
	}
	if over >= a.threshold() {
		a.over++
	} else {
		a.over = 0
	}
	baseline := a.over >= ConsecutiveWindows
	if hit == nil && !baseline {
		return Event{}, false
	}
	if !a.lastFire.IsZero() && at.Sub(a.lastFire) < Debounce {
		return Event{}, false
	}
	ev := Event{CameraID: a.CameraID, Kind: "audio", DB: round1(db), Floor: round1(floor), At: at}
	if hit != nil {
		ev.Label, ev.Score = hit.Label, hit.Score
	} else {
		ev.Label, ev.Score = BaselineLabel, math.Round(math.Min(1, over/60)*100)/100
	}
	a.lastFire = at
	return ev, true
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

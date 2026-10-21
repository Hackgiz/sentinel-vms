package detect

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Detection rate (WAVE16-DETECTION.md, stream B). Manager.Rate returns the
// configured passes per second for a camera (link.Snapshot.DetectRate);
// 0 means auto: one a second on the CPU, five with OpenVINO or Core ML.
// The result is clamped to 1–5. The inference semaphore (MaxParallel) still
// bounds the work: a frame that arrives while the detector is busy is
// skipped, as before.

const (
	MinRate = 1
	MaxRate = 5
	// motionHold: after a frame over the motion threshold, detection frames
	// keep being analyzed (rates above the 2/s motion sampling) this long.
	motionHold = time.Second
)

// AutoRate is the rate "auto" picks for an engine.
func AutoRate(engine string) int {
	switch engine {
	case EngineOpenVINO, EngineCoreML:
		return MaxRate
	}
	return MinRate
}

// ClampRate resolves a configured rate (0 = auto) for engine.
func ClampRate(configured int, engine string) int {
	if configured <= 0 {
		return AutoRate(engine)
	}
	if configured > MaxRate {
		return MaxRate
	}
	return configured
}

// EffectiveRate is the detection passes per second a camera runs at: what
// Rate returns (0 or no callback = auto), resolved against the detector's
// engine and clamped to 1–5. Use "" for the server-wide rate (the detector
// status reports it as effectiveRate).
func (m *Manager) EffectiveRate(cameraID string) int {
	configured := 0
	if m != nil && m.Rate != nil {
		configured = m.Rate(cameraID)
	}
	engine := ""
	if m != nil && m.Objects != nil {
		engine = m.Objects.Info().Engine
	}
	return ClampRate(configured, engine)
}

// rateInterval is the least time between two passes at rate.
func rateInterval(rate int) time.Duration {
	if rate < MinRate {
		rate = MinRate
	}
	return time.Second / time.Duration(rate)
}

// detectRateArgs raises the frame rate of the detection branch of the
// ffmpeg filter graph (detectArgs) to rate when that is above the motion
// sampling rate, keeping motion frames at sampleFPS so motion scores don't
// change. At or below sampleFPS, or if the graph is not the shape expected,
// args come back unchanged (detection then runs at most sampleFPS times a
// second).
func detectRateArgs(args []string, rate int) []string {
	motionFPS, _ := strconv.Atoi(sampleFPS)
	if rate <= motionFPS {
		return args
	}
	head := "[0:v]fps=" + sampleFPS + ",split=2[m][d];[m]"
	for i, a := range args {
		if i > 0 && args[i-1] == "-filter_complex" && strings.HasPrefix(a, head) {
			out := append([]string(nil), args...)
			out[i] = "[0:v]fps=" + strconv.Itoa(rate) + ",split=2[m][d];[m]fps=" + sampleFPS + "," + strings.TrimPrefix(a, head)
			return out
		}
	}
	return args
}

// tracker is the camera's object Tracker. It lives on the Manager, not the
// reader, so ids keep counting across reader restarts; ids start from the
// Manager's first use (Unix seconds × 1000) so they don't repeat across
// server restarts either.
func (m *Manager) tracker(cameraID string) *Tracker {
	m.trackMu.Lock()
	defer m.trackMu.Unlock()
	if m.trackers == nil {
		m.trackers = map[string]*Tracker{}
	}
	t := m.trackers[cameraID]
	if t == nil {
		t = newTrackerFrom(int(time.Now().Unix()) * 1000)
		m.trackers[cameraID] = t
	}
	return t
}

// errRateChanged ends a reader whose detection rate setting changed.
var errRateChanged = errors.New("detection rate changed")

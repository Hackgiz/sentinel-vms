package homekit

import (
	"sync"
	"time"
)

// motionHold is how long a camera's HomeKit motion sensor stays on after the
// last detection. Detectors report in bursts; Home automations want one
// trigger per visit, not a flutter.
const motionHold = 30 * time.Second

// motionTracker holds each camera's motion state: on at the first activity,
// off once motionHold passes without more. It never touches HomeKit itself;
// changes go out through set, so the clock and the sweep are testable.
type motionTracker struct {
	now  func() time.Time
	hold time.Duration
	set  func(cameraID string, on bool) // called outside the lock

	mu   sync.Mutex
	last map[string]time.Time // cameras currently on → last activity
}

func newMotionTracker(now func() time.Time, set func(cameraID string, on bool)) *motionTracker {
	if now == nil {
		now = time.Now
	}
	return &motionTracker{now: now, hold: motionHold, set: set, last: map[string]time.Time{}}
}

// Activity records a detection (any kind) on a camera.
func (m *motionTracker) Activity(cameraID string) {
	m.mu.Lock()
	_, was := m.last[cameraID]
	m.last[cameraID] = m.now()
	m.mu.Unlock()
	if !was {
		m.set(cameraID, true)
	}
}

// Sweep turns off every camera whose last activity is older than the hold
// time. The bridge calls it about once a second.
func (m *motionTracker) Sweep() {
	m.mu.Lock()
	now := m.now()
	var cleared []string
	for id, t := range m.last {
		if now.Sub(t) >= m.hold {
			delete(m.last, id)
			cleared = append(cleared, id)
		}
	}
	m.mu.Unlock()
	for _, id := range cleared {
		m.set(id, false)
	}
}

// Forget drops a camera that is no longer exposed, without notifying.
func (m *motionTracker) Forget(cameraID string) {
	m.mu.Lock()
	delete(m.last, cameraID)
	m.mu.Unlock()
}

// Active reports whether the camera's sensor is currently on.
func (m *motionTracker) Active(cameraID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.last[cameraID]
	return ok
}

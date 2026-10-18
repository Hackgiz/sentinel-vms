package talkback

import (
	"errors"
	"sync"
	"time"
)

// Session limits (WAVE9-REACH.md, stream B).
const (
	DefaultIdleTimeout = 30 * time.Second // no audio for this long ends the session
	DefaultMaxDuration = 5 * time.Minute  // hard cap on one session
)

// End reasons.
const (
	EndClosed = "closed" // the operator hung up
	EndIdle   = "idle"   // no audio for IdleTimeout
	EndLimit  = "limit"  // MaxDuration reached
	EndError  = "error"  // the camera connection failed
)

// ErrBusy means another operator is already talking through the camera.
var ErrBusy = errors.New("someone is already talking through this camera")

// Sink is where a talk's audio goes (a *Session; tests use fakes).
type Sink interface {
	WritePCM(samples8k []int16) error
	Close() error
}

// Manager allows one talk per camera.
type Manager struct {
	IdleTimeout time.Duration
	MaxDuration time.Duration

	mu     sync.Mutex
	active map[string]*Talk
}

// NewManager uses the default limits.
func NewManager() *Manager {
	return &Manager{IdleTimeout: DefaultIdleTimeout, MaxDuration: DefaultMaxDuration}
}

// Reserve claims the camera for user; ErrBusy when someone else holds it.
// The talk's timers start at Attach.
func (m *Manager) Reserve(cameraID, user string) (*Talk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		m.active = map[string]*Talk{}
	}
	if t := m.active[cameraID]; t != nil {
		return nil, ErrBusy
	}
	t := &Talk{m: m, CameraID: cameraID, User: user, done: make(chan struct{}), feed: make(chan struct{}, 1)}
	m.active[cameraID] = t
	return t, nil
}

// Active reports who is talking through a camera ("" = nobody).
func (m *Manager) Active(cameraID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.active[cameraID]; t != nil {
		return t.User
	}
	return ""
}

func (m *Manager) release(t *Talk) {
	m.mu.Lock()
	if m.active[t.CameraID] == t {
		delete(m.active, t.CameraID)
	}
	m.mu.Unlock()
}

// Talk is one reserved camera: audio in (Feed), limits enforced, the sink
// closed (TEARDOWN) exactly once by End.
type Talk struct {
	CameraID, User string

	m       *Manager
	sink    Sink
	halver  *Halver // 16 kHz input only
	started time.Time
	samples []int16

	feed chan struct{} // idle timer reset
	done chan struct{} // closed when a limit ends the talk (or End)

	mu     sync.Mutex
	reason string
	ended  bool
}

// Attach connects the opened camera session and starts the limits. rate
// is the input sample rate: 8000, or 16000 (downsampled here).
func (t *Talk) Attach(sink Sink, rate int) {
	t.sink = sink
	if rate == 16000 {
		t.halver = &Halver{}
	}
	t.started = time.Now()
	go t.watch(t.m.IdleTimeout, t.m.MaxDuration)
}

func (t *Talk) watch(idle, max time.Duration) {
	idleT := time.NewTimer(idle)
	defer idleT.Stop()
	maxT := time.NewTimer(max)
	defer maxT.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-t.feed:
			if !idleT.Stop() {
				select {
				case <-idleT.C:
				default:
				}
			}
			idleT.Reset(idle)
		case <-idleT.C:
			t.finish(EndIdle)
			return
		case <-maxT.C:
			t.finish(EndLimit)
			return
		}
	}
}

// finish records why the talk ends and wakes Done (first reason wins).
func (t *Talk) finish(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.reason == "" {
		t.reason = reason
		close(t.done)
	}
}

// Done closes when the talk must end: a limit was hit, the camera failed,
// or End was called.
func (t *Talk) Done() <-chan struct{} { return t.done }

// Reason is why the talk ended ("" while it runs).
func (t *Talk) Reason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reason
}

// Feed takes little-endian 16-bit mono PCM at the Attach rate. An odd
// trailing byte is dropped. Any audio counts as activity for the idle limit.
func (t *Talk) Feed(pcm []byte) error {
	select {
	case <-t.done:
		return errors.New("the talk has ended")
	default:
	}
	t.samples = t.samples[:0]
	for i := 0; i+1 < len(pcm); i += 2 {
		t.samples = append(t.samples, int16(uint16(pcm[i])|uint16(pcm[i+1])<<8))
	}
	in := t.samples
	if t.halver != nil {
		in = t.halver.Process(nil, in)
	}
	select {
	case t.feed <- struct{}{}:
	default:
	}
	if err := t.sink.WritePCM(in); err != nil {
		t.finish(EndError)
		return err
	}
	return nil
}

// End closes the camera session (TEARDOWN), frees the camera and returns
// how long the talk lasted and why it ended. Safe to call more than once;
// later calls return the same reason.
func (t *Talk) End(reason string) (time.Duration, string) {
	t.finish(reason)
	t.mu.Lock()
	already := t.ended
	t.ended = true
	t.mu.Unlock()
	var d time.Duration
	if !t.started.IsZero() {
		d = time.Since(t.started)
	}
	if !already {
		if t.sink != nil {
			_ = t.sink.Close()
		}
		t.m.release(t)
	}
	return d, t.Reason()
}

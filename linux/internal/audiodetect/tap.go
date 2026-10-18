package audiodetect

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Source is one camera to listen to. It is comparable: a change restarts the
// camera's reader.
type Source struct {
	CameraID string
	// URL is the camera's low-res live stream on the local MediaMTX (the
	// sub-stream when the camera has one); FallbackURL is the main stream,
	// tried when URL carries no audio track. "" = none.
	URL, FallbackURL string
	ThresholdDB      int // 0 = DefaultThresholdDB
}

// Manager runs one ffmpeg audio tap per enabled camera.
type Manager struct {
	FFmpeg  string
	OnEvent func(Event)
	// Classifier is the optional model; ClassifierNote says why it is absent
	// (shown in each camera's detail). Set both before the first Sync.
	Classifier     Classifier
	ClassifierNote string
	// BackoffBase is the first retry delay (default 3 s, doubling to 30 s).
	BackoffBase time.Duration

	mu      sync.Mutex
	workers map[string]*worker
	state   map[string]string // camera → detail line
}

type worker struct {
	src    Source
	cancel context.CancelFunc
}

// Available reports whether audio taps can run (ffmpeg found).
func (m *Manager) Available() bool { return m != nil && m.FFmpeg != "" }

// Status is the camera's audio detail line: what the tap is doing and, when
// the classifier is not running, why. "" = audio detection is not on for it.
func (m *Manager) Status(cameraID string) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state[cameraID]
}

func (m *Manager) setState(id, s string) {
	m.mu.Lock()
	if m.state == nil {
		m.state = map[string]string{}
	}
	if _, running := m.workers[id]; running || s == "" {
		m.state[id] = s
	}
	m.mu.Unlock()
}

// modeNote says which detectors are running.
func (m *Manager) modeNote() string {
	if m.Classifier != nil {
		return "sound classifier + loudness"
	}
	if m.ClassifierNote != "" {
		return "loudness only (" + m.ClassifierNote + ")"
	}
	return "loudness only"
}

// Sync starts, restarts or stops taps so exactly sources are listened to.
func (m *Manager) Sync(ctx context.Context, sources []Source) {
	if !m.Available() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workers == nil {
		m.workers = map[string]*worker{}
	}
	if m.state == nil {
		m.state = map[string]string{}
	}
	want := map[string]Source{}
	for _, s := range sources {
		want[s.CameraID] = s
	}
	for id, w := range m.workers {
		if s, ok := want[id]; !ok || s != w.src {
			w.cancel()
			delete(m.workers, id)
			if !ok {
				delete(m.state, id)
			}
		}
	}
	for id, s := range want {
		if _, ok := m.workers[id]; ok {
			continue
		}
		wctx, cancel := context.WithCancel(ctx)
		m.workers[id] = &worker{src: s, cancel: cancel}
		m.state[id] = "Starting"
		go m.run(wctx, s)
	}
}

// Stop ends every tap.
func (m *Manager) Stop() { m.Sync(context.Background(), nil) }

// run supervises one camera's ffmpeg with the same backoff as the motion
// reader: 3 s doubling to 30 s, reset after a minute of healthy running.
func (m *Manager) run(ctx context.Context, s Source) {
	backoff := m.backoffBase()
	urls := []string{s.URL}
	if s.FallbackURL != "" && s.FallbackURL != s.URL {
		urls = append(urls, s.FallbackURL)
	}
	attempt := 0
	for ctx.Err() == nil {
		started := time.Now()
		url := urls[attempt%len(urls)]
		samples, err := m.watch(ctx, s, url)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = m.backoffBase()
		}
		if samples == 0 {
			attempt++ // no audio here: try the other stream next time
			m.setState(s.CameraID, "No audio from the camera ("+errText(err)+"); retrying")
		} else {
			m.setState(s.CameraID, "Audio stream interrupted ("+errText(err)+"); retrying")
		}
		slog.Debug("audio tap stopped; retrying", "camera", s.CameraID, "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (m *Manager) backoffBase() time.Duration {
	if m.BackoffBase > 0 {
		return m.BackoffBase
	}
	return 3 * time.Second
}

func errText(err error) string {
	if err == nil {
		return "stream ended"
	}
	return err.Error()
}

// FFmpegArgs are the arguments of one audio tap (exported for tests).
func FFmpegArgs(url string) []string {
	return []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-rtsp_transport", "tcp", "-i", url,
		"-vn", "-f", "f32le", "-ac", "1", "-ar", "16000", "-"}
}

// tail keeps the last line ffmpeg wrote to stderr.
type tail struct {
	mu   sync.Mutex
	last string
}

func (t *tail) Write(p []byte) (int, error) {
	if s := strings.TrimSpace(string(p)); s != "" {
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		t.mu.Lock()
		t.last = s
		t.mu.Unlock()
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}

// watch runs one ffmpeg until it stops and returns how many samples it
// delivered.
func (m *Manager) watch(ctx context.Context, s Source, url string) (int64, error) {
	cmd := exec.CommandContext(ctx, m.FFmpeg, FFmpegArgs(url)...)
	cmd.WaitDelay = 3 * time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	var errs tail
	cmd.Stderr = &errs
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	an := &Analyzer{CameraID: s.CameraID, ThresholdDB: float64(s.ThresholdDB), Start: time.Now(), Classifier: m.Classifier}
	r := bufio.NewReaderSize(out, 1<<16)
	raw := make([]byte, 8192) // 2048 samples, 0.128 s
	pcm := make([]float32, len(raw)/4)
	var total int64
	reported := false
	for {
		if _, err := io.ReadFull(r, raw); err != nil {
			_ = cmd.Wait()
			if msg := errs.String(); msg != "" && total == 0 {
				return total, errors.New(msg)
			}
			return total, err
		}
		for i := range pcm {
			v := math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
			if v != v || v > 1 || v < -1 { // NaN or out of range: clip
				if v != v {
					v = 0
				} else if v > 1 {
					v = 1
				} else {
					v = -1
				}
			}
			pcm[i] = v
		}
		total += int64(len(pcm))
		if !reported {
			reported = true
			m.setState(s.CameraID, "Listening: "+m.modeNote())
		}
		hadClassifier := an.Classifier != nil
		for _, ev := range an.Feed(pcm) {
			ev.At = time.Now()
			if m.OnEvent != nil {
				m.OnEvent(ev)
			}
		}
		if hadClassifier && an.Classifier == nil {
			slog.Warn("audio classifier failed; loudness detection only", "camera", s.CameraID, "err", an.ClassifierErr)
			m.setState(s.CameraID, "Listening: loudness only (classifier failed: "+an.ClassifierErr.Error()+")")
		}
	}
}

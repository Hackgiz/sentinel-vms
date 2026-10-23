// Package media supervises MediaMTX: it generates the config from the camera
// list, keeps the process running, and reports per-camera stream state.
//
// MediaMTX only listens on 127.0.0.1. Browsers and phones reach video through
// the Sentinel server's authenticated proxy, never directly.
package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/childproc"
)

// Ports are deliberately not the Mac app's (8554/8888/9997) so both can run
// on one machine during development.
type Ports struct {
	RTSP int
	HLS  int
	API  int
}

var DefaultPorts = Ports{RTSP: 18554, HLS: 18888, API: 19997}

// Source is one camera as MediaMTX needs it.
type Source struct {
	ID     string
	URL    string // credentials embedded
	SubURL string // optional; credentials embedded
	// Mode is the effective recording mode (WAVE22): ModeContinuous,
	// ModeEvents, ModeDual or ModeOff; "" = Record ? continuous : off.
	Mode   string
	Record bool
}

// Recording modes as the media layer sees them (link.Record*).
const (
	ModeContinuous = "continuous"
	ModeEvents     = "events"
	ModeDual       = "dual"
	ModeOff        = "off"
)

// Folders under the recordings root (WAVE22-RECORDING-MODES.md section 1):
// <root>/<id>/ kept full-res footage, <root>/.buffer/<id>/ the rolling
// full-res buffer of events/dual cameras, <root>/.lowres/<id>-sub/ the
// low-res 24/7 stream of dual cameras.
const (
	BufferDir = ".buffer"
	LowResDir = ".lowres"
)

// RecordMode resolves Mode against the legacy Record switch.
func (src Source) RecordMode() string {
	switch src.Mode {
	case ModeContinuous, ModeEvents, ModeOff:
		return src.Mode
	case ModeDual:
		if src.SubURL == "" {
			return ModeEvents // dual needs a sub-stream
		}
		return ModeDual
	}
	if src.Record {
		return ModeContinuous
	}
	return ModeOff
}

type StreamState struct {
	Ready   bool `json:"ready"`
	Readers int  `json:"readers"`
	// Bytes received since the path came up; a stuck value means no video.
	BytesReceived int64 `json:"bytesReceived"`
	// Video resolution from the stream itself (0 until the first keyframe).
	Width  int `json:"width"`
	Height int `json:"height"`
	// Video codec as MediaMTX names it (H264, H265 …); "" until known.
	Codec string `json:"codec,omitempty"`
}

type Supervisor struct {
	Binary         string
	ConfigPath     string
	LogPath        string
	RecordingsRoot string
	Ports          Ports
	// PIDFile records the running MediaMTX so the next start can stop one
	// left behind by a crash (macOS has no parent-death signal); "" =
	// mediamtx.pid next to ConfigPath.
	PIDFile string

	// Low-latency live view (WHEP); see webrtc.go.
	WebRTC WebRTCConfig

	mu          sync.Mutex
	lastWriteAt time.Time
	misses      int // consecutive polls where the engine lagged the config
	sources     []Source
	subIDs      map[string]bool
	states      map[string]StreamState
	cmd         *exec.Cmd
	lastErr     string
	running     bool
	lastWrite   string

	// Dashboard-configured extra ICE hosts; see webrtc.go.
	webrtcExtraHosts []string

	// Per-path reconnects after a timestamp crash; see hlswatch.go.
	restarts    map[string]int
	restartedAt map[string]time.Time
}

// FindBinary looks next to the sentinel executable, then the data dir, then PATH.
func FindBinary(dataDir string) (string, error) {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "mediamtx"))
	}
	candidates = append(candidates, filepath.Join(dataDir, "bin", "mediamtx"))
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return c, nil
		}
	}
	if p, err := exec.LookPath("mediamtx"); err == nil {
		return p, nil
	}
	return "", errors.New("mediamtx not found (place it next to the sentinel binary, in <data>/bin, or on PATH)")
}

// SetRecordingsRoot changes where new segments are written. The next
// Apply rewrites the config so a running MediaMTX picks it up.
func (s *Supervisor) SetRecordingsRoot(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RecordingsRoot == dir {
		return
	}
	s.RecordingsRoot = dir
	s.lastWrite = ""
}

// Apply writes the config for these cameras. A running MediaMTX hot-reloads
// the file; an unchanged config is not rewritten.
func (s *Supervisor) Apply(sources []Source) error {
	s.mu.Lock()
	s.sources = sources
	s.subIDs = map[string]bool{}
	for _, src := range sources {
		if src.SubURL != "" {
			s.subIDs[src.ID] = true
		}
	}
	s.mu.Unlock()

	cfg, err := s.render(sources)
	if err != nil {
		return err
	}
	s.mu.Lock()
	unchanged := cfg == s.lastWrite
	s.mu.Unlock()
	if unchanged {
		return nil
	}
	for _, src := range sources {
		if err := os.MkdirAll(filepath.Join(s.RecordingsRoot, src.ID), 0o750); err != nil {
			return err
		}
	}
	// In place, not write-tmp-and-rename: MediaMTX watches the config file
	// itself, and on macOS (kqueue) a rename-over leaves it watching the
	// old inode, so it never reloads again. The config is one small write
	// and is re-rendered on every start, so a torn write can't stick.
	if err := os.WriteFile(s.ConfigPath, []byte(cfg), 0o600); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastWrite = cfg
	s.lastWriteAt = time.Now()
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) render(sources []Source) (string, error) {
	segName := "%Y%m%d-%H%M%S-%f"
	recordPath, err := yamlQuote(filepath.Join(s.RecordingsRoot, "%path", segName))
	if err != nil {
		return "", err
	}
	bufferPath, err := yamlQuote(filepath.Join(s.RecordingsRoot, BufferDir, "%path", segName))
	if err != nil {
		return "", err
	}
	lowResPath, err := yamlQuote(filepath.Join(s.RecordingsRoot, LowResDir, "%path", segName))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(s.webrtcYAML()) // webrtc.go
	fmt.Fprintf(&b, `logLevel: info
logDestinations: [stdout]

api: yes
apiAddress: 127.0.0.1:%d

rtsp: yes
rtspAddress: 127.0.0.1:%d
# TCP only: avoids fighting other services over UDP ports.
rtspTransports: [tcp]

hls: yes
hlsAddress: 127.0.0.1:%d
hlsAlwaysRemux: yes
# fmp4 carries H.264 and H.265 (the MPEG-TS variant is H.264 only: an H.265
# camera's live view crashed its muxer with "supports H264 video only").
# Browsers, the iPhone app and the Mac app all play fMP4 HLS.
# Seven segments keep ~14 s in the playlist: with three, a phone watching
# through Sentinel Cloud's relay fell off the back of the window between
# fetches and froze on one frame (the LAN never noticed). Latency is set by
# the player's live offset, not by this count.
hlsVariant: fmp4
hlsSegmentCount: 7
hlsSegmentDuration: 1s

rtmp: no
srt: no
# MediaMTX >= 1.21 enables MoQ by default.
moq: no
playback: no

paths:
`, s.Ports.API, s.Ports.RTSP, s.Ports.HLS)

	if len(sources) == 0 {
		b.WriteString("  all_others:\n")
	}
	restarts := s.restartCounts()
	for _, src := range sources {
		mode := src.RecordMode()
		main, err := yamlQuote(src.URL)
		if err != nil {
			// A control character in a URL would corrupt the whole config and
			// take every camera down — skip just this one.
			slog.Warn("skipping camera with unusable URL", "camera", src.ID, "err", err)
			continue
		}
		// Retention of kept footage is the recorder's sweeper's job
		// (recorder/retention.go): recordDeleteAfter 0s turns MediaMTX's
		// deletion off. The rolling buffer of events/dual cameras (60 s
		// segments, so pre-roll footage exists) keeps a 1 h safety net in
		// case the recorder's keeper isn't running.
		path, segment, deleteAfter := recordPath, "15m", "0s"
		if mode == ModeEvents || mode == ModeDual {
			path, segment, deleteAfter = bufferPath, "60s", "1h"
		}
		fmt.Fprintf(&b, `  %s:
    source: %s
    rtspTransport: tcp
    sourceOnDemand: no
    record: %s
    recordPath: %s
    recordFormat: fmp4
    recordSegmentDuration: %s
    recordDeleteAfter: %s
`, src.ID, main, yesNo(mode != ModeOff), path, segment, deleteAfter)
		b.WriteString(restartYAML(restarts[src.ID]))

		if src.SubURL != "" {
			sub, err := yamlQuote(src.SubURL)
			if err != nil {
				continue
			}
			if mode == ModeDual {
				// Low-res 24/7: always connected and recorded.
				fmt.Fprintf(&b, `  %s-sub:
    source: %s
    rtspTransport: tcp
    sourceOnDemand: no
    record: yes
    recordPath: %s
    recordFormat: fmp4
    recordSegmentDuration: 15m
    recordDeleteAfter: 0s
`, src.ID, sub, lowResPath)
				b.WriteString(restartYAML(restarts[src.ID+"-sub"]))
				continue
			}
			// View-only, on demand: many budget cameras can't serve main + sub
			// at once, and recording the main stream must never be starved.
			fmt.Fprintf(&b, `  %s-sub:
    source: %s
    rtspTransport: tcp
    sourceOnDemand: yes
    record: no
`, src.ID, sub)
			b.WriteString(restartYAML(restarts[src.ID+"-sub"]))
		}
	}
	return b.String(), nil
}

// yamlQuote returns a YAML single-quoted scalar. Passwords may contain ! : # etc.
func yamlQuote(s string) (string, error) {
	if strings.ContainsAny(s, "\n\r\x00") {
		return "", errors.New("value contains a line break")
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// Run keeps MediaMTX alive until ctx is cancelled, restarting it with backoff.
func (s *Supervisor) Run(ctx context.Context) {
	// A MediaMTX from a previous run that died without cleaning up still
	// holds the ports; stop it first (only if it's that exact process).
	childproc.KillStale(s.pidFile(), 5*time.Second)
	go s.pollStates(ctx)
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		s.setErr(fmt.Sprintf("mediamtx exited: %v", err))
		slog.Warn("mediamtx exited; restarting", "err", err, "in", backoff)
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
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

func (s *Supervisor) runOnce(ctx context.Context) error {
	logFile, err := os.OpenFile(s.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.CommandContext(ctx, s.Binary, s.ConfigPath)
	// One writer for both, so exec feeds the watcher from a single goroutine.
	out := io.MultiWriter(logFile, &lineWriter{onLine: s.watchLogLine})
	cmd.Stdout = out
	cmd.Stderr = out
	childproc.Configure(cmd) // dies with Sentinel (Linux); see package childproc for macOS
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	childproc.Track(s.pidFile(), pid)
	defer childproc.Untrack(s.pidFile(), pid)
	s.mu.Lock()
	s.cmd, s.running, s.lastErr = cmd, true, ""
	s.mu.Unlock()
	slog.Info("mediamtx started", "pid", cmd.Process.Pid)

	err = cmd.Wait()
	s.mu.Lock()
	s.cmd, s.running = nil, false
	s.mu.Unlock()
	return err
}

func (s *Supervisor) pidFile() string {
	if s.PIDFile != "" {
		return s.PIDFile
	}
	if s.ConfigPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.ConfigPath), "mediamtx.pid")
}

func (s *Supervisor) pollStates(ctx context.Context) {
	client := &http.Client{Timeout: 3 * time.Second}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/v3/paths/list?itemsPerPage=1000", s.Ports.API))
		if err != nil {
			continue
		}
		var list struct {
			Items []struct {
				Name          string `json:"name"`
				Ready         bool   `json:"ready"`
				BytesReceived int64  `json:"bytesReceived"`
				Readers       []any  `json:"readers"`
				Tracks2       []struct {
					Codec      string `json:"codec"`
					CodecProps struct {
						Width  int `json:"width"`
						Height int `json:"height"`
					} `json:"codecProps"`
				} `json:"tracks2"`
			} `json:"items"`
		}
		err = json.NewDecoder(resp.Body).Decode(&list)
		resp.Body.Close()
		if err != nil {
			continue
		}
		states := map[string]StreamState{}
		for _, it := range list.Items {
			st := StreamState{Ready: it.Ready, Readers: len(it.Readers), BytesReceived: it.BytesReceived}
			for _, t := range it.Tracks2 {
				if t.CodecProps.Width > 0 {
					st.Width, st.Height = t.CodecProps.Width, t.CodecProps.Height
					st.Codec = t.Codec
					break
				}
			}
			states[it.Name] = st
		}
		s.mu.Lock()
		s.states = states
		s.mu.Unlock()
		s.reconcile(states)
	}
}

// reconcile rewrites the config when the engine's paths don't match it.
// MediaMTX reloads the file on change, but two writes in quick succession
// can leave its watcher one reload behind — seen on macOS when several
// cameras are added within a second. The rewrite carries a changing
// comment so an identical body still counts as a change.
func (s *Supervisor) reconcile(states map[string]StreamState) {
	s.mu.Lock()
	want := map[string]bool{}
	for _, src := range s.sources {
		want[src.ID] = true
		if src.SubURL != "" {
			want[src.ID+"-sub"] = true
		}
	}
	lagging := len(states) != len(want)
	if !lagging {
		for name := range want {
			if _, ok := states[name]; !ok {
				lagging = true
				break
			}
		}
	}
	// Give a normal reload two polls before stepping in.
	if !lagging || time.Since(s.lastWriteAt) < 5*time.Second {
		s.misses = 0
		s.mu.Unlock()
		return
	}
	s.misses++
	if s.misses < 2 {
		s.mu.Unlock()
		return
	}
	s.misses = 0
	cfg := s.lastWrite
	s.lastWriteAt = time.Now()
	s.mu.Unlock()
	if cfg == "" {
		return
	}
	slog.Warn("media engine lagged the camera config; rewriting it", "paths", len(states), "want", len(want))
	_ = os.WriteFile(s.ConfigPath, []byte(cfg+"# reload "+time.Now().Format(time.RFC3339Nano)+"\n"), 0o600)
}

func (s *Supervisor) setErr(msg string) {
	s.mu.Lock()
	s.lastErr = msg
	s.mu.Unlock()
}

// State of a camera's main path (recording source).
func (s *Supervisor) State(cameraID string) StreamState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[cameraID]
}

func (s *Supervisor) Status() (running bool, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.lastErr
}

// LivePath is the MediaMTX path used for viewing: the low-res sub-stream when
// the camera has one (saves CPU and bandwidth), else the main stream.
func (s *Supervisor) LivePath(cameraID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subIDs[cameraID] {
		return cameraID + "-sub"
	}
	return cameraID
}

// HLSUpstream is the loopback URL for one HLS file of a camera's live path.
func (s *Supervisor) HLSUpstream(cameraID, file string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/%s/%s", s.Ports.HLS, s.LivePath(cameraID), file)
}

// WithCredentials embeds user/password into an RTSP URL (percent-encoded, so
// passwords with @ : / ! etc. survive).
func WithCredentials(raw, user, pass string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if u.Scheme != "rtsp" && u.Scheme != "rtsps" {
		return "", fmt.Errorf("stream URL must start with rtsp:// (got %q)", u.Scheme)
	}
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	return u.String(), nil
}

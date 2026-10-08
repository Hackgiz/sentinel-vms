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
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
	ID            string
	URL           string // credentials embedded
	SubURL        string // optional; credentials embedded
	Record        bool
	RetentionDays int
}

type StreamState struct {
	Ready   bool `json:"ready"`
	Readers int  `json:"readers"`
	// Bytes received since the path came up; a stuck value means no video.
	BytesReceived int64 `json:"bytesReceived"`
	// Video resolution from the stream itself (0 until the first keyframe).
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Supervisor struct {
	Binary         string
	ConfigPath     string
	LogPath        string
	RecordingsRoot string
	Ports          Ports
	DefaultRetain  int // days, when a camera has no override

	mu        sync.Mutex
	sources   []Source
	subIDs    map[string]bool
	states    map[string]StreamState
	cmd       *exec.Cmd
	lastErr   string
	running   bool
	lastWrite string
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
	tmp := s.ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(cfg), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.ConfigPath); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastWrite = cfg
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) render(sources []Source) (string, error) {
	recordPath, err := yamlQuote(filepath.Join(s.RecordingsRoot, "%path", "%Y%m%d-%H%M%S-%f"))
	if err != nil {
		return "", err
	}
	var b strings.Builder
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
# mpegts tolerates budget cameras' variable keyframe spacing; >= 3 segments
# or MediaMTX serves no playlist at all.
hlsVariant: mpegts
hlsSegmentCount: 3
hlsSegmentDuration: 1s

rtmp: no
srt: no
webrtc: no
# MediaMTX >= 1.21 enables MoQ by default.
moq: no
playback: no

paths:
`, s.Ports.API, s.Ports.RTSP, s.Ports.HLS)

	if len(sources) == 0 {
		b.WriteString("  all_others:\n")
	}
	for _, src := range sources {
		retain := src.RetentionDays
		if retain <= 0 {
			retain = s.DefaultRetain
		}
		if retain <= 0 {
			retain = 7
		}
		main, err := yamlQuote(src.URL)
		if err != nil {
			// A control character in a URL would corrupt the whole config and
			// take every camera down — skip just this one.
			slog.Warn("skipping camera with unusable URL", "camera", src.ID, "err", err)
			continue
		}
		fmt.Fprintf(&b, `  %s:
    source: %s
    rtspTransport: tcp
    sourceOnDemand: no
    record: %s
    recordPath: %s
    recordFormat: fmp4
    recordSegmentDuration: 15m
    recordDeleteAfter: %dh
`, src.ID, main, yesNo(src.Record), recordPath, retain*24)

		if src.SubURL != "" {
			sub, err := yamlQuote(src.SubURL)
			if err != nil {
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
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureChild(cmd)
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
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
					break
				}
			}
			states[it.Name] = st
		}
		s.mu.Lock()
		s.states = states
		s.mu.Unlock()
	}
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

// Package tunnel runs a Cloudflare quick tunnel (cloudflared, no account
// needed) so paired iPhones reach the server off-network, same as the Mac.
// The tunnel's origin is a loopback listener serving only the phone API.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var quickURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

type Status struct {
	Available bool   `json:"available"` // cloudflared binary found
	Enabled   bool   `json:"enabled"`
	State     string `json:"state"` // off | starting | connected | retrying
	URL       string `json:"url,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Tunnel struct {
	Binary  string // "" = not installed
	Origin  string // e.g. http://127.0.0.1:41234
	WorkDir string // HOME for cloudflared (keeps a stray ~/.cloudflared/config.yml out)

	mu      sync.Mutex
	url     string
	state   string
	lastErr string
	cancel  context.CancelFunc
	done    chan struct{}
}

// Find looks next to the sentinel binary, in <data>/bin, then on PATH.
func Find(dataDir string) string {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "cloudflared"))
	}
	candidates = append(candidates, filepath.Join(dataDir, "bin", "cloudflared"))
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return c
		}
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	return ""
}

func (t *Tunnel) URL() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.url
}

func (t *Tunnel) Status() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := Status{Available: t.Binary != "", Enabled: t.cancel != nil, State: t.state, URL: t.url, Error: t.lastErr}
	if st.State == "" {
		st.State = "off"
	}
	return st
}

// Start launches (and keeps relaunching) cloudflared until Stop or ctx ends.
func (t *Tunnel) Start(ctx context.Context) error {
	if t.Binary == "" {
		return errors.New("cloudflared is not installed")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	t.cancel, t.done, t.state, t.lastErr = cancel, make(chan struct{}), "starting", ""
	go t.loop(ctx, t.done)
	return nil
}

func (t *Tunnel) Stop() {
	t.mu.Lock()
	cancel, done := t.cancel, t.done
	t.cancel = nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	t.mu.Lock()
	t.url, t.state = "", "off"
	t.mu.Unlock()
}

func (t *Tunnel) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := t.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		t.mu.Lock()
		t.url, t.state = "", "retrying"
		if err != nil {
			t.lastErr = err.Error()
		}
		t.mu.Unlock()
		slog.Warn("cloudflared exited; restarting", "err", err, "in", backoff)
		if time.Since(started) > 2*time.Minute {
			backoff = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func (t *Tunnel) runOnce(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, t.Binary, "tunnel", "--no-autoupdate", "--url", t.Origin)
	if t.WorkDir != "" {
		cmd.Env = append(os.Environ(), "HOME="+t.WorkDir)
		cmd.Dir = t.WorkDir
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = io.Discard
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		if u := quickURL.FindString(sc.Text()); u != "" {
			t.mu.Lock()
			if t.url != u {
				slog.Info("remote access ready", "url", u)
			}
			t.url, t.state, t.lastErr = u, "connected", ""
			t.mu.Unlock()
		}
	}
	return cmd.Wait()
}

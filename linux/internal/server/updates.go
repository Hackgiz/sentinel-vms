package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultUpdateURL lists the newest Mac and Linux versions. The request carries
// only a User-Agent with this server's version; the gateway logs nothing.
const DefaultUpdateURL = "https://dl.sentvms.com/latest"

const updateCheckEvery = 24 * time.Hour

type updateState struct {
	mu        sync.Mutex
	latest    string
	notes     string
	url       string
	checkedAt time.Time
	lastErr   string
}

type latestRelease struct {
	Version  string `json:"version"`
	Notes    string `json:"notes"`
	URL      string `json:"url"`
	URLArm64 string `json:"urlArm64"`
}

func (s *Server) updateChecksOn(ctx context.Context) bool {
	return s.Store.Setting(ctx, "update_check", "on") != "off"
}

// RunUpdateChecks looks for a newer release shortly after start, then daily,
// while update checks are on. Failures are quiet: the server works offline.
func (s *Server) RunUpdateChecks(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if s.updateChecksOn(ctx) {
			_ = s.checkForUpdate(ctx)
		}
		timer.Reset(updateCheckEvery)
	}
}

func (s *Server) checkForUpdate(ctx context.Context) error {
	url := s.UpdateURL
	if url == "" {
		url = DefaultUpdateURL
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "SentinelLinux/"+s.Version)
	resp, err := http.DefaultClient.Do(req)
	var rel latestRelease
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			err = errors.New("update service returned " + resp.Status)
		} else {
			var body struct {
				Linux latestRelease `json:"linux"`
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
			rel = body.Linux
		}
	}

	st := &s.updates
	st.mu.Lock()
	defer st.mu.Unlock()
	st.checkedAt = time.Now()
	if err != nil {
		st.lastErr = "couldn't reach sentvms.com to check for updates"
		slog.Info("update check failed", "err", err)
		return err
	}
	st.lastErr = ""
	st.latest, st.notes, st.url = strings.TrimSpace(rel.Version), rel.Notes, rel.URL
	if runtime.GOARCH == "arm64" && rel.URLArm64 != "" {
		st.url = rel.URLArm64
	}
	if newerVersion(st.latest, s.Version) {
		slog.Info("update available", "current", s.Version, "latest", st.latest)
	}
	return nil
}

func (s *Server) updateStatus(ctx context.Context) map[string]any {
	st := &s.updates
	st.mu.Lock()
	defer st.mu.Unlock()
	resp := map[string]any{
		"current":      s.Version,
		"checkEnabled": s.updateChecksOn(ctx),
		"available":    newerVersion(st.latest, s.Version),
		"latest":       st.latest,
		"notes":        st.notes,
		"url":          st.url,
		"error":        st.lastErr,
		"docker":       os.Getenv("SENTINEL_DOCKER") == "1",
	}
	if !st.checkedAt.IsZero() {
		resp["checkedAt"] = st.checkedAt.Unix()
	}
	return resp
}

func (s *Server) handleGetUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.updateStatus(r.Context()))
}

// handlePutUpdate turns the daily check on or off; {"checkNow": true} checks
// immediately (and works even with daily checks off).
func (s *Server) handlePutUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CheckEnabled *bool `json:"checkEnabled"`
		CheckNow     bool  `json:"checkNow"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.CheckEnabled != nil {
		v := "off"
		if *in.CheckEnabled {
			v = "on"
		}
		if err := s.Store.SetSetting(r.Context(), "update_check", v); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit(r, "System", "Changed update checks", v)
	}
	if in.CheckNow {
		_ = s.checkForUpdate(r.Context())
	}
	s.handleGetUpdate(w, r)
}

// newerVersion reports whether latest is a higher release than current.
// Versions are dotted numbers with an optional "v" prefix; anything after a
// "-" or "+" (git describe, pre-release tags) is ignored. Development builds
// ("dev", or anything unparseable) never report an update.
func newerVersion(latest, current string) bool {
	l, okL := parseVersion(latest)
	c, okC := parseVersion(current)
	if !okL || !okC {
		return false
	}
	for i := 0; i < len(l) || i < len(c); i++ {
		var a, b int
		if i < len(l) {
			a = l[i]
		}
		if i < len(c) {
			b = c[i]
		}
		if a != b {
			return a > b
		}
	}
	return false
}

func parseVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

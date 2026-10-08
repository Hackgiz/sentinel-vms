package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultFeedbackURL is the Sentinel gateway that also takes Mac bug reports.
const DefaultFeedbackURL = "https://dl.sentvms.com/report"

type feedbackLimiter struct {
	mu   sync.Mutex
	sent []time.Time
}

// allow permits 5 submissions per rolling hour for the whole server.
func (l *feedbackLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.sent[:0]
	for _, t := range l.sent {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	l.sent = kept
	if len(l.sent) >= 5 {
		return false
	}
	l.sent = append(l.sent, now)
	return true
}

func osDescription() string {
	name := "Linux"
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				name = strings.Trim(v, `"`)
			}
		}
	}
	var u unix.Utsname
	if unix.Uname(&u) == nil {
		name += " · kernel " + unix.ByteSliceToString(u.Release[:])
	}
	return name
}

// handleFeedback forwards a bug report, idea or question to the Sentinel team.
// The server sends it (not the browser), so it works without extra CORS rules
// and can attach recent server activity with secrets stripped.
func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind               string `json:"kind"`
		Message            string `json:"message"`
		ContactEmail       string `json:"contactEmail"`
		IncludeDiagnostics bool   `json:"includeDiagnostics"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Message = strings.TrimSpace(in.Message)
	if in.Message == "" {
		writeError(w, http.StatusBadRequest, "write a message first")
		return
	}
	if len(in.Message) > 8000 {
		writeError(w, http.StatusBadRequest, "message is too long (8,000 characters max)")
		return
	}
	switch in.Kind {
	case "bug", "idea", "question":
	default:
		in.Kind = "bug"
	}
	if !s.feedbackLimit.allow(time.Now()) {
		writeError(w, http.StatusTooManyRequests, "you've sent a lot of feedback this hour — please try again later")
		return
	}
	cams, _ := s.Store.Cameras(r.Context())
	logTail := ""
	if in.IncludeDiagnostics && in.Kind == "bug" && s.RecentLog != nil {
		logTail = s.RecentLog()
	}
	payload := map[string]any{
		"appVersion":        s.Version,
		"build":             "linux",
		"osVersion":         osDescription(),
		"model":             runtime.GOARCH + " · " + strconv.Itoa(runtime.NumCPU()) + " CPUs",
		"cameraCount":       len(cams),
		"crashedLastLaunch": false,
		"userMessage":       in.Message,
		"contactEmail":      strings.TrimSpace(in.ContactEmail),
		"logTail":           logTail,
		"kind":              in.Kind,
		"platform":          "linux",
	}
	body, _ := json.Marshal(payload)
	url := s.FeedbackURL
	if url == "" {
		url = DefaultFeedbackURL
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SentinelLinux/"+s.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "couldn't reach Sentinel's feedback service — check this server's internet connection, or email hello@sentvms.com")
		return
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		writeError(w, http.StatusBadGateway, "the feedback service had a problem — please try again later")
		return
	}
	s.audit(r, "Support", "Sent feedback", in.Kind)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

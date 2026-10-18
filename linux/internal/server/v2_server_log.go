package server

// API v2: a recording server's recent system log (PLATFORM.md, "Audit and
// logs"). Each server keeps its last lines in memory with secrets stripped
// (internal/logring); a remote one is asked over the control link
// (link.CmdLog), the local one is this process.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/logring"
	"sentinel-linux/internal/store"
)

const (
	defaultLogLines = 300
	logFetchTimeout = 10 * time.Second
)

// handleV2RecordingServerLog returns the last ?lines= log lines (default
// 300, at most 2000), oldest first, as text. 503 when the server isn't
// connected (or too old to answer).
func (s *Server) handleV2RecordingServerLog(w http.ResponseWriter, r *http.Request) {
	if !s.siteWideAllowed(w, r) { // scope_sitewide.go
		return
	}
	lines := defaultLogLines
	if raw := r.URL.Query().Get("lines"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > link.LogMaxLines {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "lines must be a number from 1 to "+strconv.Itoa(link.LogMaxLines))
			return
		}
		lines = n
	}
	rs, err := s.Store.RecordingServer(r.Context(), r.PathValue("serverId"))
	if err != nil {
		writeV2Fail(w, err, "recording server")
		return
	}
	var text string
	if rs.ID == store.LocalRecorderID && s.Logs != nil {
		text = s.Logs(lines) // the local recording server is this process
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), logFetchTimeout)
		defer cancel()
		var res link.LogResult
		if err := s.command(ctx, rs.ID, link.CmdLog, link.LogArgs{Lines: lines}, &res); err != nil {
			writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, logFetchError(err))
			return
		}
		text = redactLines(res.Text)
	}
	s.audit(r, "Servers", "Viewed recording server log", rs.Name+" ("+rs.ID+")")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if text != "" {
		_, _ = w.Write([]byte(text + "\n"))
	}
}

// logFetchError says why a recording server's log can't be fetched.
func logFetchError(err error) string {
	var ce *link.CommandError
	switch {
	case errors.Is(err, link.ErrNoRecorder):
		return "the recording server isn't connected"
	case errors.As(err, &ce) && ce.Code == link.CodeUnknownCommand:
		return "the recording server is too old to send its log — update it"
	case errors.As(err, &ce):
		return "the recording server can't send its log: " + ce.Message
	case errors.Is(err, context.DeadlineExceeded):
		return "the recording server didn't answer in time"
	}
	return "the recording server can't be reached: " + err.Error()
}

// redactLines strips secrets again on this side (an older or misconfigured
// recording server might not).
func redactLines(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = logring.Redact(l)
	}
	return strings.Join(lines, "\n")
}

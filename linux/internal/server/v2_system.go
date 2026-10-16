package server

// API v2: the System Dashboard's small nodes (WAVE6-ADMIN.md, stream D).
//
//	GET/PUT /api/v2/site                 Site Information (kv site.*)
//	GET/PUT /api/v2/system/thresholds    System Monitor colour thresholds (kv monitor.*)
//	GET     /api/v2/system/tasks         Current Tasks (internal/tasks)
//	GET     /api/v2/system/logs          Server Logs, JSON tail or text download
//
// Settings live in the kv settings table under namespaced keys; the v1
// settings blob is untouched.

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

func (s *Server) registerV2System(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/site", auth.ViewLive, s.handleV2GetSite)
	s.v2Route(mux, "PUT /api/v2/site", auth.ChangeSettings, s.handleV2PutSite)
	s.v2Route(mux, "GET /api/v2/system/thresholds", auth.ViewAudit, s.handleV2GetThresholds)
	s.v2Route(mux, "PUT /api/v2/system/thresholds", auth.ChangeSettings, s.handleV2PutThresholds)
	s.v2Route(mux, "GET /api/v2/system/tasks", auth.ViewAudit, s.handleV2Tasks)
	s.v2Route(mux, "GET /api/v2/system/logs", auth.ViewAudit, s.handleV2SystemLogs)
	s.v2Route(mux, "GET /api/v2/system/config-report", auth.ChangeSettings, s.handleV2ConfigReport)
}

// MARK: - Site information

const (
	maxSiteName        = 80
	maxSiteDescription = 500
	maxSiteContact     = 200
)

type v2Site struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Contact     string `json:"contact"`
}

func (s *Server) loadSite(ctx context.Context) v2Site {
	return v2Site{
		Name:        s.Store.Setting(ctx, "site.name", ""),
		Description: s.Store.Setting(ctx, "site.description", ""),
		Contact:     s.Store.Setting(ctx, "site.contact", ""),
	}
}

func (s *Server) handleV2GetSite(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.loadSite(r.Context()))
}

func (s *Server) handleV2PutSite(w http.ResponseWriter, r *http.Request) {
	var in v2Site
	if !readV2JSON(w, r, &in) {
		return
	}
	in.Name, in.Description, in.Contact = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description), strings.TrimSpace(in.Contact)
	for _, f := range []struct {
		label string
		v     string
		max   int
	}{{"name", in.Name, maxSiteName}, {"description", in.Description, maxSiteDescription}, {"contact", in.Contact, maxSiteContact}} {
		if utf8.RuneCountInString(f.v) > f.max {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "the site "+f.label+" can be at most "+strconv.Itoa(f.max)+" characters")
			return
		}
	}
	ctx := r.Context()
	for k, v := range map[string]string{"site.name": in.Name, "site.description": in.Description, "site.contact": in.Contact} {
		if err := s.Store.SetSetting(ctx, k, v); err != nil {
			writeV2Err(w, err)
			return
		}
	}
	s.audit(r, "Settings", "Changed site information", in.Name)
	writeJSON(w, http.StatusOK, s.loadSite(ctx))
}

// MARK: - Monitor thresholds

const (
	defaultAmber = 80
	defaultRed   = 95
)

type v2Threshold struct {
	Amber int `json:"amber"`
	Red   int `json:"red"`
}

type v2Thresholds struct {
	CPU  v2Threshold `json:"cpu"`
	Mem  v2Threshold `json:"mem"`
	Disk v2Threshold `json:"disk"`
}

func (s *Server) loadThresholds(ctx context.Context) v2Thresholds {
	get := func(metric string) v2Threshold {
		return v2Threshold{
			Amber: settingInt(s.Store.Setting(ctx, "monitor."+metric+".amber", ""), defaultAmber),
			Red:   settingInt(s.Store.Setting(ctx, "monitor."+metric+".red", ""), defaultRed),
		}
	}
	return v2Thresholds{CPU: get("cpu"), Mem: get("mem"), Disk: get("disk")}
}

func settingInt(raw string, fallback int) int {
	if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 100 {
		return n
	}
	return fallback
}

func (s *Server) handleV2GetThresholds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.loadThresholds(r.Context()))
}

func (s *Server) handleV2PutThresholds(w http.ResponseWriter, r *http.Request) {
	var in v2Thresholds
	if !readV2JSON(w, r, &in) {
		return
	}
	named := []struct {
		name string
		t    v2Threshold
	}{{"cpu", in.CPU}, {"mem", in.Mem}, {"disk", in.Disk}}
	for _, n := range named {
		if n.t.Amber < 1 || n.t.Red > 100 || n.t.Amber >= n.t.Red {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, n.name+": amber and red must be percentages from 1 to 100, with amber below red")
			return
		}
	}
	ctx := r.Context()
	for _, n := range named {
		if err := s.Store.SetSetting(ctx, "monitor."+n.name+".amber", strconv.Itoa(n.t.Amber)); err != nil {
			writeV2Err(w, err)
			return
		}
		if err := s.Store.SetSetting(ctx, "monitor."+n.name+".red", strconv.Itoa(n.t.Red)); err != nil {
			writeV2Err(w, err)
			return
		}
	}
	s.audit(r, "Settings", "Changed monitor thresholds", "cpu "+thresholdLabel(in.CPU)+", memory "+thresholdLabel(in.Mem)+", disk "+thresholdLabel(in.Disk))
	writeJSON(w, http.StatusOK, s.loadThresholds(ctx))
}

func thresholdLabel(t v2Threshold) string {
	return strconv.Itoa(t.Amber) + "/" + strconv.Itoa(t.Red) + "%"
}

// MARK: - Current tasks

func (s *Server) handleV2Tasks(w http.ResponseWriter, _ *http.Request) {
	v2Items(w, tasks.Default.List())
}

// MARK: - Server logs

var logLevelPattern = regexp.MustCompile(`\blevel=([A-Za-z]+)`)

// logLevels orders slog's levels; anything unrecognised counts as INFO.
var logLevels = map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "WARNING": 2, "ERROR": 3}

func lineLevel(line string) string {
	if m := logLevelPattern.FindStringSubmatch(line); m != nil {
		if l := strings.ToUpper(m[1]); l == "WARNING" {
			return "WARN"
		} else if _, ok := logLevels[l]; ok {
			return l
		}
	}
	return "INFO"
}

type v2LogLine struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// filterLogLines keeps the lines at or above min (a level name, "" = all)
// and then the last limit of them, oldest first.
func filterLogLines(text, min string, limit int) []v2LogLine {
	out := []v2LogLine{}
	if text == "" {
		return out
	}
	floor := logLevels[strings.ToUpper(min)]
	for _, l := range strings.Split(text, "\n") {
		if l == "" {
			continue
		}
		lv := lineLevel(l)
		if logLevels[lv] >= floor {
			out = append(out, v2LogLine{Level: lv, Text: l})
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// handleV2SystemLogs tails one server's in-memory log ring. server is a
// recording server id (default "local": the management server's own
// process). The remote ones answer over the control link (link.CmdLog), the
// same path as /recording-servers/{id}/log; both are redacted again here.
func (s *Server) handleV2SystemLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 300
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > link.LogMaxLines {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "limit must be a number from 1 to "+strconv.Itoa(link.LogMaxLines))
			return
		}
		limit = n
	}
	level := strings.ToUpper(q.Get("level"))
	if level == "WARNING" {
		level = "WARN"
	}
	if _, ok := logLevels[level]; level != "" && !ok {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "level must be debug, info, warn or error")
		return
	}
	format := q.Get("format")
	if format != "" && format != "json" && format != "text" {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "format must be json or text")
		return
	}
	serverID := q.Get("server")
	if serverID == "" {
		serverID = store.LocalRecorderID
	}
	rs, err := s.Store.RecordingServer(r.Context(), serverID)
	if err != nil {
		writeV2Fail(w, err, "recording server")
		return
	}
	var text string
	if rs.ID == store.LocalRecorderID {
		if s.Logs != nil {
			text = s.Logs(link.LogMaxLines)
		}
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), logFetchTimeout)
		defer cancel()
		var res link.LogResult
		if err := s.command(ctx, rs.ID, link.CmdLog, link.LogArgs{Lines: link.LogMaxLines}, &res); err != nil {
			writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, logFetchError(err))
			return
		}
		text = redactLines(res.Text)
	}
	lines := filterLogLines(text, level, limit)
	if format == "text" {
		var b strings.Builder
		for _, l := range lines {
			b.WriteString(l.Text)
			b.WriteByte('\n')
		}
		name := "sentinel-" + rs.ID + "-" + time.Now().Format("20060102-150405") + ".log"
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b.String()))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"server": rs.ID, "name": rs.Name, "lines": lines})
}

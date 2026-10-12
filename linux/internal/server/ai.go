package server

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/ai"
	"sentinel-linux/internal/store"
)

// MARK: - AI settings (bring your own Anthropic key)

const (
	aiEnabledSetting = "ai_enabled"
	aiKeySetting     = "ai_key" // sealed with install.key
	aiLimitSetting   = "ai_daily_limit"
	aiDefaultLimit   = 300
)

type aiState struct {
	mu        sync.Mutex
	key       string
	client    *ai.Client
	day       string
	used      int
	lastError string
}

func (s *Server) aiKey() string {
	sealed := s.Store.Setting(s.Ctx, aiKeySetting, "")
	if sealed == "" {
		return ""
	}
	key, err := s.Store.Unseal(sealed)
	if err != nil {
		return ""
	}
	return key
}

func (s *Server) aiReady() bool {
	return s.Store.Setting(s.Ctx, aiEnabledSetting, "off") == "on" && s.aiKey() != ""
}

func (s *Server) aiClient() (*ai.Client, error) {
	key := s.aiKey()
	if key == "" {
		return nil, ai.ErrNoKey
	}
	s.ai.mu.Lock()
	defer s.ai.mu.Unlock()
	if s.ai.client == nil || s.ai.key != key {
		s.ai.client, s.ai.key = ai.New(key, s.AIOptions...), key
	}
	return s.ai.client, nil
}

func (s *Server) aiLimit() int {
	n, err := strconv.Atoi(s.Store.Setting(s.Ctx, aiLimitSetting, strconv.Itoa(aiDefaultLimit)))
	if err != nil || n < 0 {
		return aiDefaultLimit
	}
	return n
}

// aiTakeBudget counts one description against the daily cap (a runaway camera
// must not run up the operator's Anthropic bill).
func (s *Server) aiTakeBudget() bool {
	limit := s.aiLimit()
	s.ai.mu.Lock()
	defer s.ai.mu.Unlock()
	today := time.Now().Format("2006-01-02")
	if s.ai.day != today {
		s.ai.day, s.ai.used = today, 0
	}
	if s.ai.used >= limit {
		return false
	}
	s.ai.used++
	return true
}

func (s *Server) setAIError(err error) {
	s.ai.mu.Lock()
	defer s.ai.mu.Unlock()
	if err == nil {
		s.ai.lastError = ""
	} else {
		s.ai.lastError = err.Error()
	}
}

func (s *Server) aiStatus() map[string]any {
	s.ai.mu.Lock()
	used, lastErr := s.ai.used, s.ai.lastError
	if s.ai.day != time.Now().Format("2006-01-02") {
		used = 0
	}
	s.ai.mu.Unlock()
	return map[string]any{
		"enabled":     s.Store.Setting(s.Ctx, aiEnabledSetting, "off") == "on",
		"hasKey":      s.aiKey() != "",
		"dailyLimit":  s.aiLimit(),
		"usedToday":   used,
		"lastError":   lastErr,
		"detector":    s.anyObjectDetector(),
		"visionModel": ai.VisionModel,
		"searchModel": ai.TextModel,
	}
}

func (s *Server) handleGetAI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.aiStatus())
}

func (s *Server) handlePutAI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled    *bool   `json:"enabled"`
		APIKey     *string `json:"apiKey"` // "" clears
		DailyLimit *int    `json:"dailyLimit"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	// Recording servers grab extra frames only while descriptions are on.
	wasReady := s.aiReady()
	defer func() {
		if s.aiReady() != wasReady {
			if err := s.ApplyMedia(s.ctxOrBackground()); err != nil {
				slog.Error("media apply failed", "err", err)
			}
		}
	}()
	if in.APIKey != nil {
		key := strings.TrimSpace(*in.APIKey)
		if key == "" {
			_ = s.Store.SetSetting(ctx, aiKeySetting, "")
			s.audit(r, "AI", "Removed Anthropic API key", "")
		} else {
			vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := ai.Validate(vctx, key, s.AIOptions...)
			cancel()
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			sealed, err := s.Store.Seal(key)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			_ = s.Store.SetSetting(ctx, aiKeySetting, sealed)
			s.audit(r, "AI", "Saved Anthropic API key", "key ending "+tail(key, 4))
		}
	}
	if in.DailyLimit != nil {
		if *in.DailyLimit < 0 || *in.DailyLimit > 100000 {
			writeError(w, http.StatusBadRequest, "daily limit must be between 0 and 100000")
			return
		}
		_ = s.Store.SetSetting(ctx, aiLimitSetting, strconv.Itoa(*in.DailyLimit))
	}
	if in.Enabled != nil {
		val := "off"
		if *in.Enabled {
			if s.aiKey() == "" {
				writeError(w, http.StatusBadRequest, "add your Anthropic API key first")
				return
			}
			val = "on"
		}
		_ = s.Store.SetSetting(ctx, aiEnabledSetting, val)
		s.audit(r, "AI", "Turned AI "+val, "")
	}
	writeJSON(w, http.StatusOK, s.aiStatus())
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// aiEventLines turns events into the text models' corpus. Detection events
// (people, vehicles, animals) are what matter; plain motion is included only
// when there is nothing else.
func (s *Server) aiEventLines(ctx context.Context, from, to time.Time) ([]ai.EventLine, map[string]store.Event, error) {
	events, err := s.Store.EventsBetween(ctx, from, to, 20000)
	if err != nil {
		return nil, nil, err
	}
	events = s.visibleEvents(ctx, events)
	names := s.cameraNames(ctx)
	byID := map[string]store.Event{}
	var lines, motion []ai.EventLine
	for _, e := range events {
		byID[e.ID] = e
		l := ai.EventLine{ID: e.ID, Time: time.Unix(e.CreatedAt, 0), Camera: names[e.CameraID], Kind: e.Kind,
			Description: e.Description, Tags: e.Tags, Threat: e.Threat}
		if l.Camera == "" {
			l.Camera = "Camera"
		}
		if e.Kind == "Motion" {
			motion = append(motion, l)
		} else {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		lines = motion
	}
	return lines, byID, nil
}

func (s *Server) handleAISearch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query string `json:"query"`
		Days  int    `json:"days"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !s.aiReady() {
		writeError(w, http.StatusConflict, "AI is off — add your Anthropic API key and turn AI on in System → AI")
		return
	}
	if in.Days <= 0 || in.Days > 30 {
		in.Days = 7
	}
	now := time.Now()
	lines, byID, err := s.aiEventLines(r.Context(), now.Add(-time.Duration(in.Days)*24*time.Hour), now.Add(time.Minute))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	client, err := s.aiClient()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	answer, ids, err := client.Search(ctx, in.Query, lines, now)
	if err != nil {
		s.setAIError(err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	matches := []store.Event{}
	for _, id := range ids {
		if e, ok := byID[id]; ok {
			matches = append(matches, e)
		}
	}
	s.audit(r, "AI", "Searched events", in.Query)
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer, "events": matches, "searched": len(lines)})
}

func (s *Server) handleAIDigest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Period string `json:"period"` // today | 24h | yesterday
	}
	if r.ContentLength != 0 && !readJSON(w, r, &in) {
		return
	}
	if !s.aiReady() {
		writeError(w, http.StatusConflict, "AI is off — add your Anthropic API key and turn AI on in System → AI")
		return
	}
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	from, to, label := midnight, now.Add(time.Minute), "today"
	switch in.Period {
	case "24h":
		from, label = now.Add(-24*time.Hour), "the last 24 hours"
	case "yesterday":
		from, to, label = midnight.AddDate(0, 0, -1), midnight, "yesterday"
	}
	lines, _, err := s.aiEventLines(r.Context(), from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	client, err := s.aiClient()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	digest, err := client.Digest(ctx, label, lines)
	if err != nil {
		s.setAIError(err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"digest": digest, "events": len(lines), "label": label})
}

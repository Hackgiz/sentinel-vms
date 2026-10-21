package server

// Semantic search API (WAVE16-DETECTION.md, stream E; openapi tag
// "semantic"):
//
//	POST   /api/v2/events/semantic   ViewLive (as event search), audited
//	GET    /api/v2/semantic          ViewLive: model status + backfill
//	POST   /api/v2/semantic/backfill ChangeSettings, audited
//	DELETE /api/v2/semantic/backfill ChangeSettings, audited (cancel)

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"sentinel-linux/internal/auth"
)

func (s *Server) registerV2Semantic(mux *http.ServeMux) {
	s.v2Route(mux, "POST /api/v2/events/semantic", auth.ViewLive, s.handleV2SemanticSearch)
	s.v2Route(mux, "GET /api/v2/semantic", auth.ViewLive, s.handleV2SemanticStatus)
	s.v2Route(mux, "POST /api/v2/semantic/backfill", auth.ChangeSettings, s.handleV2StartSemanticBackfill)
	s.v2Route(mux, "DELETE /api/v2/semantic/backfill", auth.ChangeSettings, s.handleV2CancelSemanticBackfill)
}

func (s *Server) handleV2SemanticSearch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text     string   `json:"text"`
		Cameras  []string `json:"cameras"`
		From     int64    `json:"from"` // Unix ms; 0 = open
		To       int64    `json:"to"`
		Limit    int      `json:"limit"`
		MinScore *float64 `json:"minScore"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	text := strings.TrimSpace(in.Text)
	switch n := utf8.RuneCountInString(text); {
	case n < 2 || n > 200:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "text must be 2 to 200 characters")
		return
	case in.Limit < 0 || in.Limit > semanticMaxLimit:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "limit must be a number from 1 to 100")
		return
	case in.From < 0 || in.To < 0 || (in.To > 0 && in.To <= in.From):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "from must be before to (Unix ms)")
		return
	case in.MinScore != nil && (*in.MinScore < 0 || *in.MinScore > 1):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "minScore must be a number from 0 to 1")
		return
	}
	q := semanticQuery{Text: text, Cameras: in.Cameras, FromMs: in.From, ToMs: in.To, Limit: in.Limit, MinScore: semanticDefaultMinScore}
	if q.Limit == 0 {
		q.Limit = semanticDefaultLimit
	}
	if in.MinScore != nil {
		q.MinScore = *in.MinScore
	}
	eng, reason := s.semanticEngine()
	if eng == nil {
		writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "semantic search is unavailable: "+reason)
		return
	}
	items, err := s.semanticSearch(r.Context(), eng, q)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "Search", "Semantic search", text)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "model": eng.Model()})
}

func (s *Server) handleV2SemanticStatus(w http.ResponseWriter, r *http.Request) {
	eng, reason := s.semanticEngine()
	out := map[string]any{"available": eng != nil}
	if eng == nil {
		out["reason"] = reason
	} else {
		out["model"] = eng.Model()
		n, err := s.Store.CountSemantic(r.Context(), eng.Model())
		if err != nil {
			writeV2Err(w, err)
			return
		}
		out["indexed"] = n
	}
	st := s.semanticSt()
	st.mu.Lock()
	job := st.job
	st.mu.Unlock()
	if v := job.view(); v != nil {
		out["backfill"] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleV2StartSemanticBackfill(w http.ResponseWriter, r *http.Request) {
	who := ""
	if u := currentUser(r); u != nil {
		who = u.Name
	}
	job, started, err := s.startSemanticBackfill(who)
	if err != nil {
		writeV2Fail(w, err, "backfill")
		return
	}
	status := http.StatusOK
	if started {
		status = http.StatusCreated
		s.audit(r, "Search", "Started semantic index", fmt.Sprintf("newest %d events", semanticBackfill))
	}
	writeJSON(w, status, job.view())
}

func (s *Server) handleV2CancelSemanticBackfill(w http.ResponseWriter, r *http.Request) {
	job, wasRunning := s.cancelSemanticBackfill()
	if job == nil {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "no semantic index job")
		return
	}
	if wasRunning {
		s.audit(r, "Search", "Canceled semantic index", job.taskID)
	}
	writeJSON(w, http.StatusOK, job.view())
}

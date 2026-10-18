package server

// Appearance search API (WAVE9-REACH.md, stream A):
//
//	POST   /api/v2/events/{eventId}/similar  ViewLive (as event search), audited
//	GET    /api/v2/appearance                ViewLive: model status + backfill
//	POST   /api/v2/appearance/backfill       ChangeSettings, audited
//	DELETE /api/v2/appearance/backfill       ChangeSettings, audited (cancel)
//
// Similar search is brute-force cosine over the stored vectors of one
// model, pre-filtered by the camera_id + at_ms index and the caller's
// camera scope.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/reid"
	"sentinel-linux/internal/store"
)

const (
	similarDefaultLimit = 24
	similarMaxLimit     = 100
	similarMaxScan      = 200_000 // newest candidate vectors compared per search
)

func (s *Server) registerV2Appearance(mux *http.ServeMux) {
	s.v2Route(mux, "POST /api/v2/events/{eventId}/similar", auth.ViewLive, s.handleV2SimilarEvents)
	s.v2Route(mux, "GET /api/v2/appearance", auth.ViewLive, s.handleV2AppearanceStatus)
	s.v2Route(mux, "POST /api/v2/appearance/backfill", auth.ChangeSettings, s.handleV2StartBackfill)
	s.v2Route(mux, "DELETE /api/v2/appearance/backfill", auth.ChangeSettings, s.handleV2CancelBackfill)
}

type v2SimilarHit struct {
	Score float64 `json:"score"`
	Event v2Event `json:"event"`
}

func (s *Server) handleV2SimilarEvents(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From     int64    `json:"from"` // Unix ms; 0 = open
		To       int64    `json:"to"`
		Cameras  []string `json:"cameras"`
		Limit    int      `json:"limit"`
		MinScore float64  `json:"minScore"`
		AnyKind  bool     `json:"anyKind"`
	}
	if r.ContentLength != 0 && !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	e, err := s.scopedEvent(ctx, r.PathValue("eventId"))
	if err != nil {
		writeV2Fail(w, err, "event")
		return
	}
	switch {
	case in.Limit < 0 || in.Limit > similarMaxLimit:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "limit must be a number from 1 to 100")
		return
	case in.From < 0 || in.To < 0 || (in.To > 0 && in.To <= in.From):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "from must be before to (Unix ms)")
		return
	case in.MinScore < -1 || in.MinScore > 1:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "minScore must be a number from -1 to 1")
		return
	}
	if in.Limit == 0 {
		in.Limit = similarDefaultLimit
	}
	emb, reason := s.appearanceModel()
	if emb == nil {
		writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "appearance search is unavailable: "+reason)
		return
	}

	// The query's own vector: stored, or computed now (and stored).
	var query []float32
	if raw, err := s.Store.EventEmbedding(ctx, e.ID, emb.Model()); err == nil {
		query, err = reid.Decode(raw)
		if err != nil {
			writeV2Err(w, err)
			return
		}
	} else if errors.Is(err, store.ErrNotFound) {
		query, err = s.embedEvent(ctx, emb, e)
		switch {
		case errors.Is(err, reid.ErrNoBox):
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "this event has no detection box to search by")
			return
		case errors.Is(err, reid.ErrTooSmall):
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "the detection in this event is too small to compare")
			return
		case errors.Is(err, errNoThumbnail):
			writeV2Error(w, http.StatusConflict, codeConflict, errNoThumbnail.Error())
			return
		case err != nil:
			writeV2Err(w, err)
			return
		}
	} else {
		writeV2Err(w, err)
		return
	}

	items := []v2SimilarHit{}
	scanned := 0
	ids, none := s.scopedCameraIDs(ctx, in.Cameras)
	if !none {
		f := store.EmbeddingFilter{Model: emb.Model(), CameraIDs: ids, FromMs: in.From, ToMs: in.To, Exclude: e.ID, Max: similarMaxScan}
		if !in.AnyKind {
			f.Kind = e.Kind
		}
		rank := reid.NewRanker(query, in.Limit, in.MinScore)
		err := s.Store.ScanEmbeddings(ctx, f, func(id string, raw []byte) {
			scanned++
			if v, err := reid.Decode(raw); err == nil {
				rank.Add(id, v)
			}
		})
		if err != nil {
			writeV2Err(w, err)
			return
		}
		hits := rank.Hits()
		hitIDs := make([]string, len(hits))
		for i, h := range hits {
			hitIDs[i] = h.ID
		}
		events, err := s.Store.EventsByID(ctx, hitIDs)
		if err != nil {
			writeV2Err(w, err)
			return
		}
		sc := s.cameraScope(ctx)
		for _, h := range hits {
			if ev := events[h.ID]; ev != nil && sc.camera(ev.CameraID) {
				items = append(items, v2SimilarHit{Score: roundScore(h.Score), Event: toV2Event(ev)})
			}
		}
	}
	detail := fmt.Sprintf("%s %s at %s: %d similar", s.appearanceCamLabel(r, e.CameraID), strings.ToLower(e.Kind),
		timeLabel(e.CreatedAt), len(items))
	if len(in.Cameras) > 0 {
		detail += fmt.Sprintf(" (%d cameras asked)", len(in.Cameras))
	}
	s.audit(r, "Search", "Appearance search", detail)
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   toV2Event(e),
		"model":   emb.Model(),
		"scanned": scanned,
		"items":   items,
	})
}

func roundScore(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}

func (s *Server) appearanceCamLabel(r *http.Request, id string) string {
	if c, err := s.Store.Camera(r.Context(), id); err == nil {
		return c.Name
	}
	return "camera " + id
}

func timeLabel(sec int64) string {
	return time.Unix(sec, 0).Format("Jan 2 15:04:05")
}

func (s *Server) handleV2AppearanceStatus(w http.ResponseWriter, r *http.Request) {
	emb, reason := s.appearanceModel()
	out := map[string]any{"available": emb != nil}
	if emb == nil {
		out["reason"] = reason
	} else {
		out["model"] = emb.Model()
		n, err := s.Store.CountEmbeddings(r.Context(), emb.Model())
		if err != nil {
			writeV2Err(w, err)
			return
		}
		out["indexed"] = n
	}
	s.appearance.mu.Lock()
	job := s.appearance.job
	s.appearance.mu.Unlock()
	if v := job.view(); v != nil {
		out["backfill"] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleV2StartBackfill(w http.ResponseWriter, r *http.Request) {
	who := ""
	if u := currentUser(r); u != nil {
		who = u.Name
	}
	job, started, err := s.startBackfill(who)
	if err != nil {
		writeV2Fail(w, err, "backfill")
		return
	}
	status := http.StatusOK
	if started {
		status = http.StatusCreated
		s.audit(r, "Search", "Started appearance index", fmt.Sprintf("newest %d events", appearanceBackfill))
	}
	writeJSON(w, status, job.view())
}

func (s *Server) handleV2CancelBackfill(w http.ResponseWriter, r *http.Request) {
	job, wasRunning := s.cancelBackfill()
	if job == nil {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "no appearance index job")
		return
	}
	if wasRunning {
		s.audit(r, "Search", "Canceled appearance index", job.taskID)
	}
	writeJSON(w, http.StatusOK, job.view())
}

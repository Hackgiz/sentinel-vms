package server

// Forensic search over detection events (WAVE3-OPS.md, Stream C):
// GET /api/v2/events/search, GET /api/v2/events/facets and, when AI is on,
// POST /api/v2/events/ask. Times on the wire are Unix milliseconds like the
// rest of v2; the store works in seconds.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"sentinel-linux/internal/ai"
	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/lpr"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2Search(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/events/search", auth.ViewLive, s.handleV2EventSearch)
	s.v2Route(mux, "GET /api/v2/events/facets", auth.ViewLive, s.handleV2EventFacets)
	s.v2Route(mux, "POST /api/v2/events/ask", auth.ViewLive, s.handleV2EventAsk)
}

// v2SearchFilter is an event filter as the API speaks it.
type v2SearchFilter struct {
	Cameras  []string `json:"cameras,omitempty"`
	Kinds    []string `json:"kinds,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	From     int64    `json:"from,omitempty"` // Unix ms
	To       int64    `json:"to,omitempty"`
	MinScore float64  `json:"minScore,omitempty"`
	Threats  []string `json:"threats,omitempty"`
	Anomaly  *bool    `json:"anomaly,omitempty"`
	Q        string   `json:"q,omitempty"`
	Plate    string   `json:"plate,omitempty"`  // normalized prefix (wave16-plates)
	Person   string   `json:"person,omitempty"` // a personId (wave16-faces)
	Track    string   `json:"track,omitempty"`  // a trackId (wave16-tracking)
}

func csv(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// scopedCameraIDs intersects requested cameras with the caller's scope
// (scope.go). none reports that nothing can match: the caller sees no
// camera at all, or none of the requested ones.
func (s *Server) scopedCameraIDs(ctx context.Context, requested []string) (ids []string, none bool) {
	sc := s.cameraScope(ctx)
	if sc.all {
		return requested, false
	}
	if len(requested) == 0 {
		for id := range sc.ids {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	} else {
		for _, id := range requested {
			if sc.camera(id) {
				ids = append(ids, id)
			}
		}
	}
	return ids, len(ids) == 0
}

func msToSec(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms / 1000
}

func parseMs(w http.ResponseWriter, raw, name string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, name+" must be a Unix time in milliseconds")
		return 0, false
	}
	return n, true
}

func (s *Server) handleV2EventSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := v2SearchFilter{Cameras: csv(q.Get("cameras")), Kinds: csv(q.Get("kinds")), Labels: csv(q.Get("labels")),
		Threats: csv(q.Get("threats")), Q: strings.TrimSpace(q.Get("q"))}
	f.Person, f.Track = strings.TrimSpace(q.Get("person")), strings.TrimSpace(q.Get("track"))
	if raw := q.Get("plate"); raw != "" {
		if f.Plate = lpr.Normalize(raw); f.Plate == "" {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "plate must contain letters or digits")
			return
		}
	}
	var ok bool
	if f.From, ok = parseMs(w, q.Get("from"), "from"); !ok {
		return
	}
	if f.To, ok = parseMs(w, q.Get("to"), "to"); !ok {
		return
	}
	if raw := q.Get("minScore"); raw != "" {
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil || n < 0 || n > 1 {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "minScore must be a number from 0 to 1")
			return
		}
		f.MinScore = n
	}
	if raw := q.Get("anomaly"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "anomaly must be true or false")
			return
		}
		f.Anomaly = &b
	}
	limit := defaultPageLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageLimit {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "limit must be a number from 1 to 500")
			return
		}
		limit = n
	}
	s.writeSearchPage(w, r, f, limit, q.Get("before"), nil)
}

// writeSearchPage runs a search within the caller's scope and answers
// {items, nextCursor} (plus any extra fields).
func (s *Server) writeSearchPage(w http.ResponseWriter, r *http.Request, f v2SearchFilter, limit int, before string, extra map[string]any) {
	resp := map[string]any{}
	for k, v := range extra {
		resp[k] = v
	}
	ids, none := s.scopedCameraIDs(r.Context(), f.Cameras)
	items := []v2Event{}
	if !none {
		events, next, err := s.Store.SearchEvents(r.Context(), store.EventFilter{
			CameraIDs: ids, Kinds: f.Kinds, Labels: f.Labels, From: msToSec(f.From), To: msToSec(f.To),
			MinScore: f.MinScore, Threats: f.Threats, Anomaly: f.Anomaly, Text: f.Q, Limit: limit, Before: before,
			Plate: f.Plate, PersonID: f.Person, TrackID: f.Track,
		})
		if errors.Is(err, store.ErrBadCursor) {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "invalid cursor")
			return
		}
		if err != nil {
			writeV2Err(w, err)
			return
		}
		for i := range events {
			items = append(items, toV2Event(&events[i]))
		}
		if next != "" {
			resp["nextCursor"] = next
		}
	}
	resp["items"] = items
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleV2EventFacets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, ok := parseMs(w, q.Get("from"), "from")
	if !ok {
		return
	}
	to, ok := parseMs(w, q.Get("to"), "to")
	if !ok {
		return
	}
	facets := &store.EventFacets{Kinds: []store.FacetCount{}, Labels: []store.FacetCount{}, Threats: []store.FacetCount{}}
	if ids, none := s.scopedCameraIDs(r.Context(), csv(q.Get("cameras"))); !none {
		var err error
		if facets, err = s.Store.EventFacets(r.Context(), msToSec(from), msToSec(to), ids); err != nil {
			writeV2Err(w, err)
			return
		}
	}
	// aiEnabled tells the search view whether to offer the natural-language box.
	writeJSON(w, http.StatusOK, struct {
		*store.EventFacets
		AIEnabled bool `json:"aiEnabled"`
	}{facets, s.aiReady()})
}

// handleV2EventAsk translates a natural-language question into a filter with
// one model call (counted against the daily AI limit), then searches locally.
func (s *Server) handleV2EventAsk(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query string `json:"query"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "type what you're looking for")
		return
	}
	if !s.aiReady() {
		writeV2Error(w, http.StatusConflict, codeConflict, "AI is off — add your Anthropic API key and turn AI on in System → AI")
		return
	}
	if !s.aiTakeBudget() {
		writeV2Error(w, http.StatusTooManyRequests, codeTooMany, "the daily AI limit is reached")
		return
	}
	client, err := s.aiClient()
	if err != nil {
		writeV2Error(w, http.StatusConflict, codeConflict, err.Error())
		return
	}
	// Only cameras the caller may see are offered to the model.
	sc := s.cameraScope(r.Context())
	var refs []ai.CameraRef
	if cams, err := s.Store.Cameras(r.Context()); err == nil {
		for _, c := range cams {
			if sc.camera(c.ID) {
				refs = append(refs, ai.CameraRef{ID: c.ID, Name: c.Name})
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	spec, err := client.TranslateQuery(ctx, in.Query, time.Now(), refs)
	if err != nil {
		s.setAIError(err)
		writeV2Error(w, http.StatusBadGateway, codeUnavailable, err.Error())
		return
	}
	s.setAIError(nil)
	f := v2SearchFilter{Cameras: spec.CameraIDs, Kinds: spec.Kinds, Labels: spec.Labels, From: secToMs(spec.From), To: secToMs(spec.To),
		MinScore: spec.MinScore, Threats: spec.Threats, Anomaly: spec.Anomaly, Q: spec.Text}
	s.audit(r, "AI", "Searched events", in.Query)
	s.writeSearchPage(w, r, f, defaultPageLimit, "", map[string]any{"filter": f})
}

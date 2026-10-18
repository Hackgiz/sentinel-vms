package server

// Expiring share links, authed side (WAVE7-FEATURES.md, Stream D). Creating,
// listing and revoking need whatever gates evidence download (ExportEvidence)
// plus camera scope over the evidence item's camera. The public route that
// serves a link is share_public.go.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2Share(mux *http.ServeMux) {
	s.v2Route(mux, "POST /api/v2/evidence/{id}/share", auth.ExportEvidence, s.handleV2CreateShare)
	s.v2Route(mux, "GET /api/v2/share-links", auth.ExportEvidence, s.handleV2ListShares)
	s.v2Route(mux, "DELETE /api/v2/share-links/{id}", auth.ExportEvidence, s.handleV2RevokeShare)
	s.registerSharePublic(mux) // share_public.go: GET /share/{token}[/media], LAN origin only
}

const (
	maxShareNote   = 500
	maxShareUses   = 1000
	maxShareExpiry = 30 * 24 * time.Hour
)

var shareExpiryPresets = map[string]time.Duration{
	"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour,
}

type v2ShareLink struct {
	ID         string `json:"id"`
	EvidenceID string `json:"evidenceId"`
	CaseID     string `json:"caseId"`
	Title      string `json:"title"`
	Note       string `json:"note"`
	ExpiresAt  int64  `json:"expiresAt"` // Unix ms
	MaxUses    int    `json:"maxUses"`
	Uses       int    `json:"uses"`
	CreatedBy  string `json:"createdBy"`
	CreatedAt  int64  `json:"createdAt"`
	RevokedAt  int64  `json:"revokedAt"` // 0: not revoked
	Status     string `json:"status"`    // active, expired, revoked, used up
}

func toV2ShareLink(l *store.ShareLink, now time.Time) v2ShareLink {
	status := "active"
	switch {
	case l.RevokedAt != 0:
		status = "revoked"
	case l.ExpiresAt <= now.Unix():
		status = "expired"
	case l.MaxUses > 0 && l.Uses >= l.MaxUses:
		status = "used up"
	}
	ms := func(sec int64) int64 { return sec * 1000 }
	return v2ShareLink{ID: l.ID, EvidenceID: l.EvidenceID, CaseID: l.CaseID, Title: l.Title, Note: l.Note,
		ExpiresAt: ms(l.ExpiresAt), MaxUses: l.MaxUses, Uses: l.Uses, CreatedBy: l.CreatedBy,
		CreatedAt: ms(l.CreatedAt), RevokedAt: ms(l.RevokedAt), Status: status}
}

func (s *Server) handleV2CreateShare(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Expiry    string `json:"expiry"`    // "1h", "24h", "7d", or "custom"
		ExpiresAt int64  `json:"expiresAt"` // Unix ms, for "custom"
		MaxUses   int    `json:"maxUses"`
		Note      string `json:"note"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	ev, err := s.scopedEvidenceCase(ctx, r.PathValue("id"))
	if err != nil {
		writeV2Fail(w, err, "evidence")
		return
	}
	now := time.Now()
	var expires time.Time
	if d, ok := shareExpiryPresets[in.Expiry]; ok {
		expires = now.Add(d)
	} else if in.Expiry == "custom" {
		expires = time.UnixMilli(in.ExpiresAt)
	} else {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, `expiry must be "1h", "24h", "7d" or "custom"`)
		return
	}
	switch {
	case !expires.After(now):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "the expiry must be in the future")
		return
	case expires.After(now.Add(maxShareExpiry)):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "a link can last at most 30 days")
		return
	case in.MaxUses < 0 || in.MaxUses > maxShareUses:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "maxUses must be 0 (unlimited) to 1000")
		return
	}
	note := strings.TrimSpace(in.Note)
	if len(note) > maxShareNote {
		note = note[:maxShareNote]
	}
	l, token, err := s.Store.CreateShareLink(ctx, ev.ID, note, expires, in.MaxUses, currentUser(r).Name)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "Evidence", "Created share link", ev.CaseID+" link "+l.ID+", expires "+expires.UTC().Format(time.RFC3339)+", max uses "+strconv.Itoa(in.MaxUses))
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	// The token appears here once and is not stored: only its hash is.
	writeJSON(w, http.StatusCreated, map[string]any{
		"link":  toV2ShareLink(l, now),
		"token": token,
		"url":   scheme + "://" + r.Host + "/share/" + token,
	})
}

func (s *Server) handleV2ListShares(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ShareLinks(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	sc, now := s.cameraScope(r.Context()), time.Now()
	out := make([]v2ShareLink, 0, len(list))
	for i := range list {
		if sc.all || (list[i].CameraID != "" && sc.camera(list[i].CameraID)) {
			out = append(out, toV2ShareLink(&list[i], now))
		}
	}
	v2Items(w, out)
}

func (s *Server) handleV2RevokeShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	l, err := s.Store.ShareLinkByID(ctx, r.PathValue("id"))
	if err == nil && !s.cameraScope(ctx).all && (l.CameraID == "" || !s.cameraScope(ctx).camera(l.CameraID)) {
		err = store.ErrNotFound
	}
	if err != nil {
		writeV2Fail(w, err, "share link")
		return
	}
	if err := s.Store.RevokeShareLink(ctx, l.ID, time.Now()); err != nil {
		writeV2Fail(w, err, "share link")
		return
	}
	s.audit(r, "Evidence", "Revoked share link", l.CaseID+" link "+l.ID)
	w.WriteHeader(http.StatusNoContent)
}

package server

import (
	"context"
	"database/sql"
	"net/http"

	"sentinel-linux/internal/auth"
)

// Apple Home (HOMEKIT.md, "Server API + UI"). The HomeKit bridge lives in
// internal/homekit, which subscribes to this package's bus topics, so the
// server cannot import it; cmd/sentinel hands in an adapter that satisfies
// HomeKitBridge. The bridge exists only with --role all.

const (
	homekitSetting   = "homekit_enabled" // "1" = on; absent/"0" = off
	homekitAuditArea = "homekit"
)

// HomeKitStatus mirrors homekit.Status (same JSON).
type HomeKitStatus struct {
	Enabled   bool   `json:"enabled"`
	Paired    bool   `json:"paired"`
	SetupCode string `json:"setupCode,omitempty"`
	SetupURI  string `json:"setupURI,omitempty"`
	Cameras   int    `json:"cameras"`
	Err       string `json:"err,omitempty"`
}

// HomeKitBridge is what the API needs from the bridge.
type HomeKitBridge interface {
	Status() HomeKitStatus
	ResetPairings(ctx context.Context) error
}

func (s *Server) registerHomeKit(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/homekit", s.authed(auth.ChangeSettings, s.handleGetHomeKit))
	mux.Handle("PUT /api/v1/homekit", s.authed(auth.ChangeSettings, s.handlePutHomeKit))
	mux.Handle("POST /api/v1/homekit/reset", s.authed(auth.ChangeSettings, s.handleResetHomeKit))
	mux.Handle("GET /api/v1/homekit/cameras", s.authed(auth.ChangeSettings, s.handleHomeKitCameras))
}

// homekitBridge answers 404 {"unsupported":"multi-server"} when there is none.
func (s *Server) homekitBridge(w http.ResponseWriter) HomeKitBridge {
	if s.HomeKit == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"unsupported": "multi-server"})
		return nil
	}
	return s.HomeKit
}

func (s *Server) handleGetHomeKit(w http.ResponseWriter, r *http.Request) {
	if b := s.homekitBridge(w); b != nil {
		writeJSON(w, http.StatusOK, b.Status())
	}
}

func (s *Server) handlePutHomeKit(w http.ResponseWriter, r *http.Request) {
	b := s.homekitBridge(w)
	if b == nil {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	ctx := r.Context()
	was := s.Store.Setting(ctx, homekitSetting, "0") == "1"
	val := "0"
	if *in.Enabled {
		val = "1"
	}
	if err := s.Store.SetSetting(ctx, homekitSetting, val); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if was != *in.Enabled {
		s.audit(r, homekitAuditArea, "Turned Apple Home "+onOffWord(*in.Enabled), "")
	}
	// The bridge re-reads the setting on config changes; nudge it.
	if cams, err := s.Store.Cameras(ctx); err == nil {
		s.publishConfigChanged(cams)
	}
	writeJSON(w, http.StatusOK, b.Status())
}

func (s *Server) handleResetHomeKit(w http.ResponseWriter, r *http.Request) {
	b := s.homekitBridge(w)
	if b == nil {
		return
	}
	if err := b.ResetPairings(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, homekitAuditArea, "Reset Apple Home pairings", "new setup code issued")
	writeJSON(w, http.StatusOK, b.Status())
}

type homekitCamera struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	HomeKit bool   `json:"homekit"`
}

// handleHomeKitCameras lists every camera with its Apple Home flag.
func (s *Server) handleHomeKitCameras(w http.ResponseWriter, r *http.Request) {
	if s.homekitBridge(w) == nil {
		return
	}
	cams, err := s.Store.Cameras(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	flags, err := s.homekitFlags(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]homekitCamera, 0, len(cams))
	for _, c := range cams {
		on, ok := flags[c.ID]
		out = append(out, homekitCamera{ID: c.ID, Name: c.Name, HomeKit: on || !ok})
	}
	writeJSON(w, http.StatusOK, out)
}

// homekitFlags reads cameras.homekit (schema 4) by camera id.
func (s *Server) homekitFlags(ctx context.Context) (map[string]bool, error) {
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT id, homekit FROM cameras`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var v int
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v != 0
	}
	return out, rows.Err()
}

// setCameraHomeKit persists the per-camera Apple Home flag. Called from the
// camera save path before the configuration is re-applied, so the bridge
// hears about it through the usual ConfigChanged.
func (s *Server) setCameraHomeKit(ctx context.Context, id string, on bool) error {
	v := 0
	if on {
		v = 1
	}
	res, err := s.Store.DB.ExecContext(ctx, `UPDATE cameras SET homekit = ? WHERE id = ?`, v, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

package server

// API v2: ONVIF PTZ (pan/tilt/zoom and presets). The management server
// talks to the camera directly with the camera's stored ONVIF address and
// credentials. Routes need auth.ControlPTZ and respect camera scope
// (scope.go): a camera outside the caller's scope is a 404.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/store"
)

const (
	ptzProbeTTL   = 10 * time.Minute // a probe result is trusted this long
	ptzFailTTL    = 15 * time.Second // an unreachable camera is retried soon
	ptzCallBudget = 10 * time.Second
	maxPresetName = 64
)

const ptzRemoteMsg = "ptz is only available for cameras on the management server in this wave"

func (s *Server) registerV2PTZ(mux *http.ServeMux) {
	r := func(pattern string, h http.HandlerFunc) {
		s.v2Route(mux, pattern, auth.ControlPTZ, s.inScope("cameraId", h))
	}
	r("GET /api/v2/cameras/{cameraId}/ptz", s.handleV2PTZ)
	r("POST /api/v2/cameras/{cameraId}/ptz/move", s.handleV2PTZMove)
	r("POST /api/v2/cameras/{cameraId}/ptz/stop", s.handleV2PTZStop)
	r("POST /api/v2/cameras/{cameraId}/ptz/presets", s.handleV2PTZSetPreset)
	r("POST /api/v2/cameras/{cameraId}/ptz/presets/{token}/goto", s.handleV2PTZGoto)
	r("DELETE /api/v2/cameras/{cameraId}/ptz/presets/{token}", s.handleV2PTZRemovePreset)
}

// ptzCache remembers, per camera ID, whether the camera has PTZ and the
// open PTZ session. Saving the camera forgets it (saveCamera).
type ptzCache struct {
	mu sync.Mutex
	m  map[string]*ptzEntry
}

type ptzEntry struct {
	supported bool
	failed    bool // the probe could not reach the camera
	at        time.Time
	ptz       *onvif.PTZ // opened lazily
}

func (e *ptzEntry) fresh() bool {
	ttl := ptzProbeTTL
	if e.failed {
		ttl = ptzFailTTL
	}
	return time.Since(e.at) < ttl
}

func (c *ptzCache) get(id string) *ptzEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.m[id]; e != nil && e.fresh() {
		return e
	}
	return nil
}

func (c *ptzCache) put(id string, e *ptzEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*ptzEntry{}
	}
	c.m[id] = e
}

func (c *ptzCache) forget(id string) {
	c.mu.Lock()
	delete(c.m, id)
	c.mu.Unlock()
}

// ptzCamera is what the PTZ handlers work with after the common checks.
type ptzCamera struct {
	cam        *store.Camera
	user, pass string
}

// ptzTarget loads the camera and checks the request can be served here;
// it writes the error answer and returns nil otherwise.
func (s *Server) ptzTarget(w http.ResponseWriter, r *http.Request) *ptzCamera {
	cam, err := s.Store.Camera(r.Context(), r.PathValue("cameraId"))
	if err != nil {
		writeV2Err(w, err)
		return nil
	}
	// TODO(wave4): forward over control link
	if cam.RecorderID != "" && cam.RecorderID != store.LocalRecorderID {
		writeV2Error(w, http.StatusBadGateway, codeUnavailable, ptzRemoteMsg)
		return nil
	}
	pass, err := s.Store.CameraPassword(cam)
	if err != nil {
		slog.Error("camera password unreadable", "camera", cam.Name, "err", err)
		writeV2Error(w, http.StatusInternalServerError, codeInternal, "the camera's password could not be read")
		return nil
	}
	return &ptzCamera{cam: cam, user: cam.Username, pass: pass}
}

// probe reports whether the camera has PTZ, from the cache when it can.
func (s *Server) ptzProbe(ctx context.Context, t *ptzCamera) bool {
	if t.cam.ONVIFURL == "" {
		return false
	}
	if e := s.ptz.get(t.cam.ID); e != nil {
		return e.supported
	}
	ctx, cancel := context.WithTimeout(ctx, ptzCallBudget)
	defer cancel()
	ok, err := onvif.PTZSupported(ctx, t.cam.ONVIFURL, t.user, t.pass)
	if err != nil {
		slog.Debug("ptz probe failed", "camera", t.cam.Name, "err", err)
	}
	s.ptz.put(t.cam.ID, &ptzEntry{supported: ok, failed: err != nil, at: time.Now()})
	return ok
}

// ptzSession returns the open PTZ session, or writes an error answer.
func (s *Server) ptzSession(w http.ResponseWriter, ctx context.Context, t *ptzCamera) *onvif.PTZ {
	if !s.ptzProbe(ctx, t) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "this camera does not support PTZ")
		return nil
	}
	if e := s.ptz.get(t.cam.ID); e != nil && e.ptz != nil {
		return e.ptz
	}
	octx, cancel := context.WithTimeout(ctx, ptzCallBudget)
	defer cancel()
	p, err := onvif.OpenPTZ(octx, t.cam.ONVIFURL, t.user, t.pass)
	if err != nil {
		writePTZError(w, err)
		return nil
	}
	s.ptz.mu.Lock()
	if e := s.ptz.m[t.cam.ID]; e != nil {
		e.ptz = p
	}
	s.ptz.mu.Unlock()
	return p
}

// ptzDo runs one command on the camera; a failed command drops the session
// so the next request reopens it (the camera may have rebooted).
func (s *Server) ptzDo(w http.ResponseWriter, r *http.Request, t *ptzCamera, fn func(context.Context, *onvif.PTZ) error) bool {
	p := s.ptzSession(w, r.Context(), t)
	if p == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), ptzCallBudget)
	defer cancel()
	if err := fn(ctx, p); err != nil {
		s.ptz.forget(t.cam.ID)
		writePTZError(w, err)
		return false
	}
	return true
}

func writePTZError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, onvif.ErrNoPTZ):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "this camera does not support PTZ")
	case errors.Is(err, onvif.ErrAuth):
		writeV2Error(w, http.StatusBadGateway, codeUnavailable, "the camera rejected the stored username or password")
	default:
		writeV2Error(w, http.StatusBadGateway, codeUnavailable, "the camera did not accept the PTZ command: "+err.Error())
	}
}

type v2PTZState struct {
	Supported bool           `json:"supported"`
	Presets   []onvif.Preset `json:"presets"`
}

func (s *Server) handleV2PTZ(w http.ResponseWriter, r *http.Request) {
	t := s.ptzTarget(w, r)
	if t == nil {
		return
	}
	out := v2PTZState{Presets: []onvif.Preset{}}
	if s.ptzProbe(r.Context(), t) {
		out.Supported = true
		if p := s.ptzSession(w, r.Context(), t); p != nil {
			ctx, cancel := context.WithTimeout(r.Context(), ptzCallBudget)
			presets, err := p.GetPresets(ctx)
			cancel()
			if err != nil {
				s.ptz.forget(t.cam.ID)
				slog.Debug("ptz presets unavailable", "camera", t.cam.Name, "err", err)
			} else {
				out.Presets = presets
			}
		} else {
			return // ptzSession answered
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type v2PTZMove struct {
	Pan  float64 `json:"pan"`
	Tilt float64 `json:"tilt"`
	Zoom float64 `json:"zoom"`
}

func (s *Server) handleV2PTZMove(w http.ResponseWriter, r *http.Request) {
	var in v2PTZMove
	if !readV2JSON(w, r, &in) {
		return
	}
	t := s.ptzTarget(w, r)
	if t == nil {
		return
	}
	pan, tilt, zoom := onvif.ClampVelocity(in.Pan), onvif.ClampVelocity(in.Tilt), onvif.ClampVelocity(in.Zoom)
	if s.ptzDo(w, r, t, func(ctx context.Context, p *onvif.PTZ) error { return p.ContinuousMove(ctx, pan, tilt, zoom) }) {
		writeJSON(w, http.StatusOK, map[string]any{"pan": pan, "tilt": tilt, "zoom": zoom})
	}
}

func (s *Server) handleV2PTZStop(w http.ResponseWriter, r *http.Request) {
	t := s.ptzTarget(w, r)
	if t == nil {
		return
	}
	if s.ptzDo(w, r, t, func(ctx context.Context, p *onvif.PTZ) error { return p.Stop(ctx) }) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleV2PTZSetPreset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > maxPresetName {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "preset name must be 1 to 64 characters")
		return
	}
	t := s.ptzTarget(w, r)
	if t == nil {
		return
	}
	var preset onvif.Preset
	if !s.ptzDo(w, r, t, func(ctx context.Context, p *onvif.PTZ) (err error) { preset, err = p.SetPreset(ctx, name); return }) {
		return
	}
	s.audit(r, "Cameras", "Saved PTZ preset", t.cam.Name+": "+name)
	writeJSON(w, http.StatusCreated, preset)
}

func (s *Server) handleV2PTZGoto(w http.ResponseWriter, r *http.Request) {
	t := s.ptzTarget(w, r)
	if t == nil {
		return
	}
	token := r.PathValue("token")
	if s.ptzDo(w, r, t, func(ctx context.Context, p *onvif.PTZ) error { return p.GotoPreset(ctx, token) }) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleV2PTZRemovePreset(w http.ResponseWriter, r *http.Request) {
	t := s.ptzTarget(w, r)
	if t == nil {
		return
	}
	token := r.PathValue("token")
	if s.ptzDo(w, r, t, func(ctx context.Context, p *onvif.PTZ) error { return p.RemovePreset(ctx, token) }) {
		s.audit(r, "Cameras", "Removed PTZ preset", t.cam.Name+": "+token)
		w.WriteHeader(http.StatusNoContent)
	}
}

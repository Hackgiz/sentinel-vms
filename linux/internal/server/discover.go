package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"sentinel-linux/internal/onvif"
)

type discoveredView struct {
	onvif.Device
	Added bool `json:"added"` // a configured camera already uses this host
}

// handleDiscover scans the LAN: ONVIF WS-Discovery always, plus a TCP sweep
// of the local subnets when {"sweep": true}.
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Sweep bool `json:"sweep"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, &in) {
		return
	}
	if !s.scanMu.TryLock() {
		writeError(w, http.StatusConflict, "a network scan is already running")
		return
	}
	defer s.scanMu.Unlock()

	devices, subnets, warnings := onvif.Scan(r.Context(), in.Sweep)
	used := s.cameraHosts(r.Context())
	out := make([]discoveredView, 0, len(devices))
	for _, d := range devices {
		out = append(out, discoveredView{Device: d, Added: used[d.Host]})
	}
	detail := fmt.Sprintf("%d found", len(out))
	if in.Sweep {
		detail += " (with subnet sweep)"
	}
	s.audit(r, "Cameras", "Scanned network for cameras", detail)
	writeJSON(w, http.StatusOK, map[string]any{"devices": out, "subnets": subnets, "warnings": warnings})
}

func (s *Server) cameraHosts(ctx context.Context) map[string]bool {
	used := map[string]bool{}
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		return used
	}
	for _, c := range cams {
		for _, raw := range []string{c.RTSPURL, c.SubRTSPURL} {
			if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
				used[u.Hostname()] = true
			}
		}
	}
	return used
}

// handleDiscoverProbe signs in to one camera over ONVIF and returns its
// streams, with the recommended main (recording) and sub (viewing) picks.
func (s *Server) handleDiscoverProbe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Host       string `json:"host"`
		ServiceURL string `json:"serviceURL"`
		Username   string `json:"username"`
		Password   string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	host := strings.TrimSpace(in.Host)
	svc := strings.TrimSpace(in.ServiceURL)
	if err := onvif.ValidateProbeTarget(host, svc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	d, mainURL, subURL, err := onvif.Probe(r.Context(), host, svc, in.Username, in.Password)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, onvif.ErrAuth) {
			s.audit(r, "Cameras", "Camera sign-in failed during discovery", host)
			// 401 from this endpoint must not log the operator out of the dashboard.
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "authFailed": errors.Is(err, onvif.ErrAuth), "suggestedURL": onvif.SuggestedRTSPURL(host)})
		return
	}
	resp := map[string]any{"details": d}
	if mainURL != "" {
		resp["mainURL"] = mainURL
	}
	if subURL != "" {
		resp["subURL"] = subURL
	}
	writeJSON(w, http.StatusOK, resp)
}

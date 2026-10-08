package server

import (
	"net"
	"net/http"
	"strings"

	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/tunnel"
)

const remoteSetting = "remote_access"

// RemoteEnabled reports the saved preference. Default on when cloudflared is
// available, matching the Mac (pair once on Wi-Fi, keep working on cellular).
func (s *Server) RemoteEnabled() bool {
	return s.Store.Setting(s.Ctx, remoteSetting, "on") == "on"
}

func (s *Server) remoteStatus() tunnel.Status {
	if s.Tunnel == nil {
		return tunnel.Status{State: "off"}
	}
	return s.Tunnel.Status()
}

func (s *Server) handleGetRemote(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.remoteStatus())
}

func (s *Server) handlePutRemote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if s.Tunnel == nil || s.Tunnel.Binary == "" {
		writeError(w, http.StatusConflict, "remote access needs cloudflared, which isn't installed on this server")
		return
	}
	val := "off"
	if in.Enabled {
		val = "on"
		if err := s.Tunnel.Start(s.Ctx); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit(r, "Devices", "Turned on remote access", "Cloudflare quick tunnel")
	} else {
		s.Tunnel.Stop()
		s.audit(r, "Devices", "Turned off remote access", "")
	}
	if err := s.Store.SetSetting(r.Context(), remoteSetting, val); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.remoteStatus())
}

func (s *Server) localIPs() []string {
	var out []string
	for _, l := range onvif.LocalIPv4s() {
		out = append(out, l.IP.String())
	}
	return out
}

// splitHostPortLoose accepts "host" or "host:port".
func splitHostPortLoose(hostport string) (string, string, error) {
	if !strings.Contains(hostport, ":") || strings.HasSuffix(hostport, "]") {
		return strings.Trim(hostport, "[]"), "", nil
	}
	return net.SplitHostPort(hostport)
}

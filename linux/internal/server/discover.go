package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"sentinel-linux/internal/onvif"
)

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

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

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	found := map[string]discoveredView{}
	var order []string
	add := func(d onvif.Device) {
		if prev, ok := found[d.Host]; ok {
			// ONVIF identity wins; keep the sweep's port list.
			if len(prev.OpenPorts) == 0 {
				prev.OpenPorts = d.OpenPorts
			}
			if prev.ServiceURL == "" {
				prev.ServiceURL = d.ServiceURL
			}
			found[d.Host] = prev
			return
		}
		found[d.Host] = discoveredView{Device: d}
		order = append(order, d.Host)
	}

	devs, onvifErr := onvif.Discover(ctx, 3*time.Second)
	for _, d := range devs {
		add(d)
	}
	var subnets []string
	var sweepErr error
	if in.Sweep {
		var swept []onvif.Device
		swept, subnets, sweepErr = onvif.Sweep(ctx)
		for _, d := range swept {
			add(d)
		}
	}

	used := s.cameraHosts(r.Context())
	out := make([]discoveredView, 0, len(order))
	for _, h := range order {
		v := found[h]
		v.Added = used[h]
		out = append(out, v)
	}
	resp := map[string]any{"devices": out, "subnets": subnets}
	var warnings []string
	if onvifErr != nil {
		warnings = append(warnings, "ONVIF multicast: "+onvifErr.Error())
	}
	if sweepErr != nil {
		warnings = append(warnings, "Network sweep: "+sweepErr.Error())
	}
	resp["warnings"] = warnings
	detail := fmt.Sprintf("%d found", len(out))
	if in.Sweep {
		detail += " (with subnet sweep)"
	}
	s.audit(r, "Cameras", "Scanned network for cameras", detail)
	writeJSON(w, http.StatusOK, resp)
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
	if net.ParseIP(host) == nil && !hostnamePattern.MatchString(host) {
		writeError(w, http.StatusBadRequest, "enter the camera's IP address or hostname")
		return
	}
	svc := strings.TrimSpace(in.ServiceURL)
	if svc != "" {
		// Only talk to the camera the user picked — not an arbitrary URL.
		u, err := url.Parse(svc)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Hostname(), host) {
			writeError(w, http.StatusBadRequest, "ONVIF address must be http(s) on the same host")
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	var (
		d   *onvif.Details
		err error
	)
	if svc != "" {
		d, err = onvif.FetchDetails(ctx, svc, in.Username, in.Password)
		if err != nil && !errors.Is(err, onvif.ErrAuth) {
			// The advertised port may be wrong (NAT, multi-homed); try the usual ones.
			if d2, err2 := onvif.FetchByHost(ctx, host, in.Username, in.Password); err2 == nil {
				d, err = d2, nil
			}
		}
	} else {
		d, err = onvif.FetchByHost(ctx, host, in.Username, in.Password)
	}
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, onvif.ErrAuth) {
			status = http.StatusUnauthorized
			s.audit(r, "Cameras", "Camera sign-in failed during discovery", host)
		}
		// 401 from this endpoint must not log the operator out of the dashboard.
		if status == http.StatusUnauthorized {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "authFailed": errors.Is(err, onvif.ErrAuth), "suggestedURL": "rtsp://" + net.JoinHostPort(host, "554") + "/"})
		return
	}
	main, sub := onvif.ChooseStreams(d.Profiles)
	resp := map[string]any{"details": d}
	if main != nil {
		resp["mainURL"] = main.RTSPURL
	}
	if sub != nil {
		resp["subURL"] = sub.RTSPURL
	}
	writeJSON(w, http.StatusOK, resp)
}

package server

// API v2: camera discovery on a chosen recording server (Sentinel Admin's
// Cameras tab). The local server scans in this process; a remote one scans
// its own LAN over the control link (link.CmdDiscover / link.CmdProbe),
// since the cameras live on the recorder's network, not necessarily ours.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

// Generous command timeouts: a quick scan is ~3 s of multicast, a sweep or
// a probe with port fallbacks runs up to onvif.ScanTimeout on the recorder.
const (
	discoverQuickTimeout = 20 * time.Second
	discoverSweepTimeout = onvif.ScanTimeout + 10*time.Second
	probeCmdTimeout      = onvif.ProbeTimeout + 5*time.Second
)

func (s *Server) handleV2Discover(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Sweep bool `json:"sweep"`
	}
	if r.ContentLength != 0 && !readV2JSON(w, r, &in) {
		return
	}
	rs, err := s.Store.RecordingServer(r.Context(), r.PathValue("serverId"))
	if err != nil {
		writeV2Fail(w, err, "recording server")
		return
	}

	var (
		devices  []onvif.Device
		subnets  []string
		warnings []string
	)
	task := tasks.Default.Start(tasks.KindDiscovery, "Camera discovery on "+rs.Name)
	defer task.Finish()
	if rs.ID == store.LocalRecorderID {
		if !s.scanMu.TryLock() {
			task.Fail(errors.New("a network scan is already running"))
			writeV2Error(w, http.StatusConflict, codeConflict, "a network scan is already running")
			return
		}
		devices, subnets, warnings = onvif.Scan(r.Context(), in.Sweep)
		s.scanMu.Unlock()
	} else {
		timeout := discoverQuickTimeout
		if in.Sweep {
			timeout = discoverSweepTimeout
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		var res link.DiscoverResult
		if err := s.command(ctx, rs.ID, link.CmdDiscover, link.DiscoverArgs{Sweep: in.Sweep}, &res); err != nil {
			task.Fail(err)
			writeDiscoverCmdErr(w, err)
			return
		}
		if len(res.Devices) > 0 {
			if err := json.Unmarshal(res.Devices, &devices); err != nil {
				task.Fail(err)
				writeV2Error(w, http.StatusBadGateway, codeUnavailable, "the recording server sent an unreadable scan result")
				return
			}
		}
		subnets, warnings = res.Subnets, res.Warnings
	}

	used := s.cameraHosts(r.Context())
	out := make([]discoveredView, 0, len(devices))
	for _, d := range devices {
		out = append(out, discoveredView{Device: d, Added: used[d.Host]})
	}
	detail := fmt.Sprintf("%d found on %s", len(out), rs.Name)
	if in.Sweep {
		detail += " (with subnet sweep)"
	}
	s.audit(r, "Cameras", "Scanned network for cameras", detail)
	writeJSON(w, http.StatusOK, map[string]any{"devices": out, "subnets": subnets, "warnings": warnings})
}

func (s *Server) handleV2DiscoverProbe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Host       string `json:"host"`
		ServiceURL string `json:"serviceURL"`
		Username   string `json:"username"`
		Password   string `json:"password"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	host := strings.TrimSpace(in.Host)
	svc := strings.TrimSpace(in.ServiceURL)
	if err := onvif.ValidateProbeTarget(host, svc); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	rs, err := s.Store.RecordingServer(r.Context(), r.PathValue("serverId"))
	if err != nil {
		writeV2Fail(w, err, "recording server")
		return
	}

	var out link.ProbeResult
	if rs.ID == store.LocalRecorderID {
		d, mainURL, subURL, err := onvif.Probe(r.Context(), host, svc, in.Username, in.Password)
		if err != nil {
			if errors.Is(err, onvif.ErrAuth) {
				s.audit(r, "Cameras", "Camera sign-in failed during discovery", host)
			}
			out = link.ProbeResult{Error: err.Error(), AuthFailed: errors.Is(err, onvif.ErrAuth), SuggestedURL: onvif.SuggestedRTSPURL(host)}
		} else {
			raw, _ := json.Marshal(d)
			out = link.ProbeResult{Details: raw, MainURL: mainURL, SubURL: subURL}
		}
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), probeCmdTimeout)
		defer cancel()
		if err := s.command(ctx, rs.ID, link.CmdProbe, link.ProbeArgs{Host: host, ServiceURL: svc, Username: in.Username, Password: in.Password}, &out); err != nil {
			writeDiscoverCmdErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// writeDiscoverCmdErr reports a control-link failure in words an admin can
// act on; anything else keeps writeV2Err's status mapping (busy → 429 …).
func writeDiscoverCmdErr(w http.ResponseWriter, err error) {
	var ce *link.CommandError
	switch {
	case errors.Is(err, link.ErrNoRecorder):
		writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "the recording server isn't connected")
	case errors.As(err, &ce) && ce.Code == link.CodeUnknownCommand:
		writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "the recording server is too old to scan its network — update it")
	default:
		writeV2Err(w, err)
	}
}

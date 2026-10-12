package server

// Low-latency live view over WebRTC (WHEP, RFC 9725).
//
// MediaMTX's WHEP endpoint listens on 127.0.0.1 only. The browser talks to
// Sentinel instead, behind the same cookie session and ViewLive permission as
// HLS:
//
//	POST   /webrtc/{camera}/whep            SDP offer → SDP answer + Location
//	PATCH  /webrtc/{camera}/whep/{session}  trickle ICE candidates
//	DELETE /webrtc/{camera}/whep/{session}  end the session
//
// Nothing else on MediaMTX's listener (WHIP publishing, its built-in player
// page, other paths) is reachable. These routes live outside /api/ because
// WHEP bodies are application/sdp, not JSON, so they do their own CSRF
// checks: an exact Content-Type (a cross-site form can't send it without a
// CORS preflight we never answer) and a same-origin Origin/Sec-Fetch-Site.
//
// After those checks the request goes to the recording server's video plane,
// which proxies it to MediaMTX (internal/recorder/live.go). Media itself flows
// over MediaMTX's ICE port (UDP, TCP fallback), which browsers reach
// directly; see media.WebRTCConfig.

import (
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/media"
)

const (
	whepRateWindow    = time.Minute
	whepRatePerWindow = 60 // per user: a 16-camera wall reconnecting a few times
	webrtcHostsKey    = "webrtc_additional_hosts"
	maxWebRTCHosts    = 8
)

var (
	// MediaMTX session IDs are UUIDs.
	whepSessionPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type webrtcState struct {
	mu     sync.Mutex
	recent map[string][]time.Time // user ID → session creations in the window
}

func (s *Server) registerWebRTC(mux *http.ServeMux) {
	mux.Handle("POST /webrtc/{id}/whep", s.authed(auth.ViewLive, s.inScope("id", s.handleWHEPOffer)))
	mux.Handle("PATCH /webrtc/{id}/whep/{session}", s.authed(auth.ViewLive, s.inScope("id", s.handleWHEPSession)))
	mux.Handle("DELETE /webrtc/{id}/whep/{session}", s.authed(auth.ViewLive, s.inScope("id", s.handleWHEPSession)))
	// Anything else under /webrtc/ (GET, OPTIONS, WHIP, other paths) is refused
	// rather than falling through to the dashboard.
	mux.HandleFunc("/webrtc/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST, PATCH, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "not supported")
	})

	mux.Handle("GET /api/v1/webrtc", s.authed(auth.ViewLive, s.handleGetWebRTC))
	mux.Handle("PUT /api/v1/webrtc", s.authed(auth.ChangeSettings, s.handlePutWebRTC))
}

func splitHosts(v string) []string {
	out := []string{}
	for _, h := range strings.Split(v, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// sameOrigin is the WHEP routes' CSRF check (they bypass csrfGuard, which
// insists on JSON). Browsers always send Origin on POST/PATCH/DELETE.
func sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	return true
}

func mediaTypeIs(r *http.Request, want string) bool {
	ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(ct), want)
}

// whepPreflight runs the checks shared by all WHEP routes and returns the
// camera's recording server, or "" after writing an error.
func (s *Server) whepPreflight(w http.ResponseWriter, r *http.Request) string {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-site request refused")
		return ""
	}
	id := r.PathValue("id")
	if !cameraIDPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "camera not found")
		return ""
	}
	c, err := s.Store.Camera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return ""
	}
	if st, _ := s.statusOf(c.RecorderID); !st.Capabilities.WebRTC.Enabled {
		writeError(w, http.StatusServiceUnavailable, "WebRTC is turned off on this server")
		return ""
	}
	return c.RecorderID
}

// allowSession enforces the per-user session-creation rate.
func (s *Server) allowSession(userID string) bool {
	st := &s.webrtc
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.recent == nil {
		st.recent = map[string][]time.Time{}
	}
	now := time.Now()
	kept := st.recent[userID][:0]
	for _, t := range st.recent[userID] {
		if now.Sub(t) < whepRateWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= whepRatePerWindow {
		st.recent[userID] = kept
		return false
	}
	st.recent[userID] = append(kept, now)
	if len(st.recent) > 1000 { // drop idle users
		for k, v := range st.recent {
			if len(v) == 0 || now.Sub(v[len(v)-1]) >= whepRateWindow {
				delete(st.recent, k)
			}
		}
	}
	return true
}

func (s *Server) handleWHEPOffer(w http.ResponseWriter, r *http.Request) {
	if !mediaTypeIs(r, "application/sdp") {
		writeError(w, http.StatusUnsupportedMediaType, "send an SDP offer (application/sdp)")
		return
	}
	rid := s.whepPreflight(w, r)
	if rid == "" {
		return
	}
	if !s.allowSession(currentUser(r).ID) {
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, "too many live sessions started — wait a moment")
		return
	}
	s.serveVideo(w, r, rid) // the recording server proxies it to MediaMTX
}

func (s *Server) handleWHEPSession(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPatch && !mediaTypeIs(r, "application/trickle-ice-sdpfrag") {
		writeError(w, http.StatusUnsupportedMediaType, "send application/trickle-ice-sdpfrag")
		return
	}
	rid := s.whepPreflight(w, r)
	if rid == "" {
		return
	}
	session := r.PathValue("session")
	if !whepSessionPattern.MatchString(session) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	s.serveVideo(w, r, rid)
}

// MARK: - Settings

func (s *Server) handleGetWebRTC(w http.ResponseWriter, r *http.Request) {
	rid, _ := s.defaultRecorderID(r.Context()) // v1's page shows one server
	st, _ := s.statusOf(rid)
	rtc := st.Capabilities.WebRTC
	resp := map[string]any{
		"enabled":         rtc.Enabled,
		"port":            rtc.ICEPort,
		"additionalHosts": splitHosts(s.Store.Setting(r.Context(), webrtcHostsKey, "")),
		"configHosts":     []string{},
	}
	if rtc.ConfigHosts != nil {
		resp["configHosts"] = rtc.ConfigHosts
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePutWebRTC(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AdditionalHosts []string `json:"additionalHosts"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	hosts := []string{}
	for _, h := range in.AdditionalHosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if !media.ValidICEHost(h) {
			writeError(w, http.StatusBadRequest, "not an IP address or host name: "+h)
			return
		}
		hosts = append(hosts, h)
	}
	if len(hosts) > maxWebRTCHosts {
		writeError(w, http.StatusBadRequest, "at most "+strconv.Itoa(maxWebRTCHosts)+" addresses")
		return
	}
	old := s.Store.Setting(r.Context(), webrtcHostsKey, "")
	joined := strings.Join(hosts, ",")
	if err := s.Store.SetSetting(r.Context(), webrtcHostsKey, joined); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if old != joined {
		s.audit(r, "Settings", "Changed WebRTC addresses", "["+old+"] → ["+joined+"]")
	}
	if err := s.ApplyMedia(r.Context()); err != nil {
		slog.Error("media apply failed", "err", err)
	}
	s.handleGetWebRTC(w, r)
}

package server

// The internet-facing origin (WAVE12-CLOUD.md, Stream C). Sentinel Cloud's
// relay and the quick tunnel both forward to a loopback listener serving
// RemoteHandler: what clients need from outside the site, and nothing else.

import (
	"net"
	"net/http"
	"strings"
)

// RemoteHandler serves Client API v2 (with the event-stream WebSocket),
// the /v/ video gateway, the iPhone companion API, /share/ links and
// /health. Everything else, the dashboard and /api/v1 included, is a 404.
// It runs behind a proxy on a loopback listener, so the client address
// comes from the proxy's forwarding headers.
func (s *Server) RemoteHandler() http.Handler {
	mux := http.NewServeMux()
	s.registerV2(mux)        // v2 API, /v/ gateway (v2_video.go), /share/ (share.go)
	s.registerCompanion(mux) // phone API and GET /health
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return forwardedClient(s.securityHeaders(s.csrfGuard(mux)))
}

// forwardedClient takes the client address and the public host from the
// local proxy in front of this origin. RemoteAddr becomes the client the
// proxy saw, so sign-in lockouts, rate limits and the audit trail see each
// client instead of one loopback address (Cloudflare's tunnel sends
// CF-Connecting-IP; the Sentinel Cloud relay sets X-Forwarded-For from it).
// Host becomes X-Forwarded-Host, the host the client asked for, so links
// built from the request (share links) and the same-origin check use the
// public address.
func forwardedClient(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := proxiedClientIP(r)
		host := proxiedHost(r)
		if ip != "" || host != "" {
			r2 := r.Clone(r.Context())
			if ip != "" {
				r2.RemoteAddr = net.JoinHostPort(ip, "0")
			}
			if host != "" {
				r2.Host = host
			}
			r = r2
		}
		next.ServeHTTP(w, r)
	})
}

func fromLocalProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host).IsLoopback()
}

// proxiedHost is X-Forwarded-Host from a local proxy when it is a plain
// host[:port], else "".
func proxiedHost(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if h == "" || len(h) > 255 || !fromLocalProxy(r) {
		return ""
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == ':' || c == '[' || c == ']') {
			return ""
		}
	}
	return h
}

func proxiedClientIP(r *http.Request) string {
	if !fromLocalProxy(r) {
		return "" // only a local proxy is trusted
	}
	if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); ip != nil {
		return ip.String()
	}
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return ""
	}
	parts := strings.Split(xff[len(xff)-1], ",")
	if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
		return ip.String()
	}
	return ""
}

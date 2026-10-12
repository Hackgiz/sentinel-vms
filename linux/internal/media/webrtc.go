package media

import (
	"fmt"
	"log/slog"
	"strings"
)

// WebRTC settings for low-latency live view (WHEP).
//
// Signaling (the WHEP HTTP endpoint) listens on 127.0.0.1 only and is reached
// through Sentinel's authenticated proxy, like HLS. Media can't be proxied
// over HTTP, so MediaMTX also listens on ICEPort (UDP, plus TCP as a fallback
// for networks that drop UDP) on all interfaces. That port only carries
// DTLS-SRTP for sessions Sentinel signed in: ICE checks need the per-session
// credentials from the SDP exchange.
//
// No STUN/TURN servers are configured: browsers and the server are expected
// to be on the same network (or a VPN), and the product makes no calls to
// third-party services. Host candidates come from the server's interfaces
// plus AdditionalHosts (for Docker/NAT, where the interface IPs aren't the
// ones browsers can reach).
type WebRTCConfig struct {
	// ICEPort is the UDP and TCP port for media; 0 disables WebRTC.
	// Not 8189 (MediaMTX's default) so it can't clash with the Mac app.
	ICEPort int
	// SignalPort is MediaMTX's loopback-only WHEP HTTP listener.
	SignalPort int
	// Hosts are extra IPs/hostnames advertised as ICE candidates (from the
	// command line / environment).
	Hosts []string
}

const (
	DefaultWebRTCICEPort    = 18189
	DefaultWebRTCSignalPort = 18889
)

// SetWebRTCExtraHosts sets the admin-configured extra ICE hosts (the
// dashboard setting); they are merged with WebRTC.Hosts on the next Apply.
func (s *Supervisor) SetWebRTCExtraHosts(hosts []string) {
	s.mu.Lock()
	s.webrtcExtraHosts = append([]string(nil), hosts...)
	s.mu.Unlock()
}

// WebRTCEnabled reports whether MediaMTX is configured to serve WHEP.
func (s *Supervisor) WebRTCEnabled() bool {
	return s.WebRTC.ICEPort > 0 && s.webrtcSignalPort() > 0
}

func (s *Supervisor) webrtcSignalPort() int {
	if s.WebRTC.SignalPort > 0 {
		return s.WebRTC.SignalPort
	}
	return DefaultWebRTCSignalPort
}

// WHEPUpstream is the loopback WHEP URL for a camera's live path (sub-stream
// when there is one). session is "" for the POST that creates a session, or
// the session ID for PATCH/DELETE.
func (s *Supervisor) WHEPUpstream(cameraID, session string) string {
	u := fmt.Sprintf("http://127.0.0.1:%d/%s/whep", s.webrtcSignalPort(), s.LivePath(cameraID))
	if session != "" {
		u += "/" + session
	}
	return u
}

// webrtcHosts merges the configured hosts, dropping duplicates and anything
// that can't be safely written into the YAML config.
func (s *Supervisor) webrtcHosts() []string {
	s.mu.Lock()
	extra := s.webrtcExtraHosts
	s.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, h := range append(append([]string(nil), s.WebRTC.Hosts...), extra...) {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			continue
		}
		if !ValidICEHost(h) {
			slog.Warn("ignoring invalid WebRTC host", "host", h)
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// ValidICEHost accepts an IP address or a DNS name (letters, digits, - and .).
func ValidICEHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == ':':
		default:
			return false
		}
	}
	return true
}

// webrtcYAML renders the top-level webrtc settings.
func (s *Supervisor) webrtcYAML() string {
	if !s.WebRTCEnabled() {
		return "webrtc: no\n"
	}
	hosts := s.webrtcHosts()
	quoted := make([]string, 0, len(hosts))
	for _, h := range hosts {
		q, _ := yamlQuote(h) // ValidICEHost already excludes line breaks
		quoted = append(quoted, q)
	}
	return fmt.Sprintf(`# WebRTC (WHEP) live view. Signaling is loopback-only behind Sentinel's
# authenticated proxy; only the ICE media port is reachable from the network.
webrtc: yes
webrtcAddress: 127.0.0.1:%d
webrtcEncryption: no
webrtcLocalUDPAddress: :%d
webrtcLocalTCPAddress: :%d
webrtcIPsFromInterfaces: yes
webrtcIPsFromInterfacesList: []
webrtcAdditionalHosts: [%s]
# No STUN/TURN: no calls to third-party servers (LAN/VPN use).
webrtcICEServers2: []

`, s.webrtcSignalPort(), s.WebRTC.ICEPort, s.WebRTC.ICEPort, strings.Join(quoted, ", "))
}

package media

import (
	"strings"
	"testing"
)

func TestRenderWebRTCEnabled(t *testing.T) {
	s := &Supervisor{RecordingsRoot: "/r", Ports: DefaultPorts, DefaultRetain: 7,
		WebRTC: WebRTCConfig{ICEPort: 18189, Hosts: []string{"192.168.1.10", " nvr.lan ", "192.168.1.10"}}}
	s.SetWebRTCExtraHosts([]string{"10.0.0.5", "bad host'\n- x", ""})
	cfg, err := s.render([]Source{{ID: "CAM-A", URL: "rtsp://10.0.0.2/main"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\nwebrtc: yes\n",
		"webrtcAddress: 127.0.0.1:18889\n", // signaling stays on loopback
		"webrtcEncryption: no\n",
		"webrtcLocalUDPAddress: :18189\n",
		"webrtcLocalTCPAddress: :18189\n", // TCP fallback when UDP is blocked
		"webrtcIPsFromInterfaces: yes\n",
		"webrtcAdditionalHosts: ['192.168.1.10', 'nvr.lan', '10.0.0.5']\n",
		"webrtcICEServers2: []\n", // no STUN to third parties
		"hlsAddress: 127.0.0.1:",  // HLS untouched
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config missing %q", want)
		}
	}
	if strings.Count(cfg, "\nwebrtc:") != 1 {
		t.Error("webrtc key must appear exactly once")
	}
	for _, bad := range []string{"stun:", "google", "bad host", "webrtcAddress: :"} {
		if strings.Contains(cfg, bad) {
			t.Errorf("config contains %q", bad)
		}
	}
	if got := s.WHEPUpstream("CAM-A", ""); got != "http://127.0.0.1:18889/CAM-A/whep" {
		t.Errorf("upstream %q", got)
	}
}

func TestRenderWebRTCDisabled(t *testing.T) {
	s := &Supervisor{RecordingsRoot: "/r", Ports: DefaultPorts}
	cfg, err := s.render(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg, "webrtc: no\n") || strings.Contains(cfg, "webrtcLocalUDPAddress") || strings.Count(cfg, "\nwebrtc:")+1 != 1 {
		t.Fatalf("disabled WebRTC must render exactly 'webrtc: no':\n%s", cfg)
	}
	if s.WebRTCEnabled() {
		t.Fatal("port 0 must disable WebRTC")
	}
}

func TestValidICEHost(t *testing.T) {
	for _, h := range []string{"192.168.1.2", "fe80::1", "nvr.example.com", "host-1"} {
		if !ValidICEHost(h) {
			t.Errorf("%q should be valid", h)
		}
	}
	for _, h := range []string{"", "a b", "x'y", "a\nb", "http://x", strings.Repeat("a", 254)} {
		if ValidICEHost(h) {
			t.Errorf("%q should be invalid", h)
		}
	}
}

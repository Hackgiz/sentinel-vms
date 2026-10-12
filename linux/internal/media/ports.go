package media

import (
	"fmt"
	"strconv"
	"strings"
)

// ParsePorts reads "rtsp,hls,api[,webrtc-signal]" (SENTINEL_MEDIAMTX_PORTS),
// MediaMTX's loopback listeners. Use it when the defaults (18554, 18888,
// 19997, 18889) are taken on this machine — e.g. by a VM's port forwards or
// a second Sentinel. signal is 0 when not given (keep the default).
func ParsePorts(s string) (p Ports, signal int, err error) {
	f := strings.Split(s, ",")
	if len(f) != 3 && len(f) != 4 {
		return Ports{}, 0, fmt.Errorf("want rtsp,hls,api[,webrtc-signal] ports, got %q", s)
	}
	n := make([]int, len(f))
	seen := map[int]bool{}
	for i, v := range f {
		port, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || port < 1 || port > 65535 {
			return Ports{}, 0, fmt.Errorf("bad port %q in %q", strings.TrimSpace(v), s)
		}
		if seen[port] {
			return Ports{}, 0, fmt.Errorf("port %d used twice in %q", port, s)
		}
		seen[port] = true
		n[i] = port
	}
	p = Ports{RTSP: n[0], HLS: n[1], API: n[2]}
	if len(n) == 4 {
		signal = n[3]
	}
	return p, signal, nil
}

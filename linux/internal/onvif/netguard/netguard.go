// Package netguard is for tests: it confines every connection the onvif
// package makes (SOAP over HTTP, RTSP backchannel and replay, the port
// sweep) to loopback, so fixtures whose cameras sit at LAN-looking
// addresses (10.0.0.5 …) never reach real devices on the developer's
// network.
package netguard

import (
	"context"
	"fmt"
	"net"

	"sentinel-linux/internal/onvif"
)

// LoopbackOnly installs a dialer that refuses every non-loopback address.
func LoopbackOnly() {
	var d net.Dialer
	onvif.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, fmt.Errorf("netguard: test refused to dial %s (not loopback)", addr)
		}
		return d.DialContext(ctx, network, addr)
	}
}

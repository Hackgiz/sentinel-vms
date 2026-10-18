package onvif

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Dial opens every TCP connection this package makes to a camera: SOAP
// over HTTP, RTSP (backchannel, replay relay) and the port sweep. Tests
// replace it (see package netguard) so fixtures with LAN-looking
// addresses never reach real devices on the developer's network.
var Dial func(ctx context.Context, network, addr string) (net.Conn, error) = (&net.Dialer{}).DialContext

// soapTransport is shared by every SOAP client so connections pool the way
// they did on http.DefaultTransport; its dials go through Dial.
var soapTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return Dial(ctx, network, addr)
	}
	return t
}()

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: soapTransport}
}

func dialTimeout(network, addr string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return Dial(ctx, network, addr)
}

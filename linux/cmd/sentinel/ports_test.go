package main

import (
	"testing"

	"sentinel-linux/internal/media"
)

func TestMediaPortsFromEnv(t *testing.T) {
	t.Setenv("SENTINEL_MEDIAMTX_PORTS", "")
	if p, sig := mediaPortsFromEnv(); p != media.DefaultPorts || sig != 0 {
		t.Fatalf("default %+v %d", p, sig)
	}
	t.Setenv("SENTINEL_MEDIAMTX_PORTS", "28554,28888,29997,28889")
	if p, sig := mediaPortsFromEnv(); p != (media.Ports{RTSP: 28554, HLS: 28888, API: 29997}) || sig != 28889 {
		t.Fatalf("env %+v %d", p, sig)
	}
	t.Setenv("SENTINEL_MEDIAMTX_PORTS", "nonsense")
	if p := mediaPorts(); p != media.DefaultPorts {
		t.Fatalf("bad value must fall back to defaults: %+v", p)
	}
	t.Setenv("SENTINEL_MEDIAMTX_PORTS", "28554,28888,29997")
	webrtcPort = 18189
	if c := webrtcConfig(); c.SignalPort != 0 || c.ICEPort != 18189 {
		t.Fatalf("webrtc %+v", c)
	}
}

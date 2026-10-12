package main

import (
	"log/slog"
	"os"
	"strings"

	"sentinel-linux/internal/media"
)

// mediaPorts are MediaMTX's loopback ports: the defaults, or
// SENTINEL_MEDIAMTX_PORTS=rtsp,hls,api[,webrtc-signal] when those are taken
// (e.g. a Lima/Docker VM forwarding 18554/18888/19997 on a Mac).
func mediaPorts() media.Ports {
	p, _ := mediaPortsFromEnv()
	return p
}

// mediaSignalPort is the WebRTC (WHEP) signaling port from
// SENTINEL_MEDIAMTX_PORTS, or 0 for the default.
func mediaSignalPort() int {
	_, sig := mediaPortsFromEnv()
	return sig
}

func mediaPortsFromEnv() (media.Ports, int) {
	v := strings.TrimSpace(os.Getenv("SENTINEL_MEDIAMTX_PORTS"))
	if v == "" {
		return media.DefaultPorts, 0
	}
	p, sig, err := media.ParsePorts(v)
	if err != nil {
		slog.Warn("ignoring SENTINEL_MEDIAMTX_PORTS", "err", err)
		return media.DefaultPorts, 0
	}
	return p, sig
}

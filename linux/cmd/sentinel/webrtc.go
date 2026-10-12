package main

import (
	"log/slog"
	"os"
	"strconv"
	"strings"

	"sentinel-linux/internal/media"
)

// Set from -webrtc-port / -webrtc-hosts (or SENTINEL_WEBRTC_PORT / _HOSTS).
var (
	webrtcPort  int
	webrtcHosts string
)

func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		slog.Warn("ignoring non-numeric environment value", "key", key, "value", v)
	}
	return fallback
}

func webrtcConfig() media.WebRTCConfig {
	port := webrtcPort
	if port < 0 || port > 65535 {
		slog.Warn("WebRTC port out of range; WebRTC off", "port", port)
		port = 0
	}
	cfg := media.WebRTCConfig{ICEPort: port, SignalPort: mediaSignalPort()}
	for _, h := range strings.Split(webrtcHosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			cfg.Hosts = append(cfg.Hosts, h)
		}
	}
	if port == 0 {
		slog.Info("WebRTC live view off; browsers use HLS")
	}
	return cfg
}

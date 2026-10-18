package server

import "sentinel-linux/internal/onvif/netguard"

// Camera fixtures here use LAN-looking RTSP addresses; listing one with an
// ONVIF address starts a background talkback probe, which must never reach
// a real device on the developer's network.
func init() { netguard.LoopbackOnly() }

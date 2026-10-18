package homekit

import "sentinel-linux/internal/onvif/netguard"

// Camera fixtures use LAN-looking addresses; ONVIF and RTSP calls made
// while testing must never reach a real device on the network.
func init() { netguard.LoopbackOnly() }

package talkback_test

import "sentinel-linux/internal/onvif/netguard"

// Session tests talk to fakecam on loopback only.
func init() { netguard.LoopbackOnly() }

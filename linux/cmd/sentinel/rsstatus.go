package main

// WAVE9(D): a recording server's unauthenticated health report.

import "sentinel-linux/internal/link"

// recordingStatus is what a recording server's GET /api/v1/status shows
// anyone who can reach its HTTP port: health and counts, never camera ids
// or per-camera detail (cameras are only named to signed-in users of the
// management server, within their scope).
func recordingStatus(st link.ServerStatus, pending int) map[string]any {
	ready := 0
	for _, c := range st.Cameras {
		if c.Ready {
			ready++
		}
	}
	return map[string]any{
		"role":          roleRecording,
		"version":       st.Version,
		"configRev":     st.ConfigRev,
		"cameras":       len(st.Cameras),
		"camerasReady":  ready,
		"mediaRunning":  st.Media.Running,
		"pendingEvents": pending,
	}
}

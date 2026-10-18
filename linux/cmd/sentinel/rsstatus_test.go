package main

import (
	"encoding/json"
	"strings"
	"testing"

	"sentinel-linux/internal/link"
)

// WAVE9(D): the recording server's open health report names no cameras.
func TestRecordingStatusNamesNoCameras(t *testing.T) {
	st := link.ServerStatus{Version: "9", ConfigRev: 4, Media: link.MediaStatus{Running: true, Error: "x"},
		Cameras: map[string]link.CameraStatus{
			"CAM-AAAA": {Ready: true, Width: 640, Audio: "Listening"},
			"CAM-BBBB": {Ready: false},
		}}
	out := recordingStatus(st, 3)
	raw, _ := json.Marshal(out)
	for _, leak := range []string{"CAM-AAAA", "CAM-BBBB", "Listening", "640"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("status leaks %q: %s", leak, raw)
		}
	}
	if out["cameras"] != 2 || out["camerasReady"] != 1 || out["pendingEvents"] != 3 || out["configRev"] != int64(4) {
		t.Fatalf("counts: %s", raw)
	}
}

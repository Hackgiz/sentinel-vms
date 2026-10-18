package recorder

import (
	"context"
	"testing"
	"time"

	"sentinel-linux/internal/audiodetect"
	"sentinel-linux/internal/link"
)

func TestAudioSourcesOnlyForEnabledCameras(t *testing.T) {
	r, _ := newTestRecorder(t)
	cams := []link.CameraConfig{
		{ID: "A", AudioDetect: 0},
		{ID: "B", AudioDetect: 30},
	}
	got := r.audioSources(cams)
	if len(got) != 1 || got[0].CameraID != "B" || got[0].ThresholdDB != 30 || got[0].URL == "" {
		t.Fatalf("sources = %+v", got)
	}
}

func TestOnAudioRecordsAnAudioDetectionEvent(t *testing.T) {
	r, sink := newTestRecorder(t)
	// Camera without audio detection on: ignored.
	r.OnAudio(audiodetect.Event{CameraID: camID, Kind: "audio", Label: "Loud noise", At: time.Now()})
	if n := len(sink.durable()); n != 0 {
		t.Fatalf("event for a camera with audio off: %d", n)
	}
	if _, err := r.ApplyConfig(context.Background(), link.Snapshot{Rev: 2, Cameras: []link.CameraConfig{
		{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", MotionLevel: 2, AudioDetect: 25},
	}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.OnAudio(audiodetect.Event{CameraID: camID, Kind: "audio", Label: "Glass break", Score: 0.9, At: now})
	r.OnAudio(audiodetect.Event{CameraID: "unknown", Kind: "audio", Label: "Loud noise", At: now})
	got := sink.durable()
	if len(got) != 1 {
		t.Fatalf("delivered %+v", got)
	}
	d := got[0].Detection
	if d == nil || d.Source != link.SourceAudio || d.Kind != "audio" || d.Label != "Glass break" || d.Score != 0.9 {
		t.Fatalf("detection %+v", d)
	}
}

func TestAudioStatusLine(t *testing.T) {
	r, _ := newTestRecorder(t)
	if r.audioStatus(camID) != "" {
		t.Fatal("status without a manager")
	}
	r.Audio = &audiodetect.Manager{FFmpeg: "/nonexistent/ffmpeg"}
	if r.audioStatus(camID) != "" {
		t.Fatal("status for an idle camera")
	}
}

package recorder

import (
	"fmt"

	"sentinel-linux/internal/audiodetect"
	"sentinel-linux/internal/link"
)

// Audio event detection (WAVE8(B)). The manager runs one ffmpeg per camera
// that has audio detection on; a detected sound becomes an event of kind
// "audio" through the same path motion uses, so it is stored, queued for the
// management side, snapshotted, and raises an alarm there per the camera's
// alarm mode (server/sink.go).

// audioSources lists the cameras to listen to. The tap reads the local
// MediaMTX re-stream like the motion reader: the sub-stream when the camera
// has one, then the main stream when that carries no audio.
func (r *Recorder) audioSources(cams []link.CameraConfig) []audiodetect.Source {
	var out []audiodetect.Source
	for _, c := range cams {
		if c.AudioDetect <= 0 {
			continue
		}
		live := r.liveRTSP(c.ID)
		main := r.mainRTSP(c.ID)
		src := audiodetect.Source{CameraID: c.ID, URL: live, ThresholdDB: c.AudioDetect}
		if main != live {
			src.FallbackURL = main
		}
		out = append(out, src)
	}
	return out
}

func (r *Recorder) mainRTSP(cameraID string) string {
	return fmt.Sprintf("rtsp://127.0.0.1:%d/%s", r.Media.Ports.RTSP, cameraID)
}

// audioStatus is the camera's audio detail line ("" = off).
func (r *Recorder) audioStatus(cameraID string) string {
	if r.Audio == nil {
		return ""
	}
	return r.Audio.Status(cameraID)
}

// OnAudio is the audio detector callback: one event per sound, already
// debounced to 30 s per camera by the analyzer.
func (r *Recorder) OnAudio(e audiodetect.Event) {
	cam, ok := r.camera(e.CameraID)
	if !ok || cam.AudioDetect <= 0 {
		return
	}
	r.Activity(cam.ID, "Sound", e.At) // recordctl.go (the objects trigger ignores sound: "car horn" is no car)
	_, mergeSince := r.detect.episode(cam.ID+"|audio", e.At)
	ev, ok := r.recordDetection(cam, link.Detection{Source: link.SourceAudio, Kind: e.Kind, Label: e.Label, Score: e.Score}, e.At, mergeSince)
	if !ok {
		return
	}
	r.motionSnapshot(cam, ev)
}

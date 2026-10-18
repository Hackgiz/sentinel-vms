package main

import (
	"log/slog"

	"sentinel-linux/internal/audiodetect"
	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/recorder"
)

// setupAudio wires audio event detection into the recorder (WAVE8(B)). The
// loudness detector needs only ffmpeg; the optional sound classifier loads
// from SENTINEL_AUDIO_MODEL or <data>/models/audio.onnx and, when it can't,
// the reason is shown in each camera's detail instead.
func setupAudio(dataDir string, rec *recorder.Recorder) {
	m := &audiodetect.Manager{FFmpeg: detect.Find(dataDir), OnEvent: rec.OnAudio}
	if !m.Available() {
		slog.Warn("audio event detection unavailable: ffmpeg not found")
		return
	}
	lib, _ := findObjectModel(dataDir) // the ONNX Runtime object detection uses
	if c, reason := audiodetect.OpenClassifier(dataDir, lib); c != nil {
		m.Classifier = c
		slog.Info("audio sound classifier ready")
	} else {
		m.ClassifierNote = reason
		slog.Info("audio classifier not running; loudness detection only", "reason", reason)
	}
	rec.Audio = m
}

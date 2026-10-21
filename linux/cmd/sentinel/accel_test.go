package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link/remote"
	"sentinel-linux/internal/models"
)

func TestPickDetector(t *testing.T) {
	data := t.TempDir()
	tiny := "/opt/sentinel/models/yolox_tiny.onnx"
	for _, want := range []string{"", "yolox-tiny"} {
		p := pickDetector(data, want, tiny)
		if p.ID != models.IDYOLOXTiny || p.Path != tiny || p.Note != "" || p.Spec.Input != detect.InputSize {
			t.Fatalf("%q: %+v", want, p)
		}
	}
	// Missing: YOLOX-Tiny, saying why.
	p := pickDetector(data, "yolox-s", tiny)
	if p.ID != models.IDYOLOXTiny || p.Path != tiny || !strings.Contains(p.Note, "not installed") {
		t.Fatalf("missing: %+v", p)
	}
	// A custom model installed by hand.
	dir := models.Dir(data)
	_ = os.MkdirAll(dir, 0o750)
	_ = os.WriteFile(filepath.Join(dir, "custom.onnx"), []byte("onnx"), 0o640)
	_ = os.WriteFile(filepath.Join(dir, "custom.json"), []byte(`{"family": "ultralytics", "input": 640, "labels": {"0": "person"}}`), 0o640)
	p = pickDetector(data, "custom", tiny)
	if p.ID != "custom" || p.Path != filepath.Join(dir, "custom.onnx") || p.Spec.Family != detect.FamilyUltralytics || p.Spec.Input != 640 || p.Note != "" {
		t.Fatalf("custom: %+v", p)
	}
}

func TestStartupDetectorModel(t *testing.T) {
	data := t.TempDir()
	if got := startupDetectorModel(context.Background(), nil, data); got != "" {
		t.Fatalf("nothing saved: %q", got)
	}
	_ = os.MkdirAll(remote.LinkDir(data), 0o700)
	_ = os.WriteFile(filepath.Join(remote.LinkDir(data), "config.json"), []byte(`{"rev": 4, "detectorModel": "yolox-s", "detectRate": 2}`), 0o600)
	if got := startupDetectorModel(context.Background(), nil, data); got != "yolox-s" {
		t.Fatalf("saved snapshot: %q", got)
	}
}

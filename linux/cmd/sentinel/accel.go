package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/hwinfo"
	"sentinel-linux/internal/media"
)

// Hardware acceleration wiring. Environment:
//
//	SENTINEL_FFMPEG          ffmpeg to use instead of the bundled one
//	SENTINEL_HWACCEL_DEVICE  GPU render node for decode (default: first Intel/AMD /dev/dri/renderD*)
//	SENTINEL_ORT_LIB         ONNX Runtime library to use instead of the bundled one
//	                         (an OpenVINO-enabled build runs detection on an Intel GPU/NPU)
//	SENTINEL_OPENVINO_DEVICE OpenVINO device_type: GPU (default), NPU, CPU, AUTO:GPU,CPU
//	SENTINEL_COREML_UNITS    macOS: Core ML compute units: ALL (default), ANE, GPU, CPU
//
// The "hwaccel" setting (System page: Auto / Off) gates all of it.

// setupDecodeAccel probes the GPU decoders and configures the motion readers.
func setupDecodeAccel(ctx context.Context, mode string, m *detect.Manager, sup *media.Supervisor) {
	caps := detect.ProbeDecode(ctx, m.FFmpeg, hwinfo.ProbeHost(), os.Getenv("SENTINEL_HWACCEL_DEVICE"), nil)
	m.ConfigureAccel(mode, caps, func(id string) string {
		return sup.State(sup.LivePath(id)).Codec // the path the reader decodes
	})
	switch {
	case mode == detect.AccelOff:
		slog.Info("hardware decode off (setting)")
	case len(caps.Usable) > 0:
		slog.Info("hardware decode available", "methods", caps.Usable, "device", caps.Device, "driver", caps.Driver, "ffmpeg", caps.FFmpegSource)
	default:
		slog.Info("hardware decode unavailable; detection decodes on the CPU", "why", caps.Notes)
	}
}

// findObjectModel is detect.FindObjectModel, with SENTINEL_ORT_LIB standing
// in when no bundled ONNX Runtime is installed.
func findObjectModel(dataDir string) (lib, model string) {
	lib, model = detect.FindObjectModel(dataDir)
	if lib == "" {
		lib = os.Getenv("SENTINEL_ORT_LIB")
	}
	return lib, model
}

// newObjectDetector picks OpenVINO (Linux), CoreML (macOS) or the CPU for
// person/vehicle detection.
func newObjectDetector(mode, dataDir, lib, model string, threads int) (*detect.ObjectDetector, error) {
	alt := os.Getenv("SENTINEL_ORT_LIB")
	if alt == lib {
		alt = "" // no bundled runtime; SENTINEL_ORT_LIB is the only one
	}
	// Compiled-model caches (faster restarts). Core ML doesn't notice a
	// changed model, so its cache is keyed by the model's hash.
	var cache, coreml string
	if runtime.GOOS == "darwin" {
		coreml = cacheDir(dataDir, filepath.Join("coreml", fileKey(model)))
	} else {
		cache = cacheDir(dataDir, "openvino")
	}
	od, err := detect.NewAcceleratedDetector(detect.DetectorConfig{
		Lib:            lib,
		AltLib:         alt,
		Model:          model,
		Threads:        threads,
		MaxParallel:    2,
		Accel:          mode,
		OpenVINODevice: os.Getenv("SENTINEL_OPENVINO_DEVICE"),
		CacheDir:       cache,
		CoreMLUnits:    os.Getenv("SENTINEL_COREML_UNITS"),
		CoreMLCacheDir: coreml,
	})
	if err == nil {
		info := od.Info()
		slog.Info("person/vehicle detection engine", "engine", info.Engine, "device", info.Device, "note", info.Note)
	}
	return od, err
}

// cacheDir is <data>/cache/<name>, created on demand ("" if it can't be).
func cacheDir(dataDir, name string) string {
	d := filepath.Join(dataDir, "cache", name)
	if err := os.MkdirAll(d, 0o750); err != nil {
		return ""
	}
	return d
}

// fileKey is a short content hash of path ("unknown" if unreadable).
func fileKey(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

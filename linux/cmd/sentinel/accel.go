package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/hwinfo"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/link/remote"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/models"
	"sentinel-linux/internal/server"
	"sentinel-linux/internal/store"
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

// detectorPick is the detector model the recorder loads (WAVE16 stream A).
type detectorPick struct {
	ID   string // catalog id actually loaded
	Path string
	Spec detect.ModelSpec
	Note string // why it is not the model asked for ("" = it is)
}

// startupDetectorModel is the detector the recorder starts with: the
// management setting (--role all), else the configuration a recording
// server saved from its management server. A change needs a restart.
func startupDetectorModel(ctx context.Context, st *store.Store, dataDir string) string {
	if st != nil {
		return st.Setting(ctx, server.SettingDetectorModel, "")
	}
	raw, err := os.ReadFile(filepath.Join(remote.LinkDir(dataDir), "config.json"))
	if err != nil {
		return ""
	}
	var snap link.Snapshot
	if json.Unmarshal(raw, &snap) != nil {
		return ""
	}
	return snap.DetectorModel
}

// pickDetector resolves the wanted catalog model to a file and spec,
// falling back to the bundled YOLOX-Tiny (tiny; "" = not installed) with
// the reason when the wanted one can't be used.
func pickDetector(dataDir, want, tiny string) detectorPick {
	want = models.NormDetector(want)
	bundled := detectorPick{ID: models.IDYOLOXTiny, Path: tiny, Spec: detect.DefaultSpec()}
	if want == models.IDYOLOXTiny {
		return bundled
	}
	path, spec, err := models.Detector(dataDir, want)
	if err != nil {
		bundled.Note = err.Error() + "; using YOLOX-Tiny"
		return bundled
	}
	return detectorPick{ID: want, Path: path, Spec: spec}
}

// newObjectDetector picks OpenVINO (Linux), CoreML (macOS) or the CPU for
// person/vehicle detection, with the picked model; a model other than the
// bundled one that fails to load falls back to it.
func newObjectDetector(mode, dataDir, lib string, pick detectorPick, tiny string, threads int) (*detect.ObjectDetector, error) {
	od, err := newObjectDetectorFor(mode, dataDir, lib, pick, threads)
	if err != nil && pick.ID != models.IDYOLOXTiny && tiny != "" {
		slog.Warn("detector model failed to load; using YOLOX-Tiny", "model", pick.ID, "err", err)
		note := pick.ID + " failed to load (" + err.Error() + "); using YOLOX-Tiny"
		if od, err = newObjectDetectorFor(mode, dataDir, lib, detectorPick{ID: models.IDYOLOXTiny, Path: tiny, Spec: detect.DefaultSpec()}, threads); err == nil {
			od.SetModelNote(note)
		}
		return od, err
	}
	if err == nil && pick.Note != "" {
		od.SetModelNote(pick.Note)
	}
	return od, err
}

func newObjectDetectorFor(mode, dataDir, lib string, pick detectorPick, threads int) (*detect.ObjectDetector, error) {
	model := pick.Path
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
		Spec:           pick.Spec,
		ModelID:        pick.ID,
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
		slog.Info("person/vehicle detection engine", "engine", info.Engine, "device", info.Device, "model", pick.ID, "input", info.Input, "note", info.Note)
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

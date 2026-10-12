package detect

// Accelerated object detection.
//
// The bundled ONNX Runtime (Microsoft's Linux release build) has only the CPU
// execution provider. When SENTINEL_ORT_LIB points at an ONNX Runtime built
// with the OpenVINO execution provider (Intel iGPU such as the N100's, or a
// Core Ultra NPU) and the setting is Auto, the same YOLOX model runs there;
// a warm-up inference proves it before it's used, and anything that fails
// falls back to the CPU.
//
// Other accelerators plug in through Backend. Coral (Edge TPU) and Hailo
// (Raspberry Pi AI Kit) can't run this ONNX model: they need models compiled
// for them (an *_edgetpu.tflite via the Edge TPU compiler; a .hef via Hailo's
// Dataflow Compiler) and their own runtimes (libedgetpu + TensorFlow Lite;
// HailoRT). See ROADMAP.md for the plan; neither is implemented yet.

import (
	"errors"
	"log/slog"
	"math"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"sentinel-linux/internal/ort"
)

// Detection engines (DetectorInfo.Engine).
const (
	EngineCPU      = "CPU"
	EngineOpenVINO = "OpenVINO"
)

// Backend is a pluggable detection engine. It receives the letterboxed
// 416×416 BGR24 frame (real image top-left, contentW×contentH) and returns
// objects with boxes normalized to that content, Kind Person|Vehicle|Animal.
// Implementations own their model format and pre/post-processing.
type Backend interface {
	Name() string
	Detect(bgr []byte, contentW, contentH int) ([]Object, error)
}

// NewObjectDetectorWithBackend wraps a Backend so it gets the same
// concurrency limit, timing and status as the built-in ONNX path.
func NewObjectDetectorWithBackend(b Backend, maxParallel int) *ObjectDetector {
	if maxParallel < 1 {
		maxParallel = 1
	}
	return &ObjectDetector{engine: b, sem: make(chan struct{}, maxParallel), info: DetectorInfo{Engine: b.Name()}}
}

// DetectorInfo is what the System page shows about detection.
type DetectorInfo struct {
	Engine     string   `json:"engine"`           // CPU | OpenVINO | a Backend's name
	Device     string   `json:"device,omitempty"` // OpenVINO device_type (GPU, NPU …)
	Library    string   `json:"library,omitempty"`
	Providers  []string `json:"providers,omitempty"` // execution providers in the loaded ONNX Runtime
	Note       string   `json:"note,omitempty"`      // why it isn't accelerated
	Inferences int64    `json:"inferences"`
	AvgMs      float64  `json:"avgMs"`  // moving average, per frame (pre/post-processing included)
	LastMs     float64  `json:"lastMs"` // most recent frame
}

// Info returns the engine and timing.
func (d *ObjectDetector) Info() DetectorInfo {
	if d == nil {
		return DetectorInfo{}
	}
	info := d.info
	info.Inferences, info.AvgMs, info.LastMs = d.stats.read()
	return info
}

type inferStats struct {
	mu   sync.Mutex
	n    int64
	avg  float64 // exponential moving average, ms
	last float64
}

func (s *inferStats) since(start time.Time) { s.add(time.Since(start)) }

func (s *inferStats) add(d time.Duration) {
	ms := float64(d.Microseconds()) / 1000
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	s.last = ms
	if s.n == 1 {
		s.avg = ms
	} else {
		s.avg += (ms - s.avg) * 0.1
	}
}

func (s *inferStats) reset() {
	s.mu.Lock()
	s.n, s.avg, s.last = 0, 0, 0
	s.mu.Unlock()
}

func (s *inferStats) read() (int64, float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n, math.Round(s.avg*10) / 10, math.Round(s.last*10) / 10
}

// DetectorConfig selects where YOLOX runs.
type DetectorConfig struct {
	Lib         string // bundled ONNX Runtime (CPU)
	AltLib      string // SENTINEL_ORT_LIB: e.g. an OpenVINO-enabled build
	Model       string
	Threads     int
	MaxParallel int
	Accel       string // AccelAuto | AccelOff
	// OpenVINO device_type: GPU (default), NPU, CPU, or AUTO:GPU,CPU.
	OpenVINODevice string
	CacheDir       string // OpenVINO compiled-model cache (faster restarts); "" = none
	// macOS: Core ML compute units (ALL default, CPUAndNeuralEngine,
	// CPUAndGPU, CPUOnly) and its compiled-model cache.
	CoreMLUnits    string
	CoreMLCacheDir string
}

// accelOrder is which accelerated engines Auto tries on this OS.
func accelOrder() []func(DetectorConfig) (*ObjectDetector, []string) {
	if runtime.GOOS == "darwin" {
		return []func(DetectorConfig) (*ObjectDetector, []string){tryCoreML}
	}
	return []func(DetectorConfig) (*ObjectDetector, []string){tryOpenVINO}
}

// NewAcceleratedDetector tries OpenVINO (Linux; Auto mode, OpenVINO-enabled
// library) or CoreML (macOS) and falls back to the CPU. The returned detector's Info says which and why.
func NewAcceleratedDetector(cfg DetectorConfig) (*ObjectDetector, error) {
	var notes []string
	if cfg.OpenVINODevice == "" {
		cfg.OpenVINODevice = "GPU"
	}
	if cfg.Accel == AccelOff {
		notes = append(notes, NoteAccelOff)
	} else {
		for _, try := range accelOrder() {
			d, why := try(cfg)
			if d != nil {
				return d, nil
			}
			notes = append(notes, why...)
		}
	}
	// CPU: the library the admin chose (SENTINEL_ORT_LIB) if it loads, else
	// the bundled one — so two runtimes aren't loaded without need.
	var d *ObjectDetector
	err := errors.New("no ONNX Runtime library")
	for _, lib := range []string{cfg.AltLib, cfg.Lib} {
		if lib == "" {
			continue
		}
		if d, err = NewObjectDetector(lib, cfg.Model, cfg.Threads, cfg.MaxParallel); err == nil {
			break
		}
		notes = append(notes, "couldn't use "+lib+": "+truncate(err.Error(), 200))
	}
	if err != nil {
		return nil, err
	}
	if len(notes) > 0 {
		d.info.Note = notes[0]
	}
	return d, nil
}

// warmupFrame is a mid-gray letterboxed frame for proving a provider works.
func warmupFrame() []byte {
	f := make([]byte, 3*InputSize*InputSize)
	for i := range f {
		f[i] = 114
	}
	return f
}

func tryOpenVINO(cfg DetectorConfig) (*ObjectDetector, []string) {
	var notes []string
	var libs []string
	for _, l := range []string{cfg.AltLib, cfg.Lib} {
		if l != "" && (len(libs) == 0 || libs[0] != l) {
			libs = append(libs, l)
		}
	}
	for _, lib := range libs {
		rt, err := ort.Load(lib)
		if err != nil {
			notes = append(notes, "couldn't load "+lib+": "+err.Error())
			continue
		}
		providers, _ := rt.Providers()
		// The OpenVINO EP is a plugin (libonnxruntime_providers_openvino.so)
		// that ONNX Runtime loads from its own directory; builds made with it
		// also list it as available. Without either, don't try (ORT would
		// log a load error on every start).
		plugin := fileExists(filepath.Join(filepath.Dir(lib), "libonnxruntime_providers_openvino.so"))
		if !rt.HasProvider(ort.ProviderOpenVINO) && !plugin {
			if lib == cfg.AltLib {
				notes = append(notes, "SENTINEL_ORT_LIB ("+lib+") has no OpenVINO execution provider")
			} else if cfg.AltLib == "" {
				notes = append(notes, "the bundled ONNX Runtime is CPU-only; set SENTINEL_ORT_LIB to an OpenVINO-enabled build to use an Intel GPU/NPU")
			}
			continue
		}
		opts := map[string]string{"device_type": cfg.OpenVINODevice}
		if cfg.CacheDir != "" {
			opts["cache_dir"] = cfg.CacheDir
		}
		start := time.Now()
		sess, err := rt.NewSessionWithProvider(cfg.Model, cfg.Threads, ort.ProviderOpenVINO, opts)
		if err != nil {
			notes = append(notes, "OpenVINO ("+cfg.OpenVINODevice+") didn't start: "+truncate(err.Error(), 200))
			continue
		}
		d := newObjectDetector(sess, cfg.MaxParallel)
		if _, err := d.Detect(warmupFrame(), InputSize, InputSize); err != nil {
			sess.Close()
			notes = append(notes, "OpenVINO ("+cfg.OpenVINODevice+") failed its test inference: "+truncate(err.Error(), 200))
			continue
		}
		d.stats.reset() // don't count the warm-up (it includes compilation)
		d.info = DetectorInfo{Engine: EngineOpenVINO, Device: cfg.OpenVINODevice, Library: lib, Providers: providers}
		slog.Info("person/vehicle detection on OpenVINO", "device", cfg.OpenVINODevice, "lib", lib, "warmup", time.Since(start).Round(time.Millisecond))
		return d, nil
	}
	return nil, notes
}

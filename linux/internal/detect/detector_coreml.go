package detect

// CoreML object detection (macOS).
//
// Microsoft's macOS ONNX Runtime builds include the CoreML execution
// provider, which hands the supported part of the YOLOX graph to Core ML
// (Apple Neural Engine, GPU or CPU, as Core ML decides). Like OpenVINO on
// Linux: in Auto mode the provider is appended, a warm-up inference proves it
// works, and anything that fails leaves detection on the CPU with the reason
// shown on the System page.

import (
	"log/slog"
	"strings"
	"time"

	"sentinel-linux/internal/ort"
)

// EngineCoreML is DetectorInfo.Engine when Core ML runs detection.
const EngineCoreML = "CoreML"

// CoreML compute units (SENTINEL_COREML_UNITS).
const (
	CoreMLUnitsAll       = "ALL"                // Neural Engine, GPU and CPU (Core ML picks)
	CoreMLUnitsANE       = "CPUAndNeuralEngine" // keep the GPU free
	CoreMLUnitsGPU       = "CPUAndGPU"
	CoreMLUnitsCPUOnly   = "CPUOnly"
	defaultCoreMLFormat  = "MLProgram" // Core ML 5+ (macOS 12); supports more operators than NeuralNetwork
	coreMLFallbackFormat = "NeuralNetwork"
)

// NormCoreMLUnits maps user input (case-insensitive, short forms like "ane",
// "gpu", "cpu") to an MLComputeUnits value; "" or unknown = ALL.
func NormCoreMLUnits(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ane", "neuralengine", "cpuandneuralengine", "npu":
		return CoreMLUnitsANE
	case "gpu", "cpuandgpu":
		return CoreMLUnitsGPU
	case "cpu", "cpuonly":
		return CoreMLUnitsCPUOnly
	}
	return CoreMLUnitsAll
}

// coreMLOptions are the provider options for a session (pure; see tests).
func coreMLOptions(units, format, cacheDir string) map[string]string {
	opts := map[string]string{
		"MLComputeUnits": NormCoreMLUnits(units),
		"ModelFormat":    format,
		// YOLOX-Tiny's input is a fixed 1×3×416×416: let Core ML specialize.
		"RequireStaticInputShapes": "1",
	}
	if cacheDir != "" {
		opts["ModelCacheDirectory"] = cacheDir
	}
	return opts
}

// tryCoreML returns a detector on Core ML, or nil and why not.
func tryCoreML(cfg DetectorConfig) (*ObjectDetector, []string) {
	var notes []string
	var libs []string
	for _, l := range []string{cfg.AltLib, cfg.Lib} {
		if l != "" && (len(libs) == 0 || libs[0] != l) {
			libs = append(libs, l)
		}
	}
	units := NormCoreMLUnits(cfg.CoreMLUnits)
	for _, lib := range libs {
		rt, err := ort.Load(lib)
		if err != nil {
			notes = append(notes, "couldn't load "+lib+": "+err.Error())
			continue
		}
		providers, _ := rt.Providers()
		if !rt.HasProvider(ort.ProviderCoreML) {
			notes = append(notes, "the ONNX Runtime at "+lib+" has no CoreML execution provider")
			continue
		}
		for _, format := range []string{defaultCoreMLFormat, coreMLFallbackFormat} {
			start := time.Now()
			sess, err := rt.NewSessionWithProvider(cfg.Model, cfg.Threads, ort.ProviderCoreML, coreMLOptions(units, format, cfg.CoreMLCacheDir))
			if err != nil {
				notes = append(notes, "CoreML ("+format+") didn't start: "+truncate(err.Error(), 200))
				continue
			}
			d := newObjectDetector(sess, cfg.MaxParallel)
			if _, err := d.Detect(warmupFrame(), InputSize, InputSize); err != nil {
				sess.Close()
				notes = append(notes, "CoreML ("+format+") failed its test inference: "+truncate(err.Error(), 200))
				continue
			}
			d.stats.reset() // the warm-up includes Core ML's model compilation
			d.info = DetectorInfo{Engine: EngineCoreML, Device: units, Library: lib, Providers: providers}
			slog.Info("person/vehicle detection on CoreML", "units", units, "format", format, "lib", lib, "warmup", time.Since(start).Round(time.Millisecond))
			return d, nil
		}
	}
	return nil, notes
}

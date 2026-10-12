package detect

import (
	"image/jpeg"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

func TestNormCoreMLUnits(t *testing.T) {
	for in, want := range map[string]string{
		"": CoreMLUnitsAll, "all": CoreMLUnitsAll, "bogus": CoreMLUnitsAll,
		"ANE": CoreMLUnitsANE, "CPUAndNeuralEngine": CoreMLUnitsANE, "npu": CoreMLUnitsANE,
		"gpu": CoreMLUnitsGPU, " CPUAndGPU ": CoreMLUnitsGPU,
		"cpu": CoreMLUnitsCPUOnly, "CPUOnly": CoreMLUnitsCPUOnly,
	} {
		if got := NormCoreMLUnits(in); got != want {
			t.Errorf("NormCoreMLUnits(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCoreMLOptions(t *testing.T) {
	o := coreMLOptions("ane", "MLProgram", "/data/cache/coreml")
	if o["MLComputeUnits"] != CoreMLUnitsANE || o["ModelFormat"] != "MLProgram" ||
		o["ModelCacheDirectory"] != "/data/cache/coreml" || o["RequireStaticInputShapes"] != "1" {
		t.Fatalf("options %v", o)
	}
	if _, ok := coreMLOptions("", "NeuralNetwork", "")["ModelCacheDirectory"]; ok {
		t.Fatal("no cache dir means no ModelCacheDirectory key")
	}
}

func TestAccelOrder(t *testing.T) {
	order := accelOrder()
	if len(order) != 1 {
		t.Fatalf("order %d", len(order))
	}
}

// TestCoreMLAgainstRealRuntime runs YOLOX on Core ML and on the CPU with
// Microsoft's macOS ONNX Runtime, checks both find the same objects in a real
// photo, and logs the per-frame time of each (go test -v). Needs macOS,
// SENTINEL_ORT_LIB, SENTINEL_MODEL and SENTINEL_TEST_JPEG (skipped otherwise).
func TestCoreMLAgainstRealRuntime(t *testing.T) {
	lib, model, img := os.Getenv("SENTINEL_ORT_LIB"), os.Getenv("SENTINEL_MODEL"), os.Getenv("SENTINEL_TEST_JPEG")
	if runtime.GOOS != "darwin" || lib == "" || model == "" || img == "" {
		t.Skip("macOS only; set SENTINEL_ORT_LIB, SENTINEL_MODEL, SENTINEL_TEST_JPEG")
	}
	f, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	src, err := jpeg.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	bgr, cw, ch := letterbox(src)

	kinds := func(objs []Object) []string {
		var k []string
		for _, o := range objs {
			k = append(k, o.Kind)
		}
		sort.Strings(k)
		return k
	}
	bench := func(name string, d *ObjectDetector) ([]Object, float64) {
		objs, err := d.Detect(bgr, cw, ch) // first run after warm-up
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		const n = 30
		start := time.Now()
		for i := 0; i < n; i++ {
			if _, err := d.Detect(bgr, cw, ch); err != nil {
				t.Fatal(err)
			}
		}
		ms := float64(time.Since(start).Microseconds()) / 1000 / n
		t.Logf("%-28s %6.1f ms/frame  %v", name, ms, kinds(objs))
		return objs, ms
	}

	threads := 4
	cpu, err := NewAcceleratedDetector(DetectorConfig{Lib: lib, Model: model, Threads: threads, MaxParallel: 1, Accel: AccelOff})
	if err != nil {
		t.Fatal(err)
	}
	cpuObjs, _ := bench("CPU ("+itoa(threads)+" threads)", cpu)

	cache := t.TempDir()
	for _, units := range []string{CoreMLUnitsAll, CoreMLUnitsANE, CoreMLUnitsGPU} {
		start := time.Now()
		d, err := NewAcceleratedDetector(DetectorConfig{Lib: lib, Model: model, Threads: threads, MaxParallel: 1, Accel: AccelAuto, CoreMLUnits: units, CoreMLCacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		info := d.Info()
		if info.Engine != EngineCoreML || info.Device != units {
			t.Fatalf("expected CoreML/%s, got %+v", units, info)
		}
		t.Logf("CoreML %s ready in %v", units, time.Since(start).Round(time.Millisecond))
		objs, _ := bench("CoreML "+units, d)
		if a, b := kinds(objs), kinds(cpuObjs); len(a) != len(b) {
			t.Errorf("CoreML %s found %v, CPU %v", units, a, b)
		}
		if info := d.Info(); info.Inferences != 31 || info.AvgMs <= 0 {
			t.Errorf("timing %+v", info)
		}
	}
}

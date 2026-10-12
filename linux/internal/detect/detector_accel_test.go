package detect

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct{ calls int }

func (f *fakeBackend) Name() string { return "Fake TPU" }
func (f *fakeBackend) Detect(bgr []byte, cw, ch int) ([]Object, error) {
	f.calls++
	time.Sleep(2 * time.Millisecond)
	return []Object{{Kind: "Person", Label: "person", Score: 0.9, Box: [4]float64{0.1, 0.1, 0.2, 0.4}}}, nil
}

func TestBackendPlugsIntoObjectDetector(t *testing.T) {
	fb := &fakeBackend{}
	d := NewObjectDetectorWithBackend(fb, 1)
	objs, err := d.Detect(warmupFrame(), 416, 234)
	if err != nil || len(objs) != 1 || fb.calls != 1 {
		t.Fatalf("detect: %v %v calls=%d", objs, err, fb.calls)
	}
	if _, err := d.Detect([]byte{1, 2, 3}, 416, 234); err == nil {
		t.Fatal("bad frame must be rejected before the backend")
	}
	info := d.Info()
	if info.Engine != "Fake TPU" || info.Inferences != 1 || info.AvgMs < 1 {
		t.Fatalf("info %+v", info)
	}
}

func TestInferStats(t *testing.T) {
	var s inferStats
	s.add(10 * time.Millisecond)
	s.add(20 * time.Millisecond)
	n, avg, last := s.read()
	if n != 2 || avg != 11 || last != 20 {
		t.Fatalf("n=%d avg=%v last=%v", n, avg, last)
	}
	s.reset()
	if n, _, _ := s.read(); n != 0 {
		t.Fatal("reset")
	}
}

// TestAcceleratedDetectorFallsBackToCPU loads a real ONNX Runtime that has no
// OpenVINO provider (the bundled build, or Microsoft's macOS build) and checks
// Auto mode ends up on the CPU with a reason. Needs SENTINEL_ORT_LIB and
// SENTINEL_MODEL (skipped otherwise).
func TestAcceleratedDetectorFallsBackToCPU(t *testing.T) {
	lib, model := os.Getenv("SENTINEL_ORT_LIB"), os.Getenv("SENTINEL_MODEL")
	if lib == "" || model == "" {
		t.Skip("set SENTINEL_ORT_LIB and SENTINEL_MODEL")
	}
	d, err := NewAcceleratedDetector(DetectorConfig{AltLib: lib, Model: model, Threads: 2, MaxParallel: 1, Accel: AccelAuto})
	if err != nil {
		t.Fatal(err)
	}
	info := d.Info()
	t.Logf("info: %+v", info)
	switch {
	case runtime.GOOS == "darwin":
		// Microsoft's macOS build has the CoreML provider, so Auto uses it
		// (TestCoreMLAgainstRealRuntime compares it with the CPU).
		if info.Engine != EngineCoreML || len(info.Providers) == 0 {
			t.Fatalf("info %+v", info)
		}
	case info.Engine != EngineCPU || !strings.Contains(info.Note, "no OpenVINO") || len(info.Providers) == 0:
		t.Fatalf("info %+v", info)
	}
	if _, err := d.Detect(warmupFrame(), 416, 234); err != nil {
		t.Fatal(err)
	}
	if info := d.Info(); info.Inferences != 1 || info.LastMs <= 0 {
		t.Fatalf("timing %+v", info)
	}

	off, err := NewAcceleratedDetector(DetectorConfig{AltLib: lib, Model: model, Threads: 2, MaxParallel: 1, Accel: AccelOff})
	if err != nil || off.Info().Note != NoteAccelOff {
		t.Fatalf("off: %v %+v", err, off.Info())
	}
}

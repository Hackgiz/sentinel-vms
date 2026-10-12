package ort

import (
	"os"
	"strings"
	"testing"
)

// TestProvidersAgainstRealRuntime exercises the provider slots on a real
// ONNX Runtime without OpenVINO (bundled Linux build or Microsoft's macOS
// build): listing works, appending OpenVINO fails cleanly, CPU still works.
// Needs SENTINEL_ORT_LIB and SENTINEL_MODEL (skipped otherwise).
func TestProvidersAgainstRealRuntime(t *testing.T) {
	lib, model := os.Getenv("SENTINEL_ORT_LIB"), os.Getenv("SENTINEL_MODEL")
	if lib == "" || model == "" {
		t.Skip("set SENTINEL_ORT_LIB and SENTINEL_MODEL")
	}
	rt, err := Load(lib)
	if err != nil {
		t.Fatal(err)
	}
	if v := rt.APIVersion(); v < minAPIVersion || v > apiVersion || rt.Path() != lib { // older runtimes too (macOS Intel: 1.23)
		t.Fatalf("api %d path %s", rt.APIVersion(), rt.Path())
	}
	ps, err := rt.Providers()
	if err != nil || len(ps) == 0 || ps[len(ps)-1] != ProviderCPU {
		t.Fatalf("providers %v %v", ps, err)
	}
	t.Logf("providers: %v", ps)
	if rt.HasProvider(ProviderOpenVINO) {
		t.Skip("this runtime has OpenVINO; the failure path can't be tested")
	}
	_, err = rt.NewSessionWithProvider(model, 1, ProviderOpenVINO, map[string]string{"device_type": "GPU"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "openvino") {
		t.Fatalf("expected an OpenVINO error, got %v", err)
	}
	t.Logf("append OpenVINO: %v", err)
	s, err := rt.NewSessionWithProvider(model, 1, ProviderCPU, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if rt.HasProvider(ProviderCoreML) { // Microsoft's macOS build
		s, err := rt.NewSessionWithProvider(model, 1, ProviderCoreML, map[string]string{"MLComputeUnits": "ALL", "ModelFormat": "MLProgram"})
		if err != nil {
			t.Fatalf("CoreML: %v", err)
		}
		s.Close()
		if _, err := rt.NewSessionWithProvider(model, 1, ProviderCoreML, map[string]string{"MLComputeUnits": "Bogus"}); err == nil {
			t.Fatal("an invalid CoreML option must fail")
		}
	}
	if again, _ := Load(lib); again != rt {
		t.Fatal("Load must cache per path")
	}
	if _, err := Load("/nonexistent/libonnxruntime.so"); err == nil {
		t.Fatal("missing library must fail")
	}
}

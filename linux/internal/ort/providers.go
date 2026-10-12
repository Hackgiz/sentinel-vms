package ort

import (
	"errors"
	"runtime"
	"sort"
	"unsafe"
)

// Execution providers. The bundled Linux runtime (Microsoft's release build)
// has only the CPU provider; an OpenVINO-enabled build (Intel iGPU/NPU) can be
// loaded instead with SENTINEL_ORT_LIB. Microsoft's macOS builds include the
// CoreML provider (Apple Neural Engine / GPU).

// minAPIVersion is the oldest OrtApi version every slot below and in ort.go
// exists in: SessionOptionsAppendExecutionProvider_OpenVINO_V2 is ORT 1.17.
const minAPIVersion = 17

// OrtApi slot indices (parsed from the 1.30 header; the table is append-only).
const (
	fnGetAvailableProviders     = 125
	fnReleaseAvailableProviders = 126
	fnAppendProvider            = 216 // SessionOptionsAppendExecutionProvider (generic, by name; ORT 1.12)
	fnAppendOpenVINOV2          = 275 // SessionOptionsAppendExecutionProvider_OpenVINO_V2
)

const (
	ProviderCPU      = "CPUExecutionProvider"
	ProviderOpenVINO = "OpenVINOExecutionProvider"
	ProviderCoreML   = "CoreMLExecutionProvider"
)

// Path is the library this runtime was loaded from.
func (r *Runtime) Path() string { return r.path }

// APIVersion is the OrtApi version in use.
func (r *Runtime) APIVersion() int { return r.ver }

// Providers lists the execution providers compiled into the loaded library,
// e.g. [OpenVINOExecutionProvider CPUExecutionProvider]. Being listed does not
// guarantee the device works (OpenVINO still needs its runtime and a GPU/NPU
// driver); appending the provider and running a warm-up inference does.
func (r *Runtime) Providers() ([]string, error) {
	var arr uintptr
	var n int32
	if err := r.check(r.call(fnGetAvailableProviders, uintptr(unsafe.Pointer(&arr)), uintptr(unsafe.Pointer(&n)))); err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for i := 0; i < int(n); i++ {
		p := *(*uintptr)(cptr(arr + uintptr(i)*unsafe.Sizeof(uintptr(0))))
		out = append(out, gostr(p))
	}
	_ = r.check(r.call(fnReleaseAvailableProviders, arr, uintptr(n)))
	return out, nil
}

// HasProvider reports whether name is compiled into the loaded library.
func (r *Runtime) HasProvider(name string) bool {
	ps, err := r.Providers()
	if err != nil {
		return false
	}
	for _, p := range ps {
		if p == name {
			return true
		}
	}
	return false
}

// NewSessionWithProvider loads modelPath on an execution provider. provider
// "" or ProviderCPU is the same as NewSession. For ProviderOpenVINO, options
// are OpenVINO EP keys (device_type: GPU | NPU | CPU | AUTO:GPU,CPU;
// cache_dir; precision …). For ProviderCoreML, options are CoreML EP keys
// (MLComputeUnits: ALL | CPUAndNeuralEngine | CPUAndGPU | CPUOnly;
// ModelFormat: MLProgram | NeuralNetwork; ModelCacheDirectory …). Fails if
// the provider can't be appended; nodes the provider doesn't support still run
// on CPU inside ONNX Runtime.
func (r *Runtime) NewSessionWithProvider(modelPath string, intraThreads int, provider string, options map[string]string) (*Session, error) {
	switch provider {
	case "", ProviderCPU:
		return r.NewSession(modelPath, intraThreads)
	case ProviderOpenVINO:
		return r.newSession(modelPath, intraThreads, func(opts uintptr) error {
			return r.appendOpenVINO(opts, options)
		})
	case ProviderCoreML:
		return r.newSession(modelPath, intraThreads, func(opts uintptr) error {
			return r.appendByName(opts, "CoreML", options)
		})
	default:
		return nil, errors.New("unsupported execution provider " + provider)
	}
}

func (r *Runtime) appendOpenVINO(opts uintptr, options map[string]string) error {
	kv := newCKeyValues(options)
	err := r.check(r.call(fnAppendOpenVINOV2, opts, kv.keyPtr(), kv.valuePtr(), kv.count()))
	runtime.KeepAlive(kv)
	return err
}

// appendByName appends a provider through the generic
// SessionOptionsAppendExecutionProvider (CoreML, XNNPACK, QNN …).
func (r *Runtime) appendByName(opts uintptr, name string, options map[string]string) error {
	kv := newCKeyValues(options)
	cName := cstr(name)
	err := r.check(r.call(fnAppendProvider, opts, uintptr(unsafe.Pointer(&cName[0])), kv.keyPtr(), kv.valuePtr(), kv.count()))
	runtime.KeepAlive(cName)
	runtime.KeepAlive(kv)
	return err
}

// cKeyValues holds provider options as NUL-terminated C strings plus the two
// char* arrays ONNX Runtime reads (sorted by key, so calls are reproducible).
type cKeyValues struct {
	cKeys, cVals [][]byte
	kp, vp       []uintptr
}

func newCKeyValues(options map[string]string) *cKeyValues {
	names := make([]string, 0, len(options))
	for k := range options {
		names = append(names, k)
	}
	sort.Strings(names)
	kv := &cKeyValues{
		cKeys: make([][]byte, len(names)),
		cVals: make([][]byte, len(names)),
		kp:    make([]uintptr, len(names)+1), // +1 so &kp[0] is valid when empty
		vp:    make([]uintptr, len(names)+1),
	}
	for i, k := range names {
		kv.cKeys[i], kv.cVals[i] = cstr(k), cstr(options[k])
		kv.kp[i] = uintptr(unsafe.Pointer(&kv.cKeys[i][0]))
		kv.vp[i] = uintptr(unsafe.Pointer(&kv.cVals[i][0]))
	}
	return kv
}

func (kv *cKeyValues) keyPtr() uintptr   { return uintptr(unsafe.Pointer(&kv.kp[0])) }
func (kv *cKeyValues) valuePtr() uintptr { return uintptr(unsafe.Pointer(&kv.vp[0])) }
func (kv *cKeyValues) count() uintptr    { return uintptr(len(kv.cKeys)) }

// Package ort is a minimal ONNX Runtime binding without cgo: the shared
// library is dlopen'ed at runtime with purego, so the server still builds with
// CGO_ENABLED=0 and simply runs without object detection when the library is
// absent. Only what single-input/single-output float32 inference needs.
//
// Function-table slots come from onnxruntime_c_api.h (struct OrtApi). The
// table is append-only across releases, so slots are stable for API >= 30.
package ort

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const apiVersion = 30 // ORT_API_VERSION of the bundled 1.30 runtime

// OrtApi slot indices (parsed from the 1.30 header).
const (
	fnGetErrorMessage                = 2
	fnCreateEnv                      = 3
	fnCreateSession                  = 7
	fnRun                            = 9
	fnCreateSessionOptions           = 10
	fnSetSessionGraphOptimizationLvl = 23
	fnSetIntraOpNumThreads           = 24
	fnSetInterOpNumThreads           = 25
	fnSessionGetInputCount           = 30
	fnSessionGetOutputCount          = 31
	fnSessionGetInputName            = 36
	fnSessionGetOutputName           = 37
	fnCreateTensorWithDataAsOrtValue = 49
	fnGetTensorMutableData           = 51
	fnGetDimensionsCount             = 61
	fnGetDimensions                  = 62
	fnGetTensorTypeAndShape          = 65
	fnCreateCpuMemoryInfo            = 69
	fnAllocatorFree                  = 76
	fnGetAllocatorWithDefaultOptions = 78
	fnReleaseEnv                     = 92
	fnReleaseStatus                  = 93
	fnReleaseMemoryInfo              = 94
	fnReleaseSession                 = 95
	fnReleaseValue                   = 96
	fnReleaseTensorTypeAndShapeInfo  = 99
	fnReleaseSessionOptions          = 100
)

const (
	loggingLevelWarning = 2
	graphOptAll         = 99
	allocatorArena      = 1
	memTypeDefault      = 0
	tensorFloat         = 1
)

type Runtime struct {
	api uintptr
	env uintptr
}

var (
	loadOnce sync.Once
	loaded   *Runtime
	loadErr  error
)

// Load dlopens the ONNX Runtime library at path once per process.
func Load(path string) (*Runtime, error) {
	loadOnce.Do(func() { loaded, loadErr = load(path) })
	return loaded, loadErr
}

func load(path string) (*Runtime, error) {
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	sym, err := purego.Dlsym(lib, "OrtGetApiBase")
	if err != nil {
		return nil, err
	}
	base, _, _ := purego.SyscallN(sym)
	if base == 0 {
		return nil, errors.New("OrtGetApiBase returned nil")
	}
	getAPI := *(*uintptr)(cptr(base))
	api, _, _ := purego.SyscallN(getAPI, apiVersion)
	if api == 0 {
		return nil, fmt.Errorf("onnxruntime at %s does not support API version %d", path, apiVersion)
	}
	r := &Runtime{api: api}
	logID := cstr("sentinel")
	if err := r.check(r.call(fnCreateEnv, loggingLevelWarning, uintptr(unsafe.Pointer(&logID[0])), uintptr(unsafe.Pointer(&r.env)))); err != nil {
		return nil, err
	}
	runtime.KeepAlive(logID)
	return r, nil
}

func (r *Runtime) fn(i int) uintptr {
	return *(*uintptr)(cptr(r.api + uintptr(i)*unsafe.Sizeof(uintptr(0))))
}

func (r *Runtime) call(i int, args ...uintptr) uintptr {
	ret, _, _ := purego.SyscallN(r.fn(i), args...)
	return ret
}

// check turns an OrtStatus* into a Go error (nil status = success).
func (r *Runtime) check(status uintptr) error {
	if status == 0 {
		return nil
	}
	msg := gostr(r.call(fnGetErrorMessage, status))
	r.call(fnReleaseStatus, status)
	return errors.New("onnxruntime: " + msg)
}

// Session is one loaded model. Run is safe for concurrent use (ORT sessions are).
type Session struct {
	r       *Runtime
	ptr     uintptr
	mem     uintptr
	inName  []byte
	outName []byte
	mu      sync.Mutex // guards close
	closed  bool
}

// NewSession loads modelPath with intraThreads worker threads.
func (r *Runtime) NewSession(modelPath string, intraThreads int) (*Session, error) {
	var opts uintptr
	if err := r.check(r.call(fnCreateSessionOptions, uintptr(unsafe.Pointer(&opts)))); err != nil {
		return nil, err
	}
	defer r.call(fnReleaseSessionOptions, opts)
	_ = r.check(r.call(fnSetIntraOpNumThreads, opts, uintptr(intraThreads)))
	_ = r.check(r.call(fnSetInterOpNumThreads, opts, 1))
	_ = r.check(r.call(fnSetSessionGraphOptimizationLvl, opts, graphOptAll))

	s := &Session{r: r}
	p := cstr(modelPath)
	if err := r.check(r.call(fnCreateSession, r.env, uintptr(unsafe.Pointer(&p[0])), opts, uintptr(unsafe.Pointer(&s.ptr)))); err != nil {
		return nil, err
	}
	runtime.KeepAlive(p)
	if err := r.check(r.call(fnCreateCpuMemoryInfo, allocatorArena, memTypeDefault, uintptr(unsafe.Pointer(&s.mem)))); err != nil {
		s.Close()
		return nil, err
	}
	in, err := s.ioName(fnSessionGetInputName)
	if err != nil {
		s.Close()
		return nil, err
	}
	out, err := s.ioName(fnSessionGetOutputName)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.inName, s.outName = cstr(in), cstr(out)
	return s, nil
}

func (s *Session) ioName(fn int) (string, error) {
	var alloc, name uintptr
	if err := s.r.check(s.r.call(fnGetAllocatorWithDefaultOptions, uintptr(unsafe.Pointer(&alloc)))); err != nil {
		return "", err
	}
	if err := s.r.check(s.r.call(fn, s.ptr, 0, alloc, uintptr(unsafe.Pointer(&name)))); err != nil {
		return "", err
	}
	str := gostr(name)
	s.r.call(fnAllocatorFree, alloc, name)
	return str, nil
}

// Run feeds one float32 tensor of shape and returns the first output's data
// and shape. The input slice must not be modified during the call.
func (s *Session) Run(input []float32, shape []int64) ([]float32, []int64, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, errors.New("session closed")
	}
	s.mu.Unlock()
	r := s.r
	var inVal uintptr
	if err := r.check(r.call(fnCreateTensorWithDataAsOrtValue, s.mem,
		uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)*4),
		uintptr(unsafe.Pointer(&shape[0])), uintptr(len(shape)), tensorFloat,
		uintptr(unsafe.Pointer(&inVal)))); err != nil {
		return nil, nil, err
	}
	defer r.call(fnReleaseValue, inVal)

	inNames := [1]uintptr{uintptr(unsafe.Pointer(&s.inName[0]))}
	outNames := [1]uintptr{uintptr(unsafe.Pointer(&s.outName[0]))}
	inputs := [1]uintptr{inVal}
	var outVal uintptr
	err := r.check(r.call(fnRun, s.ptr, 0,
		uintptr(unsafe.Pointer(&inNames[0])), uintptr(unsafe.Pointer(&inputs[0])), 1,
		uintptr(unsafe.Pointer(&outNames[0])), 1, uintptr(unsafe.Pointer(&outVal))))
	runtime.KeepAlive(input)
	runtime.KeepAlive(shape)
	runtime.KeepAlive(s.inName)
	runtime.KeepAlive(s.outName)
	if err != nil {
		return nil, nil, err
	}
	defer r.call(fnReleaseValue, outVal)

	var info uintptr
	if err := r.check(r.call(fnGetTensorTypeAndShape, outVal, uintptr(unsafe.Pointer(&info)))); err != nil {
		return nil, nil, err
	}
	defer r.call(fnReleaseTensorTypeAndShapeInfo, info)
	var ndim uintptr
	if err := r.check(r.call(fnGetDimensionsCount, info, uintptr(unsafe.Pointer(&ndim)))); err != nil {
		return nil, nil, err
	}
	dims := make([]int64, ndim)
	if ndim > 0 {
		if err := r.check(r.call(fnGetDimensions, info, uintptr(unsafe.Pointer(&dims[0])), ndim)); err != nil {
			return nil, nil, err
		}
	}
	n := int64(1)
	for _, d := range dims {
		n *= d
	}
	var data uintptr
	if err := r.check(r.call(fnGetTensorMutableData, outVal, uintptr(unsafe.Pointer(&data)))); err != nil {
		return nil, nil, err
	}
	out := make([]float32, n)
	copy(out, unsafe.Slice((*float32)(cptr(data)), n))
	return out, dims, nil
}

func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.mem != 0 {
		s.r.call(fnReleaseMemoryInfo, s.mem)
	}
	if s.ptr != 0 {
		s.r.call(fnReleaseSession, s.ptr)
	}
}

func cstr(s string) []byte { return append([]byte(s), 0) }

func gostr(p uintptr) string {
	if p == 0 {
		return ""
	}
	var n int
	for *(*byte)(cptr(p + uintptr(n))) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(cptr(p)), n))
}

// cptr converts an address owned by ONNX Runtime (C heap, never moved by the
// Go GC) into an unsafe.Pointer.
func cptr(u uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&u)) }

package ort

// Multi-output inference and input introspection (WAVE16, stream C): YuNet
// face detection returns twelve output tensors (scores, objectness, boxes
// and landmarks at three strides), so Run's "first output only" is not
// enough. Kept in its own file so ort.go stays as it is.

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"
)

// OrtApi slot indices (parsed from the 1.30 header; the table is append-only).
const (
	fnSessionGetInputTypeInfo  = 33
	fnSessionGetOutputTypeInfo = 34
	fnCastTypeInfoToTensorInfo = 55
	fnReleaseTypeInfo          = 98
	fnSetSessionLogSeverityLvl = 22
)

// loggingLevelError drops ONNX Runtime's warnings (ORT_LOGGING_LEVEL_ERROR).
const loggingLevelError = 3

// NewQuietSession is NewSession that logs only errors while loading and
// running: some public exports (SFace) list their weights as graph inputs
// and ONNX Runtime warns once per weight, hundreds of lines per load.
func (r *Runtime) NewQuietSession(modelPath string, intraThreads int) (*Session, error) {
	return r.newSession(modelPath, intraThreads, func(opts uintptr) error {
		return r.check(r.call(fnSetSessionLogSeverityLvl, opts, loggingLevelError))
	})
}

// Output is one output tensor of RunOutputs.
type Output struct {
	Name  string
	Data  []float32
	Shape []int64
}

// OutputNames lists the model's outputs in graph order.
func (s *Session) OutputNames() ([]string, error) {
	var n uintptr
	if err := s.r.check(s.r.call(fnSessionGetOutputCount, s.ptr, uintptr(unsafe.Pointer(&n)))); err != nil {
		return nil, err
	}
	names := make([]string, 0, n)
	for i := uintptr(0); i < n; i++ {
		name, err := s.ioNameAt(fnSessionGetOutputName, i)
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

// InputShape is the first input's declared shape; dynamic dimensions are -1.
func (s *Session) InputShape() ([]int64, error) { return s.ioShape(fnSessionGetInputTypeInfo, 0) }

// OutputShape is output i's declared shape; dynamic dimensions are -1.
func (s *Session) OutputShape(i int) ([]int64, error) {
	return s.ioShape(fnSessionGetOutputTypeInfo, uintptr(i))
}

func (s *Session) ioNameAt(fn int, i uintptr) (string, error) {
	var alloc, name uintptr
	if err := s.r.check(s.r.call(fnGetAllocatorWithDefaultOptions, uintptr(unsafe.Pointer(&alloc)))); err != nil {
		return "", err
	}
	if err := s.r.check(s.r.call(fn, s.ptr, i, alloc, uintptr(unsafe.Pointer(&name)))); err != nil {
		return "", err
	}
	str := gostr(name)
	s.r.call(fnAllocatorFree, alloc, name)
	return str, nil
}

func (s *Session) ioShape(fn int, i uintptr) ([]int64, error) {
	r := s.r
	var ti uintptr
	if err := r.check(r.call(fn, s.ptr, i, uintptr(unsafe.Pointer(&ti)))); err != nil {
		return nil, err
	}
	defer r.call(fnReleaseTypeInfo, ti)
	var info uintptr // owned by ti
	if err := r.check(r.call(fnCastTypeInfoToTensorInfo, ti, uintptr(unsafe.Pointer(&info)))); err != nil {
		return nil, err
	}
	if info == 0 {
		return nil, errors.New("not a tensor")
	}
	return r.dims(info)
}

func (r *Runtime) dims(info uintptr) ([]int64, error) {
	var ndim uintptr
	if err := r.check(r.call(fnGetDimensionsCount, info, uintptr(unsafe.Pointer(&ndim)))); err != nil {
		return nil, err
	}
	dims := make([]int64, ndim)
	if ndim > 0 {
		if err := r.check(r.call(fnGetDimensions, info, uintptr(unsafe.Pointer(&dims[0])), ndim)); err != nil {
			return nil, err
		}
	}
	return dims, nil
}

// RunOutputs feeds one float32 tensor of shape to the first input and
// returns the named outputs (float32 tensors), in the order asked. The input
// slice must not be modified during the call.
func (s *Session) RunOutputs(input []float32, shape []int64, outputs []string) ([]Output, error) {
	if len(outputs) == 0 {
		return nil, errors.New("no outputs asked")
	}
	if len(input) == 0 || len(shape) == 0 {
		return nil, errors.New("empty input")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("session closed")
	}
	s.mu.Unlock()
	r := s.r
	var inVal uintptr
	if err := r.check(r.call(fnCreateTensorWithDataAsOrtValue, s.mem,
		uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)*4),
		uintptr(unsafe.Pointer(&shape[0])), uintptr(len(shape)), tensorFloat,
		uintptr(unsafe.Pointer(&inVal)))); err != nil {
		return nil, err
	}
	defer r.call(fnReleaseValue, inVal)

	cnames := make([][]byte, len(outputs))
	outNames := make([]uintptr, len(outputs))
	for i, n := range outputs {
		cnames[i] = cstr(n)
		outNames[i] = uintptr(unsafe.Pointer(&cnames[i][0]))
	}
	inNames := [1]uintptr{uintptr(unsafe.Pointer(&s.inName[0]))}
	inputs := [1]uintptr{inVal}
	outVals := make([]uintptr, len(outputs))
	err := r.check(r.call(fnRun, s.ptr, 0,
		uintptr(unsafe.Pointer(&inNames[0])), uintptr(unsafe.Pointer(&inputs[0])), 1,
		uintptr(unsafe.Pointer(&outNames[0])), uintptr(len(outputs)), uintptr(unsafe.Pointer(&outVals[0]))))
	runtime.KeepAlive(input)
	runtime.KeepAlive(shape)
	runtime.KeepAlive(s.inName)
	runtime.KeepAlive(cnames)
	runtime.KeepAlive(outNames)
	defer func() {
		for _, v := range outVals {
			if v != 0 {
				r.call(fnReleaseValue, v)
			}
		}
	}()
	if err != nil {
		return nil, err
	}
	out := make([]Output, len(outputs))
	for i, v := range outVals {
		if v == 0 {
			return nil, fmt.Errorf("output %s missing", outputs[i])
		}
		data, dims, err := r.tensorFloats(v)
		if err != nil {
			return nil, fmt.Errorf("output %s: %w", outputs[i], err)
		}
		out[i] = Output{Name: outputs[i], Data: data, Shape: dims}
	}
	return out, nil
}

// tensorFloats copies a float32 tensor value's data out with its shape.
func (r *Runtime) tensorFloats(val uintptr) ([]float32, []int64, error) {
	var info uintptr
	if err := r.check(r.call(fnGetTensorTypeAndShape, val, uintptr(unsafe.Pointer(&info)))); err != nil {
		return nil, nil, err
	}
	defer r.call(fnReleaseTensorTypeAndShapeInfo, info)
	dims, err := r.dims(info)
	if err != nil {
		return nil, nil, err
	}
	n := int64(1)
	for _, d := range dims {
		n *= d
	}
	if n < 0 {
		return nil, nil, errors.New("negative tensor size")
	}
	var data uintptr
	if err := r.check(r.call(fnGetTensorMutableData, val, uintptr(unsafe.Pointer(&data)))); err != nil {
		return nil, nil, err
	}
	out := make([]float32, n)
	if n > 0 {
		copy(out, unsafe.Slice((*float32)(cptr(data)), n))
	}
	return out, dims, nil
}

package ort

// wave16-semantic: int64 inputs (token ids for the CLIP text tower) and the
// session's input/output names. Kept in its own file so ort.go's float32
// Run stays exactly as it was.

import (
	"errors"
	"runtime"
	"unsafe"
)

const tensorInt64 = 7 // ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64

// IONames returns the names of the model's first input and first output
// (the ones Run and RunInt64 use).
func (s *Session) IONames() (input, output string) {
	return string(s.inName[:len(s.inName)-1]), string(s.outName[:len(s.outName)-1])
}

// RunInt64 feeds one int64 tensor of shape (for example token ids,
// [1, 77]) to the first input and returns the first output's float32 data
// and shape. The input slice must not be modified during the call.
func (s *Session) RunInt64(input []int64, shape []int64) ([]float32, []int64, error) {
	if len(input) == 0 || len(shape) == 0 {
		return nil, nil, errors.New("empty input")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, errors.New("session closed")
	}
	s.mu.Unlock()
	r := s.r
	var inVal uintptr
	if err := r.check(r.call(fnCreateTensorWithDataAsOrtValue, s.mem,
		uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)*8),
		uintptr(unsafe.Pointer(&shape[0])), uintptr(len(shape)), tensorInt64,
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
	return s.readFloatOutput(outVal)
}

// readFloatOutput copies a float32 output tensor out of ONNX Runtime.
func (s *Session) readFloatOutput(outVal uintptr) ([]float32, []int64, error) {
	r := s.r
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

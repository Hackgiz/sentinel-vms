package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"sentinel-linux/internal/detect"
)

// A custom detector is custom.onnx plus custom.json in the models
// directory, supplied by hand:
//
//	{"family": "yolox" | "ultralytics", "input": 640, "labels": {"0": "person", "2": "car"}}
//
// Labels map to kinds with detect.KindOf; other labels are ignored.
const (
	CustomModelFile = "custom.onnx"
	CustomSpecFile  = "custom.json"
)

// CustomSpec is custom.json.
type CustomSpec struct {
	Family string            `json:"family"`
	Input  int               `json:"input"`
	Labels map[string]string `json:"labels"`
}

// ReadCustom parses custom.json into a detector spec.
func ReadCustom(dataDir string) (detect.ModelSpec, error) {
	raw, err := os.ReadFile(filepath.Join(Dir(dataDir), CustomSpecFile))
	if err != nil {
		return detect.ModelSpec{}, err
	}
	return ParseCustom(raw)
}

// ParseCustom checks a custom.json and returns its detector spec.
func ParseCustom(raw []byte) (detect.ModelSpec, error) {
	var c CustomSpec
	if err := json.Unmarshal(raw, &c); err != nil {
		return detect.ModelSpec{}, fmt.Errorf("%s: %w", CustomSpecFile, err)
	}
	if c.Family != detect.FamilyYOLOX && c.Family != detect.FamilyUltralytics {
		return detect.ModelSpec{}, fmt.Errorf("%s: family must be %q or %q", CustomSpecFile, detect.FamilyYOLOX, detect.FamilyUltralytics)
	}
	if c.Input < 64 || c.Input > 1536 || c.Input%32 != 0 {
		return detect.ModelSpec{}, fmt.Errorf("%s: input must be a multiple of 32 from 64 to 1536", CustomSpecFile)
	}
	names := map[int]string{}
	for k, v := range c.Labels {
		i, err := strconv.Atoi(k)
		if err != nil || i < 0 || i > 4096 {
			return detect.ModelSpec{}, fmt.Errorf("%s: label key %q is not a class index", CustomSpecFile, k)
		}
		names[i] = v
	}
	labels := detect.LabelsFrom(names)
	if len(labels) == 0 {
		return detect.ModelSpec{}, errors.New(CustomSpecFile + ": no label Sentinel uses (person, car, truck, bus, motorcycle, an animal, package)")
	}
	return detect.ModelSpec{Family: c.Family, Input: c.Input, Labels: labels}, nil
}

// Detector resolves a detector catalog id ("" = yolox-tiny) to its model
// file and spec. The error says why it can't be used (unknown, not a
// detector, missing, corrupt, bad custom.json).
func Detector(dataDir, id string) (path string, spec detect.ModelSpec, err error) {
	id = NormDetector(id)
	e, ok := Lookup(id)
	if !ok {
		return "", spec, fmt.Errorf("unknown model %q", id)
	}
	if e.Purpose != PurposeDetector {
		return "", spec, fmt.Errorf("%s is not a detector", e.Title)
	}
	switch st := entryState(dataDir, e); st {
	case StateInstalled:
	case StateCorrupt:
		if id == IDCustom {
			_, err := ReadCustom(dataDir)
			return "", spec, err
		}
		return "", spec, fmt.Errorf("%s is corrupt (SHA-256 mismatch); download it again", e.Title)
	default:
		return "", spec, fmt.Errorf("%s is not installed (%s)", e.Title, filepath.Join(Dir(dataDir), e.File))
	}
	if id == IDCustom {
		spec, err = ReadCustom(dataDir)
		return filepath.Join(Dir(dataDir), CustomModelFile), spec, err
	}
	return Path(dataDir, id), detect.ModelSpec{Family: e.Family, Input: e.Input, Labels: detect.COCOLabels()}, nil
}

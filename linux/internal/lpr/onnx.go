package lpr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"

	"sentinel-linux/internal/ort"

	"sentinel-linux/internal/modelpath"
)

// Environment overrides; without them the files are looked up under
// <data>/models by their catalog names.
const (
	DetModelEnv = "SENTINEL_PLATE_DET_MODEL"
	RecModelEnv = "SENTINEL_PLATE_REC_MODEL"
	DictEnv     = "SENTINEL_PLATE_DICT"
)

// Catalog file names (models catalog: plate-det, plate-rec).
const (
	DetModelFile = "plate_det.onnx"
	RecModelFile = "plate_rec.onnx"
	DictFile     = "plate_rec.txt"
)

// Paths returns where the three files are expected.
func Paths(dataDir string) (det, rec, dict string) {
	pick := func(env, name string) string {
		if p := os.Getenv(env); p != "" {
			return p
		}
		return modelpath.Find(dataDir, name)
	}
	return pick(DetModelEnv, DetModelFile), pick(RecModelEnv, RecModelFile), pick(DictEnv, DictFile)
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// Missing names the files that are not installed ("" when all are).
func Missing(dataDir string) string {
	det, rec, dict := Paths(dataDir)
	for _, p := range []string{det, rec, dict} {
		if !isFile(p) {
			return p
		}
	}
	return ""
}

// ModelID is a model's identity: the first 16 hex digits of the file's
// SHA-256.
func ModelID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16], nil
}

// sessionNet adapts an ort.Session to Net.
type sessionNet struct{ s *ort.Session }

func (n sessionNet) Run(input []float32, shape []int64) ([]float32, []int64, error) {
	return n.s.Run(input, shape)
}

// Open loads the plate models. It returns (nil, reason) when plate reading
// is unavailable (a file missing, no ONNX Runtime, a model that fails to
// load or run); the reason names what is missing. It never panics on a bad
// model.
func Open(dataDir, ortLib string) (r Reader, reason string) {
	defer func() {
		if p := recover(); p != nil {
			r, reason = nil, fmt.Sprintf("plate models failed to start: %v", p)
		}
	}()
	det, rec, dictPath := Paths(dataDir)
	switch {
	case !isFile(det):
		return nil, "no plate detection model installed (" + det + ")"
	case !isFile(rec):
		return nil, "no plate recognition model installed (" + rec + ")"
	case !isFile(dictPath):
		return nil, "no plate recognition dictionary installed (" + dictPath + ")"
	case ortLib == "":
		return nil, "ONNX Runtime library not found"
	}
	raw, err := os.ReadFile(dictPath)
	if err != nil {
		return nil, "plate dictionary could not be read: " + err.Error()
	}
	dict := ParseDict(string(raw))
	detID, err := ModelID(det)
	if err != nil {
		return nil, "plate detection model could not be read: " + err.Error()
	}
	recID, err := ModelID(rec)
	if err != nil {
		return nil, "plate recognition model could not be read: " + err.Error()
	}
	rt, err := ort.Load(ortLib)
	if err != nil {
		return nil, "ONNX Runtime unavailable: " + err.Error()
	}
	ds, err := rt.NewSession(det, 1)
	if err != nil {
		return nil, "plate detection model could not be loaded: " + err.Error()
	}
	rs, err := rt.NewSession(rec, 1)
	if err != nil {
		ds.Close()
		return nil, "plate recognition model could not be loaded: " + err.Error()
	}
	return Verify(sessionNet{ds}, sessionNet{rs}, dict, detID, recID, func() { ds.Close(); rs.Close() })
}

// Verify wires the networks and proves them on a synthetic image (a light
// plate with dark bars) before any event uses them: both must run and give
// outputs of the expected shape. Finding no plate is fine. On failure closer
// (if any) runs and the reason comes back.
func Verify(det, rec Net, dict []string, detID, recID string, closer func()) (Reader, string) {
	fail := func(msg string) (Reader, string) {
		if closer != nil {
			closer()
		}
		return nil, msg
	}
	e, err := NewEngine(det, rec, dict, detID, recID)
	if err != nil {
		return fail("plate models: " + err.Error())
	}
	img := WarmupImage()
	if _, err := e.detect(toBGR(img), image.Rect(0, 0, 200, 100)); err != nil {
		return fail("plate detection model failed a test run: " + err.Error())
	}
	if _, _, err := e.recognize(toBGR(img), image.Rect(20, 30, 180, 70)); err != nil {
		return fail("plate recognition model failed a test run: " + err.Error())
	}
	return e, ""
}

// WarmupImage is a 200×100 gray scene with a white plate carrying five
// dark bars.
func WarmupImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			c := color.RGBA{90, 90, 90, 255}
			if x >= 20 && x < 180 && y >= 30 && y < 70 {
				c = color.RGBA{240, 240, 240, 255}
				if y >= 38 && y < 62 && (x-30)%30 < 14 && x >= 30 && x < 170 {
					c = color.RGBA{20, 20, 20, 255}
				}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

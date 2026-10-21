package faces

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"sync"

	"sentinel-linux/internal/ort"
	"sentinel-linux/internal/reid"

	"sentinel-linux/internal/modelpath"
)

// Model files: catalog names under <data>/models, or these environment
// overrides.
const (
	DetectModelEnv  = "SENTINEL_FACE_DET_MODEL"
	EmbedModelEnv   = "SENTINEL_FACE_REC_MODEL"
	DetectModelFile = "face_det.onnx" // OpenCV Zoo face_detection_yunet_2023mar.onnx
	EmbedModelFile  = "face_rec.onnx" // OpenCV Zoo face_recognition_sface_2021dec.onnx
)

// ModelPaths returns where the two models are expected and whether each
// exists.
func ModelPaths(dataDir string) (det, rec string, detOK, recOK bool) {
	det, rec = os.Getenv(DetectModelEnv), os.Getenv(EmbedModelEnv)
	if det == "" {
		det = modelpath.Find(dataDir, DetectModelFile)
	}
	if rec == "" {
		rec = modelpath.Find(dataDir, EmbedModelFile)
	}
	return det, rec, isFile(det), isFile(rec)
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// Missing names the missing model files ("" when both are there), for the
// status and for the 409 when someone turns faces on without them.
func Missing(dataDir string) string {
	det, rec, detOK, recOK := ModelPaths(dataDir)
	switch {
	case !detOK && !recOK:
		return "face models not installed (" + det + ", " + rec + ")"
	case !detOK:
		return "face detection model not installed (" + det + ")"
	case !recOK:
		return "face recognition model not installed (" + rec + ")"
	}
	return ""
}

// Open loads both models. It returns (nil, reason) when face recognition is
// unavailable (a model file missing, no ONNX Runtime, a model that fails to
// load or its test run); it never panics on a bad model.
func Open(dataDir, ortLib string) (e Engine, reason string) {
	defer func() {
		if r := recover(); r != nil {
			e, reason = nil, fmt.Sprintf("face models failed to start: %v", r)
		}
	}()
	if m := Missing(dataDir); m != "" {
		return nil, m
	}
	det, rec, _, _ := ModelPaths(dataDir)
	if ortLib == "" {
		return nil, "ONNX Runtime library not found"
	}
	detID, err := reid.ModelID(det)
	if err != nil {
		return nil, "face detection model could not be read: " + err.Error()
	}
	recID, err := reid.ModelID(rec)
	if err != nil {
		return nil, "face recognition model could not be read: " + err.Error()
	}
	rt, err := ort.Load(ortLib)
	if err != nil {
		return nil, "ONNX Runtime unavailable: " + err.Error()
	}
	ds, err := rt.NewQuietSession(det, 1)
	if err != nil {
		return nil, "face detection model could not be loaded: " + err.Error()
	}
	rs, err := rt.NewQuietSession(rec, 1)
	if err != nil {
		ds.Close()
		return nil, "face recognition model could not be loaded: " + err.Error()
	}
	closeAll := func() { ds.Close(); rs.Close() }
	dn, err := newDetNet(ds)
	if err != nil {
		closeAll()
		return nil, "face detection model is not a supported YuNet export: " + err.Error()
	}
	return Verify(dn, &recNet{rs}, detID, recID, closeAll)
}

// DetNet runs the detector: one CHW BGR input of the net's size in, the
// twelve YuNet outputs (yunetOutputs order) out.
type DetNet interface {
	Size() (w, h int)
	Run(input []float32) ([][]float32, error)
}

// RecNet runs the recognizer: one aligned 1×3×112×112 RGB input in, the raw
// embedding out.
type RecNet interface {
	Run(input []float32) ([]float32, error)
}

type detNet struct {
	sess  *ort.Session
	w, h  int
	names []string
}

func newDetNet(s *ort.Session) (*detNet, error) {
	shape, err := s.InputShape()
	if err != nil {
		return nil, err
	}
	if len(shape) != 4 || (shape[1] != 3 && shape[1] != -1) {
		return nil, fmt.Errorf("input shape %v, want [1 3 H W]", shape)
	}
	w, h := int(shape[3]), int(shape[2])
	if w <= 0 || h <= 0 { // dynamic: use OpenCV Zoo's default
		w, h = 640, 640
	}
	if w%32 != 0 || h%32 != 0 {
		return nil, fmt.Errorf("input %dx%d is not a multiple of 32", w, h)
	}
	have, err := s.OutputNames()
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, n := range have {
		set[n] = true
	}
	want := yunetOutputs()
	for _, n := range want {
		if !set[n] {
			return nil, fmt.Errorf("output %s missing (has %v)", n, have)
		}
	}
	return &detNet{sess: s, w: w, h: h, names: want}, nil
}

func (d *detNet) Size() (int, int) { return d.w, d.h }

func (d *detNet) Run(input []float32) ([][]float32, error) {
	outs, err := d.sess.RunOutputs(input, []int64{1, 3, int64(d.h), int64(d.w)}, d.names)
	if err != nil {
		return nil, err
	}
	data := make([][]float32, len(outs))
	for i, o := range outs {
		data[i] = o.Data
	}
	return data, nil
}

type recNet struct{ sess *ort.Session }

func (r *recNet) Run(input []float32) ([]float32, error) {
	out, _, err := r.sess.Run(input, []int64{1, 3, alignSize, alignSize})
	return out, err
}

// modelEngine is the production Engine.
type modelEngine struct {
	det          DetNet
	rec          RecNet
	detID, recID string
	mu           sync.Mutex // one inference at a time: bounds CPU next to the video work
}

func (m *modelEngine) DetectModel() string { return m.detID }
func (m *modelEngine) EmbedModel() string  { return m.recID }

func (m *modelEngine) Find(img image.Image) ([]Face, error) {
	if img == nil || img.Bounds().Dx() < 2 || img.Bounds().Dy() < 2 {
		return nil, ErrNoFace
	}
	rgb := toRGB(img)
	w, h := m.det.Size()
	in, scale := detInput(rgb, w, h)
	m.mu.Lock()
	outs, err := m.det.Run(in)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if len(outs) != 4*len(yunetStrides) {
		return nil, fmt.Errorf("face detector returned %d outputs, want %d", len(outs), 4*len(yunetStrides))
	}
	return decodeYuNet(outs, w, h, scale, rgb.w, rgb.h), nil
}

func (m *modelEngine) Embed(img image.Image, f Face) ([]float32, error) {
	if img == nil || img.Bounds().Empty() {
		return nil, ErrNoFace
	}
	rgb := toRGB(img)
	in := alignInput(rgb, pixelLandmarks(rgb, f))
	m.mu.Lock()
	out, err := m.rec.Run(in)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if len(out) != Dim {
		return nil, fmt.Errorf("face recognition model returned %d values, want %d", len(out), Dim)
	}
	return reid.Normalize(append([]float32(nil), out...))
}

// Verify wires the two networks and proves them before any event uses
// them: the detector must run on a blank frame and find nothing, and the
// recognizer must return a finite, non-zero 128-d vector for a synthetic
// face. On failure closer (if any) runs and the reason comes back.
func Verify(det DetNet, rec RecNet, detID, recID string, closer func()) (Engine, string) {
	fail := func(msg string) (Engine, string) {
		if closer != nil {
			closer()
		}
		return nil, msg
	}
	if det == nil || rec == nil || detID == "" || recID == "" {
		return fail("no face model")
	}
	m := &modelEngine{det: det, rec: rec, detID: detID, recID: recID}
	blank := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for i := range blank.Pix {
		blank.Pix[i] = 128
	}
	if _, err := m.Find(blank); err != nil {
		return fail("face detection model failed a test run: " + err.Error())
	}
	img, face := SyntheticFace(160)
	v, err := m.Embed(img, face)
	if err != nil {
		return fail("face recognition model failed a test run: " + err.Error())
	}
	if len(v) != Dim {
		return fail(fmt.Sprintf("face recognition model returned %d values, want %d", len(v), Dim))
	}
	return m, ""
}

// SyntheticFace draws a crude face (skin oval, dark eyes and mouth) on a
// size×size gray image and returns it with landmarks where it drew them:
// the warm-up input, and a fixture for tests. It is not a real face; a
// detector need not find it.
func SyntheticFace(size int) (image.Image, Face) {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	cx, cy := s/2, s/2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			c := color.RGBA{110, 120, 130, 255}
			dx, dy := (float64(x)-cx)/(0.32*s), (float64(y)-cy)/(0.42*s)
			if dx*dx+dy*dy <= 1 {
				c = color.RGBA{215, 170, 140, 255}
			}
			img.Set(x, y, c)
		}
	}
	lm := [5][2]float64{{0.38, 0.42}, {0.62, 0.42}, {0.5, 0.55}, {0.41, 0.68}, {0.59, 0.68}}
	dot := func(p [2]float64, r float64) {
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				if math.Hypot(float64(x)-p[0]*s, float64(y)-p[1]*s) <= r*s {
					img.Set(x, y, color.RGBA{40, 30, 30, 255})
				}
			}
		}
	}
	for _, p := range lm[:2] {
		dot(p, 0.04)
	}
	dot([2]float64{0.5, 0.69}, 0.05)
	return img, Face{Box: [4]float64{0.18, 0.08, 0.82, 0.92}, Score: 1, Landmarks: lm}
}

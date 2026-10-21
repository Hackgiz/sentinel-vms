package detect

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sentinel-linux/internal/ort"
)

// InputSize is YOLOX-Tiny's square input, the default model size. Frames
// arrive letterboxed to the TOP-LEFT and padded with gray 114, as YOLOX's
// own preprocessing does; a bigger model (ModelSpec.Input) gets bigger
// frames the same way.
const InputSize = 416

// Object is one detection, box normalized to the original frame (0..1).
type Object struct {
	Kind  string     `json:"kind"`  // Person | Vehicle | Animal | Package
	Label string     `json:"label"` // COCO class, e.g. "truck"
	Score float64    `json:"score"`
	Box   [4]float64 `json:"box"` // x1, y1, x2, y2
	// TrackID is the camera Tracker's id for this object (0 = untracked).
	TrackID int `json:"trackId,omitempty"`
}

// cocoKinds maps the COCO classes Sentinel cares about to event kinds.
// Bicycles are deliberately ignored (too noisy for alarms); birds are
// Animals with their own higher bar (labelMinScore) and never alarm.
var cocoKinds = map[int]struct{ kind, label string }{
	0: {"Person", "person"},
	2: {"Vehicle", "car"}, 3: {"Vehicle", "motorcycle"}, 5: {"Vehicle", "bus"}, 7: {"Vehicle", "truck"},
	14: {"Animal", "bird"}, 15: {"Animal", "cat"}, 16: {"Animal", "dog"}, 17: {"Animal", "horse"}, 18: {"Animal", "sheep"},
	19: {"Animal", "cow"}, 21: {"Animal", "bear"},
}

// MinScore per kind (objectness × class probability).
var MinScore = map[string]float64{"Person": 0.45, "Vehicle": 0.5, "Animal": 0.5, "Package": 0.5}

// Model families (ModelSpec.Family).
const (
	// FamilyYOLOX is Megvii's YOLOX export: raw 0-255 BGR in, [1, N, 5+C]
	// out on the 8/16/32 stride grid (centre offsets, log sizes, objectness,
	// class scores).
	FamilyYOLOX = "yolox"
	// FamilyUltralytics is a YOLOv8/v9/v11 export: RGB scaled to 0-1 in,
	// [1, 4+C, N] (or [1, N, 4+C]) out with absolute centre boxes and class
	// scores, no objectness.
	FamilyUltralytics = "ultralytics"
)

// Kind is what one model class becomes: the event kind and its label.
type Kind struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// ModelSpec describes a detector model: how to feed it and read it.
type ModelSpec struct {
	Family string       // FamilyYOLOX | FamilyUltralytics
	Input  int          // square input side, pixels
	Labels map[int]Kind // class index → kind; classes not listed are ignored
}

// COCOLabels is the class map of the COCO-trained catalog models.
func COCOLabels() map[int]Kind {
	m := make(map[int]Kind, len(cocoKinds))
	for i, k := range cocoKinds {
		m[i] = Kind{Kind: k.kind, Label: k.label}
	}
	return m
}

// DefaultSpec is the bundled YOLOX-Tiny.
func DefaultSpec() ModelSpec {
	return ModelSpec{Family: FamilyYOLOX, Input: InputSize, Labels: COCOLabels()}
}

// KindOf maps a class label (any model's) to a Sentinel event kind; ""
// means the class is ignored.
func KindOf(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "person":
		return "Person"
	case "car", "truck", "bus", "motorcycle":
		return "Vehicle"
	case "cat", "dog", "horse", "sheep", "cow", "bear", "bird":
		return "Animal"
	case "package", "parcel", "box":
		return "Package"
	}
	return ""
}

// LabelsFrom builds a class map from index → label names, keeping only
// the labels KindOf knows.
func LabelsFrom(names map[int]string) map[int]Kind {
	m := map[int]Kind{}
	for i, n := range names {
		if k := KindOf(n); k != "" {
			m[i] = Kind{Kind: k, Label: strings.ToLower(strings.TrimSpace(n))}
		}
	}
	return m
}

// normalize fills defaults and checks the spec.
func (s ModelSpec) normalize() (ModelSpec, error) {
	if s.Family == "" {
		s.Family = FamilyYOLOX
	}
	if s.Input == 0 {
		s.Input = InputSize
	}
	if s.Labels == nil {
		s.Labels = COCOLabels()
	}
	switch {
	case s.Family != FamilyYOLOX && s.Family != FamilyUltralytics:
		return s, errors.New("unknown model family " + strconv.Quote(s.Family))
	case s.Input < 64 || s.Input > 1536 || s.Input%32 != 0:
		return s, errors.New("model input must be a multiple of 32 from 64 to 1536")
	case len(s.Labels) == 0:
		return s, errors.New("the model has no classes Sentinel uses")
	}
	return s, nil
}

// labelMinScore raises the bar for single labels within a kind.
var labelMinScore = map[string]float64{"bird": 0.6}

type ObjectDetector struct {
	sess    *ort.Session
	spec    ModelSpec
	modelID string        // models catalog id, "" = unknown
	grids   []gridCell    // YOLOX only
	maxCls  int           // highest class index in spec.Labels
	sem     chan struct{} // bounds concurrent inferences (CPU)
	bufs    sync.Pool     // *[]float32 of 3×Input×Input

	// Acceleration (detector_accel.go): engine, when set, replaces the
	// built-in YOLOX/ONNX path (e.g. a future Coral or Hailo backend).
	engine Backend
	info   DetectorInfo
	stats  inferStats
	rate   atomic.Int32 // DetectorInfo.Rate (set by the recorder)
}

type gridCell struct{ x, y, stride float32 }

// NewObjectDetector loads ONNX Runtime from libPath and the bundled
// YOLOX-Tiny model. maxParallel bounds simultaneous inferences across all
// cameras.
func NewObjectDetector(libPath, modelPath string, threads, maxParallel int) (*ObjectDetector, error) {
	return NewObjectDetectorSpec(libPath, modelPath, DefaultSpec(), threads, maxParallel)
}

// NewObjectDetectorSpec is NewObjectDetector for any model spec.
func NewObjectDetectorSpec(libPath, modelPath string, spec ModelSpec, threads, maxParallel int) (*ObjectDetector, error) {
	spec, err := spec.normalize()
	if err != nil {
		return nil, err
	}
	rt, err := ort.Load(libPath)
	if err != nil {
		return nil, err
	}
	sess, err := rt.NewSession(modelPath, threads)
	if err != nil {
		return nil, err
	}
	d := newObjectDetector(sess, spec, maxParallel)
	d.info = DetectorInfo{Engine: EngineCPU, Library: libPath}
	d.info.Providers, _ = rt.Providers()
	return d, nil
}

// newObjectDetector wraps a session; spec must be normalized.
func newObjectDetector(sess *ort.Session, spec ModelSpec, maxParallel int) *ObjectDetector {
	if maxParallel < 1 {
		maxParallel = 1
	}
	d := &ObjectDetector{sess: sess, spec: spec, sem: make(chan struct{}, maxParallel)}
	n := 3 * spec.Input * spec.Input
	d.bufs.New = func() any { b := make([]float32, n); return &b }
	for c := range spec.Labels {
		if c > d.maxCls {
			d.maxCls = c
		}
	}
	if spec.Family == FamilyYOLOX {
		for _, stride := range []int{8, 16, 32} {
			n := spec.Input / stride
			for y := 0; y < n; y++ {
				for x := 0; x < n; x++ {
					d.grids = append(d.grids, gridCell{float32(x), float32(y), float32(stride)})
				}
			}
		}
	}
	return d
}

// SetModelID records the catalog id of the loaded model (DetectorInfo.ModelID).
func (d *ObjectDetector) SetModelID(id string) {
	d.modelID = id
	d.info.ModelID = id
}

// Input is the side of the square frames the detector takes (InputSize
// for a nil detector or a Backend).
func (d *ObjectDetector) Input() int {
	if d == nil || d.engine != nil || d.spec.Input == 0 {
		return InputSize
	}
	return d.spec.Input
}

// ContentSize is the area a srcW×srcH frame occupies after letterboxing
// to this detector's input.
func (d *ObjectDetector) ContentSize(srcW, srcH int) (int, int) {
	return ContentSizeFor(d.Input(), srcW, srcH)
}

// Detect runs the model on an Input×Input BGR24 letterboxed frame.
// contentW/H is the size of the real image inside the frame (the rest is
// padding).
func (d *ObjectDetector) Detect(bgr []byte, contentW, contentH int) ([]Object, error) {
	size := d.Input()
	plane := size * size
	if len(bgr) != 3*plane {
		return nil, fmt.Errorf("frame must be %dx%d BGR24", size, size)
	}
	d.sem <- struct{}{}
	defer func() { <-d.sem }()
	defer d.stats.since(time.Now())
	if d.engine != nil {
		return d.engine.Detect(bgr, contentW, contentH)
	}

	bp := d.bufs.Get().(*[]float32)
	defer d.bufs.Put(bp)
	in := *bp
	if d.spec.Family == FamilyUltralytics {
		// HWC BGR bytes → CHW RGB float32 in 0..1.
		const k = 1.0 / 255
		for i := 0; i < plane; i++ {
			in[i] = float32(bgr[3*i+2]) * k
			in[plane+i] = float32(bgr[3*i+1]) * k
			in[2*plane+i] = float32(bgr[3*i]) * k
		}
	} else {
		// HWC BGR bytes → CHW float32 (YOLOX takes raw 0-255 BGR, no normalization).
		for i := 0; i < plane; i++ {
			in[i] = float32(bgr[3*i])
			in[plane+i] = float32(bgr[3*i+1])
			in[2*plane+i] = float32(bgr[3*i+2])
		}
	}
	out, dims, err := d.sess.Run(in, []int64{1, 3, int64(size), int64(size)})
	if err != nil {
		return nil, err
	}
	if contentW <= 0 || contentH <= 0 {
		contentW, contentH = size, size
	}
	if d.spec.Family == FamilyUltralytics {
		return decodeUltralytics(out, dims, d.spec.Labels, d.maxCls, contentW, contentH)
	}
	if len(dims) != 3 || int(dims[1]) != len(d.grids) || int(dims[2]) < 5+d.maxCls+1 {
		return nil, fmt.Errorf("unexpected YOLOX output shape %v for input %d", dims, size)
	}
	return d.decode(out, int(dims[2]), contentW, contentH), nil
}

func (d *ObjectDetector) decode(out []float32, stride, contentW, contentH int) []Object {
	if contentW <= 0 || contentH <= 0 {
		contentW, contentH = d.Input(), d.Input()
	}
	var cands []Object
	for i, g := range d.grids {
		row := out[i*stride : (i+1)*stride]
		obj := row[4]
		if obj < 0.2 {
			continue
		}
		best, bestScore := -1, float32(0)
		for c := range d.spec.Labels {
			if 5+c < stride && row[5+c] > bestScore {
				best, bestScore = c, row[5+c]
			}
		}
		if best < 0 {
			continue
		}
		k := d.spec.Labels[best]
		score := float64(obj * bestScore)
		if score < minScore(k.Kind) || score < labelMinScore[k.Label] {
			continue
		}
		cx := (row[0] + g.x) * g.stride
		cy := (row[1] + g.y) * g.stride
		w := float32(math.Exp(float64(row[2]))) * g.stride
		h := float32(math.Exp(float64(row[3]))) * g.stride
		if o, ok := makeObject(k, score, cx, cy, w, h, contentW, contentH); ok {
			cands = append(cands, o)
		}
	}
	return nms(cands, 0.45)
}

// decodeUltralytics reads a YOLOv8/v9/v11 head: [1, 4+C, N] (the usual
// export) or [1, N, 4+C], rows of cx, cy, w, h in input pixels then C
// class scores (already sigmoid).
func decodeUltralytics(out []float32, dims []int64, labels map[int]Kind, maxCls, contentW, contentH int) ([]Object, error) {
	if len(dims) != 3 || dims[0] != 1 {
		return nil, fmt.Errorf("unexpected ultralytics output shape %v", dims)
	}
	a, b := int(dims[1]), int(dims[2])
	// The attribute axis is the short one (84 for COCO vs 8400 anchors).
	chFirst := a <= b
	attrs, n := a, b
	if !chFirst {
		attrs, n = b, a
	}
	if attrs < 5+maxCls || len(out) < attrs*n {
		return nil, fmt.Errorf("ultralytics output %v has too few classes", dims)
	}
	at := func(i, j int) float32 { // anchor i, attribute j
		if chFirst {
			return out[j*n+i]
		}
		return out[i*attrs+j]
	}
	var cands []Object
	for i := 0; i < n; i++ {
		best, bestScore := -1, float32(0)
		for c := range labels {
			if 4+c < attrs {
				if s := at(i, 4+c); s > bestScore {
					best, bestScore = c, s
				}
			}
		}
		if best < 0 {
			continue
		}
		k := labels[best]
		score := float64(bestScore)
		if score < minScore(k.Kind) || score < labelMinScore[k.Label] {
			continue
		}
		if o, ok := makeObject(k, score, at(i, 0), at(i, 1), at(i, 2), at(i, 3), contentW, contentH); ok {
			cands = append(cands, o)
		}
	}
	return nms(cands, 0.45), nil
}

// minScore is MinScore for a kind (0.5 for one without an entry).
func minScore(kind string) float64 {
	if v, ok := MinScore[kind]; ok {
		return v
	}
	return 0.5
}

// makeObject turns a centre box in input pixels into an Object normalized
// to the content; false when it lies in the padding.
func makeObject(k Kind, score float64, cx, cy, w, h float32, contentW, contentH int) (Object, bool) {
	box := [4]float64{
		clamp01(float64(cx-w/2) / float64(contentW)), clamp01(float64(cy-h/2) / float64(contentH)),
		clamp01(float64(cx+w/2) / float64(contentW)), clamp01(float64(cy+h/2) / float64(contentH)),
	}
	if box[2]-box[0] < 0.005 || box[3]-box[1] < 0.005 {
		return Object{}, false // entirely in the padding
	}
	return Object{Kind: k.Kind, Label: k.Label, Score: math.Round(score*100) / 100, Box: box}, true
}

// nms keeps the best box among overlapping same-kind detections.
func nms(c []Object, iouThr float64) []Object {
	sort.Slice(c, func(i, j int) bool { return c[i].Score > c[j].Score })
	var keep []Object
	for _, o := range c {
		ok := true
		for _, k := range keep {
			if k.Kind == o.Kind && IoU(k.Box, o.Box) > iouThr {
				ok = false
				break
			}
		}
		if ok {
			keep = append(keep, o)
		}
	}
	return keep
}

// IoU is intersection-over-union of two normalized boxes.
func IoU(a, b [4]float64) float64 {
	ix := math.Max(0, math.Min(a[2], b[2])-math.Max(a[0], b[0]))
	iy := math.Max(0, math.Min(a[3], b[3])-math.Max(a[1], b[1]))
	inter := ix * iy
	union := (a[2]-a[0])*(a[3]-a[1]) + (b[2]-b[0])*(b[3]-b[1]) - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// ContentSize is the area a srcW×srcH frame occupies after letterboxing
// to InputSize.
func ContentSize(srcW, srcH int) (int, int) { return ContentSizeFor(InputSize, srcW, srcH) }

// ContentSizeFor is ContentSize for a size×size model input.
func ContentSizeFor(size, srcW, srcH int) (int, int) {
	if srcW <= 0 || srcH <= 0 {
		srcW, srcH = 16, 9
	}
	r := math.Min(float64(size)/float64(srcW), float64(size)/float64(srcH))
	return int(math.Round(float64(srcW) * r)), int(math.Round(float64(srcH) * r))
}

// FindObjectModel locates the ONNX Runtime library and the YOLOX model: next
// to the sentinel binary (lib/, models/), then in <data>/bin.
func FindObjectModel(dataDir string) (lib, model string) {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	dirs = append(dirs, filepath.Join(dataDir, "bin"))
	libNames := []string{"libonnxruntime.so", "libonnxruntime.so.1", "libonnxruntime.dylib"}
	for _, d := range dirs {
		for _, sub := range []string{"lib", "."} {
			for _, n := range libNames {
				if p := filepath.Join(d, sub, n); fileExists(p) && lib == "" {
					lib = p
				}
			}
		}
		for _, sub := range []string{"models", "."} {
			if p := filepath.Join(d, sub, "yolox_tiny.onnx"); fileExists(p) && model == "" {
				model = p
			}
		}
	}
	return lib, model
}

// frameSide is the side of a square BGR24 frame of n bytes (0 if none).
func frameSide(n int) int {
	if n <= 0 || n%3 != 0 {
		return 0
	}
	side := int(math.Sqrt(float64(n / 3)))
	for _, s := range []int{side, side + 1} {
		if 3*s*s == n {
			return s
		}
	}
	return 0
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// FrameJPEG encodes the real-image part of a letterboxed square BGR frame
// (any model size; the side comes from the frame's length) as a JPEG,
// outlining boxes (normalized to that content) in Sentinel blue.
func FrameJPEG(bgr []byte, contentW, contentH int, boxes [][4]float64) ([]byte, error) {
	size := frameSide(len(bgr))
	if size == 0 || contentW <= 0 || contentH <= 0 || contentW > size || contentH > size {
		return nil, errors.New("bad frame")
	}
	img := image.NewRGBA(image.Rect(0, 0, contentW, contentH))
	for y := 0; y < contentH; y++ {
		for x := 0; x < contentW; x++ {
			i := 3 * (y*size + x)
			o := img.PixOffset(x, y)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = bgr[i+2], bgr[i+1], bgr[i], 255
		}
	}
	blue := color.RGBA{0x1a, 0x7f, 0xd4, 255}
	for _, b := range boxes {
		x1, y1 := int(b[0]*float64(contentW)), int(b[1]*float64(contentH))
		x2, y2 := int(b[2]*float64(contentW))-1, int(b[3]*float64(contentH))-1
		for t := 0; t < 2; t++ { // 2 px outline
			for x := x1; x <= x2; x++ {
				img.Set(x, y1+t, blue)
				img.Set(x, y2-t, blue)
			}
			for y := y1; y <= y2; y++ {
				img.Set(x1+t, y, blue)
				img.Set(x2-t, y, blue)
			}
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

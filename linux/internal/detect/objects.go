package detect

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"sentinel-linux/internal/ort"
)

// InputSize is YOLOX-Tiny's square input. Frames arrive letterboxed to the
// TOP-LEFT and padded with gray 114, as YOLOX's own preprocessing does.
const InputSize = 416

// Object is one detection, box normalized to the original frame (0..1).
type Object struct {
	Kind  string     `json:"kind"`  // Person | Vehicle | Animal
	Label string     `json:"label"` // COCO class, e.g. "truck"
	Score float64    `json:"score"`
	Box   [4]float64 `json:"box"` // x1, y1, x2, y2
}

// cocoKinds maps the COCO classes Sentinel cares about to event kinds.
// Bicycles and birds are deliberately ignored (too noisy for alarms).
var cocoKinds = map[int]struct{ kind, label string }{
	0: {"Person", "person"},
	2: {"Vehicle", "car"}, 3: {"Vehicle", "motorcycle"}, 5: {"Vehicle", "bus"}, 7: {"Vehicle", "truck"},
	15: {"Animal", "cat"}, 16: {"Animal", "dog"}, 17: {"Animal", "horse"}, 18: {"Animal", "sheep"},
	19: {"Animal", "cow"}, 21: {"Animal", "bear"},
}

// MinScore per kind (objectness × class probability).
var MinScore = map[string]float64{"Person": 0.45, "Vehicle": 0.5, "Animal": 0.5}

type ObjectDetector struct {
	sess  *ort.Session
	grids []gridCell
	sem   chan struct{} // bounds concurrent inferences (CPU)
}

type gridCell struct{ x, y, stride float32 }

// NewObjectDetector loads ONNX Runtime from libPath and the YOLOX model.
// maxParallel bounds simultaneous inferences across all cameras.
func NewObjectDetector(libPath, modelPath string, threads, maxParallel int) (*ObjectDetector, error) {
	rt, err := ort.Load(libPath)
	if err != nil {
		return nil, err
	}
	sess, err := rt.NewSession(modelPath, threads)
	if err != nil {
		return nil, err
	}
	if maxParallel < 1 {
		maxParallel = 1
	}
	d := &ObjectDetector{sess: sess, sem: make(chan struct{}, maxParallel)}
	for _, stride := range []int{8, 16, 32} {
		n := InputSize / stride
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				d.grids = append(d.grids, gridCell{float32(x), float32(y), float32(stride)})
			}
		}
	}
	return d, nil
}

var bufPool = sync.Pool{New: func() any { b := make([]float32, 3*InputSize*InputSize); return &b }}

// Detect runs YOLOX on a 416×416 BGR24 letterboxed frame. contentW/H is the
// size of the real image inside the frame (the rest is padding).
func (d *ObjectDetector) Detect(bgr []byte, contentW, contentH int) ([]Object, error) {
	const plane = InputSize * InputSize
	if len(bgr) != 3*plane {
		return nil, errors.New("frame must be 416x416 BGR24")
	}
	d.sem <- struct{}{}
	defer func() { <-d.sem }()

	bp := bufPool.Get().(*[]float32)
	defer bufPool.Put(bp)
	in := *bp
	// HWC BGR bytes → CHW float32 (YOLOX takes raw 0-255 BGR, no normalization).
	for i := 0; i < plane; i++ {
		in[i] = float32(bgr[3*i])
		in[plane+i] = float32(bgr[3*i+1])
		in[2*plane+i] = float32(bgr[3*i+2])
	}
	out, dims, err := d.sess.Run(in, []int64{1, 3, InputSize, InputSize})
	if err != nil {
		return nil, err
	}
	if len(dims) != 3 || int(dims[1]) != len(d.grids) || dims[2] < 85 {
		return nil, errors.New("unexpected YOLOX output shape")
	}
	return d.decode(out, int(dims[2]), contentW, contentH), nil
}

func (d *ObjectDetector) decode(out []float32, stride, contentW, contentH int) []Object {
	if contentW <= 0 || contentH <= 0 {
		contentW, contentH = InputSize, InputSize
	}
	var cands []Object
	for i, g := range d.grids {
		row := out[i*stride : (i+1)*stride]
		obj := row[4]
		if obj < 0.2 {
			continue
		}
		best, bestScore := -1, float32(0)
		for c := range cocoKinds {
			if s := row[5+c]; s > bestScore {
				best, bestScore = c, s
			}
		}
		if best < 0 {
			continue
		}
		k := cocoKinds[best]
		score := float64(obj * bestScore)
		if score < MinScore[k.kind] {
			continue
		}
		cx := (row[0] + g.x) * g.stride
		cy := (row[1] + g.y) * g.stride
		w := float32(math.Exp(float64(row[2]))) * g.stride
		h := float32(math.Exp(float64(row[3]))) * g.stride
		box := [4]float64{
			clamp01(float64(cx-w/2) / float64(contentW)), clamp01(float64(cy-h/2) / float64(contentH)),
			clamp01(float64(cx+w/2) / float64(contentW)), clamp01(float64(cy+h/2) / float64(contentH)),
		}
		if box[2]-box[0] < 0.005 || box[3]-box[1] < 0.005 {
			continue // entirely in the padding
		}
		cands = append(cands, Object{Kind: k.kind, Label: k.label, Score: math.Round(score*100) / 100, Box: box})
	}
	return nms(cands, 0.45)
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

// ContentSize is the area a srcW×srcH frame occupies after letterboxing.
func ContentSize(srcW, srcH int) (int, int) {
	if srcW <= 0 || srcH <= 0 {
		srcW, srcH = 16, 9
	}
	r := math.Min(float64(InputSize)/float64(srcW), float64(InputSize)/float64(srcH))
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

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// FrameJPEG encodes the real-image part of a letterboxed 416×416 BGR frame as
// a JPEG, outlining boxes (normalized to that content) in Sentinel blue.
func FrameJPEG(bgr []byte, contentW, contentH int, boxes [][4]float64) ([]byte, error) {
	if contentW <= 0 || contentH <= 0 || contentW > InputSize || contentH > InputSize || len(bgr) != 3*InputSize*InputSize {
		return nil, errors.New("bad frame")
	}
	img := image.NewRGBA(image.Rect(0, 0, contentW, contentH))
	for y := 0; y < contentH; y++ {
		for x := 0; x < contentW; x++ {
			i := 3 * (y*InputSize + x)
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

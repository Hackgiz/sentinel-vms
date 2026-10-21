package detect

import (
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKindOf(t *testing.T) {
	for label, want := range map[string]string{
		"person": "Person", "Car": "Vehicle", "truck": "Vehicle", "bus": "Vehicle", "motorcycle": "Vehicle",
		"cat": "Animal", "dog": "Animal", "bird": "Animal", "bear": "Animal",
		"package": "Package", "parcel": "Package", "box": "Package",
		"bicycle": "", "chair": "", "": "",
	} {
		if got := KindOf(label); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", label, got, want)
		}
	}
	if MinScore["Package"] != 0.5 {
		t.Fatal("package min score")
	}
}

func TestSpecNormalize(t *testing.T) {
	s, err := ModelSpec{}.normalize()
	if err != nil || s.Family != FamilyYOLOX || s.Input != InputSize || len(s.Labels) != len(cocoKinds) {
		t.Fatalf("default %+v %v", s, err)
	}
	for _, bad := range []ModelSpec{{Family: "ssd"}, {Input: 100}, {Input: 640, Labels: map[int]Kind{}}} {
		if _, err := bad.normalize(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestContentSizeFor(t *testing.T) {
	if cw, ch := ContentSizeFor(640, 1920, 1080); cw != 640 || ch != 360 {
		t.Fatalf("640: %dx%d", cw, ch)
	}
	var d *ObjectDetector
	if d.Input() != InputSize {
		t.Fatal("nil detector input")
	}
	if cw, ch := d.ContentSize(1920, 1080); cw != 416 || ch != 234 {
		t.Fatalf("nil detector content %dx%d", cw, ch)
	}
	d = newObjectDetector(nil, ModelSpec{Family: FamilyYOLOX, Input: 640, Labels: COCOLabels()}, 1)
	if d.Input() != 640 || len(d.grids) != 80*80+40*40+20*20 {
		t.Fatalf("640 grid: %d cells", len(d.grids))
	}
	if _, err := d.Detect(make([]byte, 3*416*416), 416, 234); err == nil || !strings.Contains(err.Error(), "640x640") {
		t.Fatalf("wrong-size frame: %v", err)
	}
	if info := d.Info(); info.Input != 640 {
		t.Fatalf("info %+v", info)
	}
	d.SetRate(5)
	d.SetModelID("yolox-s")
	if info := d.Info(); info.Rate != 5 || info.ModelID != "yolox-s" {
		t.Fatalf("info %+v", info)
	}
}

func TestFrameJPEGAnySize(t *testing.T) {
	if _, err := FrameJPEG(make([]byte, 3*640*640), 640, 360, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := FrameJPEG(make([]byte, 3*640*640), 641, 360, nil); err == nil {
		t.Fatal("content wider than the frame")
	}
	if _, err := FrameJPEG(make([]byte, 3*640*639), 600, 300, nil); err == nil {
		t.Fatal("non-square frame")
	}
}

func TestDetectArgsSize(t *testing.T) {
	got := strings.Join(detectArgsSize("rtsp://x/cam", DecodePlan{Method: DecodeSoftware}, true, 640), " ")
	if !strings.Contains(got, "scale=640:640:") || !strings.Contains(got, "pad=640:640:0:0") {
		t.Fatalf("args %s", got)
	}
	if a, b := strings.Join(detectArgs("rtsp://x/cam", DecodePlan{Method: DecodeSoftware}, true), " "),
		strings.Join(detectArgsSize("rtsp://x/cam", DecodePlan{Method: DecodeSoftware}, true, InputSize), " "); a != b {
		t.Fatal("detectArgs is not detectArgsSize(InputSize)")
	}
}

// ultralyticsOut builds a [1, 4+C, N] (chFirst) or [1, N, 4+C] head.
func ultralyticsOut(chFirst bool, classes int, anchors [][]float32) ([]float32, []int64) {
	attrs, n := 4+classes, len(anchors)
	out := make([]float32, attrs*n)
	for i, a := range anchors {
		for j := 0; j < attrs; j++ {
			if chFirst {
				out[j*n+i] = a[j]
			} else {
				out[i*attrs+j] = a[j]
			}
		}
	}
	if chFirst {
		return out, []int64{1, int64(attrs), int64(n)}
	}
	return out, []int64{1, int64(n), int64(attrs)}
}

func TestDecodeUltralytics(t *testing.T) {
	labels := LabelsFrom(map[int]string{0: "person", 2: "car", 3: "package"})
	row := func(cx, cy, w, h float32, scores ...float32) []float32 {
		return append([]float32{cx, cy, w, h}, scores...)
	}
	anchors := [][]float32{
		row(100, 100, 40, 80, 0.9, 0, 0, 0),  // person
		row(102, 101, 40, 80, 0.8, 0, 0, 0),  // same person, suppressed by NMS
		row(400, 200, 120, 60, 0, 0, 0.7, 0), // car
		row(300, 300, 50, 50, 0, 0.99, 0, 0), // class 1 (bicycle): not a Sentinel label
		row(500, 500, 40, 40, 0.3, 0, 0, 0),  // person below MinScore
		row(200, 150, 30, 30, 0, 0, 0, 0.6),  // package
		row(620, 620, 10, 10, 0.95, 0, 0, 0), // in the padding below the content
	}
	for len(anchors) < 20 { // anchors outnumber attributes, as in real heads
		anchors = append(anchors, make([]float32, 8))
	}
	for _, chFirst := range []bool{true, false} {
		out, dims := ultralyticsOut(chFirst, 4, anchors)
		objs, err := decodeUltralytics(out, dims, labels, 3, 640, 360)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		for _, o := range objs {
			got[o.Kind]++
		}
		if len(objs) != 3 || got["Person"] != 1 || got["Vehicle"] != 1 || got["Package"] != 1 {
			t.Fatalf("chFirst=%v: %+v", chFirst, objs)
		}
		p := objs[0]
		if p.Kind != "Person" || p.Score != 0.9 {
			t.Fatalf("best first: %+v", p)
		}
		// cx 100 ± 20 over a 640-wide content; cy 100 ± 40 over 360.
		if d := p.Box[0] - 80.0/640; d > 1e-6 || d < -1e-6 {
			t.Fatalf("box %v", p.Box)
		}
		if d := p.Box[3] - 140.0/360; d > 1e-6 || d < -1e-6 {
			t.Fatalf("box %v", p.Box)
		}
	}
	if _, err := decodeUltralytics(make([]float32, 6*10), []int64{1, 6, 10}, labels, 3, 640, 640); err == nil {
		t.Fatal("too few classes accepted")
	}
}

// letterboxTo mimics the ffmpeg filter for a size×size model.
func letterboxTo(src image.Image, size int) ([]byte, int, int) {
	b := src.Bounds()
	cw, ch := ContentSizeFor(size, b.Dx(), b.Dy())
	out := make([]byte, 3*size*size)
	for i := range out {
		out[i] = 114
	}
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			r, g, bl, _ := src.At(b.Min.X+x*b.Dx()/cw, b.Min.Y+y*b.Dy()/ch).RGBA()
			i := 3 * (y*size + x)
			out[i], out[i+1], out[i+2] = byte(bl>>8), byte(g>>8), byte(r>>8)
		}
	}
	return out, cw, ch
}

// TestYOLOXSReal runs YOLOX-S (640) through the bundled ONNX Runtime: the
// 640 grid must match the model's output and decode. Needs SENTINEL_ORT_LIB
// and the model (SENTINEL_YOLOX_S_MODEL, else linux/.models-cache/yolox_s.onnx);
// with SENTINEL_TEST_JPEG (a street photo) it also expects a person and a
// vehicle.
func TestYOLOXSReal(t *testing.T) {
	lib := os.Getenv("SENTINEL_ORT_LIB")
	model := os.Getenv("SENTINEL_YOLOX_S_MODEL")
	if model == "" {
		model = filepath.Join("..", "..", ".models-cache", "yolox_s.onnx")
	}
	if lib == "" || !fileExists(model) {
		t.Skip("set SENTINEL_ORT_LIB and download yolox_s.onnx into linux/.models-cache (or SENTINEL_YOLOX_S_MODEL)")
	}
	d, err := NewObjectDetectorSpec(lib, model, ModelSpec{Family: FamilyYOLOX, Input: 640}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Detect(warmupFrame(640), 640, 640); err != nil {
		t.Fatalf("gray frame: %v", err)
	}
	img := os.Getenv("SENTINEL_TEST_JPEG")
	if img == "" {
		t.Log("640 path runs; set SENTINEL_TEST_JPEG to check detections")
		return
	}
	f, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	src, err := jpeg.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	bgr, cw, ch := letterboxTo(src, 640)
	objs, err := d.Detect(bgr, cw, ch)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, o := range objs {
		counts[o.Kind]++
		t.Logf("%-7s %-10s %.2f box=%.2f", o.Kind, o.Label, o.Score, o.Box)
	}
	if counts["Person"] < 1 || counts["Vehicle"] < 1 {
		t.Fatalf("expected people and a vehicle, got %v", counts)
	}
	if _, err := FrameJPEG(bgr, cw, ch, [][4]float64{objs[0].Box}); err != nil {
		t.Fatal(err)
	}
}

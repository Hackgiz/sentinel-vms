package detect

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"testing"
)

// TestDetectRealImage runs YOLOX-Tiny on a real photo. It needs the ONNX
// Runtime library, the model and a JPEG; set SENTINEL_ORT_LIB, SENTINEL_MODEL
// and SENTINEL_TEST_JPEG (skipped otherwise).
func TestDetectRealImage(t *testing.T) {
	lib, model, img := os.Getenv("SENTINEL_ORT_LIB"), os.Getenv("SENTINEL_MODEL"), os.Getenv("SENTINEL_TEST_JPEG")
	if lib == "" || model == "" || img == "" {
		t.Skip("set SENTINEL_ORT_LIB, SENTINEL_MODEL, SENTINEL_TEST_JPEG")
	}
	d, err := NewObjectDetector(lib, model, 2, 1)
	if err != nil {
		t.Fatal(err)
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
	bgr, cw, ch := letterbox(src)
	objs, err := d.Detect(bgr, cw, ch)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, o := range objs {
		counts[o.Kind]++
		t.Logf("%-7s %-10s %.2f box=%.2f", o.Kind, o.Label, o.Score, o.Box)
	}
	if counts["Person"] < 2 || counts["Vehicle"] < 1 {
		t.Fatalf("expected people and a vehicle, got %v", counts)
	}
}

// letterbox mimics the ffmpeg filter: scale to fit, top-left, pad gray 114, BGR24.
func letterbox(src image.Image) ([]byte, int, int) {
	b := src.Bounds()
	cw, ch := ContentSize(b.Dx(), b.Dy())
	out := make([]byte, 3*InputSize*InputSize)
	for i := range out {
		out[i] = 114
	}
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			sx := b.Min.X + x*b.Dx()/cw
			sy := b.Min.Y + y*b.Dy()/ch
			r, g, bl, _ := src.At(sx, sy).RGBA()
			i := 3 * (y*InputSize + x)
			out[i], out[i+1], out[i+2] = byte(bl>>8), byte(g>>8), byte(r>>8)
		}
	}
	return out, cw, ch
}

func TestIoUAndNMS(t *testing.T) {
	a := Object{Kind: "Person", Score: 0.9, Box: [4]float64{0, 0, 0.5, 0.5}}
	b := Object{Kind: "Person", Score: 0.8, Box: [4]float64{0.05, 0.05, 0.5, 0.5}}
	c := Object{Kind: "Vehicle", Score: 0.7, Box: [4]float64{0, 0, 0.5, 0.5}}
	if got := nms([]Object{b, a, c}, 0.45); len(got) != 2 || got[0].Score != 0.9 {
		t.Fatalf("nms %+v", got)
	}
	if cw, ch := ContentSize(1920, 1080); cw != 416 || ch != 234 {
		t.Fatalf("content %dx%d", cw, ch)
	}
}

func TestFrameJPEG(t *testing.T) {
	frame := make([]byte, 3*InputSize*InputSize)
	b, err := FrameJPEG(frame, 416, 234, [][4]float64{{0.1, 0.1, 0.5, 0.9}})
	if err != nil || len(b) < 100 || b[0] != 0xFF || b[1] != 0xD8 {
		t.Fatalf("jpeg: %v len=%d", err, len(b))
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != 416 || img.Bounds().Dy() != 234 {
		t.Fatalf("decoded %v %v", img.Bounds(), err)
	}
}

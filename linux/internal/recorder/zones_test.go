package recorder

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
)

func zonedRecorder(zones []link.Zone) *Recorder {
	return &Recorder{Media: &media.Supervisor{Ports: media.DefaultPorts}, cams: map[string]link.CameraConfig{"cam": {ID: "cam", Zones: zones}, "plain": {ID: "plain"}}}
}

func whiteJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 80, 80))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func red(t *testing.T, jpg []byte, x, y int) uint32 {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, _ := img.At(x, y).RGBA()
	return r >> 8
}

func TestRecorderMasksEveryJPEGAndExport(t *testing.T) {
	r := zonedRecorder([]link.Zone{
		{Kind: link.ZoneDetect, Points: [][2]float64{{0, 0}, {1, 0}, {1, 1}}},
		{Kind: link.ZoneMask, Points: [][2]float64{{0, 0}, {0.5, 0}, {0.5, 0.5}, {0, 0.5}}},
	})
	src := whiteJPEG(t)
	out, ok := r.maskedJPEG("cam", src)
	if !ok || red(t, out, 10, 10) > 40 || red(t, out, 70, 70) < 200 {
		t.Fatalf("masked JPEG: ok=%v", ok)
	}
	if same, ok := r.maskedJPEG("plain", src); !ok || !bytes.Equal(same, src) {
		t.Fatal("a camera without masks passes the JPEG through")
	}
	if _, ok := r.maskedJPEG("cam", []byte("junk")); ok {
		t.Fatal("an unmaskable image must be dropped")
	}

	// Files: masked in place; unmaskable ones are deleted.
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jpg")
	_ = os.WriteFile(p, src, 0o600)
	if !r.maskFile("cam", p) {
		t.Fatal("maskFile")
	}
	b, _ := os.ReadFile(p)
	if red(t, b, 10, 10) > 40 {
		t.Fatal("file not masked")
	}
	bad := filepath.Join(dir, "bad.jpg")
	_ = os.WriteFile(bad, []byte("junk"), 0o600)
	if r.maskFile("cam", bad) {
		t.Fatal("junk reported masked")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatal("an unmaskable file must be removed, not left unmasked")
	}

	// Exports get the mask boxes; detection gets only the detect polygons.
	if m := r.exportMasks("cam"); len(m) != 1 || m[0] != [4]float64{0, 0, 0.5, 0.5} {
		t.Fatalf("export masks %v", m)
	}
	if r.exportMasks("plain") != nil {
		t.Fatal("no masks, no re-encode")
	}
	cam, _ := r.camera("cam")
	want := detect.EncodeZones([]detect.Polygon{{{0, 0}, {1, 0}, {1, 1}}})
	if got := detectZones(cam.Zones); got != want {
		t.Fatalf("detect zones %q, want %q", got, want)
	}
	if detectZones(nil) != "" {
		t.Fatal("no detect zones = whole frame")
	}
	// The detector config reaches Source.Zones.
	srcs := r.detectionSources([]link.CameraConfig{{ID: "cam", MotionLevel: 2, Zones: cam.Zones}})
	if len(srcs) != 1 || srcs[0].Zones != want {
		t.Fatalf("sources %+v", srcs)
	}
}

package media

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"testing"

	"sentinel-linux/internal/link"
)

func whiteJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func luma(t *testing.T, jpg []byte, x, y int) uint32 {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(x, y).RGBA()
	return (r + g + b) / 3 >> 8
}

func TestMaskRects(t *testing.T) {
	zs := []link.Zone{
		{Kind: link.ZoneDetect, Points: [][2]float64{{0, 0}, {1, 0}, {0, 1}}}, // not a mask
		{Kind: link.ZoneMask, Points: [][2]float64{{0.2, 0.3}, {0.6, 0.4}, {0.4, 0.8}}},
		{Kind: link.ZoneMask, Points: [][2]float64{{0.7, 0.1}, {0.9, 0.1}, {0.9, 0.2}, {0.7, 0.2}}},
	}
	got := MaskRects(zs)
	want := []Rect{{0.2, 0.3, 0.6, 0.8}, {0.7, 0.1, 0.9, 0.2}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rects %+v, want %+v", got, want)
	}
	if MaskRects(nil) != nil || MaskRects(zs[:1]) != nil {
		t.Fatal("no masks must give no rects")
	}
}

func TestMaskJPEGBurnsBoxesBlack(t *testing.T) {
	src := whiteJPEG(t, 200, 100)
	// Mask x 0.25..0.75 (px 50..150), y 0.5..1 (px 50..100).
	out, err := MaskJPEG(src, []Rect{{0.25, 0.5, 0.75, 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		x, y int
		dark bool
	}{
		{100, 75, true}, {60, 60, true}, {140, 95, true}, // inside
		{10, 10, false}, {190, 90, false}, {100, 25, false}, {20, 75, false}, {180, 75, false}, // outside
	} {
		if l := luma(t, out, c.x, c.y); c.dark != (l < 40) || (!c.dark && l < 200) {
			t.Fatalf("pixel %d,%d luma %d, dark want %v", c.x, c.y, l, c.dark)
		}
	}
	img, _ := jpeg.Decode(bytes.NewReader(out))
	if img.Bounds().Dx() != 200 || img.Bounds().Dy() != 100 {
		t.Fatalf("size changed: %v", img.Bounds())
	}
}

func TestMaskJPEGMultipleAndEdgeRects(t *testing.T) {
	src := whiteJPEG(t, 100, 100)
	out, err := MaskJPEG(src, []Rect{{0, 0, 0.1, 0.1}, {0.9, 0.9, 1.5, 1.5}}) // second runs off the frame
	if err != nil {
		t.Fatal(err)
	}
	if luma(t, out, 3, 3) > 40 || luma(t, out, 97, 97) > 40 {
		t.Fatal("both corner masks should be black")
	}
	if luma(t, out, 50, 50) < 200 {
		t.Fatal("the middle must stay white")
	}
}

func TestMaskJPEGNoRectsAndBadInput(t *testing.T) {
	src := whiteJPEG(t, 32, 32)
	out, err := MaskJPEG(src, nil)
	if err != nil || !bytes.Equal(out, src) {
		t.Fatalf("no rects must return the input untouched: %v", err)
	}
	// Garbage with a mask configured must fail closed (nil), never pass through.
	if out, err := MaskJPEG([]byte("not a jpeg"), []Rect{{0, 0, 1, 1}}); err == nil || out != nil {
		t.Fatalf("garbage: %v %v", out, err)
	}
}

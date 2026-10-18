package media

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"

	"sentinel-linux/internal/link"
)

// Privacy masks (WAVE7-FEATURES.md, Stream A). MaskJPEG is the ONE choke
// point every server-produced JPEG goes through (event thumbnails, playhead
// snapshots, AI frames, MQTT snapshots, HomeKit snapshots); the export path
// uses the same rectangles as ffmpeg drawbox filters (playback.ExportMasked).
// Masks fill the bounding box of each enabled mask polygon black.

// Rect is a normalized (0..1) rectangle of the frame.
type Rect struct{ X0, Y0, X1, Y1 float64 }

// MaskRects returns the bounding boxes of the enabled "mask" polygons in
// zones (callers pass the camera's enabled zones; link.CameraConfig.Zones).
func MaskRects(zones []link.Zone) []Rect {
	var out []Rect
	for _, z := range zones {
		if z.Kind != link.ZoneMask || len(z.Points) == 0 {
			continue
		}
		r := Rect{X0: 1, Y0: 1}
		for _, p := range z.Points {
			r.X0, r.Y0 = math.Min(r.X0, p[0]), math.Min(r.Y0, p[1])
			r.X1, r.Y1 = math.Max(r.X1, p[0]), math.Max(r.Y1, p[1])
		}
		out = append(out, clampRect(r))
	}
	return out
}

func clampRect(r Rect) Rect {
	c := func(v float64) float64 { return math.Max(0, math.Min(1, v)) }
	return Rect{c(r.X0), c(r.Y0), c(r.X1), c(r.Y1)}
}

// MaskJPEG burns the rects black into a JPEG. With no rects it returns jpg
// untouched (no recompression). On any error it returns nil and the error:
// callers must DROP the image, never fall back to the unmasked one.
func MaskJPEG(jpg []byte, rects []Rect) ([]byte, error) {
	if len(rects) == 0 {
		return jpg, nil
	}
	src, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return nil, errors.New("mask: unreadable JPEG: " + err.Error())
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	black := image.NewUniform(color.Black)
	w, h := float64(b.Dx()), float64(b.Dy())
	for _, r := range rects {
		r = clampRect(r)
		px := image.Rect(
			b.Min.X+int(math.Floor(r.X0*w)), b.Min.Y+int(math.Floor(r.Y0*h)),
			b.Min.X+int(math.Ceil(r.X1*w)), b.Min.Y+int(math.Ceil(r.Y1*h)))
		draw.Draw(dst, px.Intersect(b), black, image.Point{}, draw.Src)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 88}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

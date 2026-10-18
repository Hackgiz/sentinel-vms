package reid

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
)

// OSNet-style input: one 256×128 (H×W) RGB crop, CHW, ImageNet-normalized.
const (
	InputH = 256
	InputW = 128
)

var (
	imagenetMean = [3]float32{0.485, 0.456, 0.406}
	imagenetStd  = [3]float32{0.229, 0.224, 0.225}
)

// MinCropPx is the smallest box side (in thumbnail pixels) worth embedding.
const MinCropPx = 8

// ErrNoBox is an event without a usable detection box.
var ErrNoBox = errors.New("event has no detection box")

// ErrTooSmall is a box too small in the thumbnail to describe anyone.
var ErrTooSmall = errors.New("detection box is too small to compare")

// ParseBox reads an event's box column (JSON [x1,y1,x2,y2], normalized to
// the frame) and checks it describes a real area.
func ParseBox(raw string) ([4]float64, error) {
	var b []float64
	if raw == "" || json.Unmarshal([]byte(raw), &b) != nil || len(b) != 4 {
		return [4]float64{}, ErrNoBox
	}
	for _, v := range b {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return [4]float64{}, ErrNoBox
		}
	}
	box := [4]float64{clamp01(b[0]), clamp01(b[1]), clamp01(b[2]), clamp01(b[3])}
	if box[2] <= box[0] || box[3] <= box[1] {
		return [4]float64{}, ErrNoBox
	}
	return box, nil
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// CropRect is the pixel rectangle of a normalized box in a w×h image. The
// detection thumbnail outlines the box with a 2 px line drawn inside it, so
// boxes wide enough lose 2 px on each side to keep the outline out of the
// crop.
func CropRect(box [4]float64, w, h int) image.Rectangle {
	r := image.Rect(int(math.Floor(box[0]*float64(w))), int(math.Floor(box[1]*float64(h))),
		int(math.Ceil(box[2]*float64(w))), int(math.Ceil(box[3]*float64(h)))).Intersect(image.Rect(0, 0, w, h))
	if r.Dx() > 4*MinCropPx {
		r.Min.X, r.Max.X = r.Min.X+2, r.Max.X-2
	}
	if r.Dy() > 4*MinCropPx {
		r.Min.Y, r.Max.Y = r.Min.Y+2, r.Max.Y-2
	}
	return r
}

// CropJPEG decodes an event thumbnail and returns the detection box's part
// of it (never the whole frame).
func CropJPEG(jpg []byte, box [4]float64) (image.Image, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return nil, fmt.Errorf("thumbnail: %w", err)
	}
	b := img.Bounds()
	r := CropRect(box, b.Dx(), b.Dy()).Add(b.Min)
	if r.Dx() < MinCropPx || r.Dy() < MinCropPx {
		return nil, ErrTooSmall
	}
	if sub, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return sub.SubImage(r), nil
	}
	return nil, errors.New("thumbnail format can't be cropped")
}

// Preprocess resizes img (bilinear, pixel centers aligned) to InputW×InputH
// and lays it out as the model input: CHW float32, RGB, (v/255−mean)/std.
func Preprocess(img image.Image) []float32 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]float32, 3*InputH*InputW)
	if w <= 0 || h <= 0 {
		return out
	}
	rgb := toRGB(img)
	plane := InputH * InputW
	sx, sy := float64(w)/InputW, float64(h)/InputH
	for y := 0; y < InputH; y++ {
		fy := math.Max(0, (float64(y)+0.5)*sy-0.5)
		y0 := int(fy)
		if y0 > h-1 {
			y0 = h - 1
		}
		y1 := y0 + 1
		if y1 > h-1 {
			y1 = h - 1
		}
		dy := fy - float64(y0)
		for x := 0; x < InputW; x++ {
			fx := math.Max(0, (float64(x)+0.5)*sx-0.5)
			x0 := int(fx)
			if x0 > w-1 {
				x0 = w - 1
			}
			x1 := x0 + 1
			if x1 > w-1 {
				x1 = w - 1
			}
			dx := fx - float64(x0)
			for c := 0; c < 3; c++ {
				p00 := float64(rgb[3*(y0*w+x0)+c])
				p01 := float64(rgb[3*(y0*w+x1)+c])
				p10 := float64(rgb[3*(y1*w+x0)+c])
				p11 := float64(rgb[3*(y1*w+x1)+c])
				v := (p00*(1-dx)+p01*dx)*(1-dy) + (p10*(1-dx)+p11*dx)*dy
				out[c*plane+y*InputW+x] = (float32(v/255) - imagenetMean[c]) / imagenetStd[c]
			}
		}
	}
	return out
}

// toRGB flattens img to packed 8-bit RGB, row-major from its bounds' origin.
func toRGB(img image.Image) []uint8 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	px := make([]uint8, 3*w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			i := 3 * (y*w + x)
			px[i], px[i+1], px[i+2] = uint8(r>>8), uint8(g>>8), uint8(bl>>8)
		}
	}
	return px
}

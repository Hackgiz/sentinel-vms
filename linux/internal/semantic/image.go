package semantic

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
)

// ImageSize is the CLIP ViT-B/32 input side.
const ImageSize = 224

// CLIP's per-channel normalization (RGB).
var (
	clipMean = [3]float32{0.48145466, 0.4578275, 0.40821073}
	clipStd  = [3]float32{0.26862954, 0.26130258, 0.27577711}
)

// DecodeJPEG decodes an event thumbnail.
func DecodeJPEG(b []byte) (image.Image, error) {
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("thumbnail: %w", err)
	}
	return img, nil
}

// Preprocess lays img out as the image tower's pixel_values: the shorter
// side resized to 224 (bicubic, antialiased like PIL), the centre 224×224
// cropped, RGB, CHW, (v/255 − mean)/std.
func Preprocess(img image.Image) []float32 {
	out := make([]float32, 3*ImageSize*ImageSize)
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return out
	}
	// Resized size: shorter side 224, the other scaled (truncated, as the
	// Hugging Face CLIPImageProcessor does), never below 224.
	nw, nh := ImageSize, ImageSize
	if w < h {
		nh = max(ImageSize, int(float64(h)*ImageSize/float64(w)))
	} else {
		nw = max(ImageSize, int(float64(w)*ImageSize/float64(h)))
	}
	left, top := (nw-ImageSize)/2, (nh-ImageSize)/2

	rgb := toRGB(img)
	// Horizontal pass: only the cropped columns, every source row.
	cols := resampleWeights(w, nw, left, ImageSize)
	tmp := make([]float32, 3*ImageSize*h)
	for y := 0; y < h; y++ {
		row := rgb[3*y*w:]
		for x, cw := range cols {
			var r, g, bl float32
			for k, wt := range cw.w {
				p := 3 * (cw.start + k)
				r += wt * float32(row[p])
				g += wt * float32(row[p+1])
				bl += wt * float32(row[p+2])
			}
			i := 3 * (y*ImageSize + x)
			tmp[i], tmp[i+1], tmp[i+2] = r, g, bl
		}
	}
	// Vertical pass: only the cropped rows; clamp, scale and normalize.
	rows := resampleWeights(h, nh, top, ImageSize)
	plane := ImageSize * ImageSize
	for y, rw := range rows {
		for x := 0; x < ImageSize; x++ {
			var acc [3]float32
			for k, wt := range rw.w {
				i := 3 * ((rw.start+k)*ImageSize + x)
				acc[0] += wt * tmp[i]
				acc[1] += wt * tmp[i+1]
				acc[2] += wt * tmp[i+2]
			}
			for c := 0; c < 3; c++ {
				v := float32(math.Round(float64(min(255, max(0, acc[c])))))
				out[c*plane+y*ImageSize+x] = (v/255 - clipMean[c]) / clipStd[c]
			}
		}
	}
	return out
}

type tapWeights struct {
	start int
	w     []float32
}

// resampleWeights computes PIL-style antialiased bicubic (a = −0.5) taps
// for outputs [from, from+n) of a src → dst resize along one axis.
func resampleWeights(src, dst, from, n int) []tapWeights {
	scale := float64(src) / float64(dst)
	support := 2.0 // bicubic
	fscale := math.Max(scale, 1)
	support *= fscale
	out := make([]tapWeights, n)
	for i := 0; i < n; i++ {
		center := (float64(from+i) + 0.5) * scale
		lo := max(0, int(center-support+0.5))
		hi := min(src, int(center+support+0.5))
		ws := make([]float32, 0, hi-lo)
		var sum float64
		raw := make([]float64, 0, hi-lo)
		for j := lo; j < hi; j++ {
			v := cubic((float64(j) - center + 0.5) / fscale)
			raw = append(raw, v)
			sum += v
		}
		for _, v := range raw {
			if sum != 0 {
				v /= sum
			}
			ws = append(ws, float32(v))
		}
		if len(ws) == 0 { // degenerate: nearest pixel
			lo = min(src-1, max(0, int(center)))
			ws = []float32{1}
		}
		out[i] = tapWeights{start: lo, w: ws}
	}
	return out
}

// cubic is the bicubic kernel PIL uses (a = −0.5).
func cubic(x float64) float64 {
	const a = -0.5
	x = math.Abs(x)
	switch {
	case x < 1:
		return ((a+2)*x-(a+3))*x*x + 1
	case x < 2:
		return (((x-5)*x+8)*x - 4) * a
	}
	return 0
}

// toRGB flattens img to packed 8-bit RGB, row-major from its bounds' origin.
func toRGB(img image.Image) []uint8 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	px := make([]uint8, 3*w*h)
	switch m := img.(type) {
	case *image.YCbCr:
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				yi := m.YOffset(b.Min.X+x, b.Min.Y+y)
				ci := m.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bl := color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
				i := 3 * (y*w + x)
				px[i], px[i+1], px[i+2] = r, g, bl
			}
		}
		return px
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			i := 3 * (y*w + x)
			px[i], px[i+1], px[i+2] = uint8(r>>8), uint8(g>>8), uint8(bl>>8)
		}
	}
	return px
}

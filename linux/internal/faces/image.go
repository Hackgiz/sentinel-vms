package faces

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"sort"
	"strconv"
)

// rgbImage is packed 8-bit RGB, row-major.
type rgbImage struct {
	w, h int
	px   []uint8
}

// toRGB flattens img (from its bounds' origin).
func toRGB(img image.Image) *rgbImage {
	b := img.Bounds()
	m := &rgbImage{w: b.Dx(), h: b.Dy(), px: make([]uint8, 3*b.Dx()*b.Dy())}
	switch src := img.(type) {
	case *image.YCbCr:
		for y := 0; y < m.h; y++ {
			for x := 0; x < m.w; x++ {
				yi := src.YOffset(b.Min.X+x, b.Min.Y+y)
				ci := src.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bl := color.YCbCrToRGB(src.Y[yi], src.Cb[ci], src.Cr[ci])
				i := 3 * (y*m.w + x)
				m.px[i], m.px[i+1], m.px[i+2] = r, g, bl
			}
		}
	case *image.RGBA:
		for y := 0; y < m.h; y++ {
			row := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < m.w; x++ {
				i := 3 * (y*m.w + x)
				m.px[i], m.px[i+1], m.px[i+2] = row[4*x], row[4*x+1], row[4*x+2]
			}
		}
	default:
		for y := 0; y < m.h; y++ {
			for x := 0; x < m.w; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				i := 3 * (y*m.w + x)
				m.px[i], m.px[i+1], m.px[i+2] = uint8(r>>8), uint8(g>>8), uint8(bl>>8)
			}
		}
	}
	return m
}

// at is channel c at (x, y), bilinear. Outside the image it is 0 when
// zero is set (OpenCV warpAffine's constant border), otherwise the nearest
// edge pixel.
func (m *rgbImage) at(x, y float64, c int, zero bool) float64 {
	if zero && (x < -1 || y < -1 || x > float64(m.w) || y > float64(m.h)) {
		return 0
	}
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	dx, dy := x-float64(x0), y-float64(y0)
	px := func(xi, yi int) float64 {
		if xi < 0 || yi < 0 || xi >= m.w || yi >= m.h {
			if zero {
				return 0
			}
			xi, yi = max(0, min(m.w-1, xi)), max(0, min(m.h-1, yi))
		}
		return float64(m.px[3*(yi*m.w+xi)+c])
	}
	return (px(x0, y0)*(1-dx)+px(x0+1, y0)*dx)*(1-dy) + (px(x0, y0+1)*(1-dx)+px(x0+1, y0+1)*dx)*dy
}

// detInput lays img out as YuNet's input: resized by scale (aspect kept)
// into the top-left of an inW×inH canvas padded with zeros, CHW float32,
// BGR, 0..255 (OpenCV's blobFromImage with no scaling).
func detInput(m *rgbImage, inW, inH int) (in []float32, scale float64) {
	scale = math.Min(float64(inW)/float64(m.w), float64(inH)/float64(m.h))
	in = make([]float32, 3*inW*inH)
	cw := min(inW, int(math.Round(float64(m.w)*scale)))
	ch := min(inH, int(math.Round(float64(m.h)*scale)))
	plane := inW * inH
	for y := 0; y < ch; y++ {
		sy := math.Max(0, (float64(y)+0.5)/scale-0.5)
		for x := 0; x < cw; x++ {
			sx := math.Max(0, (float64(x)+0.5)/scale-0.5)
			for c := 0; c < 3; c++ {
				// BGR planes: plane 0 is blue (RGB channel 2).
				in[c*plane+y*inW+x] = float32(m.at(sx, sy, 2-c, false))
			}
		}
	}
	return in, scale
}

// Detection parameters (OpenCV Zoo's YuNet defaults).
const (
	detScoreMin = 0.9
	detNMSIoU   = 0.3
	detTopK     = 50
)

// yunetStrides are YuNet 2023mar's feature-map strides; its outputs are
// cls_<s>, obj_<s>, bbox_<s> and kps_<s> for each.
var yunetStrides = []int{8, 16, 32}

// yunetOutputs lists the output names in the order decode reads them.
func yunetOutputs() []string {
	var names []string
	for _, s := range yunetStrides {
		for _, p := range []string{"cls", "obj", "bbox", "kps"} {
			names = append(names, p+"_"+strconv.Itoa(s))
		}
	}
	return names
}

// rawFace is a detection in input pixels.
type rawFace struct {
	x1, y1, x2, y2 float64
	score          float64
	lm             [5][2]float64
}

// decodeYuNet turns YuNet's outputs (in yunetOutputs order) for an inW×inH
// input into faces in the original image's normalized coordinates (the
// image was scaled by scale into the canvas and is w×h).
func decodeYuNet(outs [][]float32, inW, inH int, scale float64, w, h int) []Face {
	var raw []rawFace
	for si, s := range yunetStrides {
		cols, rows := inW/s, inH/s
		n := cols * rows
		cls, obj, bbox, kps := outs[4*si], outs[4*si+1], outs[4*si+2], outs[4*si+3]
		if len(cls) < n || len(obj) < n || len(bbox) < 4*n || len(kps) < 10*n {
			continue
		}
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				i := r*cols + c
				score := math.Sqrt(clamp01(float64(cls[i])) * clamp01(float64(obj[i])))
				if score < detScoreMin {
					continue
				}
				cx := (float64(c) + float64(bbox[4*i])) * float64(s)
				cy := (float64(r) + float64(bbox[4*i+1])) * float64(s)
				bw := math.Exp(float64(bbox[4*i+2])) * float64(s)
				bh := math.Exp(float64(bbox[4*i+3])) * float64(s)
				f := rawFace{x1: cx - bw/2, y1: cy - bh/2, x2: cx + bw/2, y2: cy + bh/2, score: score}
				for k := 0; k < 5; k++ {
					f.lm[k] = [2]float64{(float64(kps[10*i+2*k]) + float64(c)) * float64(s), (float64(kps[10*i+2*k+1]) + float64(r)) * float64(s)}
				}
				raw = append(raw, f)
			}
		}
	}
	raw = nms(raw, detNMSIoU, detTopK)
	out := make([]Face, 0, len(raw))
	fw, fh := float64(w)*scale, float64(h)*scale // content size in input pixels
	for _, f := range raw {
		face := Face{Box: [4]float64{clamp01(f.x1 / fw), clamp01(f.y1 / fh), clamp01(f.x2 / fw), clamp01(f.y2 / fh)}, Score: f.score}
		if face.Box[2] <= face.Box[0] || face.Box[3] <= face.Box[1] {
			continue
		}
		for k := range f.lm {
			face.Landmarks[k] = [2]float64{f.lm[k][0] / fw, f.lm[k][1] / fh}
		}
		out = append(out, face)
	}
	return out
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}

// nms keeps the best faces, dropping any that overlap a better one by more
// than iou, at most topK.
func nms(fs []rawFace, iou float64, topK int) []rawFace {
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].score > fs[j].score })
	var keep []rawFace
	for _, f := range fs {
		ok := true
		for _, k := range keep {
			if overlap(f, k) > iou {
				ok = false
				break
			}
		}
		if ok {
			keep = append(keep, f)
			if len(keep) == topK {
				break
			}
		}
	}
	return keep
}

func overlap(a, b rawFace) float64 {
	ix := math.Max(0, math.Min(a.x2, b.x2)-math.Max(a.x1, b.x1))
	iy := math.Max(0, math.Min(a.y2, b.y2)-math.Max(a.y1, b.y1))
	inter := ix * iy
	union := (a.x2-a.x1)*(a.y2-a.y1) + (b.x2-b.x1)*(b.y2-b.y1) - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

// SFace's input and the reference landmark positions in it (the ArcFace
// 112×112 template OpenCV's FaceRecognizerSF aligns to).
const alignSize = 112

var alignRef = [5][2]float64{
	{38.2946, 51.6963}, {73.5318, 51.5014}, {56.0252, 71.7366}, {41.5493, 92.3655}, {70.7299, 92.2041},
}

// similarity is x' = a·x − b·y + tx, y' = b·x + a·y + ty.
type similarity struct{ a, b, tx, ty float64 }

// fitSimilarity is the least-squares similarity transform (rotation,
// uniform scale, translation; no reflection) taking src onto dst.
func fitSimilarity(src, dst [5][2]float64) similarity {
	var sx, sy, dx, dy float64
	for i := range src {
		sx, sy, dx, dy = sx+src[i][0], sy+src[i][1], dx+dst[i][0], dy+dst[i][1]
	}
	n := float64(len(src))
	sx, sy, dx, dy = sx/n, sy/n, dx/n, dy/n
	var num1, num2, den float64
	for i := range src {
		px, py := src[i][0]-sx, src[i][1]-sy
		qx, qy := dst[i][0]-dx, dst[i][1]-dy
		num1 += px*qx + py*qy
		num2 += px*qy - py*qx
		den += px*px + py*py
	}
	if den == 0 {
		return similarity{a: 1, tx: dx - sx, ty: dy - sy}
	}
	a, b := num1/den, num2/den
	return similarity{a: a, b: b, tx: dx - (a*sx - b*sy), ty: dy - (b*sx + a*sy)}
}

// inverse maps a destination point back to the source.
func (t similarity) inverse(x, y float64) (float64, float64) {
	det := t.a*t.a + t.b*t.b
	x, y = x-t.tx, y-t.ty
	return (t.a*x + t.b*y) / det, (-t.b*x + t.a*y) / det
}

// alignInput warps the face in m so its landmarks (pixels) land on
// alignRef, as SFace's input: 1×3×112×112, CHW, RGB, 0..255.
func alignInput(m *rgbImage, lm [5][2]float64) []float32 {
	t := fitSimilarity(lm, alignRef)
	in := make([]float32, 3*alignSize*alignSize)
	plane := alignSize * alignSize
	for y := 0; y < alignSize; y++ {
		for x := 0; x < alignSize; x++ {
			sx, sy := t.inverse(float64(x), float64(y))
			for c := 0; c < 3; c++ {
				in[c*plane+y*alignSize+x] = float32(m.at(sx, sy, c, true))
			}
		}
	}
	return in
}

// pixelLandmarks converts a face's normalized landmarks to m's pixels.
func pixelLandmarks(m *rgbImage, f Face) [5][2]float64 {
	var lm [5][2]float64
	for k := range f.Landmarks {
		lm[k] = [2]float64{f.Landmarks[k][0] * float64(m.w), f.Landmarks[k][1] * float64(m.h)}
	}
	return lm
}

// CropMax is the longest side of a stored face crop.
const CropMax = 160

// CropJPEG cuts a square around the face (its box grown by 30 %), at most
// CropMax pixels on a side, for the UI.
func CropJPEG(img image.Image, f Face) ([]byte, error) {
	m := toRGB(img)
	if m.w == 0 || m.h == 0 {
		return nil, ErrNoFace
	}
	x1, y1 := f.Box[0]*float64(m.w), f.Box[1]*float64(m.h)
	x2, y2 := f.Box[2]*float64(m.w), f.Box[3]*float64(m.h)
	side := 1.3 * math.Max(x2-x1, y2-y1)
	if side < 1 {
		return nil, ErrNoFace
	}
	cx, cy := (x1+x2)/2, (y1+y2)/2
	n := int(math.Min(CropMax, math.Ceil(side)))
	step := side / float64(n)
	out := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		sy := cy - side/2 + (float64(y)+0.5)*step - 0.5
		for x := 0; x < n; x++ {
			sx := cx - side/2 + (float64(x)+0.5)*step - 0.5
			i := out.PixOffset(x, y)
			for c := 0; c < 3; c++ {
				out.Pix[i+c] = uint8(math.Round(m.at(sx, sy, c, false)))
			}
			out.Pix[i+3] = 255
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

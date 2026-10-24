package playback

// ExportWith: exports with the Stream D options (WAVE8-DETECT-EVIDENCE.md):
// privacy masks (wave 7), pixelated regions and a burned-in watermark. Only
// core ffmpeg filters are used (crop, drawbox, overlay, scale, split); the
// watermark text is a PNG rendered in Go (watermark.go).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PixelFactor is the fixed pixelation block size in pixels.
const PixelFactor = 16

// ExportOptions are the optional export-time changes to the camera's video.
// With none set the export is a stream copy of the camera's own bitstream.
type ExportOptions struct {
	// Masks are normalized {x0, y0, x1, y1} boxes filled black.
	Masks [][4]float64
	// Watermark is the text for the burned-in strip (composed by the
	// server); empty means no watermark.
	Watermark string
	// Blur are normalized {x0, y0, x1, y1} regions pixelated by PixelFactor.
	Blur [][4]float64
	// PreferSmall: when the range holds both recording tiers, bring the whole
	// clip down to the low-res size instead of up to the full-res size.
	PreferSmall bool
}

func (o ExportOptions) hasWatermark() bool { return strings.TrimSpace(o.Watermark) != "" }

// Reencodes reports whether the options force a re-encode.
func (o ExportOptions) Reencodes() bool {
	return len(o.Masks) > 0 || len(o.Blur) > 0 || o.hasWatermark()
}

// needsGraph reports whether the options need a -filter_complex graph (blur
// or watermark); masks alone keep the wave-7 -vf drawbox chain.
func (o ExportOptions) needsGraph() bool { return len(o.Blur) > 0 || o.hasWatermark() }

const watermarkFile = "watermark.png"

func f5(v float64) string { return fmt.Sprintf("%.5f", v) }

// PixelateFilter is the filter chain pixelating one normalized region of the
// stream labelled in, producing out: split, crop the region (rounded up to a
// multiple of PixelFactor, kept inside the frame), scale down by 16, scale
// back up with nearest-neighbour, overlay in place. idx names the
// intermediate pads.
func PixelateFilter(in, out string, idx int, r [4]float64) string {
	x0, y0 := clamp01(r[0]), clamp01(r[1])
	w, h := clamp01(r[2])-x0, clamp01(r[3])-y0
	return strings.NewReplacer(
		"{in}", in, "{out}", out, "{i}", fmt.Sprint(idx),
		"{w}", f5(w), "{h}", f5(h), "{x}", f5(x0), "{y}", f5(y0), "{f}", fmt.Sprint(PixelFactor),
	).Replace(`[{in}]split=2[pa{i}][pb{i}];` +
		`[pb{i}]crop=w=min(floor(iw/{f})*{f}\,ceil(iw*{w}/{f})*{f}):h=min(floor(ih/{f})*{f}\,ceil(ih*{h}/{f})*{f})` +
		`:x=min(iw*{x}\,iw-out_w):y=min(ih*{y}\,ih-out_h),` +
		`scale=iw/{f}:ih/{f},scale=iw*{f}:ih*{f}:flags=neighbor[pp{i}];` +
		`[pa{i}][pp{i}]overlay=x=min(main_w*{x}\,main_w-overlay_w):y=min(main_h*{y}\,main_h-overlay_h)[{out}]`)
}

// ExportFilterGraph is the -filter_complex graph for options with a blur or
// a watermark: masks (drawbox), then each pixelated region, then the
// watermark overlay (input 1, bottom-left) so the strip itself is never
// pixelated. The final video pad is "vout". The watermark input (1:v) is
// only referenced when watermark is true.
func ExportFilterGraph(masks, blur [][4]float64, watermark bool) string {
	var steps []func(in, out string) string
	if len(masks) > 0 {
		chain := DrawboxFilter(masks)
		steps = append(steps, func(in, out string) string { return "[" + in + "]" + chain + "[" + out + "]" })
	}
	for i, r := range blur {
		i, r := i, r
		steps = append(steps, func(in, out string) string { return PixelateFilter(in, out, i, r) })
	}
	if watermark {
		steps = append(steps, func(in, out string) string {
			return "[" + in + "][1:v]overlay=10:main_h-overlay_h-10[" + out + "]"
		})
	}
	parts := make([]string, 0, len(steps))
	in := "0:v"
	for i, st := range steps {
		out := fmt.Sprintf("v%d", i)
		if i == len(steps)-1 {
			out = "vout"
		}
		parts = append(parts, st(in, out))
		in = out
	}
	return strings.Join(parts, ";")
}

// ClipArgsWith is ClipArgs for a clip with ExportOptions. wmPath is the
// watermark PNG (used when o.Watermark is set). With no options it is
// exactly ClipArgs; with masks only, ClipArgsMaskedWith; otherwise a
// filter_complex re-encode with the encoder arguments enc.
func ClipArgsWith(list string, offset, dur time.Duration, out string, withAudio bool, o ExportOptions, enc []string, wmPath string) []string {
	if !o.needsGraph() {
		return ClipArgsMaskedWith(list, offset, dur, out, withAudio, o.Masks, enc)
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-ss", secs(offset), "-i", list}
	if o.hasWatermark() {
		args = append(args, "-i", wmPath)
	}
	args = append(args, "-t", secs(dur),
		"-filter_complex", ExportFilterGraph(o.Masks, o.Blur, o.hasWatermark()), "-map", "[vout]")
	if withAudio {
		args = append(args, "-map", "0:a?", "-c:a", "copy")
	} else {
		args = append(args, "-an")
	}
	args = append(args, enc...)
	return append(args, "-pix_fmt", "yuv420p",
		"-dn", "-sn", "-movflags", "+faststart", "-f", "mp4", "-y", out)
}

// ExportWith writes [from, to) of a camera's recordings to out as one MP4,
// applying o. Gaps in the recording are closed up. workDir must be an empty
// scratch directory (the watermark PNG is written there).
func ExportWith(ctx context.Context, ffmpeg string, lib *Library, cameraID string, files []File, from, to time.Time, workDir, out string, o ExportOptions) error {
	if ffmpeg == "" {
		return ErrNoFFmpeg
	}
	all, err := ClipPlan(files, from, to)
	if err != nil {
		return err
	}
	if all.Mixed() {
		// Both tiers in the range: re-encode to one size (mixed.go).
		if handled, err := exportMixed(ctx, ffmpeg, lib, cameraID, all, from, to, workDir, out, o); handled {
			if err != nil {
				_ = os.Remove(out)
			}
			return err
		} else if err != nil {
			return err
		}
	}
	// One tier: stream copy of the camera's own bitstream.
	p, err := ClipPlan(OneTier(files, from, to), from, to)
	if err != nil {
		return err
	}
	names, err := writeRuns(lib, cameraID, p, workDir)
	if err != nil {
		return err
	}
	list := filepath.Join(workDir, "clip.ffconcat")
	if err := os.WriteFile(list, []byte(concatList(names, p)), 0o600); err != nil {
		return err
	}
	return encodeClip(ctx, ffmpeg, list, decodableFrom(p.Runs[0], from), p.Covered(from, to), workDir, out, o)
}

// encodeClip runs ffmpeg over a prepared concat list: stream copy, or the
// re-encode the options ask for. workDir holds the watermark PNG.
func encodeClip(ctx context.Context, ffmpeg, list string, offset, dur time.Duration, workDir, out string, o ExportOptions) error {
	var wmPath string
	if o.hasWatermark() {
		png, err := RenderWatermark(o.Watermark, WatermarkScale)
		if err != nil {
			return err
		}
		wmPath = filepath.Join(workDir, watermarkFile)
		if err := os.WriteFile(wmPath, png, 0o600); err != nil {
			return err
		}
	}
	var enc []string
	if o.Reencodes() {
		enc = MaskEncoderArgs(ctx, ffmpeg)
	}
	err := runFFmpeg(ctx, ffmpeg, ClipArgsWith(list, offset, dur, out, true, o, enc, wmPath))
	if err != nil && ctx.Err() == nil {
		// Most likely an audio codec MP4 can't hold (e.g. G.711): keep the video.
		err = runFFmpeg(ctx, ffmpeg, ClipArgsWith(list, offset, dur, out, false, o, enc, wmPath))
	}
	if err != nil {
		_ = os.Remove(out)
		return err
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		_ = os.Remove(out)
		return errors.New("ffmpeg produced no output")
	}
	return nil
}

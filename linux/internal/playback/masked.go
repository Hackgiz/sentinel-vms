package playback

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Privacy-masked exports (WAVE7-FEATURES.md, Stream A). Export keeps its
// signature; the recorder calls ExportMasked with the camera's mask boxes.

// Export is ExportMasked without masks: stream copy, the camera's own video.
func Export(ctx context.Context, ffmpeg string, lib *Library, cameraID string, files []File, from, to time.Time, workDir, out string) error {
	return ExportMasked(ctx, ffmpeg, lib, cameraID, files, from, to, workDir, out, nil)
}

// ClipArgsMasked is ClipArgs for a clip with privacy masks. Each mask is a
// normalized box {x0, y0, x1, y1}. With no boxes it is exactly ClipArgs
// (stream copy). With boxes the video is re-encoded (H.264) through one
// drawbox filter per box; audio is still copied. Slower, and no longer the
// camera's own bitstream, which the UI says.
func ClipArgsMasked(list string, offset, dur time.Duration, out string, withAudio bool, masks [][4]float64) []string {
	return ClipArgsMaskedWith(list, offset, dur, out, withAudio, masks, encoderX264)
}

// ClipArgsMaskedWith is ClipArgsMasked with the H.264 encoder arguments
// chosen by the caller (MaskEncoderArgs probes the ffmpeg build).
func ClipArgsMaskedWith(list string, offset, dur time.Duration, out string, withAudio bool, masks [][4]float64, enc []string) []string {
	if len(masks) == 0 {
		return ClipArgs(list, offset, dur, out, withAudio)
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-ss", secs(offset), "-i", list,
		"-t", secs(dur), "-map", "0:v:0"}
	if withAudio {
		args = append(args, "-map", "0:a?", "-c:a", "copy")
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-vf", DrawboxFilter(masks))
	args = append(args, enc...)
	return append(args, "-pix_fmt", "yuv420p",
		"-dn", "-sn", "-movflags", "+faststart", "-f", "mp4", "-y", out)
}

// Encoder argument sets for the masked re-encode. The Linux bundle (BtbN)
// has libx264; the macOS bundle is a VideoToolbox-only LGPL build, where
// libx264 would fail every masked export.
var (
	encoderX264         = []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "20"}
	encoderVideoToolbox = []string{"-c:v", "h264_videotoolbox", "-q:v", "55", "-allow_sw", "1"}
)

var maskEncCache sync.Map // ffmpeg path -> []string

// MaskEncoderArgs picks the H.264 encoder this ffmpeg build offers, probing
// `ffmpeg -encoders` once per path. Unknown builds get libx264, which then
// fails with ffmpeg's own error rather than silently exporting unmasked.
func MaskEncoderArgs(ctx context.Context, ffmpeg string) []string {
	if v, ok := maskEncCache.Load(ffmpeg); ok {
		return v.([]string)
	}
	out, _ := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-encoders").Output()
	args := chooseMaskEncoder(out)
	maskEncCache.Store(ffmpeg, args)
	return args
}

func chooseMaskEncoder(encoders []byte) []string {
	switch {
	case bytes.Contains(encoders, []byte(" libx264 ")):
		return encoderX264
	case bytes.Contains(encoders, []byte(" h264_videotoolbox ")):
		return encoderVideoToolbox
	default:
		return encoderX264
	}
}

// DrawboxFilter is the ffmpeg -vf chain that fills each normalized box black.
func DrawboxFilter(masks [][4]float64) string {
	parts := make([]string, 0, len(masks))
	for _, m := range masks {
		x0, y0 := clamp01(m[0]), clamp01(m[1])
		w, h := clamp01(m[2])-x0, clamp01(m[3])-y0
		parts = append(parts, fmt.Sprintf("drawbox=x=iw*%.5f:y=ih*%.5f:w=iw*%.5f:h=ih*%.5f:color=black:t=fill", x0, y0, w, h))
	}
	return strings.Join(parts, ",")
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

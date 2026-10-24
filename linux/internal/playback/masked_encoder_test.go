package playback

import (
	"strings"
	"testing"
	"time"
)

// The masked re-encode must use an encoder the ffmpeg build actually has:
// the Linux bundle has libx264, the macOS bundle only VideoToolbox.
func TestChooseMaskEncoder(t *testing.T) {
	x264 := []byte(" V....D libx264              libx264 H.264 / AVC\n V....D h264_videotoolbox    VideoToolbox H.264 Encoder (codec h264)\n")
	vt := []byte(" V....D h264_videotoolbox    VideoToolbox H.264 Encoder (codec h264)\n V....D hevc_videotoolbox    VideoToolbox H.265 Encoder (codec hevc)\n")
	if got := chooseMaskEncoder(x264); got[1] != "libx264" {
		t.Fatalf("both encoders: picked %v, want libx264", got)
	}
	if got := chooseMaskEncoder(vt); got[1] != "h264_videotoolbox" {
		t.Fatalf("videotoolbox-only build: picked %v", got)
	}
	// The shipped Linux bundle: no libx264, OpenH264 present.
	linux := []byte(" V....D libopenh264          OpenH264 H.264 / AVC\n V....D h264_vaapi           H.264/AVC (VAAPI)\n")
	if got := chooseMaskEncoder(linux); got[1] != "libopenh264" {
		t.Fatalf("Linux bundle: got %v", got)
	}
	if got := chooseMaskEncoder(nil); got[1] != "libx264" {
		t.Fatalf("unknown build: picked %v, want the loud libx264 failure", got)
	}
}

func TestClipArgsMaskedEncSubstitutesEncoder(t *testing.T) {
	masks := [][4]float64{{0.1, 0.1, 0.4, 0.5}}
	args := ClipArgsMaskedWith("l.txt", 0, time.Minute, "o.mp4", true, masks, encoderVideoToolbox)
	s := strings.Join(args, " ")
	if !strings.Contains(s, "-c:v h264_videotoolbox") || strings.Contains(s, "libx264") {
		t.Fatalf("args = %s", s)
	}
	// The exported ClipArgsMasked keeps its libx264 default (unit-test contract).
	if s := strings.Join(ClipArgsMasked("l.txt", 0, time.Minute, "o.mp4", true, masks), " "); !strings.Contains(s, "libx264") {
		t.Fatalf("default args = %s", s)
	}
}

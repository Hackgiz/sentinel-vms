package detect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/hwinfo"
)

var mac = hwinfo.Info{OS: "darwin", CPU: "Apple M2 Pro", RenderNodes: []hwinfo.RenderNode{}, Accelerators: []hwinfo.Accelerator{}}

func vtRunner(hwaccels string, trialErr string) (Runner, *[][]string) {
	var calls [][]string
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		j := strings.Join(args, " ")
		switch {
		case strings.Contains(j, "-hwaccels"):
			return []byte(hwaccels), nil
		case strings.Contains(j, "videotoolbox=vt"):
			if trialErr != "" {
				return []byte(trialErr), errors.New("exit status 1")
			}
			return nil, nil
		}
		return nil, errors.New("unexpected command " + j)
	}, &calls
}

func TestProbeDecodeVideoToolbox(t *testing.T) {
	run, calls := vtRunner("Hardware acceleration methods:\nvideotoolbox\n", "")
	caps := ProbeDecode(context.Background(), "/Applications/Sentinel/ffmpeg", mac, "", run)
	if !reflect.DeepEqual(caps.Usable, []string{DecodeVideoToolbox}) || caps.Device != "VideoToolbox" || caps.Driver != "Apple M2 Pro" {
		t.Fatalf("caps %+v", caps)
	}
	if len(*calls) != 2 || !reflect.DeepEqual((*calls)[1], VideoToolboxTrialArgs()) {
		t.Fatalf("calls %v", *calls)
	}
	// Render nodes, VAAPI and QSV are never looked at on a Mac.
	if len(caps.Notes) != 0 {
		t.Fatalf("notes %q", caps.Notes)
	}
	plan, why := chooseDecode(AccelAuto, caps, "H265", camDecode{}, time.Now())
	if plan.Method != DecodeVideoToolbox || why != "" || !plan.hw() {
		t.Fatalf("plan %+v %q", plan, why)
	}
	if args := HWInputArgs(plan); !reflect.DeepEqual(args, []string{"-hwaccel", "videotoolbox"}) {
		t.Fatalf("input args %v", args)
	}
	args := strings.Join(detectArgs("rtsp://127.0.0.1:18554/cam", plan, true), " ")
	if !strings.Contains(args, "-loglevel warning -rtsp_transport tcp -hwaccel videotoolbox -i rtsp://") || strings.Contains(args, "hwaccel_output_format") {
		t.Fatalf("detect args %s", args)
	}
}

func TestProbeDecodeVideoToolboxUnavailable(t *testing.T) {
	// Homebrew-style ffmpeg without VideoToolbox (SENTINEL_FFMPEG).
	run, calls := vtRunner("Hardware acceleration methods:\n", "")
	caps := ProbeDecode(context.Background(), "/opt/homebrew/bin/ffmpeg", mac, "", run)
	if len(caps.Usable) != 0 || len(*calls) != 1 || !strings.Contains(caps.Notes[0], "without VideoToolbox") {
		t.Fatalf("caps %+v", caps)
	}
	// Device fails to open.
	run, _ = vtRunner("Hardware acceleration methods:\nvideotoolbox\n", "[AVHWDeviceContext @ 0x1] Device creation failed: -12.\n")
	caps = ProbeDecode(context.Background(), "ffmpeg", mac, "", run)
	if len(caps.Usable) != 0 || !strings.Contains(caps.Notes[0], "VideoToolbox didn't open: Device creation failed") {
		t.Fatalf("caps %+v", caps)
	}
	if p, why := chooseDecode(AccelAuto, caps, "H264", camDecode{}, time.Now()); p.Method != DecodeSoftware || !strings.Contains(why, "VideoToolbox") {
		t.Fatalf("plan %+v %q", p, why)
	}
}

func TestVideoToolboxFallback(t *testing.T) {
	now := time.Now()
	plan := DecodePlan{Method: DecodeVideoToolbox, Device: "VideoToolbox"}
	// libavcodec gives up on the hardware for this stream.
	if !isHWRejected("[hevc @ 0x7f] Failed setup for format videotoolbox_vld: hwaccel initialisation returned error.") {
		t.Fatal("rejection not recognized")
	}
	st := afterRun(camDecode{Rejected: true}, plan, true, false, "", now)
	if !now.Add(time.Minute).Before(st.SoftwareUntil) || !strings.Contains(st.Reason, "videotoolbox rejected") {
		t.Fatalf("rejected: %+v", st)
	}
	st = afterRun(camDecode{}, plan, false, false, "[h264 @ 0x1] vt decoder cb: output image buffer is null: -12909\nError while decoding stream: VTDecompressionSession failed", now)
	if st.SoftwareUntil.IsZero() || !strings.HasPrefix(st.Reason, "videotoolbox failed") {
		t.Fatalf("hw error: %+v", st)
	}
}

// TestVideoToolboxRealFFmpeg runs the real probe and a hardware decode with
// the ffmpeg in SENTINEL_TEST_FFMPEG (macOS only; skipped otherwise).
func TestVideoToolboxRealFFmpeg(t *testing.T) {
	ff := os.Getenv("SENTINEL_TEST_FFMPEG")
	if runtime.GOOS != "darwin" || ff == "" {
		t.Skip("macOS only; set SENTINEL_TEST_FFMPEG")
	}
	caps := ProbeDecode(context.Background(), ff, hwinfo.ProbeHost(), "", nil)
	t.Logf("caps: usable=%v device=%s driver=%s notes=%q", caps.Usable, caps.Device, caps.Driver, caps.Notes)
	if !reflect.DeepEqual(caps.Usable, []string{DecodeVideoToolbox}) {
		t.Fatalf("caps %+v", caps)
	}
	// Encode a short H.264 clip with VideoToolbox, then decode it the way a
	// detection reader does (hardware decode, frames back in system memory).
	clip := t.TempDir() + "/clip.mp4"
	if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=640x360:d=1",
		"-c:v", "h264_videotoolbox", "-y", clip).CombinedOutput(); err != nil {
		t.Skipf("no H.264 encoder to make a test clip: %v %s", err, out)
	}
	args := append([]string{"-hide_banner", "-loglevel", "warning"}, HWInputArgs(DecodePlan{Method: DecodeVideoToolbox})...)
	args = append(args, "-i", clip, "-vf", "fps=2,scale=64:36:flags=area,format=gray", "-f", "rawvideo", "pipe:1")
	out, err := exec.Command(ff, args...).Output()
	if err != nil || len(out) < 2*64*36 || len(out)%(64*36) != 0 {
		t.Fatalf("decode: %v, %d bytes", err, len(out))
	}
}

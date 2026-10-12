package detect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/hwinfo"
)

func TestParseHwaccels(t *testing.T) {
	out := "Hardware acceleration methods:\nvdpau\ncuda\nvaapi\nqsv\ndrm\nopencl\nvulkan\n\n"
	want := []string{"vdpau", "cuda", "vaapi", "qsv", "drm", "opencl", "vulkan"}
	if got := ParseHwaccels(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestHWInputArgs(t *testing.T) {
	cases := []struct {
		plan DecodePlan
		want []string
	}{
		{DecodePlan{Method: DecodeSoftware}, nil},
		{DecodePlan{Method: DecodeVAAPI, Device: "/dev/dri/renderD128"}, []string{
			"-init_hw_device", "vaapi=va:/dev/dri/renderD128,connection_type=drm", "-hwaccel", "vaapi", "-hwaccel_device", "va"}},
		{DecodePlan{Method: DecodeQSV, Device: "/dev/dri/renderD128", Codec: "H264"}, []string{
			"-init_hw_device", "vaapi=va:/dev/dri/renderD128,connection_type=drm", "-init_hw_device", "qsv=qs@va", "-c:v", "h264_qsv"}},
		{DecodePlan{Method: DecodeQSV, Device: "/dev/dri/renderD129", Codec: "H265"}, []string{
			"-init_hw_device", "vaapi=va:/dev/dri/renderD129,connection_type=drm", "-init_hw_device", "qsv=qs@va", "-c:v", "hevc_qsv"}},
		{DecodePlan{Method: DecodeQSV, Device: "/dev/dri/renderD128", Codec: "MJPEG"}, nil},
	}
	for _, c := range cases {
		if got := HWInputArgs(c.plan); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: got %v want %v", c.plan, got, c.want)
		}
	}
}

func TestDetectArgsSoftwareUnchanged(t *testing.T) {
	// The software command line must be exactly what v0.4 ran.
	url := "rtsp://127.0.0.1:8554/cam"
	want := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-rtsp_transport", "tcp", "-i", url, "-an", "-sn", "-dn",
		"-vf", "fps=2,scale=64:36:flags=area,format=gray", "-f", "rawvideo", "pipe:1"}
	if got := detectArgs(url, DecodePlan{Method: DecodeSoftware}, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("motion only:\n got %q\nwant %q", got, want)
	}
	got := detectArgs(url, DecodePlan{Method: DecodeSoftware}, true)
	joined := strings.Join(got, " ")
	if !strings.HasPrefix(joined, "-nostdin -hide_banner -loglevel error -rtsp_transport tcp -i "+url+" -an -sn -dn -filter_complex [0:v]fps=2,split=2[m][d];[m]scale=64:36:flags=area,format=gray[mo];[d]scale=416:416:force_original_aspect_ratio=decrease:flags=bilinear,pad=416:416:0:0:color=0x727272,format=bgr24[do] -map [mo] -f rawvideo pipe:1 -map [do] -f rawvideo pipe:3") {
		t.Fatalf("objects: %s", joined)
	}
}

func TestDetectArgsVAAPI(t *testing.T) {
	got := detectArgs("rtsp://x/cam", DecodePlan{Method: DecodeVAAPI, Device: "/dev/dri/renderD128"}, true)
	j := strings.Join(got, " ")
	// hwaccel options are input options: they must come before -i. No
	// -hwaccel_output_format, so frames come back to system memory and the
	// software filter graph (scale/pad/bgr24) still applies.
	if !strings.Contains(j, "-loglevel warning -rtsp_transport tcp -init_hw_device vaapi=va:/dev/dri/renderD128,connection_type=drm -hwaccel vaapi -hwaccel_device va -i rtsp://x/cam") {
		t.Fatalf("args: %s", j)
	}
	if strings.Contains(j, "hwaccel_output_format") || strings.Contains(j, "scale_vaapi") {
		t.Fatalf("frames must be downloaded by ffmpeg: %s", j)
	}
	if !strings.Contains(j, "format=bgr24[do]") || !strings.HasSuffix(j, "pipe:3") {
		t.Fatalf("detection output missing: %s", j)
	}
}

// fakeFFmpeg answers the probe commands like a real ffmpeg would.
type fakeFFmpeg struct {
	hwaccels string
	vaapiErr string // non-empty: VAAPI trial fails with this output
	qsvErr   string
	calls    [][]string
}

func (f *fakeFFmpeg) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	j := strings.Join(args, " ")
	switch {
	case strings.Contains(j, "-hwaccels"):
		return []byte(f.hwaccels), nil
	case strings.Contains(j, "qsv=qs@va"): // QSV derives from the VAAPI device
		if f.qsvErr != "" {
			return []byte(f.qsvErr), errors.New("exit status 1")
		}
		return nil, nil
	case strings.Contains(j, "vaapi=va:") && strings.Contains(j, ",connection_type=drm"):
		if f.vaapiErr != "" {
			return []byte(f.vaapiErr), errors.New("exit status 1")
		}
		return []byte("[AVHWDeviceContext @ 0x55] Initialised VAAPI connection: version 1.22\n[AVHWDeviceContext @ 0x55] VAAPI driver: Intel iHD driver for Intel(R) Gen Graphics - 24.1.0 ().\n"), nil
	}
	return nil, errors.New("unexpected command " + j)
}

var n100 = hwinfo.Info{CPU: "Intel(R) N100", RenderNodes: []hwinfo.RenderNode{{Path: "/dev/dri/renderD128", Driver: "i915", Vendor: "Intel"}}}

func TestProbeDecodeIntel(t *testing.T) {
	f := &fakeFFmpeg{hwaccels: "Hardware acceleration methods:\nvaapi\nqsv\ndrm\n"}
	caps := ProbeDecode(context.Background(), "/opt/sentinel/ffmpeg", n100, "", f.run)
	if !reflect.DeepEqual(caps.Usable, []string{DecodeVAAPI, DecodeQSV}) || caps.Device != "/dev/dri/renderD128" {
		t.Fatalf("caps %+v", caps)
	}
	if caps.Driver != "Intel iHD driver for Intel(R) Gen Graphics - 24.1.0 ()" {
		t.Fatalf("driver %q", caps.Driver)
	}
	if !reflect.DeepEqual(f.calls[1], VAAPITrialArgs("/dev/dri/renderD128")) {
		t.Fatalf("trial %v", f.calls[1])
	}
}

func TestProbeDecodeVAAPIDriverMissing(t *testing.T) {
	f := &fakeFFmpeg{
		hwaccels: "Hardware acceleration methods:\nvaapi\nqsv\n",
		vaapiErr: "[AVHWDeviceContext @ 0x1] libva: /usr/lib/x86_64-linux-gnu/dri/iHD_drv_video.so init failed\n[AVHWDeviceContext @ 0x1] Failed to initialise VAAPI connection: -1 (unknown libva error).\nDevice creation failed: -5.\n",
		qsvErr:   "Error creating a MFX session: -9.\n",
	}
	caps := ProbeDecode(context.Background(), "/opt/sentinel/ffmpeg", n100, "", f.run)
	if len(caps.Usable) != 0 {
		t.Fatalf("usable %v", caps.Usable)
	}
	if len(caps.Notes) != 2 || !strings.Contains(caps.Notes[0], "Failed to initialise VAAPI connection") || !strings.Contains(caps.Notes[1], "MFX session") {
		t.Fatalf("notes %q", caps.Notes)
	}
	plan, why := chooseDecode(AccelAuto, caps, "H264", camDecode{}, time.Now())
	if plan.Method != DecodeSoftware || !strings.Contains(why, "VAAPI") {
		t.Fatalf("plan %+v %q", plan, why)
	}
}

func TestProbeDecodeAMDSkipsQSV(t *testing.T) {
	f := &fakeFFmpeg{hwaccels: "Hardware acceleration methods:\nvaapi\nqsv\n"}
	amd := hwinfo.Info{RenderNodes: []hwinfo.RenderNode{{Path: "/dev/dri/renderD128", Driver: "amdgpu", Vendor: "AMD"}}}
	caps := ProbeDecode(context.Background(), "ffmpeg", amd, "", f.run)
	if !reflect.DeepEqual(caps.Usable, []string{DecodeVAAPI}) || len(f.calls) != 2 {
		t.Fatalf("caps %+v calls %v", caps, f.calls)
	}
}

func TestProbeDecodeNoGPUOrNoSupport(t *testing.T) {
	f := &fakeFFmpeg{hwaccels: "Hardware acceleration methods:\nvaapi\n"}
	caps := ProbeDecode(context.Background(), "ffmpeg", hwinfo.Info{}, "", f.run)
	if len(caps.Usable) != 0 || len(f.calls) != 1 || !strings.Contains(caps.Notes[0], "render node") {
		t.Fatalf("no gpu: %+v", caps)
	}

	// ffmpeg without VAAPI (e.g. a minimal system build) on an Intel box.
	f = &fakeFFmpeg{hwaccels: "Hardware acceleration methods:\nvdpau\n"}
	caps = ProbeDecode(context.Background(), "ffmpeg", n100, "", f.run)
	if len(caps.Usable) != 0 || !strings.Contains(strings.Join(caps.Notes, " "), "SENTINEL_FFMPEG") {
		t.Fatalf("no vaapi build: %+v", caps)
	}

	// Raspberry Pi 5: render node exists (v3d) but there's nothing to decode with.
	pi := hwinfo.Info{Board: "Raspberry Pi 5 Model B Rev 1.0", RenderNodes: []hwinfo.RenderNode{{Path: "/dev/dri/renderD128", Driver: "v3d", Vendor: "Broadcom"}}}
	f = &fakeFFmpeg{hwaccels: "Hardware acceleration methods:\nvaapi\ndrm\n"}
	caps = ProbeDecode(context.Background(), "ffmpeg", pi, "", f.run)
	if len(caps.Usable) != 0 || len(f.calls) != 1 || !strings.Contains(caps.Notes[0], "Raspberry Pi 5 has no H.264") {
		t.Fatalf("pi5: %+v", caps)
	}

	// Unknown GPU vendor (NVIDIA): don't guess; an explicit device overrides.
	nv := hwinfo.Info{RenderNodes: []hwinfo.RenderNode{{Path: "/dev/dri/renderD128", Driver: "nvidia", Vendor: "NVIDIA"}}}
	f = &fakeFFmpeg{hwaccels: "Hardware acceleration methods:\nvaapi\n"}
	if caps = ProbeDecode(context.Background(), "ffmpeg", nv, "", f.run); len(caps.Usable) != 0 {
		t.Fatalf("nvidia: %+v", caps)
	}
	f = &fakeFFmpeg{hwaccels: "Hardware acceleration methods:\nvaapi\n"}
	if caps = ProbeDecode(context.Background(), "ffmpeg", nv, "/dev/dri/renderD129", f.run); len(caps.Usable) != 1 || caps.Device != "/dev/dri/renderD129" {
		t.Fatalf("override: %+v", caps)
	}

	if caps = ProbeDecode(context.Background(), "", n100, "", f.run); len(caps.Usable) != 0 || caps.Notes[0] != "ffmpeg not found" {
		t.Fatalf("no ffmpeg: %+v", caps)
	}
}

// Real output of the bundled ffmpeg 8.1 (linuxarm64 and linux64 builds) on a
// machine without a GPU, captured 2026-10-10.
const (
	realVAAPIDecodeFailure = `[VAAPI @ 0xac9114758470] No VA display found for device /dev/dri/renderD128.
Device creation failed: -22.
[vist#0:0/h264 @ 0xac9114697ec0] [dec:h264 @ 0xac9114685ff0] No device available for decoder: device type vaapi needed for codec h264.
[vist#0:0/h264 @ 0xac9114697ec0] [dec:h264 @ 0xac9114685ff0] Hardware device setup failed for decoder: Invalid argument
[fc#0 @ 0xac911467ac20] Error binding an input stream to complex filtergraph input fps:default.
Error binding filtergraph inputs/outputs: Invalid argument`
	realVAAPITrialFailure = `[VAAPI @ 0x55555e8d2000] Failed to open /dev/dri/renderD128 as DRM device node.
[VAAPI @ 0x55555e8d2000] No VA display found for device /dev/dri/renderD128.
Device creation failed: -22.
Failed to set value 'vaapi=va:/dev/dri/renderD128,connection_type=drm' for option 'init_hw_device': Invalid argument
Error parsing global options: Invalid argument

Exiting with exit code -22`
)

func TestExplainAndClassifyRealFailures(t *testing.T) {
	if got := explainFailure(realVAAPITrialFailure, nil); got != "No VA display found for device /dev/dri/renderD128." {
		t.Fatalf("trial: %q", got)
	}
	if !isHWError(realVAAPIDecodeFailure) {
		t.Fatal("decode failure must count as a hardware error")
	}
	st := afterRun(camDecode{}, DecodePlan{Method: DecodeVAAPI, Device: "/dev/dri/renderD128"}, false, false, realVAAPIDecodeFailure, time.Now())
	if st.SoftwareUntil.IsZero() || !strings.HasPrefix(st.Reason, "vaapi failed: ") {
		t.Fatalf("state %+v", st)
	}
	if got := explainFailure("Device creation failed: -12.\nFailed to set value 'qsv=qs@va' for option 'init_hw_device': Cannot allocate memory\nError parsing global options: Cannot allocate memory\n\nExiting with exit code -12", nil); got != "Device creation failed: -12." {
		t.Fatalf("qsv: %q", got)
	}
	if isHWError("[rtsp @ 0x1] method DESCRIBE failed: 404 Not Found\nrtsp://127.0.0.1:8554/cam: Server returned 404 Not Found") {
		t.Fatal("a missing stream is not a GPU problem")
	}
}

func TestChooseDecode(t *testing.T) {
	now := time.Now()
	both := DecodeCaps{Usable: []string{DecodeVAAPI, DecodeQSV}, Device: "/dev/dri/renderD128"}
	if p, _ := chooseDecode(AccelAuto, both, "H264", camDecode{}, now); p.Method != DecodeVAAPI || p.Device != "/dev/dri/renderD128" {
		t.Fatalf("auto: %+v", p)
	}
	if p, why := chooseDecode(AccelOff, both, "H264", camDecode{}, now); p.Method != DecodeSoftware || why != NoteAccelOff {
		t.Fatalf("off: %+v %q", p, why)
	}
	st := camDecode{SoftwareUntil: now.Add(time.Minute), Reason: "vaapi failed"}
	if p, why := chooseDecode(AccelAuto, both, "H264", st, now); p.Method != DecodeSoftware || why != "vaapi failed" {
		t.Fatalf("fallback: %+v %q", p, why)
	}
	if p, _ := chooseDecode(AccelAuto, both, "H264", st, now.Add(2*time.Minute)); p.Method != DecodeVAAPI {
		t.Fatalf("retry after fallback period: %+v", p)
	}
	qsvOnly := DecodeCaps{Usable: []string{DecodeQSV}, Device: "/dev/dri/renderD128"}
	if p, _ := chooseDecode(AccelAuto, qsvOnly, "H265", camDecode{}, now); p.Method != DecodeQSV || p.Codec != "H265" {
		t.Fatalf("qsv: %+v", p)
	}
	if p, why := chooseDecode(AccelAuto, qsvOnly, "", camDecode{}, now); p.Method != DecodeSoftware || !strings.Contains(why, "codec") {
		t.Fatalf("qsv without codec: %+v %q", p, why)
	}
}

func TestAfterRunFallback(t *testing.T) {
	now := time.Now()
	va := DecodePlan{Method: DecodeVAAPI, Device: "/dev/dri/renderD128"}

	// Healthy hardware run.
	if st := afterRun(camDecode{NoFrameRuns: 2}, va, true, false, "", now); st.NoFrameRuns != 0 || !st.SoftwareUntil.IsZero() {
		t.Fatalf("ok run: %+v", st)
	}
	// Hardware error before any frame → software immediately.
	st := afterRun(camDecode{}, va, false, false, "Failed to initialise VAAPI connection: -1 (unknown libva error).\nDevice creation failed: -5.", now)
	if !st.SoftwareUntil.After(now) || !strings.HasPrefix(st.Reason, "vaapi failed: ") {
		t.Fatalf("hw error: %+v", st)
	}
	// Hung decoder (watchdog) → software.
	if st := afterRun(camDecode{}, va, false, true, "", now); !st.SoftwareUntil.After(now) || !strings.Contains(st.Reason, "no frames within") {
		t.Fatalf("timeout: %+v", st)
	}
	// GPU rejected the stream mid-run (ffmpeg fell back internally) → software next time.
	if st := afterRun(camDecode{Rejected: true}, va, true, false, "", now); !st.SoftwareUntil.After(now) || st.Rejected {
		t.Fatalf("rejected: %+v", st)
	}
	// Camera/network errors aren't blamed on the GPU — until it happens 3 times.
	st = camDecode{}
	for i := 1; i <= hwMaxNoFrameRuns; i++ {
		st = afterRun(st, va, false, false, "rtsp://127.0.0.1:8554/cam: Connection refused", now)
		if fell := !st.SoftwareUntil.IsZero(); fell != (i == hwMaxNoFrameRuns) {
			t.Fatalf("run %d: %+v", i, st)
		}
	}
	// Software runs never change hardware state.
	sw := camDecode{NoFrameRuns: 1}
	if got := afterRun(sw, DecodePlan{Method: DecodeSoftware}, false, false, "vaapi", now); got.NoFrameRuns != 1 || !got.SoftwareUntil.IsZero() {
		t.Fatalf("software: %+v", got)
	}
}

func TestDecodeRunSeesRejection(t *testing.T) {
	m := &Manager{}
	m.ConfigureAccel(AccelAuto, DecodeCaps{Usable: []string{DecodeVAAPI}, Device: "/dev/dri/renderD128"}, nil)
	plan := m.decodePlan("c1")
	_, r := m.beginDecode(context.Background(), "c1", plan)
	r.Write([]byte("[h264 @ 0x5] Failed setup for format vaapi: hwaccel initialisation returned error.\n"))
	if _, cams := m.DecodeStatus(); len(cams) != 1 || cams[0].Method != DecodeSoftware || !strings.Contains(cams[0].Reason, "can't decode") {
		t.Fatalf("live status %+v", cams)
	}
	r.frame()
	r.end()
	if p := m.decodePlan("c1"); p.Method != DecodeSoftware {
		t.Fatalf("next plan %+v", p)
	}
}

// writeFakeFFmpeg writes a shell script standing in for ffmpeg: with
// -hwaccel it behaves as given; otherwise it streams black 64×36 frames.
func writeFakeFFmpeg(t *testing.T, hwBehaviour string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	p := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"-hwaccel\" ]; then\n" + hwBehaviour + "\n  fi\ndone\n" +
		"while :; do head -c 2304 /dev/zero || exit 0; sleep 0.05; done\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func cameraStatus(m *Manager, id string) CameraDecode {
	_, cams := m.DecodeStatus()
	for _, c := range cams {
		if c.CameraID == id {
			return c
		}
	}
	return CameraDecode{}
}

// TestPerCameraFallbackEndToEnd runs real reader processes: hardware decode
// fails to open, the camera falls back to software and keeps detecting.
func TestPerCameraFallbackEndToEnd(t *testing.T) {
	ff := writeFakeFFmpeg(t, `    echo "[AVHWDeviceContext @ 0x1] Failed to initialise VAAPI connection: -1 (unknown libva error)." >&2
    echo "Device creation failed: -5." >&2
    exit 1`)
	m := &Manager{FFmpeg: ff, OnMotion: func(Motion) {}}
	m.ConfigureAccel(AccelAuto, DecodeCaps{Usable: []string{DecodeVAAPI}, Device: "/dev/dri/renderD128"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Sync(ctx, []Source{{CameraID: "c1", URL: "rtsp://127.0.0.1:1/cam", Level: 2}})

	waitFor(t, "software fallback", func() bool {
		c := cameraStatus(m, "c1")
		return c.Method == DecodeSoftware && c.Running && strings.HasPrefix(c.Reason, "vaapi failed")
	})

	// Turning acceleration off restarts readers in software with that reason.
	m.SetAccelMode(AccelOff)
	waitFor(t, "mode off", func() bool {
		c := cameraStatus(m, "c1")
		return c.Method == DecodeSoftware && c.Running && c.Reason == NoteAccelOff
	})
	if m.AccelMode() != AccelOff {
		t.Fatal("mode not off")
	}
}

// TestHungHardwareDecodeIsKilled: a hardware run that never delivers a frame
// is stopped by the watchdog and the camera moves to software.
func TestHungHardwareDecodeIsKilled(t *testing.T) {
	old := hwFirstFrameWait
	hwFirstFrameWait = 300 * time.Millisecond
	defer func() { hwFirstFrameWait = old }()
	ff := writeFakeFFmpeg(t, "    exec sleep 30")
	m := &Manager{FFmpeg: ff, OnMotion: func(Motion) {}}
	m.ConfigureAccel(AccelAuto, DecodeCaps{Usable: []string{DecodeVAAPI}, Device: "/dev/dri/renderD128"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Sync(ctx, []Source{{CameraID: "c1", URL: "rtsp://127.0.0.1:1/cam", Level: 2}})
	waitFor(t, "watchdog fallback", func() bool {
		c := cameraStatus(m, "c1")
		return c.Method == DecodeSoftware && c.Running && strings.Contains(c.Reason, "no frames within")
	})
}

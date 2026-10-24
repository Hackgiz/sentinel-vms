package homekit

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/media"
)

// No test probes real hardware: software unless a test installs its own caps.
func init() {
	probeCaps = func(context.Context, string) detect.DecodeCaps { return detect.DecodeCaps{} }
}

func testParams() StreamParams {
	return StreamParams{
		Address: "192.168.1.20", VideoPort: 51000,
		SRTPKey: bytes.Repeat([]byte{1}, 16), SRTPSalt: bytes.Repeat([]byte{2}, 14),
		SSRC: 1234, PayloadType: 99, Width: 1280, Height: 720, FPS: 30, BitrateKbps: 2000,
	}
}

func testDeps(ffmpeg string) *Deps {
	return &Deps{FFmpeg: ffmpeg, Sup: &media.Supervisor{Ports: media.Ports{RTSP: 18554}}}
}

func has(args []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		if reflect.DeepEqual(args[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}

func TestRTSPURL(t *testing.T) {
	if got := rtspURL(testDeps("x"), "cam1"); got != "rtsp://127.0.0.1:18554/cam1" {
		t.Fatalf("got %q", got)
	}
}

func TestCaptureArgs(t *testing.T) {
	cases := []struct {
		w, h   int
		scaled bool
	}{{640, 360, true}, {0, 0, false}}
	for _, c := range cases {
		args := captureArgs("rtsp://u", c.w, c.h)
		if !has(args, "-frames:v", "1") || !has(args, "-i", "rtsp://u") || args[len(args)-1] != "pipe:1" {
			t.Fatalf("w=%d: %v", c.w, args)
		}
		if got := has(args, "-vf", "scale=640:360:force_original_aspect_ratio=decrease:force_divisible_by=2"); got != c.scaled {
			t.Fatalf("w=%d scaled=%v: %v", c.w, got, args)
		}
	}
}

func TestCanCopy(t *testing.T) {
	p := testParams() // 1280x720
	cases := []struct {
		name string
		src  source
		want bool
	}{
		{"h264 smaller", source{"H264", 640, 360}, true},
		{"h264 equal", source{"H264", 1280, 720}, true},
		{"h264 larger", source{"H264", 1920, 1080}, false},
		{"h265", source{"H265", 640, 360}, false},
		{"unknown size", source{"H264", 0, 0}, false},
		{"unknown codec", source{"", 640, 360}, false},
	}
	for _, c := range cases {
		if got := canCopy(c.src, p); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestChooseEncoder(t *testing.T) {
	cases := []struct {
		name   string
		accel  string
		usable []string
		want   string
	}{
		{"off", "off", []string{detect.DecodeVAAPI}, "libx264"},
		{"none usable", "auto", nil, "libx264"},
		{"videotoolbox", "auto", []string{detect.DecodeVideoToolbox}, "h264_videotoolbox"},
		{"vaapi first", "auto", []string{detect.DecodeVAAPI, detect.DecodeQSV}, "h264_vaapi"},
		{"qsv", "auto", []string{detect.DecodeQSV}, "h264_qsv"},
		{"unset means auto", "", []string{detect.DecodeVAAPI}, "h264_vaapi"},
	}
	for _, c := range cases {
		caps := detect.DecodeCaps{Usable: c.usable, Device: "/dev/dri/renderD128"}
		if got := chooseEncoder(c.accel, caps, "H264").Name; got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestStreamArgsCopy(t *testing.T) {
	args := streamArgs("rtsp://u", testParams(), true, softwareEncoder)
	// key||salt = 16x0x01 + 14x0x02, base64
	want := "AQEBAQEBAQEBAQEBAQEBAQICAgICAgICAgICAgIC"
	for _, seq := range [][]string{
		{"-c:v", "copy"}, {"-payload_type", "99"}, {"-ssrc", "1234"}, {"-f", "rtp"},
		{"-srtp_out_suite", "AES_CM_128_HMAC_SHA1_80"}, {"-srtp_out_params", want},
	} {
		if !has(args, seq...) {
			t.Errorf("missing %v in %v", seq, args)
		}
	}
	if has(args, "-c:v", "libx264") || has(args, "-r") {
		t.Errorf("copy must not encode: %v", args)
	}
	if got := args[len(args)-1]; got != "srtp://192.168.1.20:51000?rtcpport=51000&pkt_size=1378" {
		t.Errorf("output %q", got)
	}
}

func TestStreamArgsEncode(t *testing.T) {
	p := testParams()
	cases := []struct {
		name string
		enc  encoder
		want [][]string
		not  []string
	}{
		{"software", softwareEncoder,
			[][]string{{"-c:v", "libx264"}, {"-tune", "zerolatency"}, {"-g", "60"}, {"-r", "30"}, {"-b:v", "2000k"}}, []string{"-hwaccel"}},
		{"videotoolbox", encoder{Name: "h264_videotoolbox", Method: detect.DecodeVideoToolbox, Decode: detect.DecodePlan{Method: detect.DecodeVideoToolbox}},
			[][]string{{"-hwaccel", "videotoolbox"}, {"-c:v", "h264_videotoolbox"}, {"-realtime", "1"}}, nil},
		{"vaapi", encoder{Name: "h264_vaapi", Method: detect.DecodeVAAPI, Decode: detect.DecodePlan{Method: detect.DecodeVAAPI, Device: "/dev/dri/renderD128"}},
			[][]string{{"-hwaccel", "vaapi"}, {"-filter_hw_device", "va"}, {"-c:v", "h264_vaapi"}}, nil},
		{"qsv", encoder{Name: "h264_qsv", Method: detect.DecodeQSV, Decode: detect.DecodePlan{Method: detect.DecodeQSV, Device: "/dev/dri/renderD128", Codec: "H265"}},
			[][]string{{"-c:v", "hevc_qsv"}, {"-c:v", "h264_qsv"}}, nil},
	}
	for _, c := range cases {
		args := streamArgs("rtsp://u", p, false, c.enc)
		for _, seq := range c.want {
			if !has(args, seq...) {
				t.Errorf("%s: missing %v in %v", c.name, seq, args)
			}
		}
		for _, n := range c.not {
			if has(args, n) {
				t.Errorf("%s: unexpected %s", c.name, n)
			}
		}
		// Hardware decode options must precede the input.
		if i, j := indexOf(args, "-i"), indexOf(args, "-hwaccel"); j >= 0 && j > i {
			t.Errorf("%s: -hwaccel after -i", c.name)
		}
		if !has(args, "-force_key_frames", "expr:gte(t,n_forced*2)") {
			t.Errorf("%s: keyframe interval not bounded", c.name)
		}
	}
}

func indexOf(a []string, s string) int {
	for i, v := range a {
		if v == s {
			return i
		}
	}
	return -1
}

func TestStreamArgsFPSBounds(t *testing.T) {
	p := testParams()
	p.FPS = 0
	if args := streamArgs("u", p, false, softwareEncoder); !has(args, "-g", "60") {
		t.Fatalf("default fps should give 60-frame GOP: %v", args)
	}
	p.FPS = 15
	if args := streamArgs("u", p, false, softwareEncoder); !has(args, "-g", "30") {
		t.Fatalf("15 fps should give 30-frame GOP: %v", args)
	}
}

func TestIPv6Output(t *testing.T) {
	p := testParams()
	p.Address = "fe80::1"
	if u, _ := srtpOutput(p); !strings.HasPrefix(u, "srtp://[fe80::1]:51000?") {
		t.Fatalf("got %q", u)
	}
}

func TestValidate(t *testing.T) {
	bad := []func(*StreamParams){
		func(p *StreamParams) { p.Address = "" },
		func(p *StreamParams) { p.VideoPort = 0 },
		func(p *StreamParams) { p.SRTPKey = p.SRTPKey[:8] },
		func(p *StreamParams) { p.SRTPSalt = nil },
		func(p *StreamParams) { p.Width = 0 },
	}
	for i, mut := range bad {
		p := testParams()
		mut(&p)
		if p.validate() == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if err := testParams().validate(); err != nil {
		t.Fatal(err)
	}
}

// ---- process lifecycle, with a fake ffmpeg script -------------------------

// fakeFFmpeg writes a shell script standing in for ffmpeg. It appends its
// argv to <dir>/log, prints a JPEG header when asked for one frame, exits 1
// when the argv contains failOn (if set), exits at once when quick, and
// otherwise sleeps like a running stream.
func fakeFFmpeg(t *testing.T, failOn string, quick bool) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "log")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\n"
	if failOn != "" {
		script += "case \"$*\" in *" + failOn + "*) exit 1;; esac\n"
	}
	script += "case \"$*\" in *frames:v*) printf '\\377\\330\\377\\340jpegdata'; exit 0;; esac\n"
	if quick {
		script += "exit 0\n"
	} else {
		script += "exec sleep 30\n"
	}
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func waitForStream(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCaptureJPEG(t *testing.T) {
	bin, log := fakeFFmpeg(t, "", false)
	b, err := ffmpegCaptureJPEG(context.Background(), testDeps(bin), "cam1", 640, 360)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte{0xFF, 0xD8}) {
		t.Fatalf("got %q", b)
	}
	if l, _ := os.ReadFile(log); !strings.Contains(string(l), "rtsp://127.0.0.1:18554/cam1") {
		t.Fatalf("log %q", l)
	}
}

func TestCaptureJPEGFailures(t *testing.T) {
	if _, err := ffmpegCaptureJPEG(context.Background(), testDeps(""), "c", 1, 1); err != ErrStreamingUnavailable {
		t.Fatalf("no ffmpeg: %v", err)
	}
	bin, _ := fakeFFmpeg(t, "frames:v", false)
	if _, err := ffmpegCaptureJPEG(context.Background(), testDeps(bin), "c", 1, 1); err == nil {
		t.Fatal("ffmpeg failure not reported")
	}
}

func TestCaptureJPEGTimeout(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ffmpegCaptureJPEG(ctx, testDeps(bin), "c", 1, 1); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("did not stop at the deadline")
	}
}

func TestSessionStopIdempotent(t *testing.T) {
	bin, _ := fakeFFmpeg(t, "", false)
	s, err := ffmpegStartStream(context.Background(), testDeps(bin), "stop-cam", testParams())
	if err != nil {
		t.Fatal(err)
	}
	if sessions.count("stop-cam") != 1 {
		t.Fatal("session not registered")
	}
	s.Stop()
	s.Stop()
	if sessions.count("stop-cam") != 0 {
		t.Fatal("session still registered after Stop")
	}
}

func TestSessionFiresStopWhenFFmpegExits(t *testing.T) {
	bin, _ := fakeFFmpeg(t, "", true)
	s, err := ffmpegStartStream(context.Background(), testDeps(bin), "exit-cam", testParams())
	if err != nil {
		t.Fatal(err)
	}
	waitForStream(t, "session to unregister", func() bool { return sessions.count("exit-cam") == 0 })
	s.Stop() // still safe after the self-exit
}

func TestSessionContextEnd(t *testing.T) {
	bin, _ := fakeFFmpeg(t, "", false)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := ffmpegStartStream(ctx, testDeps(bin), "ctx-cam", testParams()); err != nil {
		t.Fatal(err)
	}
	cancel()
	waitForStream(t, "session to unregister", func() bool { return sessions.count("ctx-cam") == 0 })
}

func TestMaxTwoSessionsOldestKilled(t *testing.T) {
	bin, _ := fakeFFmpeg(t, "", false)
	d := testDeps(bin)
	var ss []*Session
	for range 3 {
		s, err := ffmpegStartStream(context.Background(), d, "cap-cam", testParams())
		if err != nil {
			t.Fatal(err)
		}
		ss = append(ss, s)
	}
	defer func() {
		for _, s := range ss {
			s.Stop()
		}
	}()
	if n := sessions.count("cap-cam"); n != 2 {
		t.Fatalf("want 2 live sessions, got %d", n)
	}
	// The first (oldest) is gone: its Stop returns immediately and the
	// survivors are the 2nd and 3rd.
	sessions.mu.Lock()
	list := sessions.byCam["cap-cam"]
	sessions.mu.Unlock()
	if len(list) != 2 {
		t.Fatalf("got %d", len(list))
	}
	// Another camera is unaffected.
	other, err := ffmpegStartStream(context.Background(), d, "other-cam", testParams())
	if err != nil {
		t.Fatal(err)
	}
	other.Stop()
	if sessions.count("cap-cam") != 2 {
		t.Fatal("other camera disturbed the cap")
	}
}

func TestHardwareEncodeFallsBackToSoftware(t *testing.T) {
	bin, log := fakeFFmpeg(t, "h264_vaapi", false)
	old := probeCaps
	probeCaps = func(context.Context, string) detect.DecodeCaps {
		return detect.DecodeCaps{Usable: []string{detect.DecodeVAAPI}, Device: "/dev/dri/renderD128"}
	}
	defer func() { probeCaps = old }()
	d := testDeps(bin)
	d.Accel = "auto"
	s, err := ffmpegStartStream(context.Background(), d, "fb-cam", testParams())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	waitForStream(t, "software retry", func() bool {
		b, _ := os.ReadFile(log)
		return strings.Contains(string(b), "libx264")
	})
	b, _ := os.ReadFile(log)
	var lines []string // stream runs only: the one-off "-encoders" probe is not one
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.Contains(l, "-encoders") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "h264_vaapi") || !strings.Contains(lines[1], "libx264") {
		t.Fatalf("log: %q", b)
	}
}

func TestStartStreamRejectsBadParams(t *testing.T) {
	bin, _ := fakeFFmpeg(t, "", false)
	p := testParams()
	p.SRTPKey = nil
	if _, err := ffmpegStartStream(context.Background(), testDeps(bin), "c", p); err == nil {
		t.Fatal("accepted bad key")
	}
	if _, err := ffmpegStartStream(context.Background(), testDeps(""), "c", testParams()); err != ErrStreamingUnavailable {
		t.Fatalf("got %v", err)
	}
}

// The shipped LGPL ffmpeg has no libx264: HomeKit must encode with OpenH264,
// with options it understands (no -preset/-tune).
func TestSoftwareEncoderFallsBackToOpenH264(t *testing.T) {
	old := listEncoders
	t.Cleanup(func() { listEncoders = old })
	swEncMu.Lock()
	swEncCache = map[string]encoder{}
	swEncMu.Unlock()
	listEncoders = func(_ context.Context, ffmpeg string) []byte {
		if ffmpeg == "x264-build" {
			return []byte(" V....D libx264              libx264 H.264\n V....D libopenh264 OpenH264\n")
		}
		return []byte(" V....D libopenh264          OpenH264 H.264\n V....D h264_vaapi           VAAPI\n")
	}
	if got := softwareEncoderFor(context.Background(), "lgpl-build"); got.Name != "libopenh264" {
		t.Fatalf("lgpl build: %q", got.Name)
	}
	if got := softwareEncoderFor(context.Background(), "x264-build"); got.Name != "libx264" {
		t.Fatalf("x264 build: %q", got.Name)
	}
	args := streamArgs("rtsp://u", testParams(), false, openH264Encoder)
	if !has(args, "-c:v", "libopenh264") || has(args, "-tune", "zerolatency") || has(args, "-preset", "veryfast") {
		t.Fatalf("openh264 args: %v", args)
	}
}

package homekit

// ffmpeg media pipeline for the HomeKit bridge (see HOMEKIT.md, "Media
// pipeline"): JPEG snapshots and live SRTP sessions.
//
// Source: always the loopback MediaMTX live path, never the camera's own
// RTSP URL, so camera credentials stay out of this package.
//
// Stream-copy detection needs no probe process. The media supervisor already
// polls MediaMTX's API and holds each path's codec and resolution
// (Supervisor.State, keyed by path name, "-sub" paths included), so
// "H264 and no bigger than the requested profile" is a map lookup. A path
// whose size is not known yet (0x0) is encoded, never copied.
//
// Hardware encode reuses detect's probe (ProbeDecode, run once per ffmpeg
// binary): the first usable method, best first, picks the encoder, and the
// decoder arguments come from detect.HWInputArgs, so decode and encode share
// one device. Accel "off" forces libx264. If a hardware encode dies within
// hwEarlyExit it is retried once in software.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/childproc"
	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/hwinfo"
)

func init() {
	captureJPEG = ffmpegCaptureJPEG
	startStream = ffmpegStartStream
}

const (
	captureTimeout     = 5 * time.Second
	maxSessionsPerCam  = 2
	maxKeyframeSeconds = 2
	srtpPktSize        = 1378
)

// hwEarlyExit: a hardware encode dying sooner than this falls back to
// software (var for tests).
var hwEarlyExit = 5 * time.Second

// rtspURL is the loopback MediaMTX URL for a camera's live path.
func rtspURL(d *Deps, camID string) string {
	return fmt.Sprintf("rtsp://127.0.0.1:%d/%s", d.Sup.Ports.RTSP, d.Sup.LivePath(camID))
}

// ---- snapshots -----------------------------------------------------------

// captureArgs is the ffmpeg command line for one JPEG: a single frame scaled
// to fit w×h (aspect preserved; w or h <= 0 = no scaling) on stdout.
func captureArgs(url string, w, h int) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-rtsp_transport", "tcp", "-i", url, "-an", "-sn", "-dn", "-frames:v", "1"}
	if w > 0 && h > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2", w, h))
	}
	return append(args, "-q:v", "5", "-f", "mjpeg", "pipe:1")
}

// ffmpegCaptureJPEG grabs one frame from the camera's live path.
func ffmpegCaptureJPEG(ctx context.Context, d *Deps, camID string, w, h int) ([]byte, error) {
	if d == nil || d.FFmpeg == "" || d.Sup == nil {
		return nil, ErrStreamingUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.FFmpeg, captureArgs(rtspURL(d, camID), w, h)...)
	cmd.WaitDelay = 2 * time.Second
	childproc.Configure(cmd)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("snapshot of %s: %w", camID, ctx.Err())
		}
		return nil, fmt.Errorf("snapshot of %s: %w: %s", camID, err, lastLine(stderr.String()))
	}
	b := out.Bytes()
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, fmt.Errorf("snapshot of %s: ffmpeg produced no JPEG", camID)
	}
	return append([]byte(nil), b...), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 200 {
		l = l[:200] + "…"
	}
	return l
}

// ---- encoder selection ---------------------------------------------------

// encoder is how a live session's video is encoded.
type encoder struct {
	Name   string            // ffmpeg encoder
	Method string            // detect.Decode* ("software" for libx264)
	Decode detect.DecodePlan // decoder args shared with the encoder's device
}

var softwareEncoder = encoder{Name: "libx264", Method: detect.DecodeSoftware}

// openH264Encoder is the software encoder of the shipped LGPL ffmpeg builds,
// which have no libx264 (the Linux bundle has OpenH264).
var openH264Encoder = encoder{Name: "libopenh264", Method: detect.DecodeSoftware}

var (
	swEncMu    sync.Mutex
	swEncCache = map[string]encoder{}
	// listEncoders is `ffmpeg -encoders` (fakeable in tests).
	listEncoders = func(ctx context.Context, ffmpeg string) []byte {
		out, _ := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-encoders").Output()
		return out
	}
)

// softwareEncoderFor is the software H.264 encoder this ffmpeg build has:
// libx264 when present, else OpenH264, else libx264 (ffmpeg then reports it).
func softwareEncoderFor(ctx context.Context, ffmpeg string) encoder {
	swEncMu.Lock()
	defer swEncMu.Unlock()
	if e, ok := swEncCache[ffmpeg]; ok {
		return e
	}
	out := listEncoders(ctx, ffmpeg)
	e := softwareEncoder
	if !bytes.Contains(out, []byte(" libx264 ")) && bytes.Contains(out, []byte(" libopenh264 ")) {
		e = openH264Encoder
	}
	swEncCache[ffmpeg] = e
	return e
}

// chooseEncoder maps the hwaccel setting and the probe result to an encoder
// (pure). Same keying as detect: "off" is software, anything else takes the
// first usable method, best first.
func chooseEncoder(accel string, caps detect.DecodeCaps, codec string) encoder {
	if accel == detect.AccelOff {
		return softwareEncoder
	}
	for _, m := range caps.Usable {
		plan := detect.DecodePlan{Method: m, Device: caps.Device, Codec: codec}
		switch m {
		case detect.DecodeVideoToolbox:
			return encoder{Name: "h264_videotoolbox", Method: m, Decode: plan}
		case detect.DecodeVAAPI:
			return encoder{Name: "h264_vaapi", Method: m, Decode: plan}
		case detect.DecodeQSV:
			return encoder{Name: "h264_qsv", Method: m, Decode: plan}
		}
	}
	return softwareEncoder
}

var (
	capsMu    sync.Mutex
	capsCache = map[string]detect.DecodeCaps{}
	// probeCaps is the hardware probe (fakeable in tests).
	probeCaps = func(ctx context.Context, ffmpeg string) detect.DecodeCaps {
		return detect.ProbeDecode(ctx, ffmpeg, hwinfo.ProbeHost(), os.Getenv("SENTINEL_HWACCEL_DEVICE"), nil)
	}
)

func decodeCaps(ctx context.Context, ffmpeg string) detect.DecodeCaps {
	capsMu.Lock()
	defer capsMu.Unlock()
	if c, ok := capsCache[ffmpeg]; ok {
		return c
	}
	c := probeCaps(ctx, ffmpeg)
	capsCache[ffmpeg] = c
	return c
}

// ---- stream-copy decision and argv --------------------------------------

// source is what MediaMTX reports about the live path.
type source struct {
	Codec         string // "H264", "H265" …; "" unknown
	Width, Height int
}

// canCopy: the camera already sends H.264 no bigger than the requested size,
// so re-encoding would only cost CPU.
func canCopy(src source, p StreamParams) bool {
	return strings.EqualFold(src.Codec, "H264") && src.Width > 0 && src.Height > 0 &&
		src.Width <= p.Width && src.Height <= p.Height
}

func (p StreamParams) validate() error {
	switch {
	case p.Address == "" || p.VideoPort <= 0 || p.VideoPort > 65535:
		return errors.New("bad controller address or port")
	case len(p.SRTPKey) != 16 || len(p.SRTPSalt) != 14:
		return errors.New("SRTP key must be 16 bytes and salt 14 (AES_CM_128_HMAC_SHA1_80)")
	case p.Width <= 0 || p.Height <= 0:
		return errors.New("bad requested resolution")
	}
	return nil
}

// srtpOutput is the srtp:// URL and master key||salt (base64) for ffmpeg's
// rtp muxer.
func srtpOutput(p StreamParams) (url, params string) {
	port := strconv.Itoa(p.VideoPort)
	url = "srtp://" + net.JoinHostPort(p.Address, port) + "?rtcpport=" + port + "&pkt_size=" + strconv.Itoa(srtpPktSize)
	return url, base64.StdEncoding.EncodeToString(append(append([]byte(nil), p.SRTPKey...), p.SRTPSalt...))
}

// streamArgs is the full ffmpeg command line for one live session (pure).
// copyVideo selects stream-copy; otherwise enc encodes to p's profile.
func streamArgs(url string, p StreamParams, copyVideo bool, enc encoder) []string {
	fps := p.FPS
	if fps <= 0 || fps > 30 {
		fps = 30
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "warning"}
	if !copyVideo {
		args = append(args, detect.HWInputArgs(enc.Decode)...)
	}
	args = append(args, "-rtsp_transport", "tcp", "-i", url, "-an", "-sn", "-dn", "-map", "0:v:0")
	if copyVideo {
		// The camera's own GOP decides keyframes; dump_extra (below) repeats
		// SPS/PPS on each so a controller joining mid-stream can decode.
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args, encodeArgs(p, fps, enc)...)
	}
	args = append(args, "-bsf:v", "dump_extra=freq=keyframe")
	out, params := srtpOutput(p)
	return append(args, "-payload_type", strconv.Itoa(int(p.PayloadType)), "-ssrc", strconv.FormatUint(uint64(p.SSRC), 10),
		"-f", "rtp", "-srtp_out_suite", "AES_CM_128_HMAC_SHA1_80", "-srtp_out_params", params, out)
}

func encodeArgs(p StreamParams, fps int, enc encoder) []string {
	scale := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2", p.Width, p.Height)
	kbps := p.BitrateKbps
	if kbps <= 0 {
		kbps = 1000
	}
	var a []string
	switch enc.Method {
	case detect.DecodeVideoToolbox:
		a = []string{"-vf", scale + ",format=nv12", "-c:v", enc.Name, "-realtime", "1", "-profile:v", "main"}
	case detect.DecodeVAAPI:
		// Frames come back from the GPU decoder in system memory (detect's
		// design), so upload them for the VAAPI encoder.
		a = []string{"-vf", scale + ",format=nv12,hwupload", "-filter_hw_device", "va", "-c:v", enc.Name, "-profile:v", "main"}
	case detect.DecodeQSV:
		a = []string{"-vf", scale + ",format=nv12", "-c:v", enc.Name, "-profile:v", "main", "-preset", "veryfast"}
	default:
		if enc.Name == openH264Encoder.Name {
			// OpenH264 has no presets or tunes; it is a real-time encoder already.
			a = []string{"-vf", scale + ",format=yuv420p", "-c:v", enc.Name, "-profile:v", "main", "-allow_skip_frames", "1"}
			break
		}
		a = []string{"-vf", scale + ",format=yuv420p", "-c:v", enc.Name, "-preset", "veryfast", "-tune", "zerolatency", "-profile:v", "main"}
	}
	return append(a,
		"-r", strconv.Itoa(fps),
		"-g", strconv.Itoa(fps*maxKeyframeSeconds),
		"-force_key_frames", "expr:gte(t,n_forced*"+strconv.Itoa(maxKeyframeSeconds)+")",
		"-b:v", fmt.Sprintf("%dk", kbps), "-maxrate", fmt.Sprintf("%dk", kbps), "-bufsize", fmt.Sprintf("%dk", kbps*2))
}

// ---- sessions ------------------------------------------------------------

// liveStream is one running ffmpeg session.
type liveStream struct {
	cam    string
	cancel context.CancelFunc
	done   chan struct{} // closed when the supervising goroutine has finished
	once   sync.Once
}

// sessionTable tracks live sessions per camera, oldest first.
type sessionTable struct {
	mu    sync.Mutex
	byCam map[string][]*liveStream
}

var sessions = &sessionTable{byCam: map[string][]*liveStream{}}

// add registers ls and returns the sessions pushed out by the per-camera cap.
func (t *sessionTable) add(ls *liveStream) (evicted []*liveStream) {
	t.mu.Lock()
	defer t.mu.Unlock()
	list := append(t.byCam[ls.cam], ls)
	for len(list) > maxSessionsPerCam {
		evicted = append(evicted, list[0])
		list = list[1:]
	}
	t.byCam[ls.cam] = list
	return evicted
}

func (t *sessionTable) remove(ls *liveStream) {
	t.mu.Lock()
	defer t.mu.Unlock()
	list := t.byCam[ls.cam]
	for i, s := range list {
		if s == ls {
			list = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(t.byCam, ls.cam)
	} else {
		t.byCam[ls.cam] = list
	}
}

func (t *sessionTable) count(cam string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byCam[cam])
}

// release kills ffmpeg (if running) and unregisters; idempotent.
func (ls *liveStream) release() {
	ls.once.Do(func() {
		ls.cancel()
		sessions.remove(ls)
	})
}

// stop is Session.Stop: release, then wait for ffmpeg to be reaped.
func (ls *liveStream) stop() {
	ls.release()
	<-ls.done
}

// ffmpegStartStream spawns ffmpeg for one HAP stream-start. The session ends
// on Session.Stop, ctx end, eviction by a newer session, or ffmpeg exiting.
func ffmpegStartStream(ctx context.Context, d *Deps, camID string, p StreamParams) (*Session, error) {
	if d == nil || d.FFmpeg == "" || d.Sup == nil {
		return nil, ErrStreamingUnavailable
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("stream for %s: %w", camID, err)
	}
	st := d.Sup.State(d.Sup.LivePath(camID))
	src := source{Codec: st.Codec, Width: st.Width, Height: st.Height}
	url := rtspURL(d, camID)

	var attempts [][]string
	sw := softwareEncoderFor(ctx, d.FFmpeg)
	if canCopy(src, p) {
		attempts = append(attempts, streamArgs(url, p, true, sw))
	} else {
		enc := chooseEncoder(d.Accel, decodeCaps(ctx, d.FFmpeg), src.Codec)
		if enc.Method == detect.DecodeSoftware {
			enc = sw
		}
		attempts = append(attempts, streamArgs(url, p, false, enc))
		if enc.Method != detect.DecodeSoftware {
			attempts = append(attempts, streamArgs(url, p, false, sw))
		}
	}

	sctx, cancel := context.WithCancel(ctx)
	ls := &liveStream{cam: camID, cancel: cancel, done: make(chan struct{})}
	for _, old := range sessions.add(ls) {
		slog.Info("homekit: stopping oldest stream (per-camera limit)", "camera", camID)
		old.release()
	}
	go func() {
		defer close(ls.done)
		defer ls.release() // ffmpeg exited on its own, or ctx ended
		superviseFFmpeg(sctx, d.FFmpeg, camID, attempts)
	}()
	return &Session{Stop: ls.stop}, nil
}

// superviseFFmpeg runs attempts in order: the next one starts only when the
// previous died on its own within hwEarlyExit (a hardware encode refusing the
// stream). It returns when ffmpeg is gone for good.
func superviseFFmpeg(ctx context.Context, bin, camID string, attempts [][]string) {
	for i, args := range attempts {
		began := time.Now()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.WaitDelay = 3 * time.Second
		childproc.Configure(cmd)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		slog.Warn("homekit: ffmpeg stream ended", "camera", camID, "err", err, "after", time.Since(began).Round(time.Millisecond), "stderr", lastLine(stderr.String()))
		if i+1 < len(attempts) && time.Since(began) < hwEarlyExit {
			slog.Warn("homekit: hardware encode failed; retrying in software", "camera", camID)
			continue
		}
		return
	}
}

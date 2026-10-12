package detect

// Hardware-accelerated decode for the detection readers.
//
// Design: the GPU only DECODES. We pass `-hwaccel vaapi -hwaccel_device
// /dev/dri/renderDxxx` WITHOUT -hwaccel_output_format, so ffmpeg copies each
// decoded frame back to system memory (NV12) and the existing software filter
// graph (fps, scale, split, pad, gray/bgr24) runs unchanged. The alternative —
// keeping frames on the GPU (-hwaccel_output_format vaapi + scale_vaapi +
// hwdownload) — saves a copy per frame, but needs a second filter graph, hits
// driver limits on tiny 64×36 surfaces, and breaks outright when libavcodec
// falls back to software for a profile the GPU can't do (frames are then not
// VAAPI surfaces and scale_vaapi fails). Detection reads the low-res live
// path (sub-stream when the camera has one), so the copy is cheap, while the
// expensive part — H.264/HEVC decode — moves to the GPU.
//
// Robustness: at startup we list ffmpeg's hwaccels, find a render node and
// open the device once (vaInitialize loads the VA driver — the usual failure).
// Per camera, a hardware run that dies with a hardware-looking error, never
// delivers a frame, or reports that the GPU rejected the stream switches that
// camera to software decode for an hour (then hardware is tried again).

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sentinel-linux/internal/hwinfo"
)

// Decode methods.
const (
	DecodeSoftware = "software"
	DecodeVAAPI    = "vaapi" // Intel (iHD/i965) and AMD (radeonsi) via libva
	DecodeQSV      = "qsv"   // Intel Quick Sync via oneVPL (codec-specific decoders)
)

// Acceleration modes (setting "hwaccel").
const (
	AccelAuto = "auto"
	AccelOff  = "off"
)

// NoteAccelOff is the reason given when the setting turned acceleration off.
const NoteAccelOff = "hardware acceleration is off"

const (
	hwRetryAfter     = time.Hour // software period after a hardware failure
	hwMaxNoFrameRuns = 3         // unexplained frameless hardware runs before falling back
)

// hwFirstFrameWait: a hardware run with no frame by then is killed (var for tests).
var hwFirstFrameWait = 20 * time.Second

// Runner runs a command and returns its combined output (fakeable in tests).
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// DecodeCaps is what the startup probe found.
type DecodeCaps struct {
	FFmpeg       string      `json:"ffmpeg"`
	FFmpegSource string      `json:"ffmpegSource"` // bundled | SENTINEL_FFMPEG | system
	Hwaccels     []string    `json:"hwaccels"`     // compiled into this ffmpeg
	Device       string      `json:"device"`       // render node used for decode
	Driver       string      `json:"driver"`       // VA driver reported by libva
	Usable       []string    `json:"usable"`       // methods whose device opened, best first
	Notes        []string    `json:"notes"`        // why something isn't available
	Hardware     hwinfo.Info `json:"hardware"`
}

// FFmpegSource says where Find got path from.
func FFmpegSource(path string) string {
	if path == "" {
		return ""
	}
	if env := os.Getenv("SENTINEL_FFMPEG"); env != "" && env == path {
		return "SENTINEL_FFMPEG"
	}
	if exe, err := os.Executable(); err == nil && strings.HasPrefix(path, dirOf(exe)) {
		return "bundled"
	}
	return "system"
}

func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i+1]
	}
	return ""
}

// ParseHwaccels parses `ffmpeg -hide_banner -hwaccels`.
func ParseHwaccels(out string) []string {
	var list []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, ":") || strings.Contains(line, " ") {
			continue
		}
		list = append(list, line)
	}
	return list
}

var vaDriverRE = regexp.MustCompile(`VAAPI driver: (.+?)\.?\s*$`)

// VAAPITrialArgs opens the VAAPI device (loads the VA driver) and pushes one
// tiny frame through ffmpeg — no encoder or sample video needed.
func VAAPITrialArgs(device string) []string {
	args := []string{"-hide_banner", "-loglevel", "verbose"}
	args = append(args, vaapiDeviceArgs(device)...)
	return append(args, "-f", "lavfi", "-i", "nullsrc=s=64x64:d=0.2", "-frames:v", "1", "-f", "null", "-")
}

// QSVTrialArgs creates a oneVPL session on device the same way.
func QSVTrialArgs(device string) []string {
	args := []string{"-hide_banner", "-loglevel", "verbose"}
	args = append(args, qsvDeviceArgs(device)...)
	return append(args, "-f", "lavfi", "-i", "nullsrc=s=64x64:d=0.2", "-frames:v", "1", "-f", "null", "-")
}

// vaapiDeviceArgs opens the render node as a named device "va", DRM only:
// without connection_type=drm, libva falls back to X11 when the node can't
// be opened, and the static ffmpeg builds abort (SIGABRT) if libX11 isn't
// installed — common on headless servers.
func vaapiDeviceArgs(device string) []string {
	return []string{"-init_hw_device", "vaapi=va:" + device + ",connection_type=drm"}
}

// qsvDeviceArgs derives a Quick Sync device "qs" from the DRM VAAPI device
// (the method ffmpeg documents for Linux).
func qsvDeviceArgs(device string) []string {
	return append(vaapiDeviceArgs(device), "-init_hw_device", "qsv=qs@va")
}

// ProbeDecode finds which hardware decoders work with ffmpeg on this machine.
// device overrides the render node (SENTINEL_HWACCEL_DEVICE); "" = auto.
func ProbeDecode(ctx context.Context, ffmpeg string, hw hwinfo.Info, device string, run Runner) DecodeCaps {
	if run == nil {
		run = execRunner
	}
	caps := DecodeCaps{FFmpeg: ffmpeg, FFmpegSource: FFmpegSource(ffmpeg), Hwaccels: []string{}, Usable: []string{}, Notes: []string{}, Hardware: hw}
	note := func(s string) { caps.Notes = append(caps.Notes, s) }
	if ffmpeg == "" {
		note("ffmpeg not found")
		return caps
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	out, err := run(tctx, ffmpeg, "-hide_banner", "-hwaccels")
	cancel()
	if err != nil {
		note("couldn't list ffmpeg hardware decoders: " + firstLine(string(out), err))
		return caps
	}
	caps.Hwaccels = ParseHwaccels(string(out))
	if hw.IsMac() {
		probeVideoToolbox(ctx, ffmpeg, &caps, run) // hwaccel_videotoolbox.go
		return caps
	}
	has := func(m string) bool {
		for _, h := range caps.Hwaccels {
			if h == m {
				return true
			}
		}
		return false
	}

	node := hw.VideoNode()
	caps.Device = device
	if caps.Device == "" {
		caps.Device = node.Path
	}
	if hw.IsRaspberryPi5() {
		note("Raspberry Pi 5 has no H.264 hardware decoder, and its HEVC decoder needs Raspberry Pi OS's own ffmpeg (V4L2 request API), which Sentinel doesn't use yet; detection decodes on the CPU. Use the camera's low-resolution sub-stream to keep CPU use down.")
		return caps
	}
	if caps.Device == "" {
		note("no GPU render node (/dev/dri/renderD*) visible to Sentinel; detection decodes on the CPU")
		return caps
	}
	if device == "" && node.Vendor != "Intel" && node.Vendor != "AMD" {
		note("GPU " + caps.Device + " (" + orUnknown(node.Driver) + ") has no VAAPI driver Sentinel knows; set SENTINEL_HWACCEL_DEVICE to try another node")
		return caps
	}

	if !has(DecodeVAAPI) {
		note("this ffmpeg was built without VAAPI; set SENTINEL_FFMPEG to an ffmpeg that has it")
	} else {
		tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		out, err := run(tctx, ffmpeg, VAAPITrialArgs(caps.Device)...)
		cancel()
		if err != nil {
			note("VAAPI on " + caps.Device + " didn't open: " + explainFailure(string(out), err) + " (install the VA driver: intel-media-va-driver or mesa-va-drivers, and check the render/video group)")
		} else {
			caps.Usable = append(caps.Usable, DecodeVAAPI)
			for _, l := range strings.Split(string(out), "\n") {
				if m := vaDriverRE.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
					caps.Driver = m[1]
				}
			}
		}
	}
	if node.Vendor == "Intel" || device != "" {
		if !has(DecodeQSV) {
			if node.Vendor == "Intel" && len(caps.Usable) == 0 {
				note("this ffmpeg was built without Quick Sync (QSV)")
			}
		} else {
			tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			out, err := run(tctx, ffmpeg, QSVTrialArgs(caps.Device)...)
			cancel()
			if err != nil {
				if len(caps.Usable) == 0 {
					note("Quick Sync on " + caps.Device + " didn't open: " + explainFailure(string(out), err) + " (needs the oneVPL GPU runtime, libmfx-gen1.2)")
				}
			} else {
				caps.Usable = append(caps.Usable, DecodeQSV)
			}
		}
	}
	return caps
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown driver"
	}
	return s
}

func firstLine(out string, err error) string {
	out = strings.TrimSpace(out)
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[:i]
	}
	if out == "" && err != nil {
		return err.Error()
	}
	return out
}

var ctxLineRE = regexp.MustCompile(`^\[[^\]]*@ 0x[0-9a-fA-F]+\]\s*(.+)$`)

// explainFailure picks the most telling line of ffmpeg's error output: the
// last message logged by a component ("[VAAPI @ 0x…] No VA display found…"),
// else the last line that isn't ffmpeg's generic epilogue.
func explainFailure(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := ctxLineRE.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil {
			return truncate(m[1], 200)
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || strings.HasPrefix(l, "Exiting with") || strings.HasPrefix(l, "Error parsing global options") ||
			strings.HasPrefix(l, "Failed to set value") || strings.HasPrefix(l, "Conversion failed") {
			continue
		}
		return truncate(l, 200)
	}
	return lastLine(out, err)
}

func lastLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return truncate(l, 200)
		}
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// DecodePlan is how one detection reader decodes.
type DecodePlan struct {
	Method string // DecodeSoftware | DecodeVAAPI | DecodeQSV | DecodeVideoToolbox
	Device string // render node
	Codec  string // stream codec as MediaMTX reports it (H264, H265); QSV needs it
}

func (p DecodePlan) hw() bool {
	return p.Method == DecodeVAAPI || p.Method == DecodeQSV || p.Method == DecodeVideoToolbox
}

// qsvDecoder maps a MediaMTX codec name to ffmpeg's Quick Sync decoder.
func qsvDecoder(codec string) string {
	switch strings.ToUpper(codec) {
	case "H264", "AVC":
		return "h264_qsv"
	case "H265", "HEVC":
		return "hevc_qsv"
	}
	return ""
}

// HWInputArgs are the ffmpeg input options (before -i) for plan.
func HWInputArgs(p DecodePlan) []string {
	switch p.Method {
	case DecodeVAAPI:
		// No -hwaccel_output_format: frames come back to system memory, and
		// libavcodec decodes in software if the GPU can't handle the profile.
		return append(vaapiDeviceArgs(p.Device), "-hwaccel", "vaapi", "-hwaccel_device", "va")
	case DecodeQSV:
		// No -hwaccel: the *_qsv decoder picks up the "qs" device itself
		// ("Using auto hwaccel type qsv with existing device qs") and, with
		// no hardware output format requested, returns NV12 in system
		// memory, so the software filter graph works unchanged.
		if dec := qsvDecoder(p.Codec); dec != "" {
			return append(qsvDeviceArgs(p.Device), "-c:v", dec)
		}
	case DecodeVideoToolbox:
		// No -hwaccel_output_format, as with VAAPI: frames come back to
		// system memory (NV12) for the software filter graph.
		return []string{"-hwaccel", "videotoolbox"}
	}
	return nil
}

// detectArgs is the full ffmpeg command line for one detection reader:
// motion frames (64×36 gray) on stdout and, with objects, YOLOX frames
// (416×416 letterboxed BGR) on fd 3.
func detectArgs(url string, plan DecodePlan, objects bool) []string {
	logLevel := "error"
	if plan.hw() {
		logLevel = "warning" // "Failed setup for format vaapi" is a warning
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", logLevel, "-rtsp_transport", "tcp"}
	args = append(args, HWInputArgs(plan)...)
	args = append(args, "-i", url, "-an", "-sn", "-dn")
	motionFilter := "scale=" + itoa(frameW) + ":" + itoa(frameH) + ":flags=area,format=gray"
	if objects {
		// One decode, two outputs: tiny gray frames for motion on stdout, and
		// YOLOX-ready letterboxed BGR frames (top-left, gray 114 padding) on fd 3.
		return append(args, "-filter_complex",
			"[0:v]fps="+sampleFPS+",split=2[m][d];[m]"+motionFilter+"[mo];"+
				"[d]scale="+itoa(InputSize)+":"+itoa(InputSize)+":force_original_aspect_ratio=decrease:flags=bilinear,"+
				"pad="+itoa(InputSize)+":"+itoa(InputSize)+":0:0:color=0x727272,format=bgr24[do]",
			"-map", "[mo]", "-f", "rawvideo", "pipe:1",
			"-map", "[do]", "-f", "rawvideo", "pipe:3")
	}
	return append(args, "-vf", "fps="+sampleFPS+","+motionFilter, "-f", "rawvideo", "pipe:1")
}

// camDecode is one camera's decode history.
type camDecode struct {
	Plan          DecodePlan
	Reason        string    // why it's (now) software
	NoFrameRuns   int       // consecutive frameless hardware runs
	SoftwareUntil time.Time // hardware is retried after this
	Rejected      bool      // this run: ffmpeg said the GPU can't decode the stream
	Running       bool
	current       *decodeRun // the live run; older runs ending late are ignored
}

// chooseDecode picks the method for a camera (pure; see tests).
func chooseDecode(mode string, caps DecodeCaps, codec string, st camDecode, now time.Time) (DecodePlan, string) {
	sw := DecodePlan{Method: DecodeSoftware, Codec: codec}
	if mode == AccelOff {
		return sw, NoteAccelOff
	}
	if now.Before(st.SoftwareUntil) {
		return sw, st.Reason
	}
	reason := "no hardware decoder available"
	if len(caps.Notes) > 0 {
		reason = caps.Notes[0]
	}
	for _, m := range caps.Usable {
		switch m {
		case DecodeVAAPI, DecodeVideoToolbox:
			return DecodePlan{Method: m, Device: caps.Device, Codec: codec}, ""
		case DecodeQSV:
			if qsvDecoder(codec) != "" {
				return DecodePlan{Method: DecodeQSV, Device: caps.Device, Codec: codec}, ""
			}
			reason = "Quick Sync needs the stream codec, which isn't known yet (" + orUnknown(codec) + ")"
		}
	}
	return sw, reason
}

// Lines ffmpeg prints when the GPU, its driver or libva/oneVPL is the problem
// (as opposed to the camera or network).
var hwErrorHints = []string{
	"vaapi", "libva", "va_", "vainitialize", "no va display", "dri/render",
	"qsv", "mfx", "onevpl", "libvpl",
	"videotoolbox", "vtdecompression",
	"hwaccel", "hw device", "hardware device", "device creation failed", "device setup failed",
	"failed setup for format", "hwdownload", "hwframe", "hw_frames",
}

func isHWError(text string) bool {
	t := strings.ToLower(text)
	for _, h := range hwErrorHints {
		if strings.Contains(t, h) {
			return true
		}
	}
	return false
}

// isHWRejected matches libavcodec giving up on the GPU for this stream and
// decoding in software instead (unsupported profile/level/resolution).
func isHWRejected(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "failed setup for format") || strings.Contains(l, "hwaccel initialisation returned error") ||
		strings.Contains(l, "no support for codec") && strings.Contains(l, "hwaccel")
}

// afterRun updates a camera's state when a reader exits (pure; see tests).
func afterRun(st camDecode, plan DecodePlan, gotFrames, timedOut bool, stderrTail string, now time.Time) camDecode {
	st.Running = false
	if !plan.hw() {
		return st
	}
	fallback := func(reason string) camDecode {
		st.SoftwareUntil = now.Add(hwRetryAfter)
		st.Reason = reason
		st.NoFrameRuns = 0
		return st
	}
	switch {
	case st.Rejected:
		st.Rejected = false
		return fallback("the GPU can't decode this stream (" + plan.Method + " rejected it); decoding on the CPU")
	case gotFrames:
		st.NoFrameRuns = 0
		return st
	case timedOut:
		return fallback(plan.Method + " delivered no frames within " + hwFirstFrameWait.String() + "; decoding on the CPU")
	case isHWError(stderrTail):
		return fallback(plan.Method + " failed: " + explainFailure(stderrTail, nil))
	}
	st.NoFrameRuns++
	if st.NoFrameRuns >= hwMaxNoFrameRuns {
		return fallback(plan.Method + " delivered no frames " + itoa(hwMaxNoFrameRuns) + " times in a row; decoding on the CPU")
	}
	return st
}

// accelState lives in Manager.
type accelState struct {
	mu      sync.Mutex
	mode    string
	caps    DecodeCaps
	codec   func(cameraID string) string
	cams    map[string]*camDecode
	sources []Source // last Sync, to restart readers on a mode change
	ctx     context.Context
	now     func() time.Time
}

func (a *accelState) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *accelState) cam(id string) *camDecode {
	if a.cams == nil {
		a.cams = map[string]*camDecode{}
	}
	c := a.cams[id]
	if c == nil {
		c = &camDecode{}
		a.cams[id] = c
	}
	return c
}

// ConfigureAccel sets the acceleration mode and probe result; call before
// the first Sync. codec reports a camera's detection-stream codec ("" =
// unknown) for Quick Sync.
func (m *Manager) ConfigureAccel(mode string, caps DecodeCaps, codec func(cameraID string) string) {
	m.accel.mu.Lock()
	defer m.accel.mu.Unlock()
	m.accel.mode, m.accel.caps, m.accel.codec = normMode(mode), caps, codec
}

func normMode(mode string) string {
	if mode == AccelOff {
		return AccelOff
	}
	return AccelAuto
}

// AccelMode is the current mode (auto when never configured).
func (m *Manager) AccelMode() string {
	m.accel.mu.Lock()
	defer m.accel.mu.Unlock()
	return normMode(m.accel.mode)
}

// SetAccelMode switches between auto and off, clears per-camera fallbacks
// and restarts the readers so the change takes effect now.
func (m *Manager) SetAccelMode(mode string) {
	m.accel.mu.Lock()
	m.accel.mode = normMode(mode)
	m.accel.cams = nil
	srcs, ctx := m.accel.sources, m.accel.ctx
	m.accel.mu.Unlock()
	if ctx == nil || !m.Available() {
		return
	}
	m.mu.Lock()
	for id, w := range m.workers {
		w.cancel()
		delete(m.workers, id)
	}
	m.mu.Unlock()
	m.Sync(ctx, srcs)
}

// rememberSources is called by Sync so SetAccelMode can restart readers.
func (m *Manager) rememberSources(ctx context.Context, sources []Source) {
	m.accel.mu.Lock()
	m.accel.ctx, m.accel.sources = ctx, append([]Source(nil), sources...)
	m.accel.mu.Unlock()
}

func (m *Manager) decodePlan(cameraID string) DecodePlan {
	codec := ""
	m.accel.mu.Lock()
	fn := m.accel.codec
	m.accel.mu.Unlock()
	if fn != nil {
		codec = fn(cameraID)
	}
	m.accel.mu.Lock()
	defer m.accel.mu.Unlock()
	c := m.accel.cam(cameraID)
	plan, reason := chooseDecode(normMode(m.accel.mode), m.accel.caps, codec, *c, m.accel.clock())
	c.Plan, c.Running, c.Rejected = plan, true, false
	if plan.Method == DecodeSoftware {
		c.Reason = reason
	} else {
		c.Reason = ""
	}
	return plan
}

// decodeRun watches one reader process: its stderr (for hardware errors),
// its first frame, and — for hardware runs — a first-frame watchdog.
type decodeRun struct {
	m        *Manager
	parent   context.Context // the reader's own context; cancelled = stopped on purpose
	camera   string
	plan     DecodePlan
	frames   atomic.Bool
	timedOut atomic.Bool
	wd       *time.Timer
	mu       sync.Mutex
	partial  []byte
	tail     []string // last stderr lines
}

// beginDecode returns the context the reader runs under (cancelled by the
// watchdog) and the run tracker, which is also the process's stderr.
func (m *Manager) beginDecode(ctx context.Context, cameraID string, plan DecodePlan) (context.Context, *decodeRun) {
	r := &decodeRun{m: m, parent: ctx, camera: cameraID, plan: plan}
	m.accel.mu.Lock()
	m.accel.cam(cameraID).current = r
	m.accel.mu.Unlock()
	if !plan.hw() {
		return ctx, r
	}
	ctx, cancel := context.WithCancel(ctx)
	r.wd = time.AfterFunc(hwFirstFrameWait, func() {
		if !r.frames.Load() {
			r.timedOut.Store(true)
			cancel()
		}
	})
	context.AfterFunc(ctx, func() { r.wd.Stop() })
	return ctx, r
}

// frame marks that the reader delivered a frame.
func (r *decodeRun) frame() {
	if !r.frames.Load() {
		r.frames.Store(true)
		if r.wd != nil {
			r.wd.Stop()
		}
	}
}

// Write collects stderr lines (hardware runs only; software output is dropped
// as before).
func (r *decodeRun) Write(p []byte) (int, error) {
	if !r.plan.hw() {
		return len(p), nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			if len(r.partial) > 4096 {
				r.partial = r.partial[:0]
			}
			break
		}
		line := strings.TrimSpace(string(r.partial[:i]))
		r.partial = r.partial[i+1:]
		if line == "" {
			continue
		}
		r.tail = append(r.tail, truncate(line, 300))
		if len(r.tail) > 8 {
			r.tail = r.tail[1:]
		}
		if isHWRejected(line) {
			r.m.accel.mu.Lock()
			c := r.m.accel.cam(r.camera)
			c.Rejected = true
			c.Reason = "the GPU can't decode this stream; ffmpeg is decoding it on the CPU"
			r.m.accel.mu.Unlock()
		}
	}
	return len(p), nil
}

// end records the outcome; the next run's plan reflects any fallback.
func (r *decodeRun) end() {
	if r.wd != nil {
		r.wd.Stop()
	}
	r.mu.Lock()
	tail := strings.Join(r.tail, "\n")
	r.mu.Unlock()
	a := &r.m.accel
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.cams[r.camera]
	if c == nil || c.current != r {
		return // forgotten (camera removed, mode changed) or superseded
	}
	if r.parent.Err() != nil {
		c.Running = false // stopped on purpose (shutdown, camera edited): not a failure
		return
	}
	before := c.SoftwareUntil
	*c = afterRun(*c, r.plan, r.frames.Load(), r.timedOut.Load(), tail, a.clock())
	if c.SoftwareUntil != before {
		slog.Warn("hardware decode failed; using software decode for this camera", "camera", r.camera, "method", r.plan.Method, "reason", c.Reason, "retryIn", hwRetryAfter)
	}
}

// CameraDecode is one camera's decode status for the System page.
type CameraDecode struct {
	CameraID string `json:"cameraId"`
	Method   string `json:"method"` // vaapi | qsv | videotoolbox | software
	Reason   string `json:"reason,omitempty"`
	Running  bool   `json:"running"`
}

// DecodeStatus reports the probe result and every watched camera's decode.
func (m *Manager) DecodeStatus() (DecodeCaps, []CameraDecode) {
	m.accel.mu.Lock()
	defer m.accel.mu.Unlock()
	out := []CameraDecode{}
	for id, c := range m.accel.cams {
		method := c.Plan.Method
		if method == "" {
			method = DecodeSoftware
		}
		if c.Rejected || (!c.Running && m.accel.clock().Before(c.SoftwareUntil)) {
			method = DecodeSoftware // rejected mid-run, or between runs after a fallback
		}
		out = append(out, CameraDecode{CameraID: id, Method: method, Reason: c.Reason, Running: c.Running})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CameraID < out[j].CameraID })
	return m.accel.caps, out
}

// forgetCamera drops status for a camera that's no longer watched.
func (m *Manager) forgetCamera(id string) {
	m.accel.mu.Lock()
	delete(m.accel.cams, id)
	m.accel.mu.Unlock()
}

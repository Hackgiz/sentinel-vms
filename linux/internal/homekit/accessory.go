package homekit

// The camera accessory and its stream negotiation follow the structure of
// github.com/brutella/hkcam (Copyright Matthias Hochgatterer, Apache License
// 2.0), the reference HAP IP camera built on the same library: two Camera
// RTP Stream Management services, the SetupEndpoints exchange, and the
// SelectedRTPStreamConfiguration commands. Where hkcam drives ffmpeg itself,
// the negotiated parameters are handed to startStream instead.

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/brutella/hap/accessory"
	"github.com/brutella/hap/characteristic"
	"github.com/brutella/hap/rtp"
	"github.com/brutella/hap/service"
	"github.com/brutella/hap/tlv8"
)

// camSpec is one camera to expose.
type camSpec struct {
	ID   string
	Name string
	AID  uint64
}

// camAccessory is a camera as HomeKit sees it: an IP camera accessory with
// two stream services and a motion sensor.
type camAccessory struct {
	spec   camSpec
	a      *accessory.A
	motion *service.MotionSensor
	active *characteristic.StatusActive
	mu     sync.Mutex // serializes sensor writes: the library's notify path is not safe against concurrent SetValue
	slots  [2]*streamSlot
}

// accEnv is what an accessory needs from the running bridge.
type accEnv struct {
	ctx     context.Context // ends when the HAP server stops: every stream dies with it
	deps    *Deps
	version string
}

func newCamAccessory(spec camSpec, env *accEnv) *camAccessory {
	name := spec.Name
	if name == "" {
		name = "Camera " + spec.ID
	}
	a := accessory.New(accessory.Info{
		Name:         name,
		SerialNumber: spec.ID,
		Manufacturer: "Handoffgrid",
		Model:        "Sentinel Camera",
		Firmware:     env.version,
	}, accessory.TypeIPCamera)
	a.Id = spec.AID

	c := &camAccessory{spec: spec, a: a}

	// Same service set as hkcam, which iOS accepts: a control service and the
	// stream services. HAP wants two streams so two viewers can watch.
	a.AddS(service.NewCameraControl().S)
	for i := range c.slots {
		svc := service.NewCameraRTPStreamManagement()
		a.AddS(svc.S)
		c.slots[i] = newStreamSlot(env, spec.ID, svc)
	}

	c.motion = service.NewMotionSensor()
	n := characteristic.NewName()
	n.SetValue(name + " Motion")
	c.motion.AddC(n.C)
	c.active = characteristic.NewStatusActive()
	c.active.SetValue(true)
	c.motion.AddC(c.active.C)
	a.AddS(c.motion.S)
	return c
}

// SetMotion drives the motion sensor.
func (c *camAccessory) SetMotion(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.motion.MotionDetected.SetValue(on)
}

// SetReachable drives the sensor's StatusActive: an offline camera still
// exists as an accessory, it just reports itself inactive.
func (c *camAccessory) SetReachable(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active.SetValue(on)
}

// Close ends any live streams.
func (c *camAccessory) Close() {
	for _, s := range c.slots {
		s.stop()
	}
}

// streamSlot is one Camera RTP Stream Management service. It carries at most
// one live session, so a camera serves two viewers at once; starting a third
// on a busy slot replaces the older one.
type streamSlot struct {
	env   *accEnv
	camID string
	svc   *service.CameraRTPStreamManagement

	mu      sync.Mutex
	pending *pendingStream
	id      string // session id of sess, hex
	sess    *Session
}

// pendingStream is what SetupEndpoints negotiated, waiting for the start
// command.
type pendingStream struct {
	id        []byte
	addr      string
	videoPort int
	key, salt []byte
	ssrc      uint32
}

func newStreamSlot(env *accEnv, camID string, svc *service.CameraRTPStreamManagement) *streamSlot {
	s := &streamSlot{env: env, camID: camID, svc: svc}
	setTLV8(svc.StreamingStatus.Bytes, rtp.StreamingStatus{Status: rtp.StreamingStatusAvailable})
	setTLV8(svc.SupportedRTPConfiguration.Bytes, rtp.NewConfiguration(rtp.CryptoSuite_AES_CM_128_HMAC_SHA1_80))
	setTLV8(svc.SupportedVideoStreamConfiguration.Bytes, supportedVideo())

	// iOS refuses to start a stream from a camera that advertises no audio
	// codec at all, so advertise the library defaults (Opus, AAC-ELD) even
	// though v1 sends no audio packets.
	setTLV8(svc.SupportedAudioStreamConfiguration.Bytes, rtp.DefaultAudioStreamConfiguration())

	svc.SelectedRTPStreamConfiguration.OnValueRemoteUpdate(s.onSelected)
	svc.SetupEndpoints.OnValueUpdate(func(new, old []byte, r *http.Request) {
		if r != nil { // a controller wrote it, not our own response
			s.onSetupEndpoints(new, r)
		}
	})
	return s
}

// supportedVideo is H.264 at 1080p, 720p and 360p, 30 fps at most.
func supportedVideo() rtp.VideoStreamConfiguration {
	cfg := rtp.NewH264VideoCodecConfiguration()
	cfg.Attributes = []rtp.VideoCodecAttributes{
		{Width: 1920, Height: 1080, Framerate: 30},
		{Width: 1280, Height: 720, Framerate: 30},
		{Width: 640, Height: 360, Framerate: 30},
	}
	return rtp.VideoStreamConfiguration{Codecs: []rtp.VideoCodecConfiguration{cfg}}
}

func (s *streamSlot) onSetupEndpoints(raw []byte, r *http.Request) {
	var req rtp.SetupEndpoints
	if err := tlv8.Unmarshal(raw, &req); err != nil {
		slog.Warn("homekit: bad SetupEndpoints", "camera", s.camID, "err", err)
		return
	}
	resp := rtp.SetupEndpointsResponse{
		SessionId: req.SessionId,
		Status:    rtp.SessionStatusError,
		Video:     req.Video,
		Audio:     req.Audio,
	}
	local, err := localIP(r, req.ControllerAddr.IPVersion)
	if err != nil {
		slog.Warn("homekit: no address for stream", "camera", s.camID, "err", err)
		setTLV8(s.svc.SetupEndpoints.Bytes, resp)
		return
	}
	ssrc := randomSSRC()
	resp.Status = rtp.SessionStatusSuccess
	resp.AccessoryAddr = rtp.Addr{
		IPVersion:    req.ControllerAddr.IPVersion,
		IPAddr:       local.String(),
		VideoRtpPort: req.ControllerAddr.VideoRtpPort,
		AudioRtpPort: req.ControllerAddr.AudioRtpPort,
	}
	resp.SsrcVideo = int32(ssrc)
	resp.SsrcAudio = int32(randomSSRC())

	s.mu.Lock()
	s.pending = &pendingStream{
		id:        req.SessionId,
		addr:      req.ControllerAddr.IPAddr,
		videoPort: int(req.ControllerAddr.VideoRtpPort),
		key:       req.Video.MasterKey,
		salt:      req.Video.MasterSalt,
		ssrc:      ssrc,
	}
	s.mu.Unlock()
	setTLV8(s.svc.SetupEndpoints.Bytes, resp) // the controller reads this back
}

func (s *streamSlot) onSelected(raw []byte) {
	var cfg rtp.StreamConfiguration
	if err := tlv8.Unmarshal(raw, &cfg); err != nil {
		slog.Warn("homekit: bad SelectedRTPStreamConfiguration", "camera", s.camID, "err", err)
		return
	}
	id := hex.EncodeToString(cfg.Command.Identifier)
	switch cfg.Command.Type {
	case rtp.SessionControlCommandTypeEnd:
		s.end(id)
	case rtp.SessionControlCommandTypeStart:
		s.start(cfg, id)
	case rtp.SessionControlCommandTypeReconfigure:
		s.reconfigure(cfg, id)
	case rtp.SessionControlCommandTypeSuspend, rtp.SessionControlCommandTypeResume:
		// Nothing to pause: ffmpeg keeps sending and the controller discards.
	default:
		slog.Debug("homekit: unknown stream command", "camera", s.camID, "type", cfg.Command.Type)
	}
}

func (s *streamSlot) start(cfg rtp.StreamConfiguration, id string) {
	s.mu.Lock()
	p := s.pending
	if p == nil || hex.EncodeToString(p.id) != id {
		s.mu.Unlock()
		slog.Warn("homekit: stream start without setup", "camera", s.camID)
		return
	}
	old := s.sess
	s.sess, s.id = nil, ""
	s.mu.Unlock()
	if old != nil {
		old.Stop() // the older viewer on this slot loses
	}

	params := StreamParams{
		Address:     p.addr,
		VideoPort:   p.videoPort,
		SRTPKey:     p.key,
		SRTPSalt:    p.salt,
		SSRC:        p.ssrc,
		PayloadType: cfg.Video.RTP.PayloadType,
		Width:       int(cfg.Video.Attributes.Width),
		Height:      int(cfg.Video.Attributes.Height),
		FPS:         int(cfg.Video.Attributes.Framerate),
		BitrateKbps: int(cfg.Video.RTP.Bitrate),
	}
	sess, err := startStream(s.env.ctx, s.env.deps, s.camID, params)
	if err != nil || sess == nil {
		slog.Warn("homekit: live stream failed to start", "camera", s.camID, "err", err)
		return
	}
	s.mu.Lock()
	s.sess, s.id = sess, id
	s.mu.Unlock()
	setTLV8(s.svc.StreamingStatus.Bytes, rtp.StreamingStatus{Status: rtp.StreamingStatusBusy})
}

// reconfigure is kill + respawn with the new parameters.
func (s *streamSlot) reconfigure(cfg rtp.StreamConfiguration, id string) {
	s.mu.Lock()
	p := s.pending
	live := s.sess != nil && s.id == id
	s.mu.Unlock()
	if p == nil || !live {
		return
	}
	s.start(cfg, id)
}

func (s *streamSlot) end(id string) {
	s.mu.Lock()
	sess := s.sess
	if sess != nil && s.id == id {
		s.sess, s.id = nil, ""
	} else {
		sess = nil
	}
	if s.pending != nil && hex.EncodeToString(s.pending.id) == id {
		s.pending = nil
	}
	s.mu.Unlock()
	if sess != nil {
		sess.Stop()
	}
	setTLV8(s.svc.StreamingStatus.Bytes, rtp.StreamingStatus{Status: rtp.StreamingStatusAvailable})
}

// stop ends the slot's session, if any (teardown).
func (s *streamSlot) stop() {
	s.mu.Lock()
	sess := s.sess
	s.sess, s.id, s.pending = nil, "", nil
	s.mu.Unlock()
	if sess != nil {
		sess.Stop()
	}
}

// localIP is the address of the interface the controller connected to, in
// the IP version it asked for.
func localIP(r *http.Request, version byte) (net.IP, error) {
	addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if addr == nil {
		return nil, fmt.Errorf("no local address on request")
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil, err
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // zone
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("unparseable local address %q", host)
	}
	if version == rtp.IPAddrVersionv4 && ip.To4() == nil {
		return nil, fmt.Errorf("controller wants IPv4, connection is %s", ip)
	}
	if version == rtp.IPAddrVersionv6 && ip.To4() != nil {
		return nil, fmt.Errorf("controller wants IPv6, connection is %s", ip)
	}
	return ip, nil
}

// randomSSRC is a nonzero 31-bit value (the TLV field is a signed int32).
func randomSSRC() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	v := binary.BigEndian.Uint32(b[:]) >> 1
	if v == 0 {
		v = 1
	}
	return v
}

func setTLV8(c *characteristic.Bytes, v any) {
	b, err := tlv8.Marshal(v)
	if err != nil {
		slog.Warn("homekit: tlv8 marshal failed", "err", err)
		return
	}
	c.SetValue(b)
}

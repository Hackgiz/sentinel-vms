package homekit

import (
	"errors"

	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/store"
)

// ErrStreamingUnavailable is what the capture and stream hooks return until
// stream.go installs the real ffmpeg implementations.
var ErrStreamingUnavailable = errors.New("homekit: streaming is not available")

// Deps is everything the bridge needs from the rest of the server.
type Deps struct {
	Store   *store.Store      // cameras, settings, homekit_* tables
	Sup     *media.Supervisor // LivePath(camID), Ports.RTSP
	Bus     *bus.Bus          // subscribe only
	FFmpeg  string            // absolute path; "" = bridge refuses to enable
	Accel   string            // hwaccel setting, same value detect uses
	Version string
}

// Status is what the API and the settings card show.
type Status struct {
	Enabled   bool   `json:"enabled"`
	Paired    bool   `json:"paired"`
	SetupCode string `json:"setupCode,omitempty"` // "XXX-XX-XXX", only until paired
	SetupURI  string `json:"setupURI,omitempty"`  // X-HM:// payload for the QR
	Cameras   int    `json:"cameras"`
	Err       string `json:"err,omitempty"`
}

// StreamParams is one live-view request as negotiated with the controller.
type StreamParams struct {
	Address           string // controller IP
	VideoPort         int
	SRTPKey, SRTPSalt []byte // AES_CM_128_HMAC_SHA1_80
	SSRC              uint32
	PayloadType       byte
	Width, Height     int
	FPS               int
	BitrateKbps       int
}

// Session is a running live stream. Stop is idempotent and is also fired by
// the streaming side when its ffmpeg exits on its own.
type Session struct{ Stop func() }

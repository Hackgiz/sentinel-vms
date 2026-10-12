// Package link is the boundary between Sentinel's two halves (PLATFORM.md,
// "Control link" and "In-process link"):
//
//   - the Management side (users, cameras config, rules, alarms, audit, the
//     client APIs) holds a Recorder per recording server, and
//   - the Recording side (MediaMTX, recordings, detection, playback) reports
//     back through a Sink.
//
// In --role all both live in one process joined by link/inproc. In phase 2
// the same calls cross the network as JSON over a WebSocket (Envelope), so
// every message type here must survive encoding/json unchanged; the
// round-trip tests enforce it.
package link

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
)

// ProtocolVersion is sent in hello (phase 2) and bumped on breaking changes.
const ProtocolVersion = 1

// Recorder is implemented by a recording server and used by the management
// server.
type Recorder interface {
	// ApplyConfig replaces the recorder's whole desired configuration.
	// Idempotent: applying the same snapshot twice changes nothing.
	ApplyConfig(ctx context.Context, s Snapshot) (Applied, error)
	// Command runs one request/response operation (see the Cmd* types).
	// A failed operation is a Result with a Code; error is for transport
	// failures (recorder unreachable, message not encodable).
	Command(ctx context.Context, c Command) (Result, error)
	// Status is the recorder's current state (also pushed to Sink.Status
	// every 15 s).
	Status() ServerStatus
	// Video is the recorder's video plane: live HLS, WebRTC (WHEP),
	// recording files, playback playlists/pieces, prepared exports, evidence
	// files and event thumbnails, served over HTTP (see VideoPaths). The
	// management server authenticates and authorizes a request, then hands
	// it to this handler unchanged. In-process that is a function call; in
	// phase 2 it is a reverse proxy (the "gateway path") or the client goes
	// to the recorder directly with a video token.
	Video() http.Handler
}

// Sink is implemented by the management server and used by a recording
// server.
type Sink interface {
	// Event delivers one event. Non-ephemeral events come from the
	// recorder's outbox and may be delivered more than once: Event must be
	// idempotent by Event.ID. An error means "not taken, retry later".
	Event(ctx context.Context, e Event) error
	// Status reports the recorder's state; it never blocks for long.
	Status(ServerStatus)
}

// ErrNoRecorder is returned when no recording server is connected.
var ErrNoRecorder = errors.New("no recording server is connected")

// NewID returns a random 128-bit hex id for messages and commands.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// MARK: - Wire envelope (phase 2)

// Message types on the control link.
const (
	TypeHello          = "hello"
	TypeConfigSnapshot = "config.snapshot"
	TypeConfigApplied  = "config.applied"
	TypeStatus         = "status"
	TypeEvent          = "event"
	TypeCommand        = "command"
	TypeResult         = "result"
	TypePing           = "ping"
	TypePong           = "pong"
)

// Envelope frames every control-link message:
//
//	{"type":"config.snapshot","id":"…","rev":42,"body":{…}}
type Envelope struct {
	Type string          `json:"type"`
	ID   string          `json:"id"`
	Re   string          `json:"re,omitempty"`  // id of the message this answers
	Rev  int64           `json:"rev,omitempty"` // config revision (config.*)
	Body json.RawMessage `json:"body,omitempty"`
}

// Wrap builds an envelope around body.
func Wrap(typ, id string, body any) (Envelope, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Type: typ, ID: id, Body: raw}, nil
}

// Decode unmarshals the body into v.
func (e Envelope) Decode(v any) error { return json.Unmarshal(e.Body, v) }

// Hello is the first message a recording server sends (phase 2).
type Hello struct {
	ServerID       string       `json:"serverId"`
	Version        string       `json:"version"`
	Protocol       int          `json:"protocol"`
	Capabilities   Capabilities `json:"capabilities"`
	LastAppliedRev int64        `json:"lastAppliedRev"`
	// VideoURL is where the server's video plane listens
	// (http://host:8090); the management server proxies /v/{token}/… there.
	VideoURL string `json:"videoUrl,omitempty"`
}

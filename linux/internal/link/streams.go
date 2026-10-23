package link

// Finding a saved camera's second stream (WAVE22 addendum): the recorder
// that owns the camera asks it over ONVIF and tries guessed addresses with
// RTSP DESCRIBE, since the camera is on the recorder's network. Resizing a
// too-small second stream reuses CmdEncoderSet.

import "encoding/json"

// CmdStreamsDiscover lists a camera's streams and recommends a second one
// (StreamsDiscoverArgs → StreamsDiscoverResult). The camera's own failures
// (unreachable, wrong password) come back inside an ok result.
const CmdStreamsDiscover = "cameras.streams.discover"

type StreamsDiscoverArgs struct {
	CameraID string `json:"cameraID"`
	// Login replaces the camera's configured username/password for this
	// discovery only (an admin trying another login); never stored. The
	// control link is TLS with pinned certificates (internal/pki).
	Login    bool   `json:"login,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type StreamsDiscoverResult struct {
	Discovery json.RawMessage `json:"discovery,omitempty"` // onvif.StreamDiscovery
	// Failure of the camera (the command itself succeeded):
	Error      string `json:"error,omitempty"`
	AuthFailed bool   `json:"authFailed,omitempty"`
}

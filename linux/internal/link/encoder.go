package link

// Camera stream settings (ONVIF video encoder configuration) on a recording
// server, for Sentinel Admin: the camera sits on the recorder's network, so
// the recorder talks to it. Credentials never travel in these commands; the
// recorder uses the ones from its applied configuration.

import "encoding/json"

// CmdEncoderGet reads a camera's encoder configurations and the ranges it
// accepts (EncoderGetArgs → EncoderGetResult). The camera's own failures
// (unreachable, wrong password) come back inside an ok result.
const CmdEncoderGet = "cameras.encoder.get"

// CmdEncoderSet changes one encoder configuration (EncoderSetArgs →
// EncoderSetResult). Zero-valued fields keep the camera's current value;
// the camera's own failures come back inside an ok result, like ProbeResult.
const CmdEncoderSet = "cameras.encoder.set"

type EncoderGetArgs struct {
	CameraID string `json:"cameraID"`
}

type EncoderGetResult struct {
	Streams json.RawMessage `json:"streams,omitempty"` // []onvif.EncoderStream
	// Failure of the camera (the command itself succeeded):
	Error      string `json:"error,omitempty"`
	AuthFailed bool   `json:"authFailed,omitempty"`
}

type EncoderSetArgs struct {
	CameraID    string `json:"cameraID"`
	ConfigToken string `json:"configToken"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	FPS         int    `json:"fps,omitempty"`
	Bitrate     int    `json:"bitrate,omitempty"`
	GovLength   int    `json:"govLength,omitempty"`
	Quality     int    `json:"quality,omitempty"`
}

type EncoderSetResult struct {
	Error      string `json:"error,omitempty"`
	AuthFailed bool   `json:"authFailed,omitempty"`
}

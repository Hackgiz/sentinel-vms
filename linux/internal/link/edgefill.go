package link

// Edge recording recovery (WAVE9-REACH.md, stream C): after an outage the
// recording server pulls the camera's own SD-card footage (ONVIF Profile G)
// into the gaps of its recordings. The camera sits on the recorder's
// network and the footage lands next to the recorder's own, so the
// recorder does the work; the management side starts the job and polls it,
// like CmdExportLong. Credentials never travel: the recorder uses the ones
// from its applied configuration.

// CmdEdgeRecovery starts, reads or cancels a recovery job
// (EdgeRecoveryArgs → EdgeRecoveryStatus).
const CmdEdgeRecovery = "recordings.edge"

// Edge recovery operations.
const (
	EdgeOpStart  = "start"
	EdgeOpStatus = "status"
	EdgeOpCancel = "cancel"
)

// EdgeRecoveryArgs: start needs CameraID, From, To (Unix ms, at most
// MaxEdgeRecoveryMs apart); status and cancel need JobID.
type EdgeRecoveryArgs struct {
	Op       string `json:"op"`
	JobID    string `json:"jobId,omitempty"`
	CameraID string `json:"cameraId,omitempty"`
	From     int64  `json:"from,omitempty"`
	To       int64  `json:"to,omitempty"`
}

// MaxEdgeRecoveryMs is the longest range one job may cover (24 h).
const MaxEdgeRecoveryMs = 24 * 3600 * 1000

// Edge recovery job states (the same words as long exports).
const (
	EdgeRunning  = "running"
	EdgeDone     = "done"
	EdgeFailed   = "failed"
	EdgeCanceled = "canceled"
)

// EdgeRecoveryStatus is a job's state. Done/Total count footage pieces
// (each at most one segment long) fetched from the camera.
type EdgeRecoveryStatus struct {
	JobID string `json:"jobId"`
	State string `json:"state"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	// Gaps is how many holes the local recordings had in the range;
	// Recovered how many segments were written (each with a .edge marker);
	// Skipped how many pieces brought nothing (the camera had no footage,
	// or local footage appeared meanwhile); Seconds the footage recovered.
	Gaps      int     `json:"gaps"`
	Recovered int     `json:"recovered"`
	Skipped   int     `json:"skipped"`
	Seconds   float64 `json:"seconds"`
	Error     string  `json:"error,omitempty"`
	Code      string  `json:"code,omitempty"` // failure class: a Code* value
}

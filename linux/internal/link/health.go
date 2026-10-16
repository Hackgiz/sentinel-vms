package link

// System health of a recording server, for the System Monitor: the
// management server asks each connected recorder for a reading.

import "encoding/json"

// CmdHealth reads the recording server's health (no args → HealthResult).
const CmdHealth = "system.health"

// HealthCamera is one camera on the recording server.
type HealthCamera struct {
	ID        string `json:"id"`
	Online    bool   `json:"online"`    // main stream is up
	Recording bool   `json:"recording"` // configured to record and the stream is up
	// LastSegmentAgeSec is the age of the newest recording file, measured
	// on the recorder (so clock skew doesn't matter); nil = none yet.
	LastSegmentAgeSec *int64 `json:"lastSegmentAgeSec,omitempty"`
	// KBPS is the ingest rate from the media layer's byte counter over the
	// interval between two readings; nil until a second reading.
	KBPS *float64 `json:"kbps,omitempty"`
}

type HealthResult struct {
	ServerID  string `json:"serverId"`
	Version   string `json:"version"`
	UptimeSec int64  `json:"uptimeSec"`
	DBBytes   int64  `json:"dbBytes"`
	// Host is a hwinfo.Health (CPU, memory, recordings disk), carried as
	// JSON so this package stays free of it (discover.go style).
	Host    json.RawMessage `json:"host"`
	Cameras []HealthCamera  `json:"cameras"`
}

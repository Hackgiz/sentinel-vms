package link

// Camera discovery on a recording server, for Sentinel Admin: the
// management server asks the recorder to scan ITS network (the cameras'
// LAN is the recorder's, not necessarily the management server's).

import "encoding/json"

// CmdDiscover scans the recording server's LAN (DiscoverArgs →
// DiscoverResult). One scan at a time; a second ask fails with CodeBusy.
const CmdDiscover = "cameras.discover"

// CmdProbe signs in to one camera over ONVIF from the recording server
// (ProbeArgs → ProbeResult). The camera's own failures (unreachable, wrong
// password) come back inside an ok ProbeResult, not as a command error.
const CmdProbe = "cameras.probe"

type DiscoverArgs struct {
	Sweep bool `json:"sweep,omitempty"` // TCP-sweep the local subnets too
}

type DiscoverResult struct {
	Devices  json.RawMessage `json:"devices"` // []onvif.Device
	Subnets  []string        `json:"subnets,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
}

type ProbeArgs struct {
	Host       string `json:"host"`
	ServiceURL string `json:"serviceURL,omitempty"`
	Username   string `json:"username,omitempty"`
	// Password travels only for this probe and is never stored; the
	// control link is TLS with pinned certificates (internal/pki).
	Password string `json:"password,omitempty"`
}

type ProbeResult struct {
	// Success:
	Details json.RawMessage `json:"details,omitempty"` // onvif.Details
	MainURL string          `json:"mainURL,omitempty"`
	SubURL  string          `json:"subURL,omitempty"`
	// Failure of the camera (the command itself succeeded):
	Error        string `json:"error,omitempty"`
	AuthFailed   bool   `json:"authFailed,omitempty"`
	SuggestedURL string `json:"suggestedURL,omitempty"`
}

package link

import (
	"encoding/json"
	"fmt"
)

// MARK: - Configuration (MS → RS)

// Snapshot is the full desired configuration of one recording server.
type Snapshot struct {
	Rev                  int64          `json:"rev"`
	Cameras              []CameraConfig `json:"cameras"`
	DefaultRetentionDays int            `json:"defaultRetentionDays"`
	// WebRTCExtraHosts are extra ICE host addresses (System → Live view).
	WebRTCExtraHosts []string `json:"webrtcExtraHosts"`
	// HWAccel is the hardware acceleration setting: "auto" or "off".
	HWAccel string `json:"hwaccel"`
	// AIDescriptions: AI scene descriptions are on, so person/vehicle
	// events need follow-up frames (sent back as event.frames).
	AIDescriptions bool `json:"aiDescriptions"`
	// VideoTokenKeys are the management server's Ed25519 public keys
	// (standard base64) that video tokens are signed with; more than one
	// during a key rotation. The recorder verifies tokens with them
	// (internal/videotoken).
	VideoTokenKeys []string `json:"videoTokenKeys,omitempty"`
	// WAVE16: object detection settings (System → Detection).
	// DetectorModel is the catalog id of the model to run ("" = yolox-tiny);
	// DetectRate is the most detection passes per second per camera while
	// there is motion (0 = auto: 1 on the CPU, 5 with an accelerator).
	DetectorModel string `json:"detectorModel,omitempty"`
	DetectRate    int    `json:"detectRate,omitempty"`
	// RecordingsDir is where this server keeps its recordings, an absolute
	// path on it; "" = <its data directory>/recordings. Existing recordings
	// follow a change (moved in the background).
	RecordingsDir string `json:"recordingsDir,omitempty"`
	// RecordingDefaults are the server-wide recording settings (WAVE22):
	// the mode, trigger and pre/post-roll of cameras set to "default",
	// low-res retention and minFreePercent. Zero from an older management
	// server: the recorder uses RecordingDefaults.Normalized().
	RecordingDefaults RecordingDefaults `json:"recordingDefaults"`
}

// CameraConfig is one camera as the recorder needs it.
type CameraConfig struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	RTSPURL    string `json:"rtspURL"`              // without credentials
	SubRTSPURL string `json:"subRTSPURL,omitempty"` // optional low-res stream
	Username   string `json:"username,omitempty"`
	// Password is plain text in-process. On the control link it is empty
	// and SealedPassword carries it, sealed to the recorder's X25519 key
	// (internal/pki.Seal), until the recorder opens it in memory.
	Password        string `json:"password,omitempty"`
	SealedPassword  string `json:"sealedPassword,omitempty"`
	Recording       bool   `json:"recording"`
	RetentionDays   int    `json:"retentionDays"` // 0 = DefaultRetentionDays
	MotionLevel     int    `json:"motionLevel"`   // 0 off … 3 high
	ONVIFURL        string `json:"onvifURL,omitempty"`
	CameraAnalytics bool   `json:"cameraAnalytics"`
	// ImportedRecordingsPath is a folder of this camera's recordings from a
	// Sentinel for Mac install on this machine; the recorder indexes it
	// read-only (recorder/import_mac.go).
	ImportedRecordingsPath string `json:"importedRecordingsPath,omitempty"`
	// Zones are the camera's enabled detection zones and privacy masks.
	Zones []Zone `json:"zones,omitempty"`
	// AudioDetect is the audio event threshold in dB over the noise floor;
	// 0 = audio detection off for this camera (WAVE8(B)).
	AudioDetect int `json:"audioDetect,omitempty"`
	// LoiterSeconds: a person track present this long raises a Loitering
	// event (WAVE16); 0 = off.
	LoiterSeconds int `json:"loiterSeconds,omitempty"`
	// WAVE22 recording modes (recording.go). RecordingMode is the camera's
	// setting: default|continuous|events|dual|off, "" = a management server
	// older than wave 22 (Recording alone says on/off). Recording stays the
	// effective mode != off for recorders older than wave 22.
	RecordingMode       string `json:"recordingMode,omitempty"`
	RecordTrigger       string `json:"recordTrigger,omitempty"`       // default|motion|objects
	PreRollSeconds      *int   `json:"preRollSeconds,omitempty"`      // nil = server default; a number (0 too) is literal (WAVE24)
	PostRollSeconds     *int   `json:"postRollSeconds,omitempty"`     // nil = server default; a number (0 too) is literal (WAVE24)
	LowResRetentionDays int    `json:"lowResRetentionDays,omitempty"` // 0 = default (dual only)
	// Source is where the video comes from (WAVE23): SourceRTSP ("" too)
	// pulls RTSPURL; SourcePhone is an iPhone that pushes fMP4 segments to
	// the recorder's video plane (/push/…), which publishes them into
	// MediaMTX. A phone camera has no RTSP/ONVIF fields and no sub-stream.
	Source string `json:"source,omitempty"`
}

// Camera sources (CameraConfig.Source, WAVE23-PHONE-CAMERA.md section A).
const (
	SourceRTSP  = "rtsp"
	SourcePhone = "phone"
)

// IsPhone reports whether the camera is an iPhone that pushes its video.
func (c CameraConfig) IsPhone() bool { return c.Source == SourcePhone }

// Applied acknowledges a snapshot.
type Applied struct {
	Rev int64 `json:"rev"`
	// Errors lists cameras that could not be configured, by camera id.
	Errors map[string]string `json:"errors,omitempty"`
}

// MARK: - Status (RS → MS)

type ServerStatus struct {
	ServerID     string                  `json:"serverId"`
	Version      string                  `json:"version"`
	Time         int64                   `json:"time"` // Unix ms
	ConfigRev    int64                   `json:"configRev"`
	Media        MediaStatus             `json:"media"`
	Capabilities Capabilities            `json:"capabilities"`
	Cameras      map[string]CameraStatus `json:"cameras"`
}

type MediaStatus struct {
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

type Capabilities struct {
	// Detection: ffmpeg is available (motion detection, event snapshots).
	Detection bool `json:"detection"`
	// Objects: the person/vehicle detector is loaded.
	Objects bool         `json:"objects"`
	WebRTC  WebRTCStatus `json:"webrtc"`
}

type WebRTCStatus struct {
	Enabled bool `json:"enabled"`
	ICEPort int  `json:"icePort"`
	// ConfigHosts are ICE hosts set at startup (-webrtc-hosts).
	ConfigHosts []string `json:"configHosts,omitempty"`
}

type CameraStatus struct {
	Ready   bool `json:"ready"`   // main stream is up
	Readers int  `json:"readers"` // viewers
	Width   int  `json:"width,omitempty"`
	Height  int  `json:"height,omitempty"`
	// Analytics is the camera-analytics subscription state; "" = none.
	Analytics string `json:"analytics,omitempty"`
	// Audio is the audio tap's detail line ("Listening: loudness only (no
	// audio model)", why there is no audio …); "" = audio detection is off.
	Audio string `json:"audio,omitempty"`
	// EdgeRecording: the camera offers ONVIF Profile G recording search and
	// replay, so its SD-card footage can fill gaps (WAVE9(C), edgefill.go).
	EdgeRecording bool `json:"edgeRecording,omitempty"`
	// Push is a phone camera's push state (WAVE23); nil for RTSP cameras.
	Push *PushStatus `json:"push,omitempty"`
}

// PushStatus is a phone camera's push ingest on its recording server.
type PushStatus struct {
	Streaming  bool   `json:"streaming"`            // a push session is open
	Device     string `json:"device,omitempty"`     // the phone's name, from its last session
	LastSeenAt int64  `json:"lastSeenAt,omitempty"` // Unix ms of the last segment; 0 = none since the recorder started
}

// MARK: - Events (RS → MS)

// Event types.
const (
	// EventActivity: a detector saw something (drives MQTT's motion/person
	// binary sensors). Ephemeral, high rate.
	EventActivity = "activity"
	// EventDetection: a new detection event was recorded.
	EventDetection = "detection"
	// EventThumbnail: an event's snapshot was saved.
	EventThumbnail = "event.thumbnail"
	// EventFrames: follow-up frames for an AI scene description. Ephemeral.
	EventFrames = "event.frames"
	// EventStorageLow / EventStorageRecovered: the recordings disk crossed
	// the low-space thresholds (under 5% free / back over 8%).
	EventStorageLow       = "storage.low"
	EventStorageRecovered = "storage.recovered"
)

// Detection sources.
const (
	SourceMotion  = "motion"  // our motion detector
	SourceObjects = "objects" // our person/vehicle detector
	SourceAudio   = "audio"   // our audio event detector (WAVE8(B))
	SourceCamera  = "camera"  // the camera's own analytics (ONVIF)
	SourceLines   = "lines"   // our line-crossing tracker (WAVE8)
)

// Event is one message from a recorder's event stream. Exactly one of the
// pointer fields is set, matching Type.
type Event struct {
	ID       string `json:"id"` // idempotency key
	Type     string `json:"type"`
	ServerID string `json:"serverId,omitempty"`
	CameraID string `json:"cameraId,omitempty"`
	Time     int64  `json:"time"` // Unix ms, when it was emitted
	// Ephemeral events are not kept in the outbox and not replayed.
	Ephemeral bool `json:"ephemeral,omitempty"`

	Activity  *Activity   `json:"activity,omitempty"`
	Detection *Detection  `json:"detection,omitempty"`
	Thumbnail *Thumbnail  `json:"thumbnail,omitempty"`
	Frames    *Frames     `json:"frames,omitempty"`
	Storage   *Storage    `json:"storage,omitempty"`
	Audit     *AuditEntry `json:"audit,omitempty"` // EventAudit (audit.go)
}

type Activity struct {
	Kind string `json:"kind"` // motion | Person | Vehicle | Line crossing …
}

// Detection is a recorded event (same fields as the events table) plus
// what the management server needs to raise or merge an alarm.
type Detection struct {
	EventID   string  `json:"eventId"`
	Source    string  `json:"source"` // Source*
	Kind      string  `json:"kind"`   // Motion | Person | Vehicle | Animal | Line crossing …
	Label     string  `json:"label,omitempty"`
	Score     float64 `json:"score"`
	Box       string  `json:"box,omitempty"` // JSON [x1,y1,x2,y2], normalized
	CreatedAt int64   `json:"createdAt"`     // Unix s (events.created_at)
	At        int64   `json:"at"`            // Unix ms, detection time
	// MergeSince is the alarm grouping boundary from the detector's episode
	// rule: fold into an open alarm whose last event is at/after it (Unix ms).
	MergeSince int64 `json:"mergeSince"`
	// TrackID is the detector's track, "<cameraId>:<n>", on events made
	// from tracked objects (WAVE16); "" otherwise.
	TrackID string `json:"trackId,omitempty"`
	// Camera analytics only.
	Rule    string `json:"rule,omitempty"`
	Topic   string `json:"topic,omitempty"`
	Summary string `json:"summary,omitempty"` // `a line crossing (person, rule "Gate")`
}

type Thumbnail struct {
	EventID string `json:"eventId"`
	File    string `json:"file"` // name in the recorder's events directory
}

type Frames struct {
	EventID string   `json:"eventId"`
	Kind    string   `json:"kind"`
	JPEGs   [][]byte `json:"jpegs"`
}

type Storage struct {
	FreePct   float64 `json:"freePct"`
	FreeBytes uint64  `json:"freeBytes"`
}

// MARK: - Commands (MS → RS, request/response)

type Command struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Args json.RawMessage `json:"args,omitempty"`
}

// NewCommand builds a command with a fresh id.
func NewCommand(typ string, args any) (Command, error) {
	c := Command{ID: NewID(), Type: typ}
	if args != nil {
		raw, err := json.Marshal(args)
		if err != nil {
			return c, err
		}
		c.Args = raw
	}
	return c, nil
}

// Decode unmarshals the arguments into v.
func (c Command) Decode(v any) error {
	if len(c.Args) == 0 {
		return nil
	}
	return json.Unmarshal(c.Args, v)
}

// Result codes. "" is success.
const (
	CodeBadRequest     = "bad_request"
	CodeNotFound       = "not_found"
	CodeBusy           = "busy"
	CodeUnavailable    = "unavailable"
	CodeTimeout        = "timeout"
	CodeInternal       = "internal"
	CodeUnknownCommand = "unknown_command"
	CodeNoClip         = "no_clip"
	CodeClipWriting    = "clip_writing"
)

type Result struct {
	Re    string          `json:"re,omitempty"`
	Code  string          `json:"code,omitempty"`
	Error string          `json:"error,omitempty"` // shown to the user
	Body  json.RawMessage `json:"body,omitempty"`
}

// OK is a successful result carrying body.
func OK(c Command, body any) Result {
	r := Result{Re: c.ID}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return Fail(c, CodeInternal, err.Error())
		}
		r.Body = raw
	}
	return r
}

// Fail is a failed result.
func Fail(c Command, code, msg string) Result { return Result{Re: c.ID, Code: code, Error: msg} }

// CommandError is a failed Result as a Go error.
type CommandError struct {
	Code    string
	Message string
}

func (e *CommandError) Error() string { return e.Message }

// Err is nil on success, else a *CommandError.
func (r Result) Err() error {
	if r.Code == "" {
		return nil
	}
	return &CommandError{Code: r.Code, Message: r.Error}
}

// Decode unmarshals the body into v, or returns the command error.
func (r Result) Decode(v any) error {
	if err := r.Err(); err != nil {
		return err
	}
	if v == nil || len(r.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("decode %s result: %w", r.Re, err)
	}
	return nil
}

// Command types, their arguments and result bodies.
const (
	CmdRecordings     = "recordings.list"   // RecordingsArgs → []Segment (newest first)
	CmdCovers         = "recordings.covers" // CoversArgs → CoversResult
	CmdTimeline       = "timeline"          // RangeArgs → Timeline
	CmdEvidenceLock   = "evidence.lock"     // EvidenceLockArgs → EvidenceFile
	CmdEvidenceDelete = "evidence.delete"   // FileArgs
	CmdExportClip     = "export.clip"       // ExportArgs → ExportResult
	CmdEvidenceAdopt  = "evidence.adopt"    // EvidenceAdoptArgs → EvidenceFile
	CmdSnapshot       = "snapshot"          // SnapshotArgs → SnapshotResult
	CmdStorageInfo    = "storage.info"      // → StorageInfo
	CmdHWAccelStatus  = "hwaccel.status"    // → HWAccelStatus
	CmdExportLong     = "export.long"       // ExportLongArgs → ExportLongStatus (WAVE7(C): see exportlong.go)
)

// Long export operations (CmdExportLong). A long export runs for minutes to
// hours, so start returns at once with a job id and the management side
// polls status; cancel stops it and removes its temp files.
const (
	ExportLongOpStart  = "start"
	ExportLongOpStatus = "status"
	ExportLongOpCancel = "cancel"
)

// ExportLongArgs: Op start needs CameraID, From, To; status and cancel need JobID.
type ExportLongArgs struct {
	Op       string `json:"op"`
	JobID    string `json:"jobId,omitempty"`
	CameraID string `json:"cameraId,omitempty"`
	From     int64  `json:"from,omitempty"` // Unix ms
	To       int64  `json:"to,omitempty"`
	// WAVE8(D): export options, see ExportArgs.
	Watermark string       `json:"watermark,omitempty"`
	Blur      [][4]float64 `json:"blur,omitempty"`
}

// Long export job states.
const (
	ExportLongRunning  = "running"
	ExportLongDone     = "done"
	ExportLongFailed   = "failed"
	ExportLongCanceled = "canceled"
)

// ExportLongStatus is a job's state. A done job carries the prepared clip
// (same Token semantics as ExportResult: download it, or CmdEvidenceAdopt it).
type ExportLongStatus struct {
	JobID  string `json:"jobId"`
	State  string `json:"state"`
	Done   int    `json:"done"` // steps finished (pieces, then the join)
	Total  int    `json:"total"`
	Error  string `json:"error,omitempty"`
	Code   string `json:"code,omitempty"` // failure class: a Code* value
	Token  string `json:"token,omitempty"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type RecordingsArgs struct {
	CameraID string `json:"cameraId"`
}

// Segment is one recording file.
type Segment struct {
	Name    string `json:"name"`
	Start   int64  `json:"start"` // Unix s
	End     int64  `json:"end"`   // Unix s (file modification time)
	Size    int64  `json:"size"`
	Writing bool   `json:"writing"`
}

type CoversArgs struct {
	Items []CoverQuery `json:"items"`
}

type CoverQuery struct {
	CameraID string `json:"cameraId"`
	At       int64  `json:"at"` // Unix s
}

type CoversResult struct {
	Covered []bool `json:"covered"` // per item: a recording covers that time
}

// RangeArgs is a camera and a time window in Unix ms.
type RangeArgs struct {
	CameraID string `json:"cameraId"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
}

type Span struct {
	Start int64   `json:"start"` // Unix ms
	End   int64   `json:"end"`
	Media float64 `json:"media"`          // seconds into playback.m3u8 for the same window
	Edge  bool    `json:"edge,omitempty"` // WAVE9(C): footage recovered from the camera's card (a .edge marker)
	// WAVE22: "full" or "low" (the low-res 24/7 tier); empty = full, so old
	// recorders and clients agree.
	Quality string `json:"quality,omitempty"`
}

type Timeline struct {
	Spans   []Span `json:"spans"`
	Pending []Span `json:"pending"`
	Writing bool   `json:"writing"`
}

type EvidenceLockArgs struct {
	CameraID string `json:"cameraId"`
	At       int64  `json:"at"`     // Unix s; the recording covering it is locked
	CaseID   string `json:"caseId"` // file name in the vault (<caseId>.mp4)
}

type EvidenceFile struct {
	File    string `json:"file"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Start   int64  `json:"start"` // Unix s, recording start
	End     int64  `json:"end"`   // Unix s
	Writing bool   `json:"writing"`
	Quality string `json:"quality,omitempty"` // WAVE22: "low" when the low-res tier was copied; empty = full
}

type FileArgs struct {
	File string `json:"file"`
}

// PreferSmall is ExportArgs.Prefer for a mixed-tier export brought down to the
// low-res size.
const PreferSmall = "small"

type ExportArgs struct {
	CameraID string `json:"cameraId"`
	From     int64  `json:"from"` // Unix ms
	To       int64  `json:"to"`
	// WAVE8(D): Watermark is the strip text the management server composed
	// (requesting user, time, case id); Blur are normalized {x0,y0,x1,y1}
	// regions to pixelate. Either forces a re-encode.
	Watermark string       `json:"watermark,omitempty"`
	Blur      [][4]float64 `json:"blur,omitempty"`
	// WAVE24: when the range holds both recording tiers the clip is
	// re-encoded to one size: the full-res size by default, the low-res size
	// with Prefer "small".
	Prefer string `json:"prefer,omitempty"`
}

// ExportResult is a prepared clip: GET /rs/exports/{token} on the video
// plane serves it for 30 minutes, or CmdEvidenceAdopt moves it into the
// evidence vault.
type ExportResult struct {
	Token  string `json:"token"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// EvidenceAdoptArgs moves a prepared clip into the evidence vault as
// <CaseID>.mp4 (read-only). It never overwrites an existing case file.
type EvidenceAdoptArgs struct {
	Token  string `json:"token"`
	CaseID string `json:"caseId"`
}

type SnapshotArgs struct {
	CameraID string `json:"cameraId"`
	At       int64  `json:"at"` // Unix ms
}

type SnapshotResult struct {
	JPEG []byte `json:"jpeg"`
}

type StorageInfo struct {
	RecordingsDir   string `json:"recordingsDir"`
	HasDisk         bool   `json:"hasDisk"`
	DiskFreeBytes   uint64 `json:"diskFreeBytes"`
	DiskTotalBytes  uint64 `json:"diskTotalBytes"`
	RecordingsBytes int64  `json:"recordingsBytes"`
}

// HWAccelStatus carries detect.DecodeCaps, []detect.CameraDecode and
// *detect.DetectorInfo as JSON so this package stays free of the detector.
type HWAccelStatus struct {
	Available bool            `json:"available"` // a detection manager exists
	Decode    json.RawMessage `json:"decode,omitempty"`
	Cameras   json.RawMessage `json:"cameras,omitempty"`
	Detection json.RawMessage `json:"detection,omitempty"` // null = no object detector
}

// MARK: - Video plane

// Video plane paths (Recorder.Video). The first group is the public video
// URL space and is passed through unchanged; the /rs/ paths are internal
// and reached only through the management server.
var VideoPaths = []string{
	"GET /live/{id}/{file}",                            // HLS from MediaMTX (query passed through)
	"POST /webrtc/{id}/whep",                           // WHEP offer → answer
	"PATCH /webrtc/{id}/whep/{session}",                // trickle ICE
	"DELETE /webrtc/{id}/whep/{session}",               // end session
	"GET /recordings/{id}/{file}",                      // a segment, with Range
	"GET /api/v1/cameras/{id}/playback.m3u8",           // HLS VOD playlist for a window
	"GET /api/v1/cameras/{id}/pieces/{file}",           // keyframe-aligned piece
	"POST /push/{id}/sessions",                         // WAVE23: a phone camera starts pushing
	"PUT /push/{id}/sessions/{session}/init",           // its fMP4 init segment
	"PUT /push/{id}/sessions/{session}/segments/{seq}", // one fMP4 media segment
	"DELETE /push/{id}/sessions/{session}",             // stop pushing
	"GET /rs/exports/{token}",                          // a prepared clip download
	"GET /rs/evidence/{file}",                          // an evidence vault file
	"GET /rs/events/{file}",                            // an event snapshot JPEG
}

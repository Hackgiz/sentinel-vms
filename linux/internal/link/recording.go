package link

// Recording modes (WAVE22-RECORDING-MODES.md): what a camera keeps, and the
// site-wide defaults a camera set to "default" follows. Both sides use the
// resolver here so the API's effectiveRecordingMode is what the recorder
// actually does.

// Camera recording modes (CameraConfig.RecordingMode, v2 recordingMode).
const (
	RecordDefault    = "default"    // follow RecordingDefaults.DefaultMode
	RecordContinuous = "continuous" // main stream 24/7
	RecordEvents     = "events"     // main stream around events (pre/post-roll)
	RecordDual       = "dual"       // sub-stream 24/7 + main stream around events
	RecordOff        = "off"        // live only
)

// What starts a keep window (CameraConfig.RecordTrigger, v2 recordTrigger).
const (
	TriggerDefault = "default"
	TriggerMotion  = "motion"  // any motion, Sentinel detections and camera events
	TriggerObjects = "objects" // people and vehicles only
)

// Limits and defaults of the recording settings.
const (
	DefaultPreRollSeconds      = 5
	MaxPreRollSeconds          = 30
	DefaultPostRollSeconds     = 15
	MinPostRollSeconds         = 5
	MaxPostRollSeconds         = 120
	DefaultRetentionDaysValue  = 7
	DefaultLowResRetentionDays = 14
	MaxLowResRetentionDays     = 365
	MinFreePercentLow          = 5
	MinFreePercentHigh         = 50
)

// RecordingDefaults are the server-wide recording settings (v2
// /api/v2/settings/recording; Snapshot.RecordingDefaults).
type RecordingDefaults struct {
	DefaultMode                string `json:"defaultMode"`    // continuous|events|dual|off
	DefaultTrigger             string `json:"defaultTrigger"` // motion|objects
	DefaultPreRollSeconds      int    `json:"defaultPreRollSeconds"`
	DefaultPostRollSeconds     int    `json:"defaultPostRollSeconds"`
	DefaultRetentionDays       int    `json:"defaultRetentionDays"`
	DefaultLowResRetentionDays int    `json:"defaultLowResRetentionDays"`
	// MinFreePercent: when free space on the recordings disk drops under
	// it, the oldest footage is deleted (low-res first); 0 = off.
	MinFreePercent int `json:"minFreePercent"`
}

// DefaultRecordingSettings are the settings of a new install: 24/7 as
// before wave 22, so upgrading changes nothing.
func DefaultRecordingSettings() RecordingDefaults {
	return RecordingDefaults{
		DefaultMode: RecordContinuous, DefaultTrigger: TriggerMotion,
		DefaultPreRollSeconds: DefaultPreRollSeconds, DefaultPostRollSeconds: DefaultPostRollSeconds,
		DefaultRetentionDays: DefaultRetentionDaysValue, DefaultLowResRetentionDays: DefaultLowResRetentionDays,
	}
}

// Normalized replaces missing or out-of-range values with the defaults (a
// snapshot from a management server older than wave 22 carries none).
func (d RecordingDefaults) Normalized() RecordingDefaults {
	def := DefaultRecordingSettings()
	if d.DefaultMode == "" && d.DefaultTrigger == "" {
		// None sent (older management server): all defaults; only the
		// retention days may have come from elsewhere.
		def.DefaultRetentionDays, def.DefaultLowResRetentionDays = d.DefaultRetentionDays, d.DefaultLowResRetentionDays
		def.MinFreePercent = d.MinFreePercent
		d = def
	}
	if !ValidRecordingMode(d.DefaultMode) || d.DefaultMode == RecordDefault {
		d.DefaultMode = def.DefaultMode
	}
	if d.DefaultTrigger != TriggerMotion && d.DefaultTrigger != TriggerObjects {
		d.DefaultTrigger = def.DefaultTrigger
	}
	if d.DefaultPreRollSeconds < 0 || d.DefaultPreRollSeconds > MaxPreRollSeconds {
		d.DefaultPreRollSeconds = def.DefaultPreRollSeconds
	}
	if d.DefaultPostRollSeconds < MinPostRollSeconds || d.DefaultPostRollSeconds > MaxPostRollSeconds {
		d.DefaultPostRollSeconds = def.DefaultPostRollSeconds
	}
	if d.DefaultRetentionDays <= 0 {
		d.DefaultRetentionDays = def.DefaultRetentionDays
	}
	if d.DefaultLowResRetentionDays <= 0 || d.DefaultLowResRetentionDays > MaxLowResRetentionDays {
		d.DefaultLowResRetentionDays = min(2*d.DefaultRetentionDays, MaxLowResRetentionDays)
	}
	if d.MinFreePercent != 0 && (d.MinFreePercent < MinFreePercentLow || d.MinFreePercent > MinFreePercentHigh) {
		d.MinFreePercent = 0
	}
	return d
}

// ValidRecordingMode reports whether m is one of the camera modes
// (including "default").
func ValidRecordingMode(m string) bool {
	switch m {
	case RecordDefault, RecordContinuous, RecordEvents, RecordDual, RecordOff:
		return true
	}
	return false
}

// ValidRecordTrigger reports whether t is a camera trigger (including "default").
func ValidRecordTrigger(t string) bool {
	return t == TriggerDefault || t == TriggerMotion || t == TriggerObjects
}

// EffectiveRecordingMode is what the recorder does for a camera: mode is
// the camera's setting ("" = a configuration from before wave 22, where
// only recording says on/off), "default" follows defaultMode, and dual
// without a sub-stream falls back to events.
func EffectiveRecordingMode(mode string, recording, hasSub bool, defaultMode string) string {
	m := mode
	if m == "" {
		if !recording {
			return RecordOff
		}
		m = RecordDefault
	}
	if m == RecordDefault {
		m = defaultMode
		if !ValidRecordingMode(m) || m == RecordDefault {
			m = RecordContinuous
		}
	}
	if !ValidRecordingMode(m) {
		m = RecordContinuous
	}
	if m == RecordDual && !hasSub {
		m = RecordEvents
	}
	return m
}

// EffectiveRecording resolves a camera's recording settings against the
// server defaults (d is normalized first).
type EffectiveRecording struct {
	Mode                string
	Trigger             string
	PreRollSeconds      int
	PostRollSeconds     int
	RetentionDays       int
	LowResRetentionDays int
}

// Effective resolves this camera's recording settings against d.
func (c CameraConfig) Effective(d RecordingDefaults) EffectiveRecording {
	d = d.Normalized()
	e := EffectiveRecording{
		Mode:    EffectiveRecordingMode(c.RecordingMode, c.Recording, c.SubRTSPURL != "", d.DefaultMode),
		Trigger: c.RecordTrigger, PreRollSeconds: c.PreRollSeconds, PostRollSeconds: c.PostRollSeconds,
		RetentionDays: c.RetentionDays, LowResRetentionDays: c.LowResRetentionDays,
	}
	if e.Trigger != TriggerMotion && e.Trigger != TriggerObjects {
		e.Trigger = d.DefaultTrigger
	}
	if e.PreRollSeconds <= 0 || e.PreRollSeconds > MaxPreRollSeconds {
		e.PreRollSeconds = d.DefaultPreRollSeconds
	}
	if e.PostRollSeconds <= 0 || e.PostRollSeconds > MaxPostRollSeconds {
		e.PostRollSeconds = d.DefaultPostRollSeconds
	}
	if e.RetentionDays <= 0 {
		e.RetentionDays = d.DefaultRetentionDays
	}
	if e.LowResRetentionDays <= 0 {
		e.LowResRetentionDays = d.DefaultLowResRetentionDays
	}
	return e
}

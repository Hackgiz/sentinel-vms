package link

// Zone kinds (WAVE7-FEATURES.md, Stream A).
const (
	ZoneDetect = "detect" // motion/object detection only counts inside
	ZoneMask   = "mask"   // privacy mask: burned black in every server JPEG and export
	ZoneLine   = "line"   // WAVE8: two-point tripwire; Direction ltr | rtl | both
)

// Zone is one enabled polygon of a camera as the recorder needs it. Points
// are normalized 0..1 against the full frame, any winding.
type Zone struct {
	Kind   string       `json:"kind"`
	Points [][2]float64 `json:"points"`
	// Lines only (kind "line"): the line's name (the event rule) and which
	// way crossings count, relative to p0→p1.
	Name      string `json:"name,omitempty"`
	Direction string `json:"direction,omitempty"`
}

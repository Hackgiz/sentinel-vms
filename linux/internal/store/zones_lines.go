package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Line crossing tripwires (WAVE8-DETECT-EVIDENCE.md, Stream A): camera_zones
// rows with kind='line'. points is exactly two vertices p0→p1 (normalized
// 0..1) and meta carries {"direction":"ltr"|"rtl"|"both"} plus, since wave 9,
// "severity":"Critical"|"Warning"|"Info" (default Critical; Info logs the
// crossing as an event without raising an alarm).
//
// Sides are relative to p0→p1 as drawn on the picture (y grows downward): a
// viewer standing at p0 looking toward p1 has "left" on their left and
// "right" on their right. "ltr" counts crossings from the left side to the
// right side, "rtl" the reverse, "both" either way.

const (
	ZoneKindLine = "line"

	LinePoints = 2
	minLineLen = 0.01 // normalized; a shorter line is a dot nobody can cross

	DirLTR  = "ltr"
	DirRTL  = "rtl"
	DirBoth = "both"

	LineSeverityCritical = "Critical"
	LineSeverityWarning  = "Warning"
	LineSeverityInfo     = "Info"
)

// ValidLineSeverity reports whether v is Critical, Warning or Info.
func ValidLineSeverity(v string) bool {
	return v == LineSeverityCritical || v == LineSeverityWarning || v == LineSeverityInfo
}

// ValidLineDirection reports whether d is ltr, rtl or both.
func ValidLineDirection(d string) bool { return d == DirLTR || d == DirRTL || d == DirBoth }

// ValidateLine checks a line's two points: exactly 2 finite in-frame
// vertices, not the same place and not a speck.
func ValidateLine(pts [][2]float64) error {
	if len(pts) != LinePoints {
		return fmt.Errorf("a line needs exactly %d points (got %d)", LinePoints, len(pts))
	}
	for i, p := range pts {
		for _, v := range p {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
				return fmt.Errorf("point %d is outside the frame (coordinates run 0 to 1)", i+1)
			}
		}
	}
	if pts[0] == pts[1] {
		return errors.New("the two points of a line are the same place")
	}
	if math.Hypot(pts[0][0]-pts[1][0], pts[0][1]-pts[1][1]) < minLineLen {
		return errors.New("the line is too short to cross")
	}
	return nil
}

// validateZoneShape applies the rule set that belongs to the zone's kind:
// polygons keep the wave-7 checks untouched; lines get their own.
func validateZoneShape(z ZoneInput) error {
	if z.Kind == ZoneKindLine {
		if err := ValidateLine(z.Points); err != nil {
			return err
		}
		_, err := NormalizeZoneMeta(z.Kind, z.Meta)
		return err
	}
	return ValidatePolygon(z.Points)
}

// NormalizeZoneMeta returns the canonical meta JSON to store. Lines: the
// direction (default "both"; anything but ltr/rtl/both is an error) and the
// severity (default "Critical"; anything but Critical/Warning/Info is an
// error). Other kinds carry no metadata: "{}".
func NormalizeZoneMeta(kind, meta string) (string, error) {
	if kind != ZoneKindLine {
		return "{}", nil
	}
	dir, sev := DirBoth, LineSeverityCritical
	if m := strings.TrimSpace(meta); m != "" && m != "null" {
		var in struct {
			Direction string `json:"direction"`
			Severity  string `json:"severity"`
		}
		if err := json.Unmarshal([]byte(m), &in); err != nil {
			return "", errors.New("line meta must be a JSON object like {\"direction\":\"both\"}")
		}
		if in.Direction != "" {
			dir = in.Direction
		}
		if in.Severity != "" {
			sev = in.Severity
		}
	}
	if !ValidLineDirection(dir) {
		return "", fmt.Errorf("line direction must be %q, %q or %q", DirLTR, DirRTL, DirBoth)
	}
	if !ValidLineSeverity(sev) {
		return "", fmt.Errorf("line severity must be %q, %q or %q", LineSeverityCritical, LineSeverityWarning, LineSeverityInfo)
	}
	b, _ := json.Marshal(map[string]string{"direction": dir, "severity": sev})
	return string(b), nil
}

// LineDirection is a line zone's direction ("" for other kinds). A damaged
// meta reads as "both" so a bad row still watches the line.
func LineDirection(z Zone) string {
	if z.Kind != ZoneKindLine {
		return ""
	}
	m, err := NormalizeZoneMeta(ZoneKindLine, z.Meta)
	if err != nil {
		return DirBoth
	}
	var out struct {
		Direction string `json:"direction"`
	}
	_ = json.Unmarshal([]byte(m), &out)
	return out.Direction
}

// LineSeverity is a line zone's severity ("" for other kinds). A damaged
// meta reads as "Critical" so a bad row still alarms.
func LineSeverity(z Zone) string {
	if z.Kind != ZoneKindLine {
		return ""
	}
	m, err := NormalizeZoneMeta(ZoneKindLine, z.Meta)
	if err != nil {
		return LineSeverityCritical
	}
	var out struct {
		Severity string `json:"severity"`
	}
	_ = json.Unmarshal([]byte(m), &out)
	return out.Severity
}

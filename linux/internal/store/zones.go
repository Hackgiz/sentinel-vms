package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"sentinel-linux/internal/link"
)

// Camera zones (WAVE7-FEATURES.md, Stream A): per-camera polygons that gate
// detection ("detect") or are burned out of every image ("mask"). Points are
// normalized 0..1 against the full frame, stored as JSON [[x,y],...].

const (
	ZoneKindDetect = link.ZoneDetect
	ZoneKindMask   = link.ZoneMask

	MinZonePoints = 3
	MaxZonePoints = 20
	MaxZones      = 50 // per camera
	maxZoneName   = 100
	minZoneArea   = 1e-6 // normalized units; rules out slivers and collapsed shapes
)

type Zone struct {
	ID        string
	CameraID  string
	Kind      string
	Name      string
	Points    [][2]float64
	Meta      string // JSON; {"direction":...} for kind "line", else "{}" (zones_lines.go)
	Enabled   bool
	CreatedAt int64
	UpdatedAt int64
}

// ZoneInput is one zone of a replacement set. ID may be empty (new zone).
type ZoneInput struct {
	ID      string
	Kind    string
	Name    string
	Points  [][2]float64
	Meta    string // JSON; for kind "line" only (zones_lines.go); "" = defaults
	Enabled bool
}

// ValidZoneKind reports whether kind is "detect", "mask" or "line".
func ValidZoneKind(kind string) bool {
	return kind == ZoneKindDetect || kind == ZoneKindMask || kind == ZoneKindLine
}

// ValidatePolygon checks a zone polygon: 3-20 finite vertices inside the
// 0..1 square, no repeated consecutive vertices, a real area, and no
// self-intersection (edges that cross, touch, or fold back on each other).
func ValidatePolygon(pts [][2]float64) error {
	n := len(pts)
	if n < MinZonePoints || n > MaxZonePoints {
		return fmt.Errorf("a zone needs %d to %d points (got %d)", MinZonePoints, MaxZonePoints, n)
	}
	for i, p := range pts {
		for _, v := range p {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
				return fmt.Errorf("point %d is outside the frame (coordinates run 0 to 1)", i+1)
			}
		}
	}
	for i := 0; i < n; i++ {
		if pts[i] == pts[(i+1)%n] {
			return fmt.Errorf("points %d and %d are the same place", i+1, (i+1)%n+1)
		}
	}
	for i := 0; i < n; i++ {
		a, b := pts[i], pts[(i+1)%n]
		for j := i + 1; j < n; j++ {
			c, d := pts[j], pts[(j+1)%n]
			if j == i+1 || (i == 0 && j == n-1) {
				// Adjacent edges share a vertex: they may only meet there,
				// so an edge that doubles back along its neighbour is out.
				shared, far1, far2 := b, a, d
				if i == 0 && j == n-1 {
					shared, far1, far2 = a, b, c
				}
				if cross(shared, far1, far2) == 0 && dot(shared, far1, far2) > 0 {
					return errors.New("the zone outline folds back on itself")
				}
				continue
			}
			if segmentsTouch(a, b, c, d) {
				return errors.New("the zone outline crosses itself")
			}
		}
	}
	if PolygonArea(pts) < minZoneArea {
		return errors.New("the zone has no area (all points are in a line)")
	}
	return nil
}

// PolygonArea is the absolute shoelace area.
func PolygonArea(pts [][2]float64) float64 {
	var s float64
	for i := range pts {
		j := (i + 1) % len(pts)
		s += pts[i][0]*pts[j][1] - pts[j][0]*pts[i][1]
	}
	return math.Abs(s) / 2
}

// cross is (b-a) x (c-a); dot is (b-a) . (c-a).
func cross(a, b, c [2]float64) float64 {
	return (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
}
func dot(a, b, c [2]float64) float64 {
	return (b[0]-a[0])*(c[0]-a[0]) + (b[1]-a[1])*(c[1]-a[1])
}

func sgn(v float64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// onSegment: c is collinear with a-b; is it within the segment's box?
func onSegment(a, b, c [2]float64) bool {
	return math.Min(a[0], b[0]) <= c[0] && c[0] <= math.Max(a[0], b[0]) &&
		math.Min(a[1], b[1]) <= c[1] && c[1] <= math.Max(a[1], b[1])
}

// segmentsTouch reports whether segments a-b and c-d share any point
// (proper crossing, T-touch or collinear overlap).
func segmentsTouch(a, b, c, d [2]float64) bool {
	d1, d2 := sgn(cross(a, b, c)), sgn(cross(a, b, d))
	d3, d4 := sgn(cross(c, d, a)), sgn(cross(c, d, b))
	if d1*d2 < 0 && d3*d4 < 0 {
		return true
	}
	return (d1 == 0 && onSegment(a, b, c)) || (d2 == 0 && onSegment(a, b, d)) ||
		(d3 == 0 && onSegment(c, d, a)) || (d4 == 0 && onSegment(c, d, b))
}

// ValidateZones checks a whole replacement set.
func ValidateZones(in []ZoneInput) error {
	if len(in) > MaxZones {
		return fmt.Errorf("at most %d zones per camera", MaxZones)
	}
	for i, z := range in {
		if !ValidZoneKind(z.Kind) {
			return fmt.Errorf("zone %d: kind must be %q, %q or %q", i+1, ZoneKindDetect, ZoneKindMask, ZoneKindLine)
		}
		if len(strings.TrimSpace(z.Name)) > maxZoneName {
			return fmt.Errorf("zone %d: name is too long (%d characters at most)", i+1, maxZoneName)
		}
		if err := validateZoneShape(z); err != nil { // polygon rules, or the line rules (zones_lines.go)
			return fmt.Errorf("zone %d: %w", i+1, err)
		}
	}
	return nil
}

// CameraZones lists a camera's zones in creation order.
func (s *Store) CameraZones(ctx context.Context, cameraID string) ([]Zone, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, camera_id, kind, name, points, meta, enabled, created_at, updated_at FROM camera_zones WHERE camera_id = ? ORDER BY created_at, rowid`, cameraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Zone{}
	for rows.Next() {
		var z Zone
		var pts string
		var en int
		if err := rows.Scan(&z.ID, &z.CameraID, &z.Kind, &z.Name, &pts, &z.Meta, &en, &z.CreatedAt, &z.UpdatedAt); err != nil {
			return nil, err
		}
		z.Enabled = en != 0
		if err := json.Unmarshal([]byte(pts), &z.Points); err != nil {
			continue // a damaged row must not take the whole camera down
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

// AllCameraZones lists every camera's zones, grouped by camera ID.
func (s *Store) AllCameraZones(ctx context.Context) (map[string][]Zone, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, camera_id, kind, name, points, meta, enabled, created_at, updated_at FROM camera_zones ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Zone{}
	for rows.Next() {
		var z Zone
		var pts string
		var en int
		if err := rows.Scan(&z.ID, &z.CameraID, &z.Kind, &z.Name, &pts, &z.Meta, &en, &z.CreatedAt, &z.UpdatedAt); err != nil {
			return nil, err
		}
		z.Enabled = en != 0
		if err := json.Unmarshal([]byte(pts), &z.Points); err != nil {
			continue
		}
		out[z.CameraID] = append(out[z.CameraID], z)
	}
	return out, rows.Err()
}

// SetCameraZones atomically replaces the camera's full zone set. Zones
// keeping an existing ID keep their created_at. The caller validates with
// ValidateZones; a missing camera is ErrNotFound.
func (s *Store) SetCameraZones(ctx context.Context, cameraID string, in []ZoneInput) ([]Zone, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cameras WHERE id = ?`, cameraID).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	created := map[string]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT id, created_at FROM camera_zones WHERE camera_id = ?`, cameraID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var at int64
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return nil, err
		}
		created[id] = at
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, `DELETE FROM camera_zones WHERE camera_id = ?`, cameraID); err != nil {
		return nil, err
	}
	t := now()
	seen := map[string]bool{}
	for _, z := range in {
		id := z.ID
		at, existed := created[id]
		if id == "" || !existed || seen[id] {
			id, at = NewID(), t
		}
		seen[id] = true
		raw, err := json.Marshal(z.Points)
		if err != nil {
			return nil, err
		}
		meta, err := NormalizeZoneMeta(z.Kind, z.Meta)
		if err != nil {
			return nil, err
		}
		en := 0
		if z.Enabled {
			en = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO camera_zones(id, camera_id, kind, name, points, meta, enabled, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, cameraID, z.Kind, strings.TrimSpace(z.Name), string(raw), meta, en, at, t); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.CameraZones(ctx, cameraID)
}

// LinkZones is the camera's enabled zones in the form the recorder takes.
func LinkZones(zs []Zone) []link.Zone {
	var out []link.Zone
	for _, z := range zs {
		if z.Enabled {
			out = append(out, link.Zone{Kind: z.Kind, Points: z.Points, Name: z.Name, Direction: LineDirection(z)})
		}
	}
	return out
}

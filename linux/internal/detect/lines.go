package detect

import (
	"encoding/json"
	"sync"
	"time"
)

// Line crossing (WAVE8-DETECT-EVIDENCE.md, Stream A). Object detections
// arrive one to five times a second per camera with boxes. The camera's
// Tracker (tracker.go) follows objects between analyzed frames; a
// LineTracker reports a crossing when the segment a track's bottom-center
// travelled between two frames intersects a tripwire in the line's direction.

// Line directions, relative to p0→p1 (see store/zones_lines.go): "ltr" counts
// the left side → right side (as seen standing at p0 looking at p1, y down),
// "rtl" the reverse, "both" either.
const (
	DirLTR  = "ltr"
	DirRTL  = "rtl"
	DirBoth = "both"
)

const (
	// lineDebounce: one event per track per line per 10 s.
	lineDebounce = 10 * time.Second
	// trackTTL: a track unmatched for longer is dropped. At the slowest rate
	// (one pass a second) this tolerates two missed frames.
	trackTTL = 3 * time.Second
	// matchDist is the largest center-to-center distance (normalized) one
	// object may move between consecutive analyzed frames and still be the
	// same object; matchIoU lets large slow objects match on overlap too.
	matchDist = 0.25
	matchIoU  = 0.1
	maxTracks = 64
)

// Line is one tripwire, normalized 0..1 against the full frame.
type Line struct {
	Name      string     `json:"name"`
	P0        [2]float64 `json:"p0"`
	P1        [2]float64 `json:"p1"`
	Direction string     `json:"direction"`
}

// LineCrossing is one object crossing one line.
type LineCrossing struct {
	CameraID  string
	Line      Line
	Direction string // ltr | rtl: the way this object actually crossed
	Object    Object
	TrackID   int
	At        time.Time
	// Frame is the analyzed frame (416×416 BGR, letterboxed) the crossing
	// was seen in, with its real-image size, for the event snapshot.
	Frame              []byte
	ContentW, ContentH int
}

// EncodeLines serializes lines for Source.Lines (a comparable string, so a
// line edit restarts the reader). nil/empty gives "".
func EncodeLines(lines []Line) string {
	if len(lines) == 0 {
		return ""
	}
	b, _ := json.Marshal(lines)
	return string(b)
}

// ParseLines is the inverse of EncodeLines; "" or garbage gives nil.
func ParseLines(s string) []Line {
	if s == "" {
		return nil
	}
	var lines []Line
	if json.Unmarshal([]byte(s), &lines) != nil {
		return nil
	}
	return lines
}

// side is which side of p0→p1 the point is on: cross(p1-p0, pt-p0). Positive
// is the right-hand side (y down), negative the left. Zero (on the line)
// counts as the right side, so a point sitting exactly on the line is
// attributed consistently and a crossing is never counted twice or missed.
func side(p0, p1, pt [2]float64) float64 {
	return (p1[0]-p0[0])*(pt[1]-p0[1]) - (p1[1]-p0[1])*(pt[0]-p0[0])
}

// SegmentCrossesLine reports whether the movement a→b crosses the tripwire
// p0→p1 (the crossing point must lie within the line's two ends). dir is
// DirLTR when the movement goes from the left side to the right side and
// DirRTL for the reverse.
func SegmentCrossesLine(a, b, p0, p1 [2]float64) (dir string, ok bool) {
	sa, sb := side(p0, p1, a), side(p0, p1, b)
	switch {
	case sa < 0 && sb >= 0:
		dir = DirLTR
	case sa >= 0 && sb < 0:
		dir = DirRTL
	default:
		return "", false
	}
	// Where along p0→p1 does the movement cut the line? t in [0,1] inside.
	// sa and sb differ in sign, so the denominator is never zero.
	s := [2]float64{b[0] - a[0], b[1] - a[1]}
	r := [2]float64{p1[0] - p0[0], p1[1] - p0[1]}
	denom := r[0]*s[1] - r[1]*s[0]
	t := ((a[0]-p0[0])*s[1] - (a[1]-p0[1])*s[0]) / denom
	if t < 0 || t > 1 {
		return "", false
	}
	return dir, true
}

// counts reports whether a crossing in direction got is wanted by a line
// configured for want ("" and unknown values mean both).
func countsDirection(want, got string) bool {
	switch want {
	case DirLTR, DirRTL:
		return want == got
	}
	return true
}

// lineTrack is what line crossing remembers about one tracked object.
type lineTrack struct {
	pos      [2]float64 // bottom-center at the last pass
	lastSeen time.Time
	fired    map[int]time.Time // line index → last crossing event
}

func bottomCenter(b [4]float64) [2]float64 { return [2]float64{(b[0] + b[2]) / 2, b[3]} }
func center(b [4]float64) [2]float64       { return [2]float64{(b[0] + b[2]) / 2, (b[1] + b[3]) / 2} }

// LineTracker finds line crossings for one camera. It reads Object.TrackID
// (set by the camera's Tracker, tracker.go); objects that arrive without
// ids (TrackID 0 throughout, as in tests) go through a private Tracker first.
// Safe for concurrent use.
type LineTracker struct {
	mu     sync.Mutex
	lines  []Line
	own    *Tracker // ids for untracked input
	tracks map[int]*lineTrack
}

// NewLineTracker returns nil when there are no lines (nothing to track).
func NewLineTracker(lines []Line) *LineTracker {
	if len(lines) == 0 {
		return nil
	}
	return &LineTracker{lines: lines, own: NewTracker(), tracks: map[int]*lineTrack{}}
}

// Update feeds one analysis pass (possibly with no objects) taken at time at
// and returns the crossings it completed. A new track only starts: it can
// cross from the next pass on.
func (t *LineTracker) Update(objs []Object, at time.Time) []LineCrossing {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !hasTrackIDs(objs) {
		objs, _ = t.own.Update(objs, at)
	}
	var out []LineCrossing
	for _, o := range objs {
		if o.TrackID == 0 {
			continue // the tracker was full
		}
		now := bottomCenter(o.Box)
		tr, ok := t.tracks[o.TrackID]
		if !ok || at.Sub(tr.lastSeen) > trackTTL {
			if len(t.tracks) < maxTracks {
				t.tracks[o.TrackID] = &lineTrack{pos: now, lastSeen: at}
			}
			continue
		}
		for li, ln := range t.lines {
			dir, ok := SegmentCrossesLine(tr.pos, now, ln.P0, ln.P1)
			if !ok || !countsDirection(ln.Direction, dir) {
				continue
			}
			if last, seen := tr.fired[li]; seen && at.Sub(last) < lineDebounce {
				continue
			}
			if tr.fired == nil {
				tr.fired = map[int]time.Time{}
			}
			tr.fired[li] = at
			out = append(out, LineCrossing{Line: ln, Direction: dir, Object: o, TrackID: o.TrackID, At: at})
		}
		tr.pos, tr.lastSeen = now, at
	}
	for id, tr := range t.tracks {
		if at.Sub(tr.lastSeen) > trackTTL {
			delete(t.tracks, id)
		}
	}
	return out
}

// hasTrackIDs reports whether a Tracker already set the objects' ids.
func hasTrackIDs(objs []Object) bool {
	for _, o := range objs {
		if o.TrackID != 0 {
			return true
		}
	}
	return false
}

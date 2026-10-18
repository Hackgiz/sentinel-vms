package detect

import (
	"encoding/json"
	"math"
	"sort"
	"sync"
	"time"
)

// Line crossing (WAVE8-DETECT-EVIDENCE.md, Stream A). Object detections
// arrive about once a second per camera with boxes. A LineTracker follows
// objects between consecutive analyzed frames (greedy nearest-center match)
// and reports a crossing when the segment a track's bottom-center travelled
// between two frames intersects a tripwire in the line's direction.

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
	// trackTTL: a track unmatched for longer is dropped. Analysis runs about
	// once a second, so this tolerates two missed frames.
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

type track struct {
	id       int
	kind     string
	box      [4]float64
	pos      [2]float64 // bottom-center
	lastSeen time.Time
	fired    map[int]time.Time // line index → last crossing event
}

func bottomCenter(b [4]float64) [2]float64 { return [2]float64{(b[0] + b[2]) / 2, b[3]} }
func center(b [4]float64) [2]float64       { return [2]float64{(b[0] + b[2]) / 2, (b[1] + b[3]) / 2} }

// LineTracker matches detections across frames and finds line crossings for
// one camera. Safe for concurrent use.
type LineTracker struct {
	mu     sync.Mutex
	lines  []Line
	tracks []*track
	nextID int
}

// NewLineTracker returns nil when there are no lines (nothing to track).
func NewLineTracker(lines []Line) *LineTracker {
	if len(lines) == 0 {
		return nil
	}
	return &LineTracker{lines: lines}
}

// Update feeds one analysis pass (possibly with no objects) taken at time at
// and returns the crossings it completed. A new object only starts a track:
// it can cross from the next frame on.
func (t *LineTracker) Update(objs []Object, at time.Time) []LineCrossing {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	// Drop stale tracks.
	live := t.tracks[:0]
	for _, tr := range t.tracks {
		if at.Sub(tr.lastSeen) <= trackTTL {
			live = append(live, tr)
		}
	}
	t.tracks = live

	// Greedy nearest-center matching: every viable (track, detection) pair,
	// closest first; each track and detection used once.
	type pair struct {
		ti, oi int
		d      float64
	}
	var pairs []pair
	for ti, tr := range t.tracks {
		tc := center(tr.box)
		for oi, o := range objs {
			if o.Kind != tr.kind {
				continue
			}
			oc := center(o.Box)
			d := math.Hypot(tc[0]-oc[0], tc[1]-oc[1])
			if d <= matchDist || IoU(tr.box, o.Box) >= matchIoU {
				pairs = append(pairs, pair{ti, oi, d})
			}
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].d < pairs[j].d })
	trackUsed := make([]bool, len(t.tracks))
	objUsed := make([]bool, len(objs))
	var out []LineCrossing
	for _, p := range pairs {
		if trackUsed[p.ti] || objUsed[p.oi] {
			continue
		}
		trackUsed[p.ti], objUsed[p.oi] = true, true
		tr, o := t.tracks[p.ti], objs[p.oi]
		now := bottomCenter(o.Box)
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
			out = append(out, LineCrossing{Line: ln, Direction: dir, Object: o, TrackID: tr.id, At: at})
		}
		tr.box, tr.pos, tr.lastSeen = o.Box, now, at
	}
	// Unmatched detections start tracks (bounded, so a flapping detector
	// can't grow this without limit).
	for oi, o := range objs {
		if objUsed[oi] || len(t.tracks) >= maxTracks {
			continue
		}
		t.nextID++
		t.tracks = append(t.tracks, &track{id: t.nextID, kind: o.Kind, box: o.Box, pos: bottomCenter(o.Box), lastSeen: at})
	}
	return out
}

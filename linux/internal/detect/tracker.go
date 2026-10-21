package detect

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Object tracking (WAVE16-DETECTION.md, stream B). One Tracker per camera
// follows every detected object between analysis passes with the matching
// rule line crossing has used since wave 8: greedy nearest-centre, same kind
// only, a centre within matchDist or boxes overlapping by matchIoU, a track
// dropped after trackTTL without a match. Each object leaves Update with its
// track's id in Object.TrackID; line crossing (lines.go), loitering and the
// event's trackId (recorder) read that id instead of matching on their own.

// TrackTTL is how long a track survives without a match (trackTTL).
const TrackTTL = trackTTL

// Track is a tracked object as Update last saw it.
type Track struct {
	ID        int
	Kind      string
	Label     string
	Box       [4]float64
	FirstSeen time.Time
	LastSeen  time.Time
}

// Duration is how long the object has been followed.
func (t Track) Duration() time.Duration { return t.LastSeen.Sub(t.FirstSeen) }

// Tracker assigns track ids to one camera's detections. Safe for concurrent
// use; ids are per tracker and never reused.
type Tracker struct {
	mu     sync.Mutex
	tracks []*Track
	nextID int
}

// NewTracker returns an empty tracker whose first id is 1.
func NewTracker() *Tracker { return &Tracker{} }

// newTrackerFrom starts ids after start (the Manager seeds it from the clock
// so ids stay unique across restarts).
func newTrackerFrom(start int) *Tracker { return &Tracker{nextID: start} }

// Update feeds one analysis pass (possibly with no objects) taken at time at.
// It returns a copy of objs with TrackID set (0 when the tracker is full) and
// the tracks that ended, unmatched for longer than TrackTTL, in id order.
func (t *Tracker) Update(objs []Object, at time.Time) (tracked []Object, ended []Track) {
	if t == nil {
		return objs, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	live := t.tracks[:0]
	for _, tr := range t.tracks {
		if at.Sub(tr.LastSeen) <= trackTTL {
			live = append(live, tr)
		} else {
			ended = append(ended, *tr)
		}
	}
	for i := len(live); i < len(t.tracks); i++ {
		t.tracks[i] = nil
	}
	t.tracks = live

	// Every viable (track, detection) pair, closest first; each track and
	// detection used once.
	type pair struct {
		ti, oi int
		d      float64
	}
	var pairs []pair
	for ti, tr := range t.tracks {
		tc := center(tr.Box)
		for oi, o := range objs {
			if o.Kind != tr.Kind {
				continue
			}
			oc := center(o.Box)
			d := math.Hypot(tc[0]-oc[0], tc[1]-oc[1])
			if d <= matchDist || IoU(tr.Box, o.Box) >= matchIoU {
				pairs = append(pairs, pair{ti, oi, d})
			}
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].d < pairs[j].d })
	tracked = make([]Object, len(objs))
	copy(tracked, objs)
	trackUsed := make([]bool, len(t.tracks))
	objUsed := make([]bool, len(objs))
	for _, p := range pairs {
		if trackUsed[p.ti] || objUsed[p.oi] {
			continue
		}
		trackUsed[p.ti], objUsed[p.oi] = true, true
		tr, o := t.tracks[p.ti], objs[p.oi]
		tr.Box, tr.Label, tr.LastSeen = o.Box, o.Label, at
		tracked[p.oi].TrackID = tr.ID
	}
	// Unmatched detections start tracks (bounded, so a flapping detector
	// can't grow this without limit).
	for oi, o := range objs {
		if objUsed[oi] {
			continue
		}
		tracked[oi].TrackID = 0
		if len(t.tracks) >= maxTracks {
			continue
		}
		t.nextID++
		t.tracks = append(t.tracks, &Track{ID: t.nextID, Kind: o.Kind, Label: o.Label, Box: o.Box, FirstSeen: at, LastSeen: at})
		tracked[oi].TrackID = t.nextID
	}
	return tracked, ended
}

// Track returns the live track with id.
func (t *Tracker) Track(id int) (Track, bool) {
	if t == nil || id == 0 {
		return Track{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tr := range t.tracks {
		if tr.ID == id {
			return *tr, true
		}
	}
	return Track{}, false
}

// Len is the number of live tracks.
func (t *Tracker) Len() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.tracks)
}

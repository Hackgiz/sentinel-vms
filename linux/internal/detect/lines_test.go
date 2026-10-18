package detect

import (
	"testing"
	"time"
)

type pt = [2]float64

func TestSegmentCrossesLineDirectionSense(t *testing.T) {
	// A horizontal line drawn left to right across the middle. Standing at
	// p0 looking at p1 (to the right on screen), "left" is UP the picture
	// and "right" is DOWN, so moving down is left→right ("ltr").
	p0, p1 := pt{0.2, 0.5}, pt{0.8, 0.5}
	for _, c := range []struct {
		name string
		a, b pt
		dir  string
		ok   bool
	}{
		{"down crosses ltr", pt{0.5, 0.4}, pt{0.5, 0.6}, DirLTR, true},
		{"up crosses rtl", pt{0.5, 0.6}, pt{0.5, 0.4}, DirRTL, true},
		{"diagonal down", pt{0.3, 0.45}, pt{0.7, 0.55}, DirLTR, true},
		{"stays above", pt{0.5, 0.2}, pt{0.5, 0.4}, "", false},
		{"stays below", pt{0.5, 0.6}, pt{0.5, 0.9}, "", false},
		{"parallel along the line", pt{0.3, 0.45}, pt{0.7, 0.45}, "", false},
		{"crosses the extension left of p0", pt{0.1, 0.4}, pt{0.1, 0.6}, "", false},
		{"crosses the extension right of p1", pt{0.9, 0.4}, pt{0.9, 0.6}, "", false},
		{"slanted path misses the end", pt{0.1, 0.4}, pt{0.15, 0.6}, "", false},
		{"ends exactly on the line counts once (arriving)", pt{0.5, 0.4}, pt{0.5, 0.5}, DirLTR, true},
		{"starting on the line, moving away down: no crossing", pt{0.5, 0.5}, pt{0.5, 0.6}, "", false},
		{"starting on the line, moving up crosses back", pt{0.5, 0.5}, pt{0.5, 0.4}, DirRTL, true},
		{"endpoint of the line itself", pt{0.2, 0.4}, pt{0.2, 0.6}, DirLTR, true},
		{"not moving", pt{0.5, 0.4}, pt{0.5, 0.4}, "", false},
	} {
		dir, ok := SegmentCrossesLine(c.a, c.b, p0, p1)
		if ok != c.ok || dir != c.dir {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, dir, ok, c.dir, c.ok)
		}
	}
	// Reversing the line swaps the sides, so the same movement flips sense.
	if dir, ok := SegmentCrossesLine(pt{0.5, 0.4}, pt{0.5, 0.6}, p1, p0); !ok || dir != DirRTL {
		t.Errorf("reversed line: got (%q, %v), want rtl", dir, ok)
	}
	// A vertical line drawn top to bottom: left of travel is screen-right.
	v0, v1 := pt{0.5, 0.1}, pt{0.5, 0.9}
	if dir, ok := SegmentCrossesLine(pt{0.6, 0.5}, pt{0.4, 0.5}, v0, v1); !ok || dir != DirLTR {
		t.Errorf("vertical, moving screen-left: got (%q, %v), want ltr", dir, ok)
	}
}

func TestCountsDirection(t *testing.T) {
	for _, c := range []struct {
		want, got string
		ok        bool
	}{
		{DirLTR, DirLTR, true}, {DirLTR, DirRTL, false},
		{DirRTL, DirRTL, true}, {DirRTL, DirLTR, false},
		{DirBoth, DirLTR, true}, {DirBoth, DirRTL, true},
		{"", DirRTL, true}, {"sideways", DirLTR, true},
	} {
		if countsDirection(c.want, c.got) != c.ok {
			t.Errorf("countsDirection(%q, %q) != %v", c.want, c.got, c.ok)
		}
	}
}

// person is a person-sized box standing with its feet (bottom-center) at x, y.
func person(x, y float64) Object {
	return Object{Kind: "Person", Label: "person", Score: 0.9, Box: [4]float64{x - 0.03, y - 0.2, x + 0.03, y}}
}

func vehicle(x, y float64) Object {
	return Object{Kind: "Vehicle", Label: "car", Score: 0.9, Box: [4]float64{x - 0.1, y - 0.1, x + 0.1, y}}
}

var t0 = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func sec(n int) time.Time { return t0.Add(time.Duration(n) * time.Second) }

var gate = Line{Name: "Gate", P0: pt{0.2, 0.5}, P1: pt{0.8, 0.5}, Direction: DirBoth}

func TestLineTrackerCrossingNeedsTwoFramesAndUsesBottomCenter(t *testing.T) {
	tr := NewLineTracker([]Line{gate})
	// First sighting only starts a track, even if it is already past the line.
	if got := tr.Update([]Object{person(0.5, 0.6)}, sec(0)); len(got) != 0 {
		t.Fatalf("first frame fired %v", got)
	}
	// Feet still below the line though the box top is above it: no crossing.
	if got := tr.Update([]Object{person(0.5, 0.55)}, sec(1)); len(got) != 0 {
		t.Fatalf("box overlapping the line fired %v", got)
	}
	// Feet cross upward: rtl.
	got := tr.Update([]Object{person(0.5, 0.45)}, sec(2))
	if len(got) != 1 || got[0].Direction != DirRTL || got[0].Line.Name != "Gate" || got[0].Object.Label != "person" || !got[0].At.Equal(sec(2)) {
		t.Fatalf("got %+v", got)
	}
}

func TestLineTrackerDirectionFilter(t *testing.T) {
	for _, c := range []struct {
		dir      string
		wantDown int // moving down = ltr
		wantUp   int // moving up = rtl
	}{{DirLTR, 1, 0}, {DirRTL, 0, 1}, {DirBoth, 1, 1}} {
		l := gate
		l.Direction = c.dir
		down := NewLineTracker([]Line{l})
		down.Update([]Object{person(0.5, 0.4)}, sec(0))
		if got := len(down.Update([]Object{person(0.5, 0.6)}, sec(1))); got != c.wantDown {
			t.Errorf("%s moving down: %d crossings, want %d", c.dir, got, c.wantDown)
		}
		up := NewLineTracker([]Line{l})
		up.Update([]Object{person(0.5, 0.6)}, sec(0))
		if got := len(up.Update([]Object{person(0.5, 0.4)}, sec(1))); got != c.wantUp {
			t.Errorf("%s moving up: %d crossings, want %d", c.dir, got, c.wantUp)
		}
	}
}

func TestLineTrackerMatchingKeepsIdentitiesApart(t *testing.T) {
	tr := NewLineTracker([]Line{gate})
	// Two people walking in opposite vertical directions, passing each other
	// side by side. Greedy nearest-center matching must not swap them.
	tr.Update([]Object{person(0.3, 0.4), person(0.7, 0.6)}, sec(0))
	got := tr.Update([]Object{person(0.3, 0.6), person(0.7, 0.4)}, sec(1))
	if len(got) != 2 {
		t.Fatalf("got %d crossings, want 2: %+v", len(got), got)
	}
	dirs := map[float64]string{}
	for _, c := range got {
		dirs[(c.Object.Box[0]+c.Object.Box[2])/2] = c.Direction
	}
	if dirs[0.3] != DirLTR || dirs[0.7] != DirRTL {
		t.Fatalf("directions by x: %v", dirs)
	}
	if got[0].TrackID == got[1].TrackID {
		t.Fatalf("both crossings came from track %d", got[0].TrackID)
	}
}

func TestLineTrackerMatchRules(t *testing.T) {
	// Different kinds never match: a car appearing where a person was is a
	// new track, so nothing crosses.
	tr := NewLineTracker([]Line{gate})
	tr.Update([]Object{person(0.5, 0.45)}, sec(0))
	if got := tr.Update([]Object{vehicle(0.5, 0.55)}, sec(1)); len(got) != 0 {
		t.Fatalf("a vehicle continued a person track: %v", got)
	}

	// A jump beyond the distance cutoff (and no overlap) is a new object.
	tr = NewLineTracker([]Line{gate})
	tr.Update([]Object{person(0.1, 0.45)}, sec(0))
	if got := tr.Update([]Object{person(0.9, 0.55)}, sec(1)); len(got) != 0 {
		t.Fatalf("teleport matched: %v", got)
	}

	// A track unmatched for longer than the TTL is gone: the reappearing
	// object only starts a fresh track.
	tr = NewLineTracker([]Line{gate})
	tr.Update([]Object{person(0.5, 0.45)}, sec(0))
	if got := tr.Update([]Object{person(0.5, 0.55)}, sec(10)); len(got) != 0 {
		t.Fatalf("expired track fired: %v", got)
	}
	// ...but within the TTL (a missed frame or two) the track survives.
	tr = NewLineTracker([]Line{gate})
	tr.Update([]Object{person(0.5, 0.45)}, sec(0))
	tr.Update(nil, sec(1))
	if got := tr.Update([]Object{person(0.5, 0.55)}, sec(2)); len(got) != 1 {
		t.Fatalf("track lost after one empty frame: %v", got)
	}
}

func TestLineTrackerDebouncePerTrackPerLine(t *testing.T) {
	tr := NewLineTracker([]Line{gate})
	y := 0.4
	tr.Update([]Object{person(0.5, y)}, sec(0))
	if got := tr.Update([]Object{person(0.5, 0.6)}, sec(1)); len(got) != 1 {
		t.Fatalf("first crossing: %v", got)
	}
	// Back across at 3 s and again at 5 s: same track, same line, inside 10 s.
	if got := tr.Update([]Object{person(0.5, 0.4)}, sec(3)); len(got) != 0 {
		t.Fatalf("recross inside debounce fired: %v", got)
	}
	if got := tr.Update([]Object{person(0.5, 0.6)}, sec(5)); len(got) != 0 {
		t.Fatalf("recross inside debounce fired: %v", got)
	}
	// Keep the track alive until the 10 s window (measured from the first
	// event at 1 s) has passed, then cross again.
	for s := 6; s < 11; s++ {
		tr.Update([]Object{person(0.5, 0.6)}, sec(s))
	}
	if got := tr.Update([]Object{person(0.5, 0.4)}, sec(11)); len(got) != 1 || got[0].Direction != DirRTL {
		t.Fatalf("crossing after debounce: %v", got)
	}
}

func TestLineTrackerDebounceIsPerLineAndPerTrack(t *testing.T) {
	second := Line{Name: "Door", P0: pt{0.2, 0.6}, P1: pt{0.8, 0.6}, Direction: DirBoth}
	tr := NewLineTracker([]Line{gate, second})
	tr.Update([]Object{person(0.3, 0.45), person(0.7, 0.45)}, sec(0))
	// One step crosses both lines for both people: 4 crossings, one each.
	got := tr.Update([]Object{person(0.3, 0.65), person(0.7, 0.65)}, sec(1))
	if len(got) != 4 {
		t.Fatalf("got %d crossings, want 4 (2 tracks x 2 lines): %+v", len(got), got)
	}
	seen := map[[2]any]bool{}
	for _, c := range got {
		seen[[2]any{c.TrackID, c.Line.Name}] = true
	}
	if len(seen) != 4 {
		t.Fatalf("crossings are not distinct per track and line: %v", seen)
	}
}

func TestLineTrackerNilAndEncoding(t *testing.T) {
	var nilTracker *LineTracker
	if got := nilTracker.Update([]Object{person(0.5, 0.5)}, sec(0)); got != nil {
		t.Fatal("nil tracker fired")
	}
	if NewLineTracker(nil) != nil || NewLineTracker([]Line{}) != nil {
		t.Fatal("no lines must give a nil tracker")
	}
	if EncodeLines(nil) != "" || ParseLines("") != nil || ParseLines("{not json") != nil {
		t.Fatal("empty and garbage encodings must be nil")
	}
	round := ParseLines(EncodeLines([]Line{gate}))
	if len(round) != 1 || round[0] != gate {
		t.Fatalf("round trip: %+v", round)
	}
}

func TestLineTrackerBoundsTracks(t *testing.T) {
	tr := NewLineTracker([]Line{gate})
	var objs []Object
	for i := 0; i < maxTracks+30; i++ {
		objs = append(objs, person(0.01+float64(i)*0.005, 0.3))
	}
	tr.Update(objs, sec(0))
	if len(tr.tracks) > maxTracks {
		t.Fatalf("%d tracks", len(tr.tracks))
	}
}

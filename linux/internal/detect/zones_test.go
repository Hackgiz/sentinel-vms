package detect

import (
	"strings"
	"testing"
)

// rowSpans renders the 64×36 grid as, per row, "first-last" of the active
// pixels ("" for an empty row), so hand-computed expectations stay readable.
func rowCounts(z *ZoneSet) [frameH]int {
	var out [frameH]int
	g := z.Grid()
	for y := 0; y < frameH; y++ {
		for x := 0; x < frameW; x++ {
			if g[y*frameW+x] {
				out[y]++
			}
		}
	}
	return out
}

func TestRasterizeQuarterRectangle(t *testing.T) {
	// Pixel centers are (x+.5)/64 and (y+.5)/36. For the top-left quarter
	// [0,0.5)², x+.5 < 32 ⇒ x ≤ 31 (32 columns) and y+.5 < 18 ⇒ y ≤ 17
	// (18 rows): 32×18 = 576 pixels; the 2×2 cells give 16×9 = 144.
	z := NewZoneSet([]Polygon{{{0, 0}, {0.5, 0}, {0.5, 0.5}, {0, 0.5}}})
	rc := rowCounts(z)
	for y := 0; y < frameH; y++ {
		want := 0
		if y <= 17 {
			want = 32
		}
		if rc[y] != want {
			t.Fatalf("row %d: %d active pixels, want %d", y, rc[y], want)
		}
	}
	g := z.Grid()
	if !g[0] || !g[31] || g[32] || !g[17*frameW+31] || g[18*frameW] {
		t.Fatal("corner pixels wrong")
	}
	if z.ActiveCells() != 144 {
		t.Fatalf("active cells %d, want 144", z.ActiveCells())
	}
}

func TestRasterizeTriangle(t *testing.T) {
	// Right triangle (0,0),(1,0),(0,1): a pixel center is inside when
	// (x+.5)/64 + (y+.5)/36 < 1, i.e. x+.5 < 64·(1 − (y+.5)/36).
	//   y=0:  64·(1−0.5/36)  = 63.11 ⇒ x ≤ 62 ⇒ 63 pixels
	//   y=18: 64·(1−18.5/36) = 31.11 ⇒ x ≤ 30 ⇒ 31 pixels
	//   y=35: 64·(1−35.5/36) = 0.89  ⇒ x = 0  ⇒ 1 pixel
	z := NewZoneSet([]Polygon{{{0, 0}, {1, 0}, {0, 1}}})
	rc := rowCounts(z)
	for y, want := range map[int]int{0: 63, 18: 31, 35: 1} {
		if rc[y] != want {
			t.Fatalf("row %d: %d, want %d", y, rc[y], want)
		}
	}
	// Winding must not matter.
	rev := NewZoneSet([]Polygon{{{0, 1}, {1, 0}, {0, 0}}})
	if rowCounts(rev) != rc {
		t.Fatal("reversed winding rasterizes differently")
	}
}

func TestRasterizeUnionAndCells(t *testing.T) {
	// Left strip x<0.25 (16 columns: x+.5<16 ⇒ x ≤ 15) and right strip
	// x>0.75 (x+.5>48 ⇒ x ≥ 48 ⇒ 16 columns), full height: 2×16×36 = 1152
	// pixels; as cells 2×8 columns × 18 rows = 288.
	left := Polygon{{0, 0}, {0.25, 0}, {0.25, 1}, {0, 1}}
	right := Polygon{{0.75, 0}, {1, 0}, {1, 1}, {0.75, 1}}
	z := NewZoneSet([]Polygon{left, right})
	total := 0
	for _, n := range rowCounts(z) {
		total += n
	}
	if total != 1152 {
		t.Fatalf("union pixels %d, want 1152", total)
	}
	if z.ActiveCells() != 288 {
		t.Fatalf("union cells %d, want 288", z.ActiveCells())
	}
	// A sliver smaller than a pixel center spacing activates no pixel.
	if s := NewZoneSet([]Polygon{{{0.001, 0.001}, {0.002, 0.001}, {0.002, 0.002}}}); s.ActiveCells() != 0 {
		t.Fatalf("sliver activated %d cells", s.ActiveCells())
	}
	// No polygons: the whole frame is active.
	var nilSet *ZoneSet
	if NewZoneSet(nil) != nil || nilSet.ActiveCells() != cellsX*cellsY || !nilSet.Contains(0.5, 0.5) {
		t.Fatal("no zones must mean the whole frame")
	}
}

func TestCellActiveWhenAnyPixelIs(t *testing.T) {
	// x < 0.5/64·... pick a strip covering only pixel column 0 of cell 0:
	// x in [0, 1/64): pixel 0's center .5/64 is inside, pixel 1's 1.5/64 is not.
	z := NewZoneSet([]Polygon{{{0, 0}, {1.0 / 64, 0}, {1.0 / 64, 1}, {0, 1}}})
	g := z.Grid()
	if !g[0] || g[1] {
		t.Fatal("pixel column 0 only")
	}
	if z.ActiveCells() != 18 { // cell column 0, every row
		t.Fatalf("cells %d, want 18", z.ActiveCells())
	}
}

func TestEncodeParseZones(t *testing.T) {
	if EncodeZones(nil) != "" || ParseZones("") != nil || ParseZones("garbage") != nil {
		t.Fatal("empty / unreadable zones must be nil")
	}
	polys := []Polygon{{{0, 0}, {0.5, 0}, {0.5, 0.5}, {0, 0.5}}}
	enc := EncodeZones(polys)
	if enc != EncodeZones(polys) {
		t.Fatal("encoding must be deterministic (Source is compared with !=)")
	}
	z := ParseZones(enc)
	if z == nil || z.ActiveCells() != 144 {
		t.Fatalf("round trip lost the zone: %+v", z)
	}
}

// frames builds two flat grayscale frames, the second brighter by delta in
// the pixel rectangle [x0,x1)×[y0,y1).
func framePair(x0, y0, x1, y1 int, delta byte) (a, b []byte) {
	a = make([]byte, frameW*frameH)
	b = make([]byte, frameW*frameH)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			b[y*frameW+x] = delta
		}
	}
	return
}

func TestScoreZonesGatesMotion(t *testing.T) {
	// Motion over the bottom-right 16×9 cells (pixels 32..63 × 18..35).
	a, b := framePair(32, 18, 64, 36, 100)
	if got := Score(a, b); got != 144.0/576 {
		t.Fatalf("ungated score %v", got)
	}
	if got := ScoreZones(a, b, nil); got != Score(a, b) {
		t.Fatalf("nil zones must equal Score: %v", got)
	}
	// Zone = the opposite (top-left) quarter: the motion is outside, the
	// score is zero and can never reach a threshold.
	topLeft := NewZoneSet([]Polygon{{{0, 0}, {0.5, 0}, {0.5, 0.5}, {0, 0.5}}})
	if got := ScoreZones(a, b, topLeft); got != 0 {
		t.Fatalf("motion outside the zone scored %v", got)
	}
	// Zone = the bottom-right quarter: every active cell changed ⇒ 1.0.
	botRight := NewZoneSet([]Polygon{{{0.5, 0.5}, {1, 0.5}, {1, 1}, {0.5, 1}}})
	if got := ScoreZones(a, b, botRight); got != 1 {
		t.Fatalf("motion filling the zone scored %v, want 1", got)
	}
	// Zone = bottom half: 288 active cells, 144 changed ⇒ 0.5.
	bottom := NewZoneSet([]Polygon{{{0, 0.5}, {1, 0.5}, {1, 1}, {0, 1}}})
	if got := ScoreZones(a, b, bottom); got != 0.5 {
		t.Fatalf("half-filled zone scored %v, want 0.5", got)
	}
	// A zone smaller than a pixel: no active cells, no motion.
	tiny := NewZoneSet([]Polygon{{{0.001, 0.001}, {0.002, 0.001}, {0.002, 0.002}}})
	if got := ScoreZones(a, b, tiny); got != 0 {
		t.Fatalf("empty zone scored %v", got)
	}
}

func TestKeepObjectsByCenter(t *testing.T) {
	z := NewZoneSet([]Polygon{{{0, 0}, {0.5, 0}, {0.5, 0.5}, {0, 0.5}}})
	in := Object{Kind: "Person", Box: [4]float64{0.1, 0.1, 0.3, 0.3}}       // center .2,.2
	straddle := Object{Kind: "Person", Box: [4]float64{0.3, 0.3, 0.9, 0.6}} // center .6,.45: outside (x)
	edge := Object{Kind: "Person", Box: [4]float64{0.4, 0.1, 0.7, 0.3}}     // center .55: outside
	out := Object{Kind: "Vehicle", Box: [4]float64{0.6, 0.6, 0.9, 0.9}}
	got := z.KeepObjects([]Object{in, straddle, edge, out})
	if len(got) != 1 || got[0] != in {
		t.Fatalf("kept %+v", got)
	}
	if all := (*ZoneSet)(nil).KeepObjects([]Object{in, out}); len(all) != 2 {
		t.Fatal("no zones must keep every object")
	}
	if !strings.Contains(EncodeZones([]Polygon{{{0, 0}, {1, 0}, {0, 1}}}), "[[0,0],[1,0],[0,1]]") {
		t.Fatal("encoding shape changed")
	}
}

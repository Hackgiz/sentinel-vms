package detect

import "encoding/json"

// Detection zones (WAVE7-FEATURES.md, Stream A). The union of a camera's
// enabled "detect" polygons is rasterized once onto the 64×36 motion frame
// when a reader starts (a zone change restarts the reader through
// Source.Zones, so the cache is always current). No polygons = the whole
// frame is active, as before zones existed.

// Polygon is a zone outline, normalized 0..1 against the full frame.
type Polygon [][2]float64

// ZoneSet is the rasterized union of a camera's detect polygons.
type ZoneSet struct {
	polys []Polygon
	// px marks the 64×36 frame pixels whose centers fall inside the union;
	// cells marks the 32×18 motion cells (2×2 pixels) with any active pixel.
	px          [frameW * frameH]bool
	cells       [cellsX * cellsY]bool
	activeCells int
}

// EncodeZones serializes polygons for Source.Zones (a comparable string, so
// Sync restarts a reader whose zones changed). nil/empty gives "".
func EncodeZones(polys []Polygon) string {
	if len(polys) == 0 {
		return ""
	}
	b, _ := json.Marshal(polys)
	return string(b)
}

// ParseZones is the inverse of EncodeZones. "" (or an unreadable value)
// returns nil, meaning the whole frame is active.
func ParseZones(s string) *ZoneSet {
	if s == "" {
		return nil
	}
	var polys []Polygon
	if json.Unmarshal([]byte(s), &polys) != nil {
		return nil
	}
	return NewZoneSet(polys)
}

// NewZoneSet rasterizes the union of polys. No polygons returns nil.
func NewZoneSet(polys []Polygon) *ZoneSet {
	if len(polys) == 0 {
		return nil
	}
	z := &ZoneSet{polys: polys}
	for y := 0; y < frameH; y++ {
		for x := 0; x < frameW; x++ {
			// Sample at the pixel center.
			if z.Contains((float64(x)+0.5)/frameW, (float64(y)+0.5)/frameH) {
				z.px[y*frameW+x] = true
			}
		}
	}
	for cy := 0; cy < cellsY; cy++ {
		for cx := 0; cx < cellsX; cx++ {
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					if z.px[(cy*2+dy)*frameW+cx*2+dx] {
						z.cells[cy*cellsX+cx] = true
					}
				}
			}
			if z.cells[cy*cellsX+cx] {
				z.activeCells++
			}
		}
	}
	return z
}

// Grid returns the 64×36 pixel mask, row-major, true = inside the union.
func (z *ZoneSet) Grid() []bool {
	if z == nil {
		return nil
	}
	return append([]bool(nil), z.px[:]...)
}

// ActiveCells is how many of the 32×18 motion cells can fire.
func (z *ZoneSet) ActiveCells() int {
	if z == nil {
		return cellsX * cellsY
	}
	return z.activeCells
}

// Contains reports whether the normalized point is inside the union (even-odd
// ray casting; zone polygons are validated simple). A nil set contains
// everything.
func (z *ZoneSet) Contains(x, y float64) bool {
	if z == nil {
		return true
	}
	for _, p := range z.polys {
		if pointInPolygon(p, x, y) {
			return true
		}
	}
	return false
}

// KeepObjects drops detections whose box center is outside the union.
func (z *ZoneSet) KeepObjects(objs []Object) []Object {
	if z == nil {
		return objs
	}
	var out []Object
	for _, o := range objs {
		if z.Contains((o.Box[0]+o.Box[2])/2, (o.Box[1]+o.Box[3])/2) {
			out = append(out, o)
		}
	}
	return out
}

func pointInPolygon(p Polygon, x, y float64) bool {
	in := false
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		if (p[i][1] > y) != (p[j][1] > y) &&
			x < (p[j][0]-p[i][0])*(y-p[i][1])/(p[j][1]-p[i][1])+p[i][0] {
			in = !in
		}
	}
	return in
}

// ScoreZones is Score restricted to the zone set: cells outside the union
// are zeroed (never count as changed) and the score is the changed fraction
// of the ACTIVE cells, so a small zone still reaches the sensitivity
// threshold when most of it moves. nil zones = Score.
func ScoreZones(a, b []byte, z *ZoneSet) float64 {
	if z == nil {
		return Score(a, b)
	}
	if z.activeCells == 0 {
		return 0
	}
	changed := 0
	for cy := 0; cy < cellsY; cy++ {
		for cx := 0; cx < cellsX; cx++ {
			if z.cells[cy*cellsX+cx] && cellChanged(a, b, cx, cy) {
				changed++
			}
		}
	}
	return float64(changed) / float64(z.activeCells)
}

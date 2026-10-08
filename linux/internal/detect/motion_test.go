package detect

import "testing"

func TestScore(t *testing.T) {
	a := make([]byte, frameW*frameH)
	b := make([]byte, frameW*frameH)
	if s := Score(a, b); s != 0 {
		t.Fatalf("static scene scored %v", s)
	}
	// Brighten a 16×12-pixel block (8×6 = 48 cells) by 40 levels.
	for y := 0; y < 12; y++ {
		for x := 0; x < 16; x++ {
			b[y*frameW+x] = 40
		}
	}
	want := 48.0 / float64(cellsX*cellsY)
	if s := Score(a, b); s != want {
		t.Fatalf("score %v, want %v", s, want)
	}
	// Sensor noise below the per-cell delta must not count.
	for i := range b {
		b[i] = 10
	}
	if s := Score(a, b); s != 0 {
		t.Fatalf("noise scored %v", s)
	}
}

package recorder

import (
	"testing"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
)

func TestDetectLinesPicksOnlyLines(t *testing.T) {
	zones := []link.Zone{
		{Kind: link.ZoneDetect, Points: [][2]float64{{0, 0}, {1, 0}, {1, 1}}},
		{Kind: link.ZoneMask, Points: [][2]float64{{0, 0}, {0.5, 0}, {0.5, 0.5}}},
		{Kind: link.ZoneLine, Name: "Gate", Direction: "ltr", Points: [][2]float64{{0.2, 0.5}, {0.8, 0.5}}},
		{Kind: link.ZoneLine, Points: [][2]float64{{0.1, 0.1}, {0.1, 0.9}}}, // unnamed, no direction
		{Kind: link.ZoneLine, Name: "Broken", Points: [][2]float64{{0.1, 0.1}}},
	}
	lines := detect.ParseLines(detectLines(zones))
	if len(lines) != 2 || lines[0].Name != "Gate" || lines[0].Direction != "ltr" || lines[0].P1 != [2]float64{0.8, 0.5} || lines[1].Name != "Line" {
		t.Fatalf("lines %+v", lines)
	}
	if detectLines(nil) != "" || detectLines(zones[:2]) != "" {
		t.Fatal("no lines must encode to empty")
	}
	// Lines never gate detection or mask pictures.
	if got := detect.ParseZones(detectZones(zones)); got == nil || !got.Contains(0.9, 0.1) {
		t.Fatalf("detect zones were disturbed by lines")
	}
	if rects := len(media.MaskRects(zones)); rects != 1 {
		t.Fatalf("%d mask rects, want 1", rects)
	}
}

package playback

import (
	"fmt"
	"math"
)

// Limits for pixelated export regions.
const (
	MaxBlurRegions = 8
	// MinRegionSize is the smallest width/height (normalized) of a region:
	// anything smaller would pixelate less than one block on typical video.
	MinRegionSize = 0.01
)

// ValidateRegions checks normalized {x0, y0, x1, y1} regions for an export
// (finite, inside the frame, x0<x1, y0<y1, at least MinRegionSize wide and
// tall, at most MaxBlurRegions) and returns them unchanged.
func ValidateRegions(in [][4]float64) ([][4]float64, error) {
	if len(in) > MaxBlurRegions {
		return nil, fmt.Errorf("at most %d regions can be pixelated", MaxBlurRegions)
	}
	for i, r := range in {
		for _, v := range r {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
				return nil, fmt.Errorf("region %d must be inside the frame (0 to 1)", i+1)
			}
		}
		if r[2]-r[0] < MinRegionSize || r[3]-r[1] < MinRegionSize {
			return nil, fmt.Errorf("region %d is too small", i+1)
		}
	}
	return in, nil
}

package talkback

import "math"

// Halver downsamples 16 kHz PCM to 8 kHz: a low-pass FIR (cut-off just
// under 4 kHz, so nothing above the new Nyquist folds back into the voice
// band) followed by keeping every second sample. It is streaming: state
// carries over between Process calls, so chunk boundaries don't matter.
type Halver struct {
	hist  [len(halfbandTaps) - 1]float64 // the last inputs, oldest first
	phase int                            // 0: the next output is due at the next input
}

// halfbandTaps: a 31-tap windowed-sinc (Blackman) half-band low-pass,
// normalized to unity gain at DC. Every second tap but the centre is zero,
// which is what makes it half-band.
var halfbandTaps = func() [31]float64 {
	var t [31]float64
	const n = len(t)
	mid := (n - 1) / 2
	sum := 0.0
	for i := range t {
		k := i - mid
		var sinc float64
		if k == 0 {
			sinc = 0.5
		} else if k%2 == 0 {
			sinc = 0
		} else {
			sinc = math.Sin(math.Pi*float64(k)/2) / (math.Pi * float64(k))
		}
		x := float64(i) / float64(n-1)
		w := 0.42 - 0.5*math.Cos(2*math.Pi*x) + 0.08*math.Cos(4*math.Pi*x) // Blackman
		t[i] = sinc * w
		sum += t[i]
	}
	for i := range t {
		t[i] /= sum
	}
	return t
}()

// Process filters and decimates in, appending the 8 kHz samples to dst.
func (h *Halver) Process(dst, in []int16) []int16 {
	const hl = len(halfbandTaps) - 1
	for _, s := range in {
		x := float64(s)
		if h.phase == 0 {
			// y = Σ taps[k]·x[n-k]: taps[0] meets the newest sample.
			acc := halfbandTaps[0] * x
			for k := 1; k <= hl; k++ {
				acc += halfbandTaps[k] * h.hist[hl-k]
			}
			dst = append(dst, clamp16(acc))
		}
		h.phase ^= 1
		copy(h.hist[:], h.hist[1:])
		h.hist[hl-1] = x
	}
	return dst
}

func clamp16(v float64) int16 {
	switch {
	case v >= 32767:
		return 32767
	case v <= -32768:
		return -32768
	case v >= 0:
		return int16(v + 0.5)
	default:
		return int16(v - 0.5)
	}
}

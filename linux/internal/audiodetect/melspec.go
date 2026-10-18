package audiodetect

import "math"

// Log-mel front end with YAMNet's parameters: 16 kHz audio, 25 ms periodic
// Hann window, 10 ms hop, 512-point FFT magnitude, 64 HTK mel bands from
// 125 Hz to 7500 Hz, log(mel + 0.001). A patch is 96 frames (0.96 s).
const (
	MelBands   = 64
	MelFrames  = 96
	melWin     = 400 // 25 ms
	melHop     = 160 // 10 ms
	melFFT     = 512
	melLowHz   = 125.0
	melHighHz  = 7500.0
	melLogBias = 0.001

	// PatchSamples is the audio one patch covers: (96-1)*160 + 400.
	PatchSamples = (MelFrames-1)*melHop + melWin
)

// MelSpectrogram computes log-mel frames. Safe for concurrent use (read-only
// tables, per-call scratch).
type MelSpectrogram struct {
	window  [melWin]float64
	weights [][]float64 // [band][fft bin], sparse enough not to matter
	cos     [melFFT / 2]float64
	sin     [melFFT / 2]float64
	rev     [melFFT]int
}

func hzToMel(f float64) float64 { return 1127.0 * math.Log(1.0+f/700.0) }

// NewMelSpectrogram builds the window, filterbank and FFT tables.
func NewMelSpectrogram() *MelSpectrogram {
	m := &MelSpectrogram{}
	for i := range m.window {
		m.window[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/melWin)
	}
	bins := melFFT/2 + 1
	edges := make([]float64, MelBands+2)
	lo, hi := hzToMel(melLowHz), hzToMel(melHighHz)
	for i := range edges {
		edges[i] = lo + (hi-lo)*float64(i)/float64(MelBands+1)
	}
	m.weights = make([][]float64, MelBands)
	for b := 0; b < MelBands; b++ {
		w := make([]float64, bins)
		for k := 1; k < bins; k++ { // bin 0 (DC) stays zero
			mel := hzToMel(float64(k) * (SampleRate / 2.0) / float64(bins-1))
			lower := (mel - edges[b]) / (edges[b+1] - edges[b])
			upper := (edges[b+2] - mel) / (edges[b+2] - edges[b+1])
			w[k] = math.Max(0, math.Min(lower, upper))
		}
		m.weights[b] = w
	}
	for i := 0; i < melFFT/2; i++ {
		a := -2 * math.Pi * float64(i) / melFFT
		m.cos[i], m.sin[i] = math.Cos(a), math.Sin(a)
	}
	bitsN := 0
	for 1<<bitsN < melFFT {
		bitsN++
	}
	for i := range m.rev {
		r := 0
		for b := 0; b < bitsN; b++ {
			if i&(1<<b) != 0 {
				r |= 1 << (bitsN - 1 - b)
			}
		}
		m.rev[i] = r
	}
	return m
}

// Frames returns the log-mel spectrogram of x, row-major [frame][MelBands];
// only whole frames (no padding): 1 + (len-400)/160 of them.
func (m *MelSpectrogram) Frames(x []float32) []float32 {
	if len(x) < melWin {
		return nil
	}
	n := 1 + (len(x)-melWin)/melHop
	out := make([]float32, 0, n*MelBands)
	re := make([]float64, melFFT)
	im := make([]float64, melFFT)
	mag := make([]float64, melFFT/2+1)
	for f := 0; f < n; f++ {
		off := f * melHop
		for i := 0; i < melFFT; i++ {
			re[i], im[i] = 0, 0
		}
		for i := 0; i < melWin; i++ {
			re[m.rev[i]] = float64(x[off+i]) * m.window[i]
		}
		m.fft(re, im)
		for k := range mag {
			mag[k] = math.Hypot(re[k], im[k])
		}
		for b := 0; b < MelBands; b++ {
			var s float64
			for k, w := range m.weights[b] {
				if w != 0 {
					s += w * mag[k]
				}
			}
			out = append(out, float32(math.Log(s+melLogBias)))
		}
	}
	return out
}

// Patch is Frames of exactly PatchSamples samples: MelFrames*MelBands values.
func (m *MelSpectrogram) Patch(x []float32) []float32 {
	if len(x) > PatchSamples {
		x = x[len(x)-PatchSamples:]
	}
	return m.Frames(x)
}

// fft is an in-place radix-2 transform; the input is already bit-reversed.
func (m *MelSpectrogram) fft(re, im []float64) {
	for size := 2; size <= melFFT; size <<= 1 {
		half, step := size/2, melFFT/size
		for start := 0; start < melFFT; start += size {
			for j := 0; j < half; j++ {
				c, s := m.cos[j*step], m.sin[j*step]
				a, b := start+j, start+j+half
				tr := re[b]*c - im[b]*s
				ti := re[b]*s + im[b]*c
				re[b], im[b] = re[a]-tr, im[a]-ti
				re[a], im[a] = re[a]+tr, im[a]+ti
			}
		}
	}
}

package audiodetect

import (
	"math"
	"testing"
)

// Expected values come from an independent pure-Python reference (naive DFT,
// YAMNet parameters) run on the same two-tone signal.
func twoTone(n int) []float32 {
	x := make([]float32, n)
	for i := range x {
		x[i] = float32(0.5*math.Sin(2*math.Pi*1000*float64(i)/SampleRate) + 0.25*math.Sin(2*math.Pi*3000*float64(i)/SampleRate))
	}
	return x
}

func TestMelPatchMatchesReference(t *testing.T) {
	m := NewMelSpectrogram()
	p := m.Patch(twoTone(PatchSamples))
	if len(p) != MelFrames*MelBands {
		t.Fatalf("patch has %d values, want %d", len(p), MelFrames*MelBands)
	}
	want := map[int]float64{0: -6.40127, 5: -5.53455, 10: -4.46715, 20: 4.08183, 31: -5.78138, 40: -0.93373, 50: -6.26141, 63: -6.84029}
	for _, frame := range []int{0, 47, 95} {
		for band, w := range want {
			if got := float64(p[frame*MelBands+band]); math.Abs(got-w) > 2e-3 {
				t.Errorf("frame %d band %d = %.5f, want %.5f", frame, band, got, w)
			}
		}
	}
}

func TestMelFilterbankWeights(t *testing.T) {
	m := NewMelSpectrogram()
	want := map[int]float64{0: 0.947690465, 10: 1.27235411, 31: 2.889027796, 63: 8.944183294}
	for b, w := range want {
		var s float64
		for _, v := range m.weights[b] {
			s += v
		}
		if math.Abs(s-w) > 1e-6 {
			t.Errorf("band %d weight sum = %.9f, want %.9f", b, s, w)
		}
	}
	for b := range m.weights {
		if m.weights[b][0] != 0 {
			t.Fatalf("band %d weights the DC bin", b)
		}
	}
}

func TestMelSilenceAndShapes(t *testing.T) {
	m := NewMelSpectrogram()
	p := m.Patch(make([]float32, PatchSamples))
	for i, v := range p {
		if math.Abs(float64(v)-math.Log(0.001)) > 1e-6 {
			t.Fatalf("silence value %d = %v, want log(0.001)", i, v)
		}
	}
	if got := len(m.Frames(make([]float32, 399))); got != 0 {
		t.Fatalf("short input gave %d values", got)
	}
	if got := len(m.Frames(make([]float32, 400+160*3))) / MelBands; got != 4 {
		t.Fatalf("frames = %d, want 4", got)
	}
	if PatchSamples != 15600 {
		t.Fatalf("PatchSamples = %d", PatchSamples)
	}
}

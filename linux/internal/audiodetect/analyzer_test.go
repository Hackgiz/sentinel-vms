package audiodetect

import (
	"errors"
	"math"
	"math/rand"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// noise returns n samples of uniform noise at the given RMS (linear).
func noise(r *rand.Rand, n int, rms float64) []float32 {
	x := make([]float32, n)
	amp := rms * math.Sqrt(3) // uniform(-a,a) has rms a/sqrt3
	for i := range x {
		x[i] = float32((r.Float64()*2 - 1) * amp)
	}
	return x
}

func dbToRMS(db float64) float64 { return math.Pow(10, db/20) }

func seconds(r *rand.Rand, s float64, db float64) []float32 {
	return noise(r, int(s*SampleRate), dbToRMS(db))
}

func TestRMSdBFS(t *testing.T) {
	if got := RMSdBFS(make([]float32, 100)); got != SilenceDB {
		t.Fatalf("silence = %v", got)
	}
	if got := RMSdBFS(nil); got != SilenceDB {
		t.Fatalf("empty = %v", got)
	}
	sq := make([]float32, 1000)
	for i := range sq {
		sq[i] = 1
	}
	if got := RMSdBFS(sq); math.Abs(got) > 1e-9 {
		t.Fatalf("full-scale square = %v dBFS, want 0", got)
	}
	for i := range sq {
		sq[i] = 0.1
	}
	if got := RMSdBFS(sq); math.Abs(got+20) > 1e-6 {
		t.Fatalf("0.1 DC = %v dBFS, want -20", got)
	}
	sine := make([]float32, SampleRate)
	for i := range sine {
		sine[i] = float32(math.Sin(2 * math.Pi * 440 * float64(i) / SampleRate))
	}
	if got := RMSdBFS(sine); math.Abs(got+3.0103) > 1e-3 {
		t.Fatalf("full-scale sine = %v dBFS, want -3.01", got)
	}
}

func TestNoiseFloorPercentileAndWarmup(t *testing.T) {
	var f NoiseFloor
	if f.Ready() || f.Floor() != MinFloorDB {
		t.Fatal("empty floor must be unready at the minimum")
	}
	for i := 0; i < floorWarmup-1; i++ {
		f.Add(-50)
	}
	if f.Ready() {
		t.Fatal("ready too early")
	}
	f.Add(-50)
	if !f.Ready() || f.Floor() != -50 {
		t.Fatalf("floor = %v ready=%v", f.Floor(), f.Ready())
	}
	// A burst of loud windows (less than the percentile) does not lift it.
	for i := 0; i < 8; i++ {
		f.Add(-10)
	}
	if f.Floor() != -50 {
		t.Fatalf("burst lifted the floor to %v", f.Floor())
	}
	// Digital silence clamps.
	var s NoiseFloor
	for i := 0; i < floorWarmup; i++ {
		s.Add(SilenceDB)
	}
	if s.Floor() != MinFloorDB {
		t.Fatalf("silence floor = %v, want %v", s.Floor(), MinFloorDB)
	}
	// The ring forgets: after 120 loud windows the floor follows.
	for i := 0; i < floorWindows; i++ {
		f.Add(-30)
	}
	if f.Floor() != -30 {
		t.Fatalf("floor did not adapt: %v", f.Floor())
	}
}

func feedAll(a *Analyzer, chunks ...[]float32) []Event {
	var out []Event
	for _, c := range chunks {
		out = append(out, a.Feed(c)...)
	}
	return out
}

func TestBaselineFiresAfterTwoWindowsOverThreshold(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	a := &Analyzer{CameraID: "cam", Start: t0}
	// 15 s of room noise at -60 dBFS: learns the floor, no events.
	if ev := feedAll(a, seconds(r, 15, -60)); len(ev) != 0 {
		t.Fatalf("quiet room fired: %+v", ev)
	}
	// A 3 s burst at -30 dBFS (30 dB over the floor, threshold 25).
	ev := feedAll(a, seconds(r, 3, -30), seconds(r, 3, -60))
	if len(ev) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(ev), ev)
	}
	e := ev[0]
	if e.Kind != "audio" || e.Label != BaselineLabel || e.CameraID != "cam" {
		t.Fatalf("event = %+v", e)
	}
	if e.DB < -35 || e.Floor > -55 || e.Floor < -65 {
		t.Fatalf("levels: db %v floor %v", e.DB, e.Floor)
	}
	// It fired on the second over-threshold window, i.e. between 15.5 and 17 s
	// of stream time (burst starts at 15 s; a window needs most of its energy
	// from the burst, so not before the second hop after it).
	if d := e.At.Sub(t0); d < 16*time.Second || d > 18*time.Second {
		t.Fatalf("fired at %v", d)
	}
}

func TestBaselineSingleWindowBlipIgnored(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	a := &Analyzer{Start: t0}
	feedAll(a, seconds(r, 15, -60))
	// A 0.2 s click raises only windows whose RMS is diluted: it reads about
	// 7 dB over the floor, far below the threshold, and never fires.
	ev := feedAll(a, seconds(r, 0.2, -30), seconds(r, 6, -60))
	if len(ev) != 0 {
		t.Fatalf("click fired: %+v", ev)
	}
}

func TestBaselineThresholdPerCamera(t *testing.T) {
	burst := func(thr float64) int {
		r := rand.New(rand.NewSource(3))
		a := &Analyzer{Start: t0, ThresholdDB: thr}
		return len(feedAll(a, seconds(r, 15, -60), seconds(r, 4, -45), seconds(r, 2, -60))) // 15 dB over
	}
	if n := burst(0); n != 0 { // default 25 dB
		t.Fatalf("default threshold fired on +15 dB: %d", n)
	}
	if n := burst(10); n != 1 {
		t.Fatalf("10 dB threshold events = %d, want 1", n)
	}
	if n := burst(20); n != 0 {
		t.Fatalf("20 dB threshold fired on +15 dB: %d", n)
	}
}

func TestBaselineDebounce30s(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	a := &Analyzer{Start: t0}
	feedAll(a, seconds(r, 15, -60))
	var all []Event
	// Bursts every 10 s for 60 s: only events >= 30 s apart count.
	for i := 0; i < 6; i++ {
		all = append(all, feedAll(a, seconds(r, 3, -30), seconds(r, 7, -60))...)
	}
	if len(all) != 2 {
		t.Fatalf("got %d events over 60 s of bursts, want 2: %+v", len(all), all)
	}
	if gap := all[1].At.Sub(all[0].At); gap < Debounce {
		t.Fatalf("events %v apart, debounce is %v", gap, Debounce)
	}
}

func TestNoEventDuringWarmup(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	a := &Analyzer{Start: t0}
	// Loud from the very first second: nothing can be measured against.
	if ev := feedAll(a, seconds(r, 8, -20)); len(ev) != 0 {
		t.Fatalf("fired during warm-up: %+v", ev)
	}
}

func TestChunkSizeDoesNotMatter(t *testing.T) {
	r := rand.New(rand.NewSource(6))
	sig := append(append(seconds(r, 15, -60), seconds(r, 3, -30)...), seconds(r, 3, -60)...)
	whole := feedAll(&Analyzer{Start: t0}, sig)
	var split []Event
	a := &Analyzer{Start: t0}
	for i := 0; i < len(sig); i += 777 {
		end := i + 777
		if end > len(sig) {
			end = len(sig)
		}
		split = append(split, a.Feed(sig[i:end])...)
	}
	if len(whole) != 1 || len(split) != 1 || !whole[0].At.Equal(split[0].At) {
		t.Fatalf("whole %+v vs split %+v", whole, split)
	}
}

// fakeClassifier answers from a script keyed by call number.
type fakeClassifier struct {
	calls int
	reply func(call int) ([]ClassHit, error)
}

func (f *fakeClassifier) Classify([]float32) ([]ClassHit, error) {
	f.calls++
	return f.reply(f.calls)
}

func TestClassifierLabelReplacesBaseline(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	fc := &fakeClassifier{reply: func(int) ([]ClassHit, error) {
		return []ClassHit{{LabelAlarm, 0.6}, {LabelGlass, 0.9}}, nil
	}}
	a := &Analyzer{Start: t0, Classifier: fc}
	// The first 10 s are warm-up: classifier runs (floor not ready) but
	// cannot fire.
	if ev := feedAll(a, seconds(r, 15, -60)); len(ev) != 0 {
		t.Fatalf("classifier fired in a quiet room: %+v", ev)
	}
	ev := feedAll(a, seconds(r, 3, -30), seconds(r, 3, -60))
	if len(ev) != 1 || ev[0].Label != LabelGlass || ev[0].Score != 0.9 {
		t.Fatalf("events = %+v", ev)
	}
}

func TestClassifierOnlyRunsOnAudibleWindows(t *testing.T) {
	r := rand.New(rand.NewSource(8))
	fc := &fakeClassifier{reply: func(int) ([]ClassHit, error) { return nil, nil }}
	a := &Analyzer{Start: t0, Classifier: fc}
	feedAll(a, seconds(r, 15, -60))
	warm := fc.calls
	if warm == 0 || warm > floorWarmup+1 {
		t.Fatalf("classifier ran %d times during warm-up", warm)
	}
	feedAll(a, seconds(r, 20, -60))
	if fc.calls != warm {
		t.Fatalf("classifier ran on silence: %d -> %d", warm, fc.calls)
	}
	feedAll(a, seconds(r, 2, -30))
	if fc.calls == warm {
		t.Fatal("classifier skipped a loud window")
	}
}

func TestClassifierHitFiresWithoutBaselineAndSharesDebounce(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	fc := &fakeClassifier{reply: func(call int) ([]ClassHit, error) {
		return []ClassHit{{LabelScream, 0.8}}, nil
	}}
	a := &Analyzer{Start: t0, ThresholdDB: 40, Classifier: fc}
	feedAll(a, seconds(r, 15, -60))
	// +15 dB: below the baseline threshold but audible to the classifier.
	ev := feedAll(a, seconds(r, 4, -45), seconds(r, 4, -60))
	if len(ev) != 1 || ev[0].Label != LabelScream {
		t.Fatalf("events = %+v", ev)
	}
	// Another hit 10 s later is debounced.
	ev = feedAll(a, seconds(r, 3, -45), seconds(r, 3, -60))
	if len(ev) != 0 {
		t.Fatalf("debounce failed: %+v", ev)
	}
}

func TestClassifierErrorDegradesToBaseline(t *testing.T) {
	r := rand.New(rand.NewSource(10))
	fc := &fakeClassifier{reply: func(int) ([]ClassHit, error) { return nil, errors.New("boom") }}
	a := &Analyzer{Start: t0, Classifier: fc}
	feedAll(a, seconds(r, 15, -60))
	if a.Classifier != nil || a.ClassifierErr == nil {
		t.Fatalf("classifier not dropped: %v %v", a.Classifier, a.ClassifierErr)
	}
	ev := feedAll(a, seconds(r, 3, -30), seconds(r, 3, -60))
	if len(ev) != 1 || ev[0].Label != BaselineLabel {
		t.Fatalf("baseline did not take over: %+v", ev)
	}
}

func TestFFmpegArgs(t *testing.T) {
	got := FFmpegArgs("rtsp://x/y")
	want := []string{"-vn", "-f", "f32le", "-ac", "1", "-ar", "16000", "-"}
	tail := got[len(got)-len(want):]
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("args tail = %v", tail)
		}
	}
}

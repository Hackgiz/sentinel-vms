package detect

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestTrackerAssignsStableIDsAndReportsEndedTracks(t *testing.T) {
	tr := NewTracker()
	in := []Object{person(0.3, 0.5), vehicle(0.7, 0.5)}
	got, ended := tr.Update(in, sec(0))
	if len(got) != 2 || got[0].TrackID != 1 || got[1].TrackID != 2 || len(ended) != 0 {
		t.Fatalf("first pass: %+v ended %v", got, ended)
	}
	if in[0].TrackID != 0 {
		t.Fatal("Update must not modify its input")
	}
	// Both move a little: same ids, whatever the order they arrive in.
	got, _ = tr.Update([]Object{vehicle(0.72, 0.5), person(0.32, 0.52)}, sec(1))
	if got[0].TrackID != 2 || got[1].TrackID != 1 {
		t.Fatalf("second pass ids %d, %d", got[0].TrackID, got[1].TrackID)
	}
	if tk, ok := tr.Track(1); !ok || !tk.FirstSeen.Equal(sec(0)) || !tk.LastSeen.Equal(sec(1)) || tk.Duration() != time.Second {
		t.Fatalf("track 1 = %+v %v", tk, ok)
	}
	// The vehicle leaves; past the TTL its track ends (reported once).
	tr.Update([]Object{person(0.34, 0.54)}, sec(2))
	tr.Update([]Object{person(0.36, 0.56)}, sec(4))
	got, ended = tr.Update([]Object{person(0.38, 0.58)}, sec(6))
	if len(ended) != 1 || ended[0].ID != 2 || ended[0].Kind != "Vehicle" || !ended[0].LastSeen.Equal(sec(1)) {
		t.Fatalf("ended %+v", ended)
	}
	if got[0].TrackID != 1 || tr.Len() != 1 {
		t.Fatalf("person lost its track: %+v, %d live", got, tr.Len())
	}
	// A returning vehicle is a new track: ids are never reused.
	got, _ = tr.Update([]Object{person(0.4, 0.6), vehicle(0.7, 0.5)}, sec(7))
	if got[1].TrackID != 3 {
		t.Fatalf("new vehicle id %d, want 3", got[1].TrackID)
	}
	var nilTracker *Tracker
	if out, ended := nilTracker.Update(in, sec(0)); len(out) != 2 || ended != nil || nilTracker.Len() != 0 {
		t.Fatal("nil tracker")
	}
}

func TestTrackerBoundsTracks(t *testing.T) {
	tr := newTrackerFrom(1000)
	var objs []Object
	for i := 0; i < maxTracks+10; i++ {
		objs = append(objs, person(0.01+float64(i)*0.005, 0.3))
	}
	got, _ := tr.Update(objs, sec(0))
	untracked := 0
	for _, o := range got {
		if o.TrackID == 0 {
			untracked++
		}
	}
	if tr.Len() != maxTracks || untracked != 10 || got[0].TrackID != 1001 {
		t.Fatalf("%d tracks, %d untracked, first id %d", tr.Len(), untracked, got[0].TrackID)
	}
}

// The LineTracker reads ids set by the camera's Tracker: crossings follow
// those ids, not its own matching.
func TestLineTrackerConsumesTrackerIDs(t *testing.T) {
	tr, lt := newTrackerFrom(41), NewLineTracker([]Line{gate})
	objs, _ := tr.Update([]Object{person(0.5, 0.4)}, sec(0))
	lt.Update(objs, sec(0))
	objs, _ = tr.Update([]Object{person(0.5, 0.6)}, sec(1))
	got := lt.Update(objs, sec(1))
	if len(got) != 1 || got[0].TrackID != 42 || got[0].Object.TrackID != 42 {
		t.Fatalf("crossing %+v", got)
	}
	// An id the Tracker gave a different object starts over: no crossing
	// even though a box with that id is now on the other side.
	o := person(0.5, 0.4)
	o.TrackID = 99
	if got := lt.Update([]Object{o}, sec(2)); len(got) != 0 {
		t.Fatalf("new id crossed: %+v", got)
	}
}

func TestRateResolution(t *testing.T) {
	for _, c := range []struct {
		configured int
		engine     string
		want       int
	}{
		{0, EngineCPU, 1}, {0, "", 1}, {0, EngineOpenVINO, 5}, {0, EngineCoreML, 5},
		{3, EngineCPU, 3}, {9, EngineCPU, 5}, {-2, EngineOpenVINO, 5}, {1, EngineCoreML, 1},
	} {
		if got := ClampRate(c.configured, c.engine); got != c.want {
			t.Errorf("ClampRate(%d, %q) = %d, want %d", c.configured, c.engine, got, c.want)
		}
	}
	var m *Manager
	if m.EffectiveRate("cam") != 1 {
		t.Fatal("nil manager: auto on no detector is 1")
	}
	m = &Manager{Rate: func(id string) int {
		if id == "fast" {
			return 4
		}
		return 0
	}}
	if m.EffectiveRate("fast") != 4 || m.EffectiveRate("cam") != 1 {
		t.Fatalf("effective %d %d", m.EffectiveRate("fast"), m.EffectiveRate("cam"))
	}
	if rateInterval(5) != 200*time.Millisecond || rateInterval(1) != time.Second || rateInterval(0) != time.Second {
		t.Fatal("intervals")
	}
}

// detectRateArgs rewrites the graph detectArgs builds. If the graph's shape
// changes, this test fails instead of the rate silently capping at 2/s.
func TestDetectRateArgsRaisesOnlyTheDetectionBranch(t *testing.T) {
	args := detectArgs("rtsp://127.0.0.1:8554/cam", DecodePlan{Method: DecodeSoftware}, true)
	for _, rate := range []int{1, 2} {
		if got := detectRateArgs(args, rate); strings.Join(got, " ") != strings.Join(args, " ") {
			t.Fatalf("rate %d changed the args: %v", rate, got)
		}
	}
	got := detectRateArgs(args, 5)
	var graph string
	for i, a := range got {
		if i > 0 && got[i-1] == "-filter_complex" {
			graph = a
		}
	}
	if !strings.HasPrefix(graph, "[0:v]fps=5,split=2[m][d];[m]fps="+sampleFPS+",") {
		t.Fatalf("graph %q", graph)
	}
	if strings.Join(args, " ") == strings.Join(got, " ") {
		t.Fatal("input args were modified in place or not rewritten")
	}
	motionOnly := detectArgs("rtsp://x", DecodePlan{Method: DecodeSoftware}, false)
	if strings.Join(detectRateArgs(motionOnly, 5), " ") != strings.Join(motionOnly, " ") {
		t.Fatal("motion-only args changed")
	}
}

// Birds are Animals with a 0.6 bar; other animals keep 0.5.
func TestDecodeBird(t *testing.T) {
	d := newObjectDetector(nil, DefaultSpec(), 1)
	const stride = 85
	cell := 26*(InputSize/8) + 26 // a stride-8 cell in the middle
	out := func(class int, prob float32) []float32 {
		o := make([]float32, len(d.grids)*stride)
		row := o[cell*stride : (cell+1)*stride]
		row[2], row[3] = float32(math.Log(10)), float32(math.Log(10)) // 80×80 px
		row[4], row[5+class] = 0.9, prob
		return o
	}
	if got := d.decode(out(14, 0.6), stride, InputSize, InputSize); len(got) != 0 {
		t.Fatalf("bird at 0.54 kept: %+v", got)
	}
	got := d.decode(out(14, 0.75), stride, InputSize, InputSize)
	if len(got) != 1 || got[0].Kind != "Animal" || got[0].Label != "bird" || got[0].Score != 0.67 {
		t.Fatalf("bird at 0.675: %+v", got)
	}
	if got := d.decode(out(15, 0.6), stride, InputSize, InputSize); len(got) != 1 || got[0].Label != "cat" {
		t.Fatalf("cat at 0.54: %+v", got)
	}
}

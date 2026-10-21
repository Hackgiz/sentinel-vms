package recorder

import (
	"context"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
)

func setLoiter(t *testing.T, r *Recorder, seconds int) {
	t.Helper()
	if _, err := r.ApplyConfig(context.Background(), link.Snapshot{Rev: 2, Cameras: []link.CameraConfig{
		{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", MotionLevel: 2, LoiterSeconds: seconds},
	}}); err != nil {
		t.Fatal(err)
	}
}

func tracked(kind string, id int, x float64) detect.Object {
	label := map[string]string{"Person": "person", "Vehicle": "car"}[kind]
	return detect.Object{Kind: kind, Label: label, Score: 0.8, Box: [4]float64{x, 0.2, x + 0.1, 0.6}, TrackID: id}
}

func loiterEvents(sink *fakeSink) []*link.Detection {
	var out []*link.Detection
	for _, e := range sink.durable() {
		if e.Detection != nil && e.Detection.Kind == "Loitering" {
			out = append(out, e.Detection)
		}
	}
	return out
}

// A loitering camera raises exactly one Loitering event per track, driven
// by the detection timestamps (a fake clock), however long the person stays.
func TestLoiteringOneEventPerTrack(t *testing.T) {
	r, sink := newTestRecorder(t)
	setLoiter(t, r, 30)
	t0 := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

	// Track 7 stays two minutes at 5 passes a second; track 8 (a car) and
	// track 9 (a person who leaves after 20 s) never loiter.
	for i := 0; i <= 600; i++ {
		s := float64(i) / 5
		objs := []detect.Object{tracked("Person", 7, 0.3), tracked("Vehicle", 8, 0.6)}
		if s <= 20 {
			objs = append(objs, tracked("Person", 9, 0.1))
		}
		r.OnObjects(detect.Detections{CameraID: camID, At: at(s), Objects: objs})
		if s == 29.8 && len(loiterEvents(sink)) != 0 {
			t.Fatal("fired before 30 s")
		}
	}
	got := loiterEvents(sink)
	if len(got) != 1 {
		t.Fatalf("%d loitering events, want 1: %+v", len(got), got)
	}
	d := got[0]
	if d.TrackID != camID+":7" || d.Label != "person" || d.Source != link.SourceObjects || d.At != at(30).UnixMilli() || d.Box == "" {
		t.Fatalf("event %+v", d)
	}

	// A second person who stays: one more event, for their own track.
	for i := 0; i <= 200; i++ {
		s := 121 + float64(i)/5
		r.OnObjects(detect.Detections{CameraID: camID, At: at(s), Objects: []detect.Object{tracked("Person", 7, 0.3), tracked("Person", 10, 0.5)}})
	}
	got = loiterEvents(sink)
	if len(got) != 2 || got[1].TrackID != camID+":10" || got[1].At != at(151).UnixMilli() {
		t.Fatalf("second track: %+v", got)
	}
	// Loitering off: nothing more, and the bookkeeping is dropped.
	setLoiter(t, r, 0)
	r.OnObjects(detect.Detections{CameraID: camID, At: at(400), Objects: []detect.Object{tracked("Person", 11, 0.3)}})
	r.OnObjects(detect.Detections{CameraID: camID, At: at(500), Objects: []detect.Object{tracked("Person", 11, 0.3)}})
	if n := len(loiterEvents(sink)); n != 2 {
		t.Fatalf("loitering off still fired: %d", n)
	}
}

// A track that drops out of detection longer than the tracker's TTL is
// forgotten: the person seen again under the same id (never happens with
// the Tracker, which issues new ids) starts counting from zero.
func TestLoiteringForgetsStaleTracks(t *testing.T) {
	r, sink := newTestRecorder(t)
	setLoiter(t, r, 10)
	t0 := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
	r.OnObjects(detect.Detections{CameraID: camID, At: t0, Objects: []detect.Object{tracked("Person", 3, 0.3)}})
	r.OnObjects(detect.Detections{CameraID: camID, At: t0.Add(12 * time.Second), Objects: []detect.Object{tracked("Person", 3, 0.3)}})
	if n := len(loiterEvents(sink)); n != 0 {
		t.Fatalf("a gap longer than the TTL counted as loitering: %d", n)
	}
	// Untracked people never loiter.
	for s := 0; s <= 20; s++ {
		r.OnObjects(detect.Detections{CameraID: camID, At: t0.Add(time.Duration(30+s) * time.Second), Objects: []detect.Object{tracked("Person", 0, 0.3)}})
	}
	if n := len(loiterEvents(sink)); n != 0 {
		t.Fatalf("untracked person loitered: %d", n)
	}
}

func TestObjectEventsCarryTrackID(t *testing.T) {
	r, sink := newTestRecorder(t)
	t0 := time.Now()
	r.OnObjects(detect.Detections{CameraID: camID, At: t0, Objects: []detect.Object{tracked("Person", 12, 0.3)}})
	r.OnLineCross(detect.LineCrossing{CameraID: camID, Line: detect.Line{Name: "Gate"}, Direction: detect.DirLTR,
		Object: tracked("Person", 12, 0.3), TrackID: 12, At: t0.Add(time.Second)})
	r.OnObjects(detect.Detections{CameraID: camID, At: t0.Add(2 * time.Minute), Objects: []detect.Object{tracked("Vehicle", 0, 0.6)}})
	var ids []string
	for _, e := range sink.durable() {
		if e.Detection != nil {
			ids = append(ids, e.Detection.Kind+"="+e.Detection.TrackID)
		}
	}
	want := []string{"Person=" + camID + ":12", "Line crossing=" + camID + ":12", "Vehicle="}
	if len(ids) != len(want) {
		t.Fatalf("events %v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("events %v, want %v", ids, want)
		}
	}
}

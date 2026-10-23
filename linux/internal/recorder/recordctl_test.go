package recorder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/onvif"
)

const cam2ID = "11111111-2222-4333-8444-555555555555"

// applyModes configures camID (and cam2ID) with these recording settings.
func applyModes(t *testing.T, r *Recorder, d link.RecordingDefaults, cams ...link.CameraConfig) {
	t.Helper()
	if _, err := r.ApplyConfig(context.Background(), link.Snapshot{Rev: 9, RecordingDefaults: d, Cameras: cams}); err != nil {
		t.Fatal(err)
	}
}

// writeSeg creates a segment named for start whose last write is end.
func writeModeSeg(t *testing.T, dir string, start, end time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	name := start.Local().Format("20060102-150405") + "-0.mp4"
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("segment"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, end, end); err != nil {
		t.Fatal(err)
	}
	return name
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestTriggerAccepts(t *testing.T) {
	for _, c := range []struct {
		trigger, kind, label string
		want                 bool
	}{
		{link.TriggerMotion, "Motion", "", true},
		{link.TriggerMotion, "Sound", "", true},
		{link.TriggerObjects, "Motion", "", false},
		{link.TriggerObjects, "Sound", "", false},
		{link.TriggerObjects, "Person", "", true},
		{link.TriggerObjects, "Vehicle", "car", true},
		{link.TriggerObjects, "Line crossing", "person", true},
		{link.TriggerObjects, "Line crossing", "", false},
		{link.TriggerObjects, "Intrusion", "truck", true},
		{link.TriggerObjects, "Tamper", "", false},
	} {
		if got := triggerAccepts(c.trigger, c.kind, c.label); got != c.want {
			t.Errorf("%s %s/%s = %v", c.trigger, c.kind, c.label, got)
		}
	}
}

func TestActivityOpensAndExtendsKeepWindows(t *testing.T) {
	r, _ := newTestRecorder(t)
	d := link.DefaultRecordingSettings()
	applyModes(t, r, d,
		link.CameraConfig{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", RecordingMode: link.RecordEvents, PreRollSeconds: 5, PostRollSeconds: 15, CameraAnalytics: true},
		link.CameraConfig{ID: cam2ID, Name: "Yard", RTSPURL: "rtsp://10.0.0.8/s", RecordingMode: link.RecordEvents, RecordTrigger: link.TriggerObjects, CameraAnalytics: true},
	)
	t0 := time.Date(2026, 10, 23, 14, 0, 0, 0, time.Local)
	r.Activity(camID, "Motion", t0)
	r.Activity(camID, "Motion", t0.Add(10*time.Second)) // within post-roll: extends
	r.Activity(camID, "Motion", t0.Add(2*time.Minute))  // a new window
	ws := r.windowsOf(camID)
	if len(ws) != 2 {
		t.Fatalf("windows %v", ws)
	}
	if !ws[0][0].Equal(t0.Add(-5*time.Second)) || !ws[0][1].Equal(t0.Add(25*time.Second)) {
		t.Fatalf("first window %v", ws[0])
	}
	// Objects trigger: pixel motion and camera motion don't count, a person does.
	r.Activity(cam2ID, "Motion", t0)
	r.OnCameraEvent(cam2ID, onvif.AnalyticsEvent{Kind: "Motion", Active: true, At: t0})
	if ws := r.windowsOf(cam2ID); len(ws) != 0 {
		t.Fatalf("objects trigger took motion: %v", ws)
	}
	// A camera Person event (and its end) counts, defaults pre/post-roll.
	r.OnCameraEvent(cam2ID, onvif.AnalyticsEvent{Kind: "Person", Label: "person", Active: true, At: t0})
	r.OnCameraEvent(cam2ID, onvif.AnalyticsEvent{Kind: "Person", Label: "person", Active: false, At: t0.Add(20 * time.Second)})
	ws = r.windowsOf(cam2ID)
	if len(ws) != 1 || !ws[0][0].Equal(t0.Add(-link.DefaultPreRollSeconds*time.Second)) || !ws[0][1].Equal(t0.Add(20*time.Second+link.DefaultPostRollSeconds*time.Second)) {
		t.Fatalf("camera event window %v", ws)
	}
	// Continuous cameras keep no windows.
	applyModes(t, r, d, link.CameraConfig{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", RecordingMode: link.RecordContinuous})
	r.OnMotion(detect.Motion{CameraID: camID, Score: 0.1, At: t0.Add(time.Hour)})
	if ws := r.windowsOf(camID); len(ws) != 2 {
		t.Fatalf("continuous camera opened a window: %v", ws)
	}
	// The windows were written to the database.
	r.flushWindows(t0)
	var n int
	if err := r.store.DB.QueryRow(`SELECT COUNT(*) FROM keep_windows`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("keep_windows rows %d %v", n, err)
	}
	var end int64
	_ = r.store.DB.QueryRow(`SELECT end_ms FROM keep_windows WHERE camera_id = ? ORDER BY start_ms LIMIT 1`, camID).Scan(&end)
	if end != t0.Add(25*time.Second).UnixMilli() {
		t.Fatalf("extended end not persisted: %d", end)
	}
}

func TestKeepWindowsSurviveARestart(t *testing.T) {
	r, _ := newTestRecorder(t)
	applyModes(t, r, link.RecordingDefaults{}, link.CameraConfig{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", RecordingMode: link.RecordEvents})
	now := time.Now()
	r.Activity(camID, "Motion", now)
	r2 := New(r.DataDir, r.Media, r.store, "test", "")
	if ws := r2.windowsOf(camID); len(ws) != 0 {
		t.Fatal("not loaded yet")
	}
	r2.loadWindows()
	if ws := r2.windowsOf(camID); len(ws) != 1 {
		t.Fatalf("restart lost the window: %v", ws)
	}
}

func TestKeeperMovesKeptSegmentsAndDropsOldOnes(t *testing.T) {
	r, _ := newTestRecorder(t)
	applyModes(t, r, link.RecordingDefaults{}, link.CameraConfig{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", RecordingMode: link.RecordEvents, PreRollSeconds: 5, PostRollSeconds: 15})
	now := time.Now().Truncate(time.Second)
	r.rec.now = func() time.Time { return now }
	root := r.recordingsRoot()
	buf := filepath.Join(root, media.BufferDir, camID)
	old := writeModeSeg(t, buf, now.Add(-20*time.Minute), now.Add(-19*time.Minute))     // unkept, old: deleted
	recent := writeModeSeg(t, buf, now.Add(-5*time.Minute), now.Add(-4*time.Minute))    // unkept, recent: stays
	pre := writeModeSeg(t, buf, now.Add(-62*time.Second), now.Add(-2*time.Second))      // pre-roll: kept
	writing := writeModeSeg(t, buf, now.Add(-2*time.Second), now)                       // being written: kept
	oldKept := writeModeSeg(t, buf, now.Add(-14*time.Minute), now.Add(-13*time.Minute)) // old but in a window
	r.extendWindow(camID, now.Add(-13*time.Minute-30*time.Second), now.Add(-13*time.Minute-10*time.Second))
	r.Activity(camID, "Motion", now.Add(-time.Second)) // window [now-6s, now+14s]

	r.keepOnce()
	kept := filepath.Join(root, camID)
	for _, name := range []string{pre, writing, oldKept} {
		if !fileExists(filepath.Join(kept, name)) || fileExists(filepath.Join(buf, name)) {
			t.Errorf("%s was not moved into the camera's footage", name)
		}
	}
	if fileExists(filepath.Join(buf, old)) || fileExists(filepath.Join(kept, old)) {
		t.Error("old unkept buffer segment survived")
	}
	if !fileExists(filepath.Join(buf, recent)) {
		t.Error("recent buffer segment deleted before bufferKeep")
	}
	// A segment already kept under that name is left in the buffer.
	dup := writeModeSeg(t, buf, now.Add(-62*time.Second), now)
	if dup != pre {
		t.Fatal("test setup")
	}
	r.keepOnce()
	if !fileExists(filepath.Join(buf, dup)) {
		t.Error("the keeper overwrote a kept segment")
	}
}

func TestHealthEventsAndDual(t *testing.T) {
	r, _ := newTestRecorder(t)
	applyModes(t, r, link.RecordingDefaults{},
		link.CameraConfig{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", RecordingMode: link.RecordEvents},
		link.CameraConfig{ID: cam2ID, Name: "Yard", RTSPURL: "rtsp://10.0.0.8/s", SubRTSPURL: "rtsp://10.0.0.8/sub", RecordingMode: link.RecordDual},
	)
	now := time.Now()
	root := r.recordingsRoot()
	writeModeSeg(t, filepath.Join(root, media.BufferDir, camID), now.Add(-30*time.Second), now.Add(-20*time.Second))
	keptEnd := now.Add(-time.Hour).Truncate(time.Second)
	writeModeSeg(t, filepath.Join(root, camID), now.Add(-61*time.Minute), keptEnd)
	writeModeSeg(t, filepath.Join(root, media.LowResDir, cam2ID+"-sub"), now.Add(-5*time.Minute), now.Add(-40*time.Second))
	res := r.healthResult()
	got := map[string]link.HealthCamera{}
	for _, c := range res.Cameras {
		got[c.ID] = c
	}
	ev, dual := got[camID], got[cam2ID]
	if ev.RecordingMode != link.RecordEvents || ev.LastSegmentAgeSec == nil || *ev.LastSegmentAgeSec > 25 {
		t.Fatalf("events camera health %+v", ev)
	}
	if ev.LastKeptClipAt != keptEnd.UTC().Format(time.RFC3339) {
		t.Fatalf("lastKeptClipAt %q want %s", ev.LastKeptClipAt, keptEnd.UTC().Format(time.RFC3339))
	}
	if dual.RecordingMode != link.RecordDual || dual.LastSegmentAgeSec == nil || *dual.LastSegmentAgeSec < 39 || *dual.LastSegmentAgeSec > 45 {
		t.Fatalf("dual camera health %+v", dual)
	}
	if dual.LastKeptClipAt != "" {
		t.Fatalf("no kept clip yet: %q", dual.LastKeptClipAt)
	}
}

func TestApplyConfigRendersEffectiveModes(t *testing.T) {
	r, _ := newTestRecorder(t)
	applyModes(t, r, link.RecordingDefaults{DefaultMode: link.RecordDual},
		link.CameraConfig{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", RecordingMode: link.RecordDefault}, // dual without sub → events
		link.CameraConfig{ID: cam2ID, Name: "Yard", RTSPURL: "rtsp://10.0.0.8/s", SubRTSPURL: "rtsp://10.0.0.8/sub", RecordingMode: link.RecordDefault},
	)
	b, err := os.ReadFile(r.Media.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b)
	if strings.Count(cfg, "/"+media.BufferDir+"/%path/") != 2 || strings.Count(cfg, "/"+media.LowResDir+"/%path/") != 1 {
		t.Fatalf("config:\n%s", cfg)
	}
	// A management server older than wave 22: Recording alone → continuous/off.
	applyModes(t, r, link.RecordingDefaults{}, link.CameraConfig{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", Recording: true})
	b, _ = os.ReadFile(r.Media.ConfigPath)
	if strings.Contains(string(b), media.BufferDir) || !strings.Contains(string(b), "record: yes") {
		t.Fatalf("legacy config:\n%s", b)
	}
}

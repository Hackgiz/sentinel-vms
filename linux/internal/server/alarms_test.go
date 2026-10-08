package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/store"
)

func addCam(t *testing.T, s *Server) *store.Camera {
	t.Helper()
	c, err := s.Store.SaveCamera(context.Background(), "", store.CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", Recording: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAlarmMergeWindowAndSnooze(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	c := addCam(t, s)
	t0 := time.Now()
	raise := func(at time.Time) (*store.Alarm, bool) {
		a, isNew, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: c.ID, CameraName: c.Name, Kind: "Motion", Title: "Motion detected",
			Severity: "Warning", Detail: "Door detected motion.", At: at, MergeWindow: alarmMergeWin, Origin: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return a, isNew
	}
	a1, n1 := raise(t0)
	a2, n2 := raise(t0.Add(40 * time.Second))
	if !n1 || n2 || a1.ID != a2.ID || a2.EventCount != 2 || !strings.Contains(a2.Detail, "Grouped with 2") {
		t.Fatalf("merge: %v %v %+v", n1, n2, a2)
	}
	if _, n3 := raise(t0.Add(3 * time.Minute)); !n3 {
		t.Fatal("event 2+ minutes later should open a new alarm")
	}
	// Snoozed alarms swallow new events.
	open, _ := s.Store.Alarms(ctx, true, 10)
	if _, err := s.Store.SetAlarmState(ctx, open[0].ID, "Snoozed", "admin", ""); err != nil {
		t.Fatal(err)
	}
	if a, isNew := raise(t0.Add(3*time.Minute + 10*time.Second)); isNew || a.EventCount != 1 {
		t.Fatalf("snoozed alarm changed: new=%v %+v", isNew, a)
	}
}

func TestMotionEpisodes(t *testing.T) {
	s := newCompanionServer(t)
	c := addCam(t, s)
	t0 := time.Now()
	for i := 0; i <= 140; i++ { // 70 s of continuous motion at 2 fps → episode start + one per minute
		s.OnMotion(detect.Motion{CameraID: c.ID, Score: 0.1, At: t0.Add(time.Duration(i) * 500 * time.Millisecond)})
	}
	s.OnMotion(detect.Motion{CameraID: c.ID, Score: 0.1, At: t0.Add(200 * time.Second)}) // after a long quiet gap → new alarm
	events, _ := s.Store.Events(context.Background(), "", 50)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 (episode start, one per minute, new episode)", len(events))
	}
	alarms, _ := s.Store.Alarms(context.Background(), true, 10)
	if len(alarms) != 2 || alarms[1].EventCount != 2 || alarms[0].EventCount != 1 || alarms[0].Kind != "Motion" || alarms[0].Severity != "Warning" {
		t.Fatalf("want sustained motion in one alarm (2 events) + a new alarm after the gap; got %+v", alarms)
	}
	if c := motionConfidence(detect.Thresholds[2]); c != 0.7 {
		t.Fatalf("confidence at threshold = %v", c)
	}
}

func TestPhoneAcknowledgeAndLockEvidence(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	h := s.Handler()
	c := addCam(t, s)
	_, token, _ := s.Store.CreatePairedDevice(ctx, "Eric's iPhone", "admin")

	// A finished recording segment covering the alarm time.
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	dir := filepath.Join(s.recordingsRoot(), c.ID)
	_ = os.MkdirAll(dir, 0o755)
	seg := filepath.Join(dir, start.Format("20060102-150405")+"-000001.mp4")
	if err := os.WriteFile(seg, []byte("fake mp4 bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(seg, start.Add(time.Minute), start.Add(time.Minute))
	a, _, _ := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: c.ID, CameraName: c.Name, Kind: "Motion", Title: "Motion detected",
		Severity: "Warning", Detail: "x", At: start.Add(20 * time.Second), Origin: "test"})

	rec := do(t, h, "GET", "/alerts", token, "")
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0]["hasClip"] != true || list[0]["state"] != "New" {
		t.Fatalf("alerts %s", rec.Body)
	}
	rec = do(t, h, "POST", "/alerts/"+a.ID+"/acknowledge", token, "")
	var acked map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &acked)
	if rec.Code != 200 || acked["state"] != "Acknowledged" || acked["owner"] != "Eric's iPhone (iPhone)" {
		t.Fatalf("ack %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "POST", "/alerts/"+a.ID+"/lock-evidence", token, "")
	var ev map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &ev)
	if rec.Code != 200 || ev["isLocked"] != true || !strings.HasPrefix(ev["caseID"].(string), "HG-") || len(ev["sha256"].(string)) != 64 {
		t.Fatalf("lock %d %s", rec.Code, rec.Body)
	}
	// Idempotent retry returns the same clip.
	rec2 := do(t, h, "POST", "/alerts/"+a.ID+"/lock-evidence", token, "")
	var ev2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &ev2)
	if ev2["id"] != ev["id"] {
		t.Fatal("second lock created another evidence package")
	}
	// The vault copy survives the recording being deleted by retention.
	_ = os.Remove(seg)
	if b, err := os.ReadFile(filepath.Join(s.evidenceDir(), ev["caseID"].(string)+".mp4")); err != nil || string(b) != "fake mp4 bytes" {
		t.Fatalf("evidence file: %v %q", err, b)
	}
}

func TestCompleteMP4Prefix(t *testing.T) {
	box := func(typ string, payload int) []byte {
		n := 8 + payload
		b := []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n), typ[0], typ[1], typ[2], typ[3]}
		return append(b, make([]byte, payload)...)
	}
	file := append(append(box("ftyp", 12), box("moov", 40)...), box("moof", 30)...)
	whole := int64(len(file))
	partial := append(file, box("mdat", 500)[:100]...) // fragment still being written
	n, err := completeMP4Prefix(bytes.NewReader(partial), int64(len(partial)))
	if err != nil || n != whole {
		t.Fatalf("prefix = %d, %v; want %d", n, err, whole)
	}
}

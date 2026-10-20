package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSource is a Management Server with alarms, snapshots and clips.
type fakeSource struct {
	mu       sync.Mutex
	alarms   []BackupAlarm // oldest first
	snaps    map[string][]byte
	pending  map[string]bool
	broken   map[string]bool // Snapshot fails (recording server away)
	clips    []BackupClip
	clipData map[string][]byte
}

func newFakeSource() *fakeSource {
	return &fakeSource{snaps: map[string][]byte{}, pending: map[string]bool{}, broken: map[string]bool{}, clipData: map[string][]byte{}}
}

func (s *fakeSource) add(id string, created time.Time, snap []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, _ := json.Marshal(map[string]any{"id": id, "cameraId": "cam-1", "cameraName": "Front door", "kind": "Person", "title": "Person detected",
		"severity": "Warning", "state": "New", "createdAt": created.UnixMilli(), "responseLog": []string{}})
	s.alarms = append(s.alarms, BackupAlarm{ID: id, CreatedAt: created, Record: rec,
		Meta: map[string]any{"cameraId": "cam-1", "cameraName": "Front door", "kind": "Person", "severity": "Warning", "title": "Person detected"}})
	if snap != nil {
		s.snaps[id] = snap
	}
}

func (s *fakeSource) LatestAlarmID(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.alarms) == 0 {
		return "", nil
	}
	return s.alarms[len(s.alarms)-1].ID, nil
}

func (s *fakeSource) AlarmsAfter(_ context.Context, afterID string, since time.Time, limit int) ([]BackupAlarm, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := 0
	for i, a := range s.alarms {
		if a.ID == afterID {
			start = i + 1
		}
	}
	var out []BackupAlarm
	for _, a := range s.alarms[start:] {
		if !a.CreatedAt.Before(since) && len(out) < limit {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *fakeSource) Alarm(_ context.Context, id string) (BackupAlarm, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.alarms {
		if a.ID == id {
			return a, nil
		}
	}
	return BackupAlarm{}, errors.New("no such alarm")
}

func (s *fakeSource) Snapshot(_ context.Context, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.broken[id]:
		return nil, errors.New("recording server offline")
	case s.pending[id]:
		return nil, ErrSnapshotPending
	case s.snaps[id] != nil:
		return s.snaps[id], nil
	}
	return nil, ErrNoSnapshot
}

func (s *fakeSource) ClipsAfter(_ context.Context, lockedAt time.Time, afterID string, limit int) ([]BackupClip, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []BackupClip
	for _, c := range s.clips {
		if (c.LockedAt.After(lockedAt) || (c.LockedAt.Equal(lockedAt) && c.EvidenceID > afterID)) && len(out) < limit {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *fakeSource) OpenClip(_ context.Context, c BackupClip) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return io.NopCloser(bytes.NewReader(s.clipData[c.EvidenceID])), nil
}

func (s *fakeSource) lock(evidenceID, alarmID string, at time.Time, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clips = append(s.clips, BackupClip{EvidenceID: evidenceID, AlarmID: alarmID, LockedAt: at, Size: int64(len(data))})
	s.clipData[evidenceID] = data
}

func runBackup(t *testing.T, f *fakeCloud, st *memStore, src *fakeSource) *Backup {
	t.Helper()
	b := &Backup{BaseURL: f.srv.URL + "/v1/backup", Token: testToken, Store: st, Source: src,
		Debounce: time.Millisecond, Poll: 20 * time.Millisecond, UsageEvery: 40 * time.Millisecond, RetryMin: 5 * time.Millisecond, RetryMax: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return b
}

func alarmJSON(t *testing.T, f *fakeCloud, id string) map[string]any {
	t.Helper()
	raw, ok := f.object(AlarmKey(id, "alarm.json"))
	if !ok {
		t.Fatalf("no alarm.json for %s", id)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBackupUploadsNewAlarms(t *testing.T) {
	f, st, src := newFakeCloud(t), newMemStore(), newFakeSource()
	src.add("A0", time.Now().Add(-time.Hour), []byte("old"))
	b := runBackup(t, f, st, src)
	ctx := context.Background()
	waitFor(t, "cursor", func() bool { return st.Setting(ctx, KeyBackupCursor, "") == "A0" })

	src.add("A1", time.Now(), []byte("\xff\xd8jpeg-1"))
	src.add("A2", time.Now(), nil) // a health alarm: no picture
	b.Notify()
	waitFor(t, "A2 uploaded", func() bool { _, ok := f.object(AlarmKey("A2", "alarm.json")); return ok })

	if _, ok := f.object(AlarmKey("A0", "alarm.json")); ok {
		t.Fatal("an alarm from before the backup started was uploaded")
	}
	if snap, _ := f.object(AlarmKey("A1", "snapshot.jpg")); string(snap) != "\xff\xd8jpeg-1" {
		t.Fatalf("snapshot %q", snap)
	}
	f.mu.Lock()
	ct, meta := f.types[AlarmKey("A1", "snapshot.jpg")], f.metas[AlarmKey("A1", "alarm.json")]
	jsonType := f.types[AlarmKey("A1", "alarm.json")]
	f.mu.Unlock()
	if ct != "image/jpeg" || jsonType != "application/json" {
		t.Fatalf("content types %q %q", ct, jsonType)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(meta), &m); err != nil || m["alarmId"] != "A1" || m["cameraName"] != "Front door" || m["createdAt"] == nil {
		t.Fatalf("meta %q", meta)
	}
	a1 := alarmJSON(t, f, "A1")
	if a1["snapshotKey"] != "alarms/A1/snapshot.jpg" || a1["clipKey"] != nil || a1["cameraName"] != "Front door" || a1["backedUpAt"] == nil {
		t.Fatalf("A1 alarm.json %v", a1)
	}
	if a2 := alarmJSON(t, f, "A2"); a2["snapshotKey"] != nil || a2["id"] != "A2" {
		t.Fatalf("A2 alarm.json %v", a2)
	}
	if st.Setting(ctx, KeyBackupCursor, "") != "A2" {
		t.Fatalf("cursor %q", st.Setting(ctx, KeyBackupCursor, ""))
	}
	s := b.Status()
	if s.LastUploadAt == nil || s.BytesUsed == 0 || s.Error != "" {
		t.Fatalf("status %+v", s)
	}
	waitFor(t, "usage", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.usageCalls >= 2 })
	if s := b.Status(); s.Objects != 3 {
		t.Fatalf("objects after usage %d", s.Objects)
	}
}

func TestBackupFromTheStartWhenThereWereNoAlarms(t *testing.T) {
	f, st, src := newFakeCloud(t), newMemStore(), newFakeSource()
	b := runBackup(t, f, st, src)
	ctx := context.Background()
	waitFor(t, "cursor", func() bool { return st.Setting(ctx, KeyBackupCursor, "") == "-" })
	src.add("A1", time.Now(), nil)
	b.Notify()
	waitFor(t, "A1", func() bool { _, ok := f.object(AlarmKey("A1", "alarm.json")); return ok })
}

func TestBackupWaitsForTheSnapshot(t *testing.T) {
	f, st, src := newFakeCloud(t), newMemStore(), newFakeSource()
	_ = st.SetSetting(context.Background(), KeyBackupCursor, "-")
	src.add("OLD", time.Now().Add(-10*time.Minute), nil)
	src.pending["OLD"] = true // never came: backed up without it
	src.add("NEW", time.Now(), nil)
	src.pending["NEW"] = true
	runBackup(t, f, st, src)
	waitFor(t, "OLD", func() bool { _, ok := f.object(AlarmKey("OLD", "alarm.json")); return ok })
	if m := alarmJSON(t, f, "OLD"); m["snapshotKey"] != nil {
		t.Fatalf("OLD %v", m)
	}
	time.Sleep(80 * time.Millisecond) // several polls
	if _, ok := f.object(AlarmKey("NEW", "alarm.json")); ok {
		t.Fatal("uploaded before its snapshot was written")
	}
	if c := st.Setting(context.Background(), KeyBackupCursor, ""); c != "OLD" {
		t.Fatalf("cursor %q", c)
	}
	src.mu.Lock()
	delete(src.pending, "NEW")
	src.snaps["NEW"] = []byte("jpeg")
	src.mu.Unlock()
	waitFor(t, "NEW", func() bool { _, ok := f.object(AlarmKey("NEW", "alarm.json")); return ok })
	if m := alarmJSON(t, f, "NEW"); m["snapshotKey"] != "alarms/NEW/snapshot.jpg" {
		t.Fatalf("NEW %v", m)
	}
}

func TestBackupUploadsLockedClips(t *testing.T) {
	f, st, src := newFakeCloud(t), newMemStore(), newFakeSource()
	ctx := context.Background()
	_ = st.SetSetting(ctx, KeyBackupCursor, "-")
	src.add("A1", time.Now().Add(-5*time.Minute), []byte("jpeg"))
	src.lock("EV-OLD", "A1", time.Now().Add(-time.Hour), []byte("old clip"))
	runBackup(t, f, st, src)
	waitFor(t, "A1", func() bool { _, ok := f.object(AlarmKey("A1", "alarm.json")); return ok })
	waitFor(t, "clip cursor", func() bool { return st.Setting(ctx, KeyBackupClipCursor, "") != "" })
	if _, ok := f.object(AlarmKey("A1", "clip.mp4")); ok {
		t.Fatal("evidence locked before the backup started was uploaded")
	}

	// Locked now: the clip goes up and alarm.json is rewritten with clipKey.
	// The snapshot can't be read any more, but it is already in the cloud.
	src.mu.Lock()
	src.broken["A1"] = true
	src.mu.Unlock()
	clip := bytes.Repeat([]byte("mp4!"), 300_000) // 1.2 MB: several DATA-sized writes
	src.lock("EV-1", "A1", time.Now().Add(time.Second), clip)
	waitFor(t, "clipKey", func() bool {
		raw, ok := f.object(AlarmKey("A1", "alarm.json"))
		return ok && strings.Contains(string(raw), "clipKey")
	})
	if got, _ := f.object(AlarmKey("A1", "clip.mp4")); !bytes.Equal(got, clip) {
		t.Fatalf("clip of %d bytes", len(got))
	}
	m := alarmJSON(t, f, "A1")
	if m["clipKey"] != "alarms/A1/clip.mp4" || m["snapshotKey"] != "alarms/A1/snapshot.jpg" {
		t.Fatalf("rewritten alarm.json %v", m)
	}
	f.mu.Lock()
	deletes, ct := f.objDeletes, f.types[AlarmKey("A1", "clip.mp4")]
	f.mu.Unlock()
	if len(deletes) != 1 || deletes[0] != "alarms/A1/alarm.json" || ct != "video/mp4" {
		t.Fatalf("deletes %v, type %q", deletes, ct)
	}
	if c := st.Setting(ctx, KeyBackupClipCursor, ""); !strings.HasSuffix(c, ":EV-1") {
		t.Fatalf("clip cursor %q", c)
	}
}

func TestBackupSkipsClipsOver100MB(t *testing.T) {
	f, st, src := newFakeCloud(t), newMemStore(), newFakeSource()
	ctx := context.Background()
	_ = st.SetSetting(ctx, KeyBackupCursor, "-")
	_ = st.SetSetting(ctx, KeyBackupClipCursor, clipCursor(time.Now().Add(-time.Minute), ""))
	src.add("A1", time.Now(), nil)
	src.mu.Lock()
	src.clips = append(src.clips, BackupClip{EvidenceID: "EV-BIG", AlarmID: "A1", LockedAt: time.Now(), Size: 150_000_000})
	src.mu.Unlock()
	b := runBackup(t, f, st, src)
	waitFor(t, "skip noted", func() bool { return strings.Contains(b.Status().Error, "too large") })
	if c := st.Setting(ctx, KeyBackupClipCursor, ""); !strings.HasSuffix(c, ":EV-BIG") {
		t.Fatalf("cursor stuck at %q", c)
	}
	if _, ok := f.object(AlarmKey("A1", "clip.mp4")); ok {
		t.Fatal("uploaded a clip over the limit")
	}
	// A clip that fits clears the note.
	src.lock("EV-OK", "A1", time.Now().Add(time.Second), []byte("small clip"))
	waitFor(t, "note cleared", func() bool { return b.Status().Error == "" })
}

func TestBackupStopsWhenFullAndResumes(t *testing.T) {
	f, st, src := newFakeCloud(t), newMemStore(), newFakeSource()
	f.quota = 10
	_ = st.SetSetting(context.Background(), KeyBackupCursor, "-")
	src.add("A1", time.Now(), bytes.Repeat([]byte("x"), 100))
	b := runBackup(t, f, st, src)
	waitFor(t, "full", func() bool { return strings.Contains(b.Status().Error, "full") })
	time.Sleep(60 * time.Millisecond)
	if _, ok := f.object(AlarmKey("A1", "alarm.json")); ok {
		t.Fatal("uploaded while full")
	}
	if c := st.Setting(context.Background(), KeyBackupCursor, ""); c != "-" {
		t.Fatalf("cursor moved while full: %q", c)
	}
	f.mu.Lock()
	f.quota = 1 << 30 // the owner upgraded, or old backups expired
	f.mu.Unlock()
	waitFor(t, "resumed", func() bool { _, ok := f.object(AlarmKey("A1", "alarm.json")); return ok })
	waitFor(t, "error cleared", func() bool { return b.Status().Error == "" })
}

func TestBackupRetriesTransientFailures(t *testing.T) {
	f, st, src := newFakeCloud(t), newMemStore(), newFakeSource()
	f.putFail = 3
	_ = st.SetSetting(context.Background(), KeyBackupCursor, "-")
	src.add("A1", time.Now(), []byte("jpeg"))
	b := runBackup(t, f, st, src)
	waitFor(t, "uploaded", func() bool { _, ok := f.object(AlarmKey("A1", "alarm.json")); return ok })
	waitFor(t, "error cleared", func() bool { return b.Status().Error == "" })
}

func TestAlarmDocumentAndMeta(t *testing.T) {
	a := BackupAlarm{ID: "A1", CreatedAt: time.UnixMilli(1_760_000_000_000), Record: json.RawMessage(`{"id":"A1","kind":"Person"}`),
		Meta: map[string]any{"cameraName": strings.Repeat("n", 500)}}
	raw, err := alarmDocument(a, "alarms/A1/snapshot.jpg", "", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["kind"] != "Person" || m["snapshotKey"] != "alarms/A1/snapshot.jpg" || m["backedUpAt"] != "1970-01-01T00:00:00Z" {
		t.Fatalf("doc %s", raw)
	}
	if _, err := alarmDocument(BackupAlarm{ID: "x", Record: json.RawMessage("{")}, "", "", time.Now()); err == nil {
		t.Fatal("bad record accepted")
	}
	meta := metaHeader(a)
	if len(meta) > maxMeta || !strings.Contains(meta, `"alarmId":"A1"`) || !strings.Contains(meta, `"createdAt":1760000000000`) {
		t.Fatalf("meta %s", meta)
	}
	big := a
	big.Meta = map[string]any{}
	for i := 0; i < 50; i++ {
		big.Meta[strings.Repeat("k", i+1)] = strings.Repeat("v", 150)
	}
	if meta := metaHeader(big); len(meta) > maxMeta || !strings.Contains(meta, "A1") {
		t.Fatalf("oversized meta %d bytes", len(meta))
	}
}

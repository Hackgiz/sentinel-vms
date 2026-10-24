package recorder

// WAVE23 stream M: playback index sidecars (<name>.mp4.idx) are never
// mistaken for recordings, go with their segment, and move with it.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/playback"
)

func writeSidecarFile(t *testing.T, seg string) string {
	t.Helper()
	p := playback.SidecarPath(seg)
	if err := os.WriteFile(p, []byte("SNIX-test"), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScansIgnoreSidecars(t *testing.T) {
	r := New(t.TempDir(), nil, nil, "test", "")
	r.Ctx = context.Background()
	t.Cleanup(r.Wait)
	now := time.Now()
	dir := filepath.Join(r.recordingsRoot(), camID)
	low := playback.TierDir(r.recordingsRoot(), camID, true)
	name := writeModeSeg(t, dir, now.Add(-20*time.Minute), now.Add(-5*time.Minute))
	lowName := writeModeSeg(t, low, now.Add(-20*time.Minute), now.Add(-5*time.Minute))
	// Sidecars written after the segment: newer mtime, later name.
	writeSidecarFile(t, filepath.Join(dir, name))
	writeSidecarFile(t, filepath.Join(low, lowName))
	_ = os.WriteFile(filepath.Join(dir, "."+name+".idx-42"), []byte("tmp"), 0o640)

	segs, err := r.listSegments(camID)
	if err != nil || len(segs) != 1 || segs[0].Name != name || segs[0].Writing {
		t.Fatalf("listSegments %+v %v", segs, err)
	}
	if lows, _ := r.listTier(camID, true); len(lows) != 1 || lows[0].Name != lowName {
		t.Fatalf("low tier %+v", lows)
	}
	if got := listDirSegments(dir); len(got) != 1 || got[0].name != name {
		t.Fatalf("listDirSegments %+v", got)
	}
	// Health: the newest segment is the mp4, 5 min old, not the sidecar.
	if age, ok := r.lastSegmentAge(camID, now); !ok || age < 290 {
		t.Fatalf("lastSegmentAge %d %v", age, ok)
	}
	files, err := r.library().Files(camID, now.Add(-time.Hour), now)
	if err != nil || len(files) != 2 {
		t.Fatalf("library files %d %v", len(files), err)
	}
}

func TestSweeperDeletesSidecars(t *testing.T) {
	r, _ := newTestRecorder(t)
	applyModes(t, r, link.RecordingDefaults{DefaultRetentionDays: 3, DefaultLowResRetentionDays: 3, MinFreePercent: 10},
		link.CameraConfig{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", SubRTSPURL: "rtsp://10.0.0.9/sub", RecordingMode: link.RecordDual})
	now := time.Now()
	r.rec.now = func() time.Time { return now }
	root := r.recordingsRoot()
	full, low := filepath.Join(root, camID), filepath.Join(root, media.LowResDir, camID+"-sub")
	seg := func(dir string, age time.Duration) string {
		return filepath.Join(dir, writeModeSeg(t, dir, now.Add(-age), now.Add(-age+15*time.Minute)))
	}
	expired, kept := seg(full, 5*24*time.Hour), seg(full, 2*time.Hour)
	lowExpired, lowOld := seg(low, 5*24*time.Hour), seg(low, 3*time.Hour)
	expiredIdx, keptIdx := writeSidecarFile(t, expired), writeSidecarFile(t, kept)
	lowExpiredIdx, lowOldIdx := writeSidecarFile(t, lowExpired), writeSidecarFile(t, lowOld)
	// An orphan (its segment went some other way) and a stale temp file.
	orphan := playback.SidecarPath(filepath.Join(full, "20200101-000000-0.mp4"))
	_ = os.WriteFile(orphan, []byte("x"), 0o640)
	staleTmp := filepath.Join(full, ".20200101-000000-0.mp4.idx-7")
	_ = os.WriteFile(staleTmp, []byte("x"), 0o640)
	_ = os.Chtimes(staleTmp, now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	freshTmp := filepath.Join(full, "."+filepath.Base(kept)+".idx-8")
	_ = os.WriteFile(freshTmp, []byte("x"), 0o640)

	// One segment over minFreePercent: the oldest low-res one goes.
	freed := false
	r.rec.freeSpace = func(string) (float64, uint64, bool) {
		if !fileExists(lowOld) {
			freed = true
		}
		if freed {
			return 50, 0, true
		}
		return 5, 0, true
	}
	res := r.sweepOnce()
	if res.Expired != 2 || res.ForSpace != 1 {
		t.Fatalf("sweep %+v", res)
	}
	for _, p := range []string{expired, expiredIdx, lowExpired, lowExpiredIdx, lowOld, lowOldIdx, orphan, staleTmp} {
		if fileExists(p) {
			t.Errorf("%s survived", filepath.Base(p))
		}
	}
	for _, p := range []string{kept, keptIdx, freshTmp} {
		if !fileExists(p) {
			t.Errorf("%s was deleted", filepath.Base(p))
		}
	}
}

func TestMoveTreeCarriesSidecars(t *testing.T) {
	from, to := t.TempDir(), filepath.Join(t.TempDir(), "new")
	segs := []string{
		filepath.Join(tierCam, "20261023-090000-000000.mp4"),
		filepath.Join(playback.LowResDir, tierCam+playback.LowResSuffix, "20261023-090000-000000.mp4"),
	}
	old := time.Now().Add(-time.Hour)
	var all []string
	for _, s := range segs {
		for _, f := range []string{s, playback.SidecarPath(s)} {
			p := filepath.Join(from, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
				t.Fatal(err)
			}
			_ = os.Chtimes(p, old, old)
			all = append(all, f)
		}
	}
	// The new folder already indexed one segment (moved earlier): the old
	// sidecar is dropped, not left behind as "skipped".
	dup := filepath.Join(tierCam, "20261023-091500-000000.mp4.idx")
	for _, base := range []string{from, to} {
		p := filepath.Join(base, dup)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(base), 0o644)
		_ = os.Chtimes(p, old, old)
	}
	moved, skipped, err := moveTree(from, to, MoveRecentFile)
	if err != nil || moved != len(all) || skipped != 0 {
		t.Fatalf("moved %d skipped %d err %v", moved, skipped, err)
	}
	for _, f := range all {
		if b, err := os.ReadFile(filepath.Join(to, f)); err != nil || string(b) != f {
			t.Errorf("%s: %v %q", f, err, b)
		}
	}
	if fileExists(filepath.Join(from, dup)) {
		t.Error("redundant sidecar left in the old folder")
	}
	if b, _ := os.ReadFile(filepath.Join(to, dup)); string(b) != to {
		t.Error("the new folder's sidecar was replaced")
	}
}

// After the recorder starts, the last day of every camera is indexed in the
// background, and a real (fMP4) segment gets its sidecar.
func TestPlaybackWarmUpAfterStart(t *testing.T) {
	r, start, fullPath, lowPath := tierFixture(t)
	r.Ctx = context.Background()
	t.Cleanup(r.Wait)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.startPlaybackWarm(ctx, 0)
	deadline := time.Now().Add(5 * time.Second)
	for !(fileExists(playback.SidecarPath(fullPath)) && fileExists(playback.SidecarPath(lowPath))) {
		if time.Now().After(deadline) {
			t.Fatal("warm-up wrote no sidecars")
		}
		time.Sleep(10 * time.Millisecond)
	}
	buf := filepath.Join(r.recordingsRoot(), playback.BufferDir, tierCam, start.Add(150*time.Second).Format("20060102-150405")+"-000000.mp4")
	if !fileExists(buf) || fileExists(playback.SidecarPath(buf)) {
		t.Fatal("the rolling buffer got a sidecar")
	}
	// Restarting the warm-up cancels the previous one cleanly.
	r.startPlaybackWarm(ctx, time.Hour)
	r.startPlaybackWarm(ctx, time.Hour)
	cancel()
}

package recorder

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
)

const goneID = "99999999-8888-4777-8666-555555555555"

func TestSweeperWaitsForConfiguration(t *testing.T) {
	r := New(t.TempDir(), nil, nil, "test", "")
	dir := filepath.Join(r.recordingsRoot(), camID)
	old := writeModeSeg(t, dir, time.Now().AddDate(0, 0, -400), time.Now().AddDate(0, 0, -400))
	if res := r.sweepOnce(); res.Expired != 0 || !fileExists(filepath.Join(dir, old)) {
		t.Fatal("the sweeper ran before the recorder knew its cameras")
	}
}

func TestSweeperRetentionPerTier(t *testing.T) {
	r, _ := newTestRecorder(t)
	applyModes(t, r, link.RecordingDefaults{DefaultRetentionDays: 7, DefaultLowResRetentionDays: 20},
		link.CameraConfig{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", SubRTSPURL: "rtsp://10.0.0.9/sub", RecordingMode: link.RecordDual, RetentionDays: 3, LowResRetentionDays: 10},
		link.CameraConfig{ID: cam2ID, Name: "Yard", RTSPURL: "rtsp://10.0.0.8/s", RecordingMode: link.RecordContinuous},
	)
	now := time.Now()
	day := 24 * time.Hour
	root := r.recordingsRoot()
	full, low := filepath.Join(root, camID), filepath.Join(root, media.LowResDir, camID+"-sub")
	full2, gone := filepath.Join(root, cam2ID), filepath.Join(root, goneID)
	seg := func(dir string, age time.Duration) string {
		return writeModeSeg(t, dir, now.Add(-age), now.Add(-age+15*time.Minute))
	}
	fullOld, fullNew := seg(full, 4*day), seg(full, 2*day) // camera: 3 days
	lowOld, lowNew := seg(low, 11*day), seg(low, 9*day)    // camera: 10 days
	f2Old, f2New := seg(full2, 8*day), seg(full2, 6*day)   // server default: 7 days
	goneOld := seg(gone, 8*day)                            // deleted camera: server default, then the folder goes
	notes := filepath.Join(full, "notes.txt")
	if err := os.WriteFile(notes, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(notes, now.AddDate(-1, 0, 0), now.AddDate(-1, 0, 0))

	res := r.sweepOnce()
	if res.Expired != 4 {
		t.Fatalf("expired %d", res.Expired)
	}
	for _, p := range []string{filepath.Join(full, fullOld), filepath.Join(low, lowOld), filepath.Join(full2, f2Old), filepath.Join(gone, goneOld), gone} {
		if fileExists(p) {
			t.Errorf("%s survived", p)
		}
	}
	for _, p := range []string{filepath.Join(full, fullNew), filepath.Join(low, lowNew), filepath.Join(full2, f2New), notes} {
		if !fileExists(p) {
			t.Errorf("%s was deleted", p)
		}
	}
}

func TestSweeperMinFreePercent(t *testing.T) {
	r, _ := newTestRecorder(t)
	applyModes(t, r, link.RecordingDefaults{DefaultRetentionDays: 365, DefaultLowResRetentionDays: 365, MinFreePercent: 10},
		link.CameraConfig{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", SubRTSPURL: "rtsp://10.0.0.9/sub", RecordingMode: link.RecordDual},
	)
	now := time.Now()
	r.rec.now = func() time.Time { return now }
	root := r.recordingsRoot()
	full, low, buf := filepath.Join(root, camID), filepath.Join(root, media.LowResDir, camID+"-sub"), filepath.Join(root, media.BufferDir, camID)
	seg := func(dir string, age time.Duration) string {
		return filepath.Join(dir, writeModeSeg(t, dir, now.Add(-age), now.Add(-age+time.Minute)))
	}
	full1, full2 := seg(full, 5*time.Hour), seg(full, 4*time.Hour)
	low1, low2 := seg(low, 3*time.Hour), seg(low, 2*time.Hour)
	bufFree := seg(buf, 8*time.Minute)
	bufKept := seg(buf, 6*time.Minute)
	r.extendWindow(camID, now.Add(-6*time.Minute+10*time.Second), now.Add(-6*time.Minute+20*time.Second))
	writing := seg(full, 30*time.Second) // ends ~now: still being written
	outside := filepath.Join(t.TempDir(), "20200101-000000-0.mp4")
	if err := os.WriteFile(outside, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Each deleted segment frees 2%; 4% free → need 3 deletions.
	base := 4.0
	all := []string{full1, full2, low1, low2, bufFree, bufKept, writing}
	free := func(string) (float64, uint64, bool) {
		pct := base
		for _, p := range all {
			if !fileExists(p) {
				pct += 2
			}
		}
		return pct, 0, true
	}
	r.rec.freeSpace = free
	res := r.sweepOnce()
	if res.ForSpace != 3 {
		t.Fatalf("deleted %d for space", res.ForSpace)
	}
	// low-res first (oldest first), then the unkept buffer segment.
	for _, p := range []string{low1, low2, bufFree} {
		if fileExists(p) {
			t.Errorf("%s should have gone first", p)
		}
	}
	for _, p := range []string{full1, full2, bufKept, writing, outside} {
		if !fileExists(p) {
			t.Errorf("%s should have stayed", p)
		}
	}
	// Under pressure again: full-res next, oldest first; never the buffer
	// segment in a window or the file being written.
	base = -2 // three deletions short again
	res = r.sweepOnce()
	if res.ForSpace != 2 || fileExists(full1) || fileExists(full2) || !fileExists(bufKept) || !fileExists(writing) {
		t.Fatalf("second pass %d: full1 %v full2 %v bufKept %v writing %v", res.ForSpace, fileExists(full1), fileExists(full2), fileExists(bufKept), fileExists(writing))
	}
	// Off: nothing is deleted for space.
	applyModes(t, r, link.RecordingDefaults{DefaultRetentionDays: 365, DefaultLowResRetentionDays: 365},
		link.CameraConfig{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s"})
	if res := r.sweepOnce(); res.ForSpace != 0 {
		t.Fatalf("minFreePercent off still deleted %d", res.ForSpace)
	}
}

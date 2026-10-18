package recorder

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/playback"
)

const longCam = "6F1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F"

func fakeFFmpegBin(t *testing.T, body string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SENTINEL_FFMPEG", bin)
}

func longRecorder(t *testing.T) (*Recorder, time.Time) {
	r := New(t.TempDir(), nil, nil, "test", "")
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	dir := filepath.Join(r.RecordingsRoot(), longCam)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := start.Format("20060102-150405") + "-000000.mp4"
	if err := os.WriteFile(filepath.Join(dir, name), playback.SynthSegmentPCM(60, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	return r, start
}

func waitLong(t *testing.T, r *Recorder, id string) link.ExportLongStatus {
	t.Helper()
	for i := 0; i < 500; i++ {
		st, code, msg := r.exportLong(link.ExportLongArgs{Op: link.ExportLongOpStatus, JobID: id})
		if code != "" {
			t.Fatal(code, msg)
		}
		if st.State != link.ExportLongRunning {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return link.ExportLongStatus{}
}

func TestLongExportJobProducesPreparedClip(t *testing.T) {
	fakeFFmpegBin(t, "for last; do :; done\necho fake-mp4 > \"$last\"")
	r, start := longRecorder(t)
	st, code, msg := r.exportLong(link.ExportLongArgs{Op: link.ExportLongOpStart, CameraID: longCam,
		From: start.UnixMilli(), To: start.Add(50 * time.Second).UnixMilli()})
	if code != "" || st.JobID == "" || st.Total < 2 {
		t.Fatal(code, msg, st)
	}
	st = waitLong(t, r, st.JobID)
	if st.State != link.ExportLongDone || st.Token == "" || st.Done != st.Total {
		t.Fatalf("status %+v", st)
	}
	if b, err := os.ReadFile(filepath.Join(r.exportsDir(), st.Token+".mp4")); err != nil || string(b) != "fake-mp4\n" {
		t.Fatalf("prepared clip %q %v", b, err)
	}
	r.bg.Wait()
	if left, _ := os.ReadDir(r.longWorkDir()); len(left) != 0 {
		t.Fatalf("work dir not cleaned: %v", left)
	}
	if len(r.playback.slots) != 0 {
		t.Fatal("export slot not released")
	}
}

func TestLongExportJobCancel(t *testing.T) {
	fakeFFmpegBin(t, "exec sleep 30")
	r, start := longRecorder(t)
	st, code, msg := r.exportLong(link.ExportLongArgs{Op: link.ExportLongOpStart, CameraID: longCam,
		From: start.UnixMilli(), To: start.Add(50 * time.Second).UnixMilli()})
	if code != "" {
		t.Fatal(code, msg)
	}
	st, code, msg = r.exportLong(link.ExportLongArgs{Op: link.ExportLongOpCancel, JobID: st.JobID})
	if code != "" {
		t.Fatal(code, msg)
	}
	st = waitLong(t, r, st.JobID)
	if st.State != link.ExportLongCanceled {
		t.Fatalf("status %+v", st)
	}
	r.bg.Wait()
	if left, _ := os.ReadDir(r.longWorkDir()); len(left) != 0 {
		t.Fatalf("work dir not cleaned: %v", left)
	}
}

func TestLongExportStartRefusals(t *testing.T) {
	fakeFFmpegBin(t, "exit 1")
	r, start := longRecorder(t)
	cases := map[string]link.ExportLongArgs{
		"bad camera":   {Op: link.ExportLongOpStart, CameraID: "../x", From: 1, To: 2},
		"over 12 h":    {Op: link.ExportLongOpStart, CameraID: longCam, From: start.UnixMilli(), To: start.Add(13 * time.Hour).UnixMilli()},
		"no recording": {Op: link.ExportLongOpStart, CameraID: longCam, From: start.Add(-5 * time.Hour).UnixMilli(), To: start.Add(-4 * time.Hour).UnixMilli()},
		"unknown op":   {Op: "bogus"},
		"unknown job":  {Op: link.ExportLongOpStatus, JobID: "nope"},
	}
	for name, a := range cases {
		if _, code, _ := r.exportLong(a); code == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

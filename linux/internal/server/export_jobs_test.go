package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

// waitJobs blocks until every export job the server knows has finished.
func waitJobs(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		s.exportJobs.mu.Lock()
		var pending []*exportJob
		for _, j := range s.exportJobs.jobs {
			if !j.isFinished() {
				pending = append(pending, j)
			}
		}
		s.exportJobs.mu.Unlock()
		if len(pending) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("export jobs did not finish")
		}
		for _, j := range pending {
			select {
			case <-j.done:
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
}

func jobBody(camID string, from, to time.Time, dest string) string {
	return fmt.Sprintf(`{"cameraId":%q,"fromMs":%d,"toMs":%d,"dest":%q,"title":"Whole night"}`, camID, from.UnixMilli(), to.UnixMilli(), dest)
}

func TestV2ExportJobEvidenceAndDownload(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	s.exportJobs.pollEvery = 10 * time.Millisecond
	h := s.Handler()
	op := v2Token(t, s, "op", "Operator")
	other := v2Token(t, s, "other", "Operator")

	// Evidence: 202 with a task id, polled to done, filed as a case.
	code, job := v2Call(t, h, "POST", "/api/v2/export-jobs", op, jobBody(c.ID, t0, t0.Add(3*time.Minute), "evidence"))
	if code != 201 || job["taskId"] == nil || job["state"] != "running" {
		t.Fatalf("start: %d %v", code, job)
	}
	waitJobs(t, s)
	id := job["taskId"].(string)
	code, job = v2Call(t, h, "GET", "/api/v2/export-jobs/"+id, op, "")
	if code != 200 || job["state"] != "done" || job["progress"] != float64(100) || job["evidenceId"] == "" || job["caseId"] == "" {
		t.Fatalf("status: %d %v", code, job)
	}
	_, list := v2Call(t, h, "GET", "/api/v2/evidence", op, "")
	rows := items(t, list)
	if len(rows) != 1 || rows[0]["id"] != job["evidenceId"] || rows[0]["title"] != "Whole night" {
		t.Fatalf("evidence list: %v", list)
	}
	// The task registry shows it as a finished export at 100 %.
	found := false
	for _, tk := range tasks.Default.List() {
		if tk.ID == id {
			found = tk.Kind == tasks.KindExport && tk.State == tasks.Done && tk.Progress == 100 && strings.HasPrefix(tk.Title, "Long export:")
		}
	}
	if !found {
		t.Errorf("task %s missing or wrong: %+v", id, tasks.Default.List())
	}
	// Another user can't see it.
	if code, _ := v2Call(t, h, "GET", "/api/v2/export-jobs/"+id, other, ""); code != 404 {
		t.Errorf("other user's job: %d", code)
	}

	// Download: a prepared-download URL for the owner.
	code, job = v2Call(t, h, "POST", "/api/v2/export-jobs", op, jobBody(c.ID, t0, t0.Add(3*time.Minute), "download"))
	if code != 201 {
		t.Fatalf("start download: %d %v", code, job)
	}
	waitJobs(t, s)
	_, job = v2Call(t, h, "GET", "/api/v2/export-jobs/"+job["taskId"].(string), op, "")
	if job["state"] != "done" || !strings.HasPrefix(job["downloadUrl"].(string), "/api/v2/export-jobs/") || !strings.HasSuffix(job["filename"].(string), "-long.mp4") {
		t.Fatalf("download job: %v", job)
	}
	dl := do(t, h, "GET", job["downloadUrl"].(string), op, "")
	if dl.Code != 200 || !strings.HasPrefix(dl.Body.String(), "clip:") || !strings.Contains(dl.Header().Get("Content-Disposition"), "-long.mp4") || dl.Header().Get("X-Content-SHA256") == "" {
		t.Fatalf("download: %d %q %v", dl.Code, dl.Body.String(), dl.Header())
	}
	if r := do(t, h, "GET", job["downloadUrl"].(string), other, ""); r.Code != 404 {
		t.Errorf("another user's download: %d", r.Code)
	}
	// An evidence-destination job has nothing to download.
	if r := do(t, h, "GET", "/api/v2/export-jobs/"+id+"/download", op, ""); r.Code != 404 {
		t.Errorf("evidence job download: %d", r.Code)
	}
	// The start and the result are audited with the range.
	logs, _ := s.Store.AuditLog(t.Context(), 50)
	var started, locked bool
	for _, e := range logs {
		started = started || (e.Action == "Started long export" && strings.Contains(e.Detail, "to evidence"))
		locked = locked || (e.Action == "Locked evidence" && strings.Contains(e.Detail, "long clip"))
	}
	if !started || !locked {
		t.Errorf("audit rows missing (started %v, locked %v)", started, locked)
	}
}

func TestV2ExportJobValidationAndScope(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	h := s.Handler()
	ctx := t.Context()
	op := v2Token(t, s, "op", "Operator")
	viewer := v2Token(t, s, "viewer", "Viewer")
	scoped := v2Token(t, s, "scoped", "Operator")
	other, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Other", RTSPURL: "rtsp://10.0.0.7/s"})
	g, _ := s.Store.SaveCameraGroup(ctx, "", "Other only", []string{other.ID})
	u, _ := s.Store.UserByName(ctx, "scoped")
	if err := s.Store.SetUserScope(ctx, u.ID, store.UserScope{GroupIDs: []string{g.ID}}); err != nil {
		t.Fatal(err)
	}
	body := jobBody(c.ID, t0, t0.Add(3*time.Minute), "evidence")

	if r := do(t, h, "POST", "/api/v2/export-jobs", viewer, body); r.Code != 403 {
		t.Errorf("viewer: %d", r.Code)
	}
	if r := do(t, h, "POST", "/api/v2/export-jobs", scoped, body); r.Code != 404 {
		t.Errorf("out of scope: %d", r.Code)
	}
	if r := do(t, h, "POST", "/api/v2/export-jobs", op, jobBody("nope", t0, t0.Add(time.Minute), "evidence")); r.Code != 404 {
		t.Errorf("unknown camera: %d", r.Code)
	}
	if r := do(t, h, "POST", "/api/v2/export-jobs", op, jobBody(c.ID, t0, t0.Add(12*time.Hour+time.Second), "evidence")); r.Code != 400 || !strings.Contains(r.Body.String(), "12 hours") {
		t.Errorf("over 12 h: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "POST", "/api/v2/export-jobs", op, jobBody(c.ID, t0.Add(time.Minute), t0, "evidence")); r.Code != 400 {
		t.Errorf("inverted: %d", r.Code)
	}
	if r := do(t, h, "POST", "/api/v2/export-jobs", op, jobBody(c.ID, t0, t0.Add(time.Minute), "email")); r.Code != 400 {
		t.Errorf("bad dest: %d", r.Code)
	}
	// Nothing recorded in the range: refused up front, not as a failed task.
	if r := do(t, h, "POST", "/api/v2/export-jobs", op, jobBody(c.ID, t0.Add(-5*time.Hour), t0.Add(-4*time.Hour), "evidence")); r.Code != 404 {
		t.Errorf("no recording: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, "GET", "/api/v2/export-jobs/nope", op, ""); r.Code != 404 {
		t.Errorf("unknown job: %d", r.Code)
	}
}

func TestV2ExportJobCancel(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	s.exportJobs.pollEvery = 10 * time.Millisecond
	h := s.Handler()
	op := v2Token(t, s, "op", "Operator")
	// A slow ffmpeg, so the job is still running when we cancel it.
	slow := "#!/bin/sh\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(s.Store.DataDir(), "bin", "ffmpeg"), []byte(slow), 0o755); err != nil {
		t.Fatal(err)
	}
	code, job := v2Call(t, h, "POST", "/api/v2/export-jobs", op, jobBody(c.ID, t0, t0.Add(3*time.Minute), "evidence"))
	if code != 201 {
		t.Fatalf("start: %d %v", code, job)
	}
	id := job["taskId"].(string)
	code, job = v2Call(t, h, "DELETE", "/api/v2/export-jobs/"+id, op, "")
	if code != 200 || job["state"] != "canceled" {
		t.Fatalf("cancel: %d %v", code, job)
	}
	if _, list := v2Call(t, h, "GET", "/api/v2/evidence", op, ""); len(items(t, list)) != 0 {
		t.Fatal("a canceled job filed evidence")
	}
	// The recording server cleaned its temp pieces.
	deadline := time.Now().Add(5 * time.Second)
	dir := filepath.Join(s.Store.DataDir(), "exports-long")
	for {
		left, _ := os.ReadDir(dir)
		empty := true
		for _, e := range left {
			if sub, _ := os.ReadDir(filepath.Join(dir, e.Name())); len(sub) > 0 {
				empty = false
			}
		}
		if empty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("temp pieces left in %s", dir)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Cancelling again is harmless.
	if code, job = v2Call(t, h, "DELETE", "/api/v2/export-jobs/"+id, op, ""); code != 200 || job["state"] != "canceled" {
		t.Fatalf("second cancel: %d %v", code, job)
	}
}

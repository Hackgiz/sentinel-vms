package server

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/onvif/fakecam"
	"sentinel-linux/internal/playback"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

// edgeFixture is playbackFixture (two one-minute segments with a 30 s hole
// between them) with the camera behind a Profile G fakecam and a fake
// ffmpeg that "replays" 30 s of synthetic recording.
func edgeFixture(t *testing.T) (*Server, *store.Camera, time.Time) {
	t.Helper()
	s, c, t0 := playbackFixture(t)
	s.edgeJobs.pollEvery = 10 * time.Millisecond
	cam := &fakecam.Camera{User: "admin", Pass: "pw", Manufacturer: "Acme", Model: "G1",
		Streams: []fakecam.Stream{{Token: "main", Name: "Main", Encoding: "H264", Width: 1920, Height: 1080, FPS: 25, RTSPURL: "rtsp://127.0.0.1:1/live"}}}
	cam.EnableProfileG(t0.Add(-time.Hour), t0.Add(time.Hour))
	ln, err := net.Listen("tcp", "127.0.0.1:0") // OS-chosen free port
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	cam.ServeReplay(ln)
	srv := httptest.NewServer(cam)
	t.Cleanup(srv.Close)

	onvifURL, pw, off := srv.URL+"/onvif/device_service", "pw", false
	c, err = s.Store.SaveCamera(context.Background(), c.ID, store.CameraInput{Name: c.Name, RTSPURL: c.RTSPURL, Recording: true,
		Username: "admin", Password: &pw, ONVIFURL: &onvifURL, CameraAnalytics: &off})
	if err != nil {
		t.Fatal(err)
	}
	syncCams(t, s)

	bin := filepath.Join(s.Store.DataDir(), "bin")
	src := filepath.Join(bin, "replay.mp4")
	if err := os.WriteFile(src, playback.SynthSegment(30, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nfor a; do out=\"$a\"; done\n[ \"$out\" = \"-encoders\" ] && exit 0\ncp " + src + " \"$out\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return s, c, t0
}

func waitEdgeJob(t *testing.T, s *Server, id string) {
	t.Helper()
	s.edgeJobs.mu.Lock()
	j := s.edgeJobs.jobs[id]
	s.edgeJobs.mu.Unlock()
	if j == nil {
		t.Fatalf("no job %s", id)
	}
	select {
	case <-j.done:
	case <-time.After(20 * time.Second):
		t.Fatal("recovery job did not finish")
	}
}

func edgeBody(from, to time.Time) string {
	return fmt.Sprintf(`{"fromMs":%d,"toMs":%d}`, from.UnixMilli(), to.UnixMilli())
}

func TestV2EdgeRecoveryEndToEnd(t *testing.T) {
	s, c, t0 := edgeFixture(t)
	h := s.Handler()
	op := v2Token(t, s, "op", "Operator")

	// The camera payload says it can (the probe runs when the config applies).
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, cam := v2Call(t, h, "GET", "/api/v2/cameras/"+c.ID, op, "")
		if cam["edgeRecording"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("edgeRecording never true: %v", cam)
		}
		time.Sleep(20 * time.Millisecond)
	}

	code, job := v2Call(t, h, "POST", "/api/v2/cameras/"+c.ID+"/edge-recovery", op, edgeBody(t0, t0.Add(150*time.Second)))
	if code != 201 || job["taskId"] == nil || job["gaps"] != float64(1) {
		t.Fatalf("start: %d %v", code, job)
	}
	id := job["taskId"].(string)
	waitEdgeJob(t, s, id)
	code, job = v2Call(t, h, "GET", "/api/v2/edge-recovery/"+id, op, "")
	if code != 200 || job["state"] != "done" || job["progress"] != float64(100) || job["recovered"] != float64(1) || job["seconds"] != float64(30) {
		t.Fatalf("status: %d %v", code, job)
	}

	// The timeline now has three spans; the recovered one says so.
	_, tl := v2Call(t, h, "GET", fmt.Sprintf("/api/v2/cameras/%s/timeline?from=%d&to=%d", c.ID, t0.Add(-time.Hour).UnixMilli(), t0.Add(time.Hour).UnixMilli()), op, "")
	spans, _ := tl["spans"].([]any)
	if len(spans) != 3 {
		t.Fatalf("timeline spans %v", tl["spans"])
	}
	for i, sp := range spans {
		edge := sp.(map[string]any)["edge"] == true
		if edge != (i == 1) {
			t.Errorf("span %d edge=%v: %v", i, edge, sp)
		}
	}
	if sp := spans[1].(map[string]any); sp["start"] != float64(t0.Add(time.Minute).UnixMilli()) {
		t.Errorf("recovered span starts %v", sp["start"])
	}

	// Tracked as a task, audited at start and end.
	found := false
	for _, tk := range tasks.Default.List() {
		if tk.ID == id {
			found = tk.State == tasks.Done && strings.HasPrefix(tk.Title, "Edge recovery: Door")
		}
	}
	if !found {
		t.Errorf("task %s missing or wrong: %+v", id, tasks.Default.List())
	}
	logs, _ := s.Store.AuditLog(t.Context(), 50)
	var started, recovered bool
	for _, e := range logs {
		started = started || (e.Action == "Started edge recovery" && strings.Contains(e.Detail, "Door") && strings.Contains(e.Detail, "1 gap"))
		recovered = recovered || (e.Action == "Recovered footage from camera" && strings.Contains(e.Detail, "1 segment"))
	}
	if !started || !recovered {
		t.Errorf("audit rows missing (started %v, recovered %v)", started, recovered)
	}
}

func TestV2EdgeRecoveryPermissionScopeValidation(t *testing.T) {
	s, c, t0 := edgeFixture(t)
	h := s.Handler()
	ctx := t.Context()
	op := v2Token(t, s, "op", "Operator")
	other := v2Token(t, s, "other", "Operator")
	viewer := v2Token(t, s, "viewer", "Viewer")
	scoped := v2Token(t, s, "scoped", "Operator")
	plain, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Plain", RTSPURL: "rtsp://10.0.0.7/s"})
	g, _ := s.Store.SaveCameraGroup(ctx, "", "Plain only", []string{plain.ID})
	u, _ := s.Store.UserByName(ctx, "scoped")
	if err := s.Store.SetUserScope(ctx, u.ID, store.UserScope{GroupIDs: []string{g.ID}}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v2/cameras/" + c.ID + "/edge-recovery"
	body := edgeBody(t0, t0.Add(150*time.Second))

	if r := do(t, h, "POST", path, viewer, body); r.Code != 403 {
		t.Errorf("viewer: %d", r.Code)
	}
	if r := do(t, h, "POST", path, scoped, body); r.Code != 404 {
		t.Errorf("out of scope: %d", r.Code)
	}
	if r := do(t, h, "POST", "/api/v2/cameras/nope/edge-recovery", op, body); r.Code != 404 {
		t.Errorf("unknown camera: %d", r.Code)
	}
	if r := do(t, h, "POST", path, op, edgeBody(t0, t0.Add(25*time.Hour))); r.Code != 400 {
		t.Errorf("over 24 h: %d", r.Code)
	}
	if r := do(t, h, "POST", path, op, edgeBody(t0, t0)); r.Code != 400 {
		t.Errorf("empty range: %d", r.Code)
	}
	if r := do(t, h, "POST", "/api/v2/cameras/"+plain.ID+"/edge-recovery", op, body); r.Code != 400 || !strings.Contains(r.Body.String(), "ONVIF") {
		t.Errorf("camera without ONVIF: %d %s", r.Code, r.Body)
	}

	code, job := v2Call(t, h, "POST", path, op, body)
	if code != 201 {
		t.Fatalf("start: %d %v", code, job)
	}
	id := job["taskId"].(string)
	waitEdgeJob(t, s, id)
	// Only the user who started it sees it; scope applies too.
	if r := do(t, h, "GET", "/api/v2/edge-recovery/"+id, other, ""); r.Code != 404 {
		t.Errorf("another user's job: %d", r.Code)
	}
	if r := do(t, h, "GET", "/api/v2/edge-recovery/"+id, viewer, ""); r.Code != 403 {
		t.Errorf("viewer reading a job: %d", r.Code)
	}
	if r := do(t, h, "DELETE", "/api/v2/edge-recovery/"+id, op, ""); r.Code != 200 {
		t.Errorf("cancel a finished job: %d", r.Code)
	}
}

func TestV2EdgeRecoveryCancel(t *testing.T) {
	s, c, t0 := edgeFixture(t)
	slow := "#!/bin/sh\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(s.Store.DataDir(), "bin", "ffmpeg"), []byte(slow), 0o755); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	op := v2Token(t, s, "op", "Operator")
	code, job := v2Call(t, h, "POST", "/api/v2/cameras/"+c.ID+"/edge-recovery", op, edgeBody(t0, t0.Add(150*time.Second)))
	if code != 201 {
		t.Fatalf("start: %d %v", code, job)
	}
	id := job["taskId"].(string)
	code, job = v2Call(t, h, "DELETE", "/api/v2/edge-recovery/"+id, op, "")
	if code != 200 || job["state"] != "canceled" {
		t.Fatalf("cancel: %d %v", code, job)
	}
	logs, _ := s.Store.AuditLog(t.Context(), 50)
	canceled := false
	for _, e := range logs {
		canceled = canceled || e.Action == "Canceled edge recovery"
	}
	if !canceled {
		t.Error("cancel not audited")
	}
	recOf(s).Wait()
	all, _ := os.ReadDir(filepath.Join(recOf(s).RecordingsRoot(), c.ID))
	var entries []os.DirEntry
	for _, e := range all {
		if strings.HasSuffix(e.Name(), ".mp4") { // not the playback index sidecars (.mp4.idx)
			entries = append(entries, e)
		}
	}
	if len(entries) != 2 {
		t.Fatalf("recordings changed after cancel: %v", entries)
	}
}

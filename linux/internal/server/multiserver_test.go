package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/link/inproc"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/playback"
	"sentinel-linux/internal/recorder"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/videotoken"
)

// addRecorder enrolls (in the registry) and connects a second in-process
// recording server under id, with its own data directory, the way the
// control link registers a remote one.
func addRecorder(t *testing.T, s *Server, id, name string) *recorder.Recorder {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	rst, err := recorder.OpenStore(filepath.Join(dir, "recorder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rst.DB.Close() })
	sup := &media.Supervisor{ConfigPath: filepath.Join(dir, "mediamtx.yml"), RecordingsRoot: filepath.Join(dir, "rec"), Ports: media.DefaultPorts}
	rec := recorder.New(dir, sup, rst, "test-"+id, "")
	rec.ServerID = id
	rec.Ctx = ctx
	t.Cleanup(rec.Wait)
	if _, err := s.Store.RecordingServer(ctx, id); errors.Is(err, store.ErrNotFound) {
		if err := s.Store.AddRecordingServer(ctx, &store.RecordingServer{ID: id, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	ms, sink := inproc.Connect(rec, s.Sink())
	s.ConnectRecorder(id, ms)
	rec.Attach(sink)
	if err := s.ApplyRecorder(ctx, id); err != nil {
		t.Fatal(err)
	}
	return rec
}

// writeSegment puts a 60 s recording of camID starting at start on rec.
func writeSegment(t *testing.T, rec *recorder.Recorder, camID string, start time.Time) string {
	t.Helper()
	dir := filepath.Join(rec.RecordingsRoot(), camID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := start.Format("20060102-150405") + "-000000.mp4"
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, playback.SynthSegment(60, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	end := start.Add(time.Minute)
	_ = os.Chtimes(p, end, end)
	return name
}

// setPlacements rewrites a camera's placement history: local until split,
// then rs (Unix s).
func setPlacements(t *testing.T, s *Server, camID, rs string, split time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := s.Store.SetCameraRecorder(ctx, camID, rs); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE camera_recorder_history SET from_at = 0, to_at = ? WHERE camera_id = ? AND recorder_id = 'local'`, []any{split.Unix(), camID}},
		{`UPDATE camera_recorder_history SET from_at = ? WHERE camera_id = ? AND recorder_id = ?`, []any{split.Unix(), camID, rs}},
	} {
		if _, err := s.Store.DB.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCameraAssignAndMove(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	rs2 := addRecorder(t, s, "rs2", "Second")
	admin := v2Token(t, s, "admin", "Admin")
	_ = s.Store.AddRecordingServer(ctx, &store.RecordingServer{ID: "gone", Name: "Gone"})
	_ = s.Store.RevokeRecordingServer(ctx, "gone")

	body := func(extra string) string { return `{"name":"Gate","rtspUrl":"rtsp://10.0.0.5/s"` + extra + `}` }
	code, cam := v2Call(t, h, "POST", "/api/v2/cameras", admin, body(`,"recorderId":"rs2"`))
	if code != 201 || cam["recorderId"] != "rs2" {
		t.Fatalf("create on rs2: %d %v", code, cam)
	}
	id := cam["id"].(string)
	if _, ok := rs2.Status().Cameras[id]; !ok {
		t.Fatal("rs2 didn't get the camera in its snapshot")
	}
	if _, ok := recOf(s).Status().Cameras[id]; ok {
		t.Fatal("the local server got rs2's camera")
	}
	for _, bad := range []string{"nope", "gone"} {
		if code, m := v2Call(t, h, "POST", "/api/v2/cameras", admin, body(`,"recorderId":"`+bad+`"`)); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("create on %s: %d %v", bad, code, m)
		}
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/cameras", admin, body("")); code != 201 || m["recorderId"] != "local" {
		t.Fatalf("default placement: %d %v", code, m)
	}

	// Move rs2 → local: both servers get new snapshots, history kept, audited.
	if code, m := v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, `{"recorderId":"gone"}`); code != 400 {
		t.Fatalf("move to revoked: %d %v", code, m)
	}
	code, moved := v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, `{"recorderId":"local","name":"Gate 2"}`)
	if code != 200 || moved["recorderId"] != "local" || moved["name"] != "Gate 2" {
		t.Fatalf("move: %d %v", code, moved)
	}
	if _, ok := rs2.Status().Cameras[id]; ok {
		t.Fatal("rs2 still has the moved camera")
	}
	if _, ok := recOf(s).Status().Cameras[id]; !ok {
		t.Fatal("local didn't get the moved camera")
	}
	ps, _ := s.Store.CameraPlacements(ctx, id)
	if len(ps) != 2 || ps[0].RecorderID != "rs2" || ps[0].To == 0 || ps[1].RecorderID != "local" || ps[1].To != 0 {
		t.Fatalf("placements %+v", ps)
	}
	if !auditActions(t, s, "Cameras")["Moved camera"] {
		t.Fatal("move not audited")
	}

	// A camera on a disconnected server says so (v2 and v1).
	if code, m := v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, `{"recorderId":"rs2"}`); code != 200 {
		t.Fatalf("move back: %d %v", code, m)
	}
	s.ConnectRecorder("rs2", nil)
	_, got := v2Call(t, h, "GET", "/api/v2/cameras/"+id, admin, "")
	if st := got["status"].(map[string]any); st["message"] != "Recording server offline" || st["online"] != false {
		t.Fatalf("offline server: %v", got)
	}
	web := signIn(t, s, "web", "Admin")
	var v1 []map[string]any
	_ = json.Unmarshal(doWeb(t, h, web, "GET", "/api/v1/cameras", "").Body.Bytes(), &v1)
	for _, c := range v1 {
		want := "Recording server offline"
		if c["id"] != id {
			want = "Media engine starting" // local, connected, no MediaMTX in tests
		}
		if c["status"] != want {
			t.Fatalf("v1 camera %v: status %v, want %s", c["id"], c["status"], want)
		}
	}
}

// multiFootage: a camera recorded on local until t0+1h, then on rs2; one
// segment on each (t0 and t0+2h).
func multiFootage(t *testing.T) (s *Server, rs2 *recorder.Recorder, c *store.Camera, t0 time.Time, localSeg, rs2Seg string) {
	t.Helper()
	s = newCompanionServer(t)
	rs2 = addRecorder(t, s, "rs2", "Second")
	c = addCam(t, s)
	t0 = time.Date(2026, 10, 9, 9, 0, 0, 0, time.Local)
	localSeg = writeSegment(t, recOf(s), c.ID, t0)
	rs2Seg = writeSegment(t, rs2, c.ID, t0.Add(2*time.Hour))
	setPlacements(t, s, c.ID, "rs2", t0.Add(time.Hour))
	syncCams(t, s)
	c, _ = s.Store.Camera(context.Background(), c.ID)
	return
}

func TestFootageAcrossServers(t *testing.T) {
	s, _, c, t0, localSeg, rs2Seg := multiFootage(t)
	h := s.Handler()
	viewer := v2Token(t, s, "viewer", "Viewer")
	q := fmt.Sprintf("?from=%d&to=%d", t0.Add(-time.Hour).UnixMilli(), t0.Add(3*time.Hour).UnixMilli())

	code, tl := v2Call(t, h, "GET", "/api/v2/cameras/"+c.ID+"/timeline"+q, viewer, "")
	spans, _ := tl["spans"].([]any)
	if code != 200 || len(spans) != 2 {
		t.Fatalf("timeline: %d %v", code, tl)
	}
	first, second := spans[0].(map[string]any), spans[1].(map[string]any)
	if first["recorderId"] != "local" || first["start"] != float64(t0.UnixMilli()) ||
		second["recorderId"] != "rs2" || second["start"] != float64(t0.Add(2*time.Hour).UnixMilli()) || second["media"] != 0.0 {
		t.Fatalf("spans %v", spans)
	}
	code, m := v2Call(t, h, "GET", "/api/v2/cameras/"+c.ID+"/recordings", viewer, "")
	segs := items(t, m)
	if code != 200 || len(segs) != 2 || segs[0]["name"] != rs2Seg || segs[0]["recorderId"] != "rs2" || segs[1]["recorderId"] != "local" {
		t.Fatalf("recordings: %d %v", code, m)
	}

	// v1 finds each file on its server.
	web := signIn(t, s, "web", "Viewer")
	for _, f := range []string{localSeg, rs2Seg} {
		if rec := doWeb(t, h, web, "GET", "/recordings/"+c.ID+"/"+f, ""); rec.Code != 200 || rec.Body.Len() == 0 {
			t.Fatalf("v1 segment %s: %d", f, rec.Code)
		}
	}
	if rec := doWeb(t, h, web, "GET", "/api/v1/cameras/"+c.ID+"/timeline"+q, ""); rec.Code != 200 || strings.Count(rec.Body.String(), `"recorderId"`) != 2 {
		t.Fatalf("v1 timeline: %d %s", rec.Code, rec.Body)
	}

	// With rs2 offline, what's left is still served.
	s.ConnectRecorder("rs2", nil)
	_, tl = v2Call(t, h, "GET", "/api/v2/cameras/"+c.ID+"/timeline"+q, viewer, "")
	if spans := tl["spans"].([]any); len(spans) != 1 || spans[0].(map[string]any)["recorderId"] != "local" {
		t.Fatalf("timeline with rs2 offline: %v", tl)
	}
	if _, m := v2Call(t, h, "GET", "/api/v2/cameras/"+c.ID+"/recordings", viewer, ""); len(items(t, m)) != 1 {
		t.Fatalf("recordings with rs2 offline: %v", m)
	}
}

func TestVideoTokensAcrossServers(t *testing.T) {
	s, _, c, t0, localSeg, rs2Seg := multiFootage(t)
	h := s.Handler()
	admin := v2Token(t, s, "admin", "Admin")
	viewer := v2Token(t, s, "viewer", "Viewer")
	signer, _ := s.videoSigner(context.Background())
	aud := func(tok string) string {
		c, err := videotoken.NewVerifier(tokenAudience(tok), signer.PublicKey()).Verify(tok)
		if err != nil {
			t.Fatal(err)
		}
		return c.Audience
	}

	// Default: the camera's current server, through the gateway.
	code, m := videoToken(t, s, viewer, c.ID, "")
	tok, _ := m["token"].(string)
	if code != 200 || m["recorderId"] != "rs2" || aud(tok) != "rs2" || m["baseUrl"] != "/v/"+tok || m["gatewayUrl"] != "/v/"+tok {
		t.Fatalf("default token: %d %v", code, m)
	}
	// Older footage: a placement, playback/export only.
	code, m = videoToken(t, s, viewer, c.ID, `{"rights":["playback"],"recorderId":"local"}`)
	old, _ := m["token"].(string)
	if code != 200 || m["recorderId"] != "local" || aud(old) != "local" {
		t.Fatalf("token for local footage: %d %v", code, m)
	}
	for _, bad := range []string{`{"recorderId":"local"}`, `{"rights":["live","playback"],"recorderId":"local"}`, `{"rights":["playback"],"recorderId":"nope"}`} {
		if code, m := videoToken(t, s, viewer, c.ID, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("%s: %d %v", bad, code, m)
		}
	}

	// The gateway forwards to the token's server.
	_, m = videoToken(t, s, viewer, c.ID, `{"rights":["playback"]}`)
	cur := m["token"].(string)
	for _, tc := range []struct {
		tok, file string
		code      int
	}{{cur, rs2Seg, 200}, {old, localSeg, 200}, {cur, localSeg, 404}, {old, rs2Seg, 404}} {
		if w := gatewayGet(h, "/v/"+tc.tok+"/recordings/"+c.ID+"/"+tc.file); w.Code != tc.code {
			t.Errorf("gateway %s via %s: %d, want %d", tc.file, aud(tc.tok), w.Code, tc.code)
		}
	}
	q := fmt.Sprintf("?from=%d&to=%d", t0.Add(-time.Hour).UnixMilli(), t0.Add(3*time.Hour).UnixMilli())
	if w := gatewayGet(h, "/v/"+old+"/api/v1/cameras/"+c.ID+"/playback.m3u8"+q); w.Code != 200 || !strings.Contains(w.Body.String(), localSeg) || strings.Contains(w.Body.String(), rs2Seg) {
		t.Fatalf("local playlist: %d %s", w.Code, w.Body)
	}

	// With a public URL, clients go to the recording server directly.
	if code, m := v2Call(t, h, "PATCH", "/api/v2/recording-servers/rs2", admin, `{"publicUrl":"https://rs2.lan:8090/"}`); code != 200 || m["publicUrl"] != "https://rs2.lan:8090" {
		t.Fatalf("set public URL: %d %v", code, m)
	}
	_, m = videoToken(t, s, viewer, c.ID, "")
	tok = m["token"].(string)
	if m["baseUrl"] != "https://rs2.lan:8090/v/"+tok || m["gatewayUrl"] != "/v/"+tok {
		t.Fatalf("token with public URL: %v", m)
	}

	// A server that isn't connected: no token; a revoked one: the gateway refuses its tokens.
	s.ConnectRecorder("rs2", nil)
	if code, m := videoToken(t, s, viewer, c.ID, ""); code != 503 || errCode(m) != codeUnavailable {
		t.Fatalf("offline server: %d %v", code, m)
	}
	if w := gatewayGet(h, "/v/"+cur+"/recordings/"+c.ID+"/"+rs2Seg); w.Code != 503 {
		t.Fatalf("gateway to offline server: %d", w.Code)
	}
	_ = s.Store.RevokeRecordingServer(context.Background(), "rs2")
	if w := gatewayGet(h, "/v/"+cur+"/recordings/"+c.ID+"/"+rs2Seg); w.Code != 401 {
		t.Fatalf("gateway to revoked server: %d", w.Code)
	}
}

func TestEvidenceAndThumbnailsOnTheirServer(t *testing.T) {
	s, rs2, c, t0, _, _ := multiFootage(t)
	h := s.Handler()
	ctx := context.Background()
	op := v2Token(t, s, "op", "Operator")

	// The event at t0+2h was reported by rs2, which keeps its snapshot.
	dir := filepath.Join(rs2.DataDir, "events")
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "snap2.jpg"), []byte("\xff\xd8rs2"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &store.Event{CameraID: c.ID, Kind: "Person", CreatedAt: t0.Add(2*time.Hour + 5*time.Second).Unix(), Thumbnail: filepath.Join(dir, "snap2.jpg")}
	_ = s.Store.AddEvent(ctx, e)
	if rec := do(t, h, "GET", "/api/v2/events/"+e.ID+"/thumbnail.jpg", op, ""); rec.Code != 200 || rec.Body.String() != "\xff\xd8rs2" {
		t.Fatalf("thumbnail: %d %q", rec.Code, rec.Body)
	}
	if jpeg, ok := s.eventThumbnail(ctx, e, 1<<20); !ok || string(jpeg) != "\xff\xd8rs2" {
		t.Fatalf("eventThumbnail %q %v", jpeg, ok)
	}

	// Evidence is cut on, recorded against and served from the server
	// that has the alarm's footage.
	for _, at := range []time.Time{t0.Add(20 * time.Second), t0.Add(2*time.Hour + 20*time.Second)} {
		a, _, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: c.ID, CameraName: c.Name, Kind: "Person", Title: "Person", Severity: "Critical", At: at, MergeSince: at, Origin: "test"})
		if err != nil {
			t.Fatal(err)
		}
		code, ev := v2Call(t, h, "POST", "/api/v2/alarms/"+a.ID+"/evidence", op, "")
		if code != 200 {
			t.Fatalf("lock at %v: %d %v", at, code, ev)
		}
		want := "local"
		if at.After(t0.Add(time.Hour)) {
			want = "rs2"
		}
		if rid, err := s.Store.EvidenceRecorderID(ctx, ev["id"].(string)); err != nil || rid != want {
			t.Fatalf("evidence on %q (%v), want %s", rid, err, want)
		}
		web := signIn(t, s, "web"+want, "Admin")
		if rec := doWeb(t, h, web, "GET", "/api/v1/evidence/"+ev["id"].(string)+"/file", ""); rec.Code != 200 || rec.Header().Get("X-Content-SHA256") != ev["sha256"] {
			t.Fatalf("evidence file from %s: %d", want, rec.Code)
		}
		if clips := s.hasClips(ctx, []store.Alarm{*a}); !clips[0] {
			t.Fatalf("hasClip on %s", want)
		}
	}
}

// fakeControl stands in for the control link listener.
type fakeControl struct{ secrets, dropped []string }

func (f *fakeControl) EnrollmentToken(secret string) string {
	f.secrets = append(f.secrets, secret)
	return "sen1." + secret + ".fp"
}
func (f *fakeControl) EnrollCommand(tok string) string {
	return "sentinel --role recording --management https://ms:8091 --enroll " + tok
}
func (f *fakeControl) Disconnect(id string) { f.dropped = append(f.dropped, id) }

func TestRecordingServersAPI(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	rec2 := addRecorder(t, s, "rs2", "Second")
	_ = s.Store.AddRecordingServer(ctx, &store.RecordingServer{ID: "rs3", Name: "Third", Version: "0.9"})
	admin, viewer := v2Token(t, s, "admin", "Admin"), v2Token(t, s, "viewer", "Viewer")
	cam := addCam(t, s)
	_ = s.Store.SetCameraRecorder(ctx, cam.ID, "rs2")
	if _, m := v2Call(t, h, "GET", "/api/v2/recording-servers/rs2", viewer, ""); m["recordingsDir"] != "" || m["storage"].(map[string]any)["recordingsDir"] != rec2.RecordingsRoot() {
		t.Fatalf("default recordings folder %v", m)
	}

	code, m := v2Call(t, h, "GET", "/api/v2/recording-servers", viewer, "")
	list := items(t, m)
	if code != 200 || len(list) != 3 || list[0]["id"] != "local" || list[0]["local"] != true {
		t.Fatalf("list: %d %v", code, m)
	}
	byID := map[string]map[string]any{}
	for _, rs := range list {
		byID[rs["id"].(string)] = rs
	}
	if rs := byID["rs2"]; rs["online"] != true || rs["cameras"] != 1.0 || rs["version"] != "test-rs2" || rs["storage"] == nil || rs["local"] != false || !isMs(rs["lastSeenAt"]) {
		t.Fatalf("rs2 %v", rs)
	}
	if rs := byID["rs3"]; rs["online"] != false || rs["storage"] != nil || rs["version"] != "0.9" || rs["revoked"] != false {
		t.Fatalf("rs3 %v", rs)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/recording-servers/rs2", viewer, ""); code != 200 || m["name"] != "Second" {
		t.Fatalf("get: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/recording-servers/nope", viewer, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("get unknown: %d %v", code, m)
	}

	// Edit.
	if code, _ := v2Call(t, h, "PATCH", "/api/v2/recording-servers/rs2", viewer, `{"name":"x"}`); code != 403 {
		t.Fatalf("viewer patch: %d", code)
	}
	for _, bad := range []string{`{"name":" "}`, `{"publicUrl":"ftp://x"}`, `{"publicUrl":"https://u:p@x"}`, `{"publicUrl":"rs2.lan"}`, `{"other":1}`} {
		if code, m := v2Call(t, h, "PATCH", "/api/v2/recording-servers/rs2", admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("patch %s: %d %v", bad, code, m)
		}
	}
	if code, m := v2Call(t, h, "PATCH", "/api/v2/recording-servers/rs2", admin, `{"name":"Warehouse","publicUrl":"http://10.0.0.2:8090"}`); code != 200 || m["name"] != "Warehouse" || m["publicUrl"] != "http://10.0.0.2:8090" {
		t.Fatalf("patch: %d %v", code, m)
	}

	// Revoke.
	ctl := &fakeControl{}
	s.Control = ctl
	if code, m := v2Call(t, h, "DELETE", "/api/v2/recording-servers/local", admin, ""); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("revoke local: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/recording-servers/rs2", admin, ""); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if s.recorderConnected("rs2") || len(ctl.dropped) != 1 || ctl.dropped[0] != "rs2" {
		t.Fatalf("revoked server still connected (%v)", ctl.dropped)
	}
	if _, m := v2Call(t, h, "GET", "/api/v2/recording-servers/rs2", viewer, ""); m["revoked"] != true || m["online"] != false {
		t.Fatalf("after revoke %v", m)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/recording-servers/rs2", admin, ""); code != 204 {
		t.Fatalf("revoke again: %d", code)
	}
	if c, _ := s.Store.Camera(ctx, cam.ID); c.RecorderID != "rs2" {
		t.Fatal("revoking moved the camera")
	}

	// Remove: only a revoked server with no cameras, never the local one.
	if code, m := v2Call(t, h, "DELETE", "/api/v2/recording-servers/local?remove=true", admin, ""); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("remove local: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "DELETE", "/api/v2/recording-servers/rs3?remove=true", admin, ""); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("remove active: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "DELETE", "/api/v2/recording-servers/rs2?remove=true", admin, ""); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("remove with cameras: %d %v", code, m)
	}
	_ = s.Store.SetCameraRecorder(ctx, cam.ID, "local")
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/recording-servers/rs2?remove=true", viewer, ""); code != 403 {
		t.Fatalf("viewer remove: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/recording-servers/rs2?remove=true", admin, ""); code != 204 {
		t.Fatalf("remove: %d", code)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/recording-servers/rs2", viewer, ""); code != 404 {
		t.Fatalf("removed server still listed: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/recording-servers/rs2?remove=true", admin, ""); code != 404 {
		t.Fatalf("remove twice: %d", code)
	}
	if _, m := v2Call(t, h, "GET", "/api/v2/recording-servers", viewer, ""); len(items(t, m)) != 2 {
		t.Fatalf("after remove %v", m)
	}

	// Enrollment tokens.
	code, m = v2Call(t, h, "POST", "/api/v2/recording-servers/enrollment-tokens", admin, `{"name":"Dock"}`)
	if code != 201 || !strings.HasPrefix(m["token"].(string), "sen1.") || !strings.Contains(m["command"].(string), m["token"].(string)) || !isMs(m["expiresAt"]) {
		t.Fatalf("enrollment token: %d %v", code, m)
	}
	if exp := time.UnixMilli(int64(m["expiresAt"].(float64))); exp.Sub(time.Now()) > time.Hour || exp.Sub(time.Now()) < 59*time.Minute {
		t.Fatalf("expires %v", exp)
	}
	if tok, err := s.Store.ConsumeEnrollmentToken(ctx, ctl.secrets[0], "rs4"); err != nil || tok.Name != "Dock" || tok.CreatedBy != "admin" {
		t.Fatalf("stored token %+v %v", tok, err)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/recording-servers/enrollment-tokens", admin, ""); code != 201 {
		t.Fatalf("token without a body: %d", code)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/recording-servers/enrollment-tokens", viewer, ""); code != 403 {
		t.Fatalf("viewer token: %d", code)
	}
	s.Control = nil
	if code, m := v2Call(t, h, "POST", "/api/v2/recording-servers/enrollment-tokens", admin, ""); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("no control link: %d %v", code, m)
	}
	got := auditActions(t, s, "Servers")
	for _, a := range []string{"Edited recording server", "Revoked recording server", "Created enrollment token"} {
		if !got[a] {
			t.Errorf("no audit entry %q", a)
		}
	}
}

func TestRecordingServersHidesUnusedLocal(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, nil, "test") // --role management
	s.Ctx = context.Background()
	addRecorder(t, s, "rs2", "Second")
	viewer := v2Token(t, s, "viewer", "Viewer")
	_, m := v2Call(t, s.Handler(), "GET", "/api/v2/recording-servers", viewer, "")
	if list := items(t, m); len(list) != 1 || list[0]["id"] != "rs2" {
		t.Fatalf("list %v", m)
	}
	// The only enrolled server is where new cameras go.
	admin := v2Token(t, s, "admin", "Admin")
	if code, c := v2Call(t, s.Handler(), "POST", "/api/v2/cameras", admin, `{"name":"Gate","rtspUrl":"rtsp://10.0.0.5/s"}`); code != 201 || c["recorderId"] != "rs2" {
		t.Fatalf("default placement: %d %v", code, c)
	}
	// With two, the client must choose.
	addRecorder(t, s, "rs3", "Third")
	if code, m := v2Call(t, s.Handler(), "POST", "/api/v2/cameras", admin, `{"name":"Gate","rtspUrl":"rtsp://10.0.0.5/s"}`); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("ambiguous placement: %d %v", code, m)
	}
}

func TestSavedViews(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	c1, c2 := addCam(t, s), addCam(t, s)
	op, other := v2Token(t, s, "op", "Operator"), v2Token(t, s, "other", "Operator")

	view := func(tiles string) string {
		return `{"name":"Lobby","columns":2,"rows":2,"tiles":[` + tiles + `]}`
	}
	tile := func(cam string, x, y int, wh string) string {
		return fmt.Sprintf(`{"cameraId":%q,"x":%d,"y":%d%s}`, cam, x, y, wh)
	}
	code, v := v2Call(t, h, "POST", "/api/v2/views", op, view(tile(c1.ID, 0, 0, `,"w":2`)+","+tile(c2.ID, 1, 1, "")))
	if code != 201 || v["name"] != "Lobby" || !isMs(v["createdAt"]) || len(v["tiles"].([]any)) != 2 {
		t.Fatalf("create: %d %v", code, v)
	}
	id := v["id"].(string)
	if t2 := v["tiles"].([]any)[1].(map[string]any); t2["w"] != 1.0 || t2["h"] != 1.0 {
		t.Fatalf("tile defaults %v", t2)
	}
	for name, bad := range map[string]string{
		"outside grid":   view(tile(c1.ID, 1, 0, `,"w":2`)),
		"negative":       view(tile(c1.ID, -1, 0, "")),
		"unknown camera": view(tile("nope", 0, 0, "")),
		"no name":        `{"name":"","columns":1,"rows":1,"tiles":[]}`,
		"big grid":       `{"name":"x","columns":9,"rows":1,"tiles":[]}`,
		"no tiles":       `{"name":"x","columns":1,"rows":1}`,
		"unknown field":  `{"name":"x","columns":1,"rows":1,"tiles":[],"color":"red"}`,
	} {
		if code, m := v2Call(t, h, "POST", "/api/v2/views", op, bad); code != 400 || errCode(m) != "bad_request" {
			t.Errorf("%s: %d %v", name, code, m)
		}
	}
	// Cameras outside the user's scope can't be put in a view.
	g, err := s.Store.SaveCameraGroup(ctx, "", "Front", []string{c1.ID})
	if err != nil {
		t.Fatal(err)
	}
	ou, _ := s.Store.UserByName(ctx, "other")
	_ = s.Store.SetUserScope(ctx, ou.ID, store.UserScope{GroupIDs: []string{g.ID}})
	if code, m := v2Call(t, h, "POST", "/api/v2/views", other, view(tile(c2.ID, 0, 0, ""))); code != 400 {
		t.Fatalf("out-of-scope camera: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/views", other, view(tile(c1.ID, 0, 0, ""))); code != 201 {
		t.Fatalf("in-scope camera: %d", code)
	}

	// Views are per user.
	if _, m := v2Call(t, h, "GET", "/api/v2/views", other, ""); len(items(t, m)) != 1 {
		t.Fatalf("other's views %v", m)
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/views/"+id, other, view("")); code != 404 {
		t.Fatalf("other user's PUT: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/views/"+id, other, ""); code != 404 {
		t.Fatalf("other user's DELETE: %d", code)
	}
	code, v = v2Call(t, h, "PUT", "/api/v2/views/"+id, op, `{"name":"Lobby 2","columns":1,"rows":1,"tiles":[]}`)
	if code != 200 || v["name"] != "Lobby 2" || v["id"] != id || len(v["tiles"].([]any)) != 0 {
		t.Fatalf("replace: %d %v", code, v)
	}
	if _, m := v2Call(t, h, "GET", "/api/v2/views", op, ""); len(items(t, m)) != 1 || items(t, m)[0]["name"] != "Lobby 2" {
		t.Fatalf("list %v", m)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/views/"+id, op, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, m := v2Call(t, h, "DELETE", "/api/v2/views/"+id, op, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("delete again: %d %v", code, m)
	}
}

func TestStreamRecorderStatus(t *testing.T) {
	s := newCompanionServer(t)
	_, wsURL := streamServer(t, s)
	c, tok := dialBearer(t, s, wsURL, "")
	addRecorder(t, s, "rs2", "Second")
	for _, want := range []bool{true, false} {
		if !want {
			s.ConnectRecorder("rs2", nil)
		}
		for {
			m := readStream(t, c)
			if m.Type == "config.changed" || m.Type == "camera.status" {
				continue
			}
			if m.Type != "recorder.status" || m.Data["recorderId"] != "rs2" || m.Data["online"] != want {
				t.Fatalf("message %+v, want recorder.status online=%v", m, want)
			}
			break
		}
	}
	// servers can be asked for by name (and is on by default above).
	srv, resp, err := dialStream(t, wsURL+"?topics=servers", bearerHeader(tok))
	if err != nil {
		t.Fatalf("dial topics=servers: %v %v", err, resp)
	}
	if m := readStream(t, srv); m.Type != "hello" || fmt.Sprint(m.Data["topics"]) != "[servers]" {
		t.Fatalf("hello for topics=servers %+v", m)
	}
	// servers is a topic of its own.
	only, resp, err := dialStream(t, wsURL+"?topics=alarm", bearerHeader(tok))
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	if m := readStream(t, only); m.Type != "hello" {
		t.Fatalf("first message %+v", m)
	}
	s.ConnectRecorder("rs2", nil)
	addRecorder(t, s, "rs2", "Second")
	s.publishActivity("x", "motion")
	_ = only.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, data, err := only.ReadMessage(); err == nil {
		t.Fatalf("alarm-only stream got %s", data)
	}
}

// A management-only server (no local recorder) with several enrolled
// servers must refuse a camera that names no server, on v2 and on v1 —
// otherwise the camera lands on "local", which nothing records on.
func TestCameraCreateNeedsAServerChoice(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, nil, "test") // --role management
	h := s.Handler()
	ctx := context.Background()
	for _, rs := range []string{"rs1", "rs2"} {
		if err := st.AddRecordingServer(ctx, &store.RecordingServer{ID: rs, Name: rs}); err != nil {
			t.Fatal(err)
		}
	}
	admin := v2Token(t, s, "admin", "Admin")
	code, body := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"Gate","rtspUrl":"rtsp://10.0.0.5/s"}`)
	if code != 400 {
		t.Fatalf("v2 create without recorderId: %d %v", code, body)
	}
	cookie := signIn(t, s, "admin2", "Admin")
	if rec := doWeb(t, h, cookie, "POST", "/api/v1/cameras", `{"name":"Gate","rtspUrl":"rtsp://10.0.0.5/s"}`); rec.Code != 400 {
		t.Fatalf("v1 create: %d %s", rec.Code, rec.Body.String())
	}
	// One live server: it becomes the default on both APIs.
	if err := st.RevokeRecordingServer(ctx, "rs2"); err != nil {
		t.Fatal(err)
	}
	code, cam := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"Gate","rtspUrl":"rtsp://10.0.0.5/s"}`)
	if code != 201 || cam["recorderId"] != "rs1" {
		t.Fatalf("v2 create with one server left: %d %v", code, cam)
	}
	if rec := doWeb(t, h, cookie, "POST", "/api/v1/cameras", `{"name":"Door","rtspUrl":"rtsp://10.0.0.6/s"}`); rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("v1 create with one server left: %d %s", rec.Code, rec.Body.String())
	}
	cams, err := st.Cameras(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cams {
		if c.RecorderID != "rs1" {
			t.Fatalf("camera %s landed on %q, want rs1", c.Name, c.RecorderID)
		}
	}
}

// The recordings folder is per server: the management server stores it,
// the recorder switches at once and carries existing recordings over.
func TestRecordingsFolder(t *testing.T) {
	recorder.MoveSettleDelay, recorder.MoveRetryDelay, recorder.MoveRecentFile = 10*time.Millisecond, 50*time.Millisecond, 0
	t.Cleanup(func() { recorder.MoveSettleDelay, recorder.MoveRetryDelay, recorder.MoveRecentFile = 5*time.Second, 60*time.Second, 30*time.Second })
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	rec := addRecorder(t, s, "rs2", "Second")
	admin := v2Token(t, s, "admin", "Admin")
	cam := addCam(t, s)
	_ = s.Store.SetCameraRecorder(ctx, cam.ID, "rs2")
	oldRoot := rec.RecordingsRoot()
	old := filepath.Join(oldRoot, cam.ID, "20260101-120000-0.mp4")
	if err := os.MkdirAll(filepath.Dir(old), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("segment"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{`{"recordingsDir":"video"}`, `{"recordingsDir":"./x"}`} {
		if code, m := v2Call(t, h, "PATCH", "/api/v2/recording-servers/rs2", admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("patch %s: %d %v", bad, code, m)
		}
	}
	newRoot := filepath.Join(t.TempDir(), "video")
	code, m := v2Call(t, h, "PATCH", "/api/v2/recording-servers/rs2", admin, `{"recordingsDir":"`+newRoot+`/"}`)
	if code != 200 || m["recordingsDir"] != newRoot || m["storage"].(map[string]any)["recordingsDir"] != newRoot {
		t.Fatalf("patch folder: %d %v", code, m)
	}
	if rec.RecordingsRoot() != newRoot {
		t.Fatalf("recorder root %q", rec.RecordingsRoot())
	}
	moved := filepath.Join(newRoot, cam.ID, "20260101-120000-0.mp4")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(moved); err == nil && string(b) == "segment" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recording not moved to %s", moved)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old file still there: %v", err)
	}
	if rs, _ := s.Store.RecordingServer(ctx, "rs2"); rs.RecordingsDir != newRoot {
		t.Fatalf("stored %q", rs.RecordingsDir)
	}
	if _, m := v2Call(t, h, "GET", "/api/v2/recording-servers/rs2", admin, ""); m["recordingsDir"] != newRoot {
		t.Fatalf("get %v", m)
	}

	// Back to the default, and the file follows again.
	if code, m := v2Call(t, h, "PATCH", "/api/v2/recording-servers/rs2", admin, `{"recordingsDir":""}`); code != 200 || m["recordingsDir"] != "" {
		t.Fatalf("patch default: %d %v", code, m)
	}
	if rec.RecordingsRoot() != oldRoot {
		t.Fatalf("recorder root %q, want %q", rec.RecordingsRoot(), oldRoot)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(old); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recording not moved back")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The local server refuses a folder it can't write to; the setting is undone.
	blocked := filepath.Join(t.TempDir(), "file-not-dir")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, m := v2Call(t, h, "PATCH", "/api/v2/recording-servers/local", admin, `{"recordingsDir":"`+filepath.Join(blocked, "x")+`"}`); code != 400 {
		t.Fatalf("local bad folder: %d %v", code, m)
	}
	if rs, _ := s.Store.RecordingServer(ctx, "local"); rs.RecordingsDir != "" {
		t.Fatalf("bad folder stored %q", rs.RecordingsDir)
	}
}

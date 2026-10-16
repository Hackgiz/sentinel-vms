package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sentinel-linux/internal/onvif/fakecam"
	"sentinel-linux/internal/store"
)

type encFixture struct {
	s                 *Server
	h                 http.Handler
	cam               *fakecam.Camera
	id                string
	admin, op, viewer string
}

// newEncFixture: a fake ONVIF camera with a main and a sub stream, stored
// with RTSP URLs that match its stream addresses, plus an Admin, an
// Operator and a Viewer.
func newEncFixture(t *testing.T) *encFixture {
	t.Helper()
	f := &encFixture{s: newCompanionServer(t)}
	f.cam = &fakecam.Camera{User: "admin", Pass: "pw", Streams: []fakecam.Stream{
		{Token: "p1", Name: "Main", Encoding: "H264", Width: 1920, Height: 1080, FPS: 25, Bitrate: 4096, GovLength: 50, Quality: 5, RTSPURL: "rtsp://10.0.0.5/s1"},
		{Token: "p2", Name: "Sub", Encoding: "H264", Width: 640, Height: 360, FPS: 10, RTSPURL: "rtsp://10.0.0.5/s2"},
	}}
	srv := httptest.NewServer(f.cam)
	t.Cleanup(srv.Close)
	pw, onvifURL := "pw", srv.URL+"/onvif/device_service"
	c, err := f.s.Store.SaveCamera(context.Background(), "", store.CameraInput{
		Name: "Dock", RTSPURL: "rtsp://10.0.0.5/s1", SubRTSPURL: "rtsp://10.0.0.5/s2", Username: "admin", Password: &pw, ONVIFURL: &onvifURL})
	if err != nil {
		t.Fatal(err)
	}
	f.id = c.ID
	f.admin = v2Token(t, f.s, "boss", "Admin")
	f.op = v2Token(t, f.s, "op", "Operator")
	f.viewer = v2Token(t, f.s, "viewer", "Viewer")
	f.h = f.s.Handler()
	return f
}

func (f *encFixture) path(token string) string {
	p := "/api/v2/cameras/" + f.id + "/stream-settings"
	if token != "" {
		p += "/" + token
	}
	return p
}

func getStreams(t *testing.T, f *encFixture, path string) []map[string]any {
	t.Helper()
	r := do(t, f.h, "GET", path, f.admin, "")
	if r.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, r.Code, r.Body)
	}
	var out struct {
		Streams []map[string]any `json:"streams"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Streams
}

func TestStreamSettingsGetAndSetLocal(t *testing.T) {
	f := newEncFixture(t)
	streams := getStreams(t, f, f.path(""))
	if len(streams) != 2 {
		t.Fatalf("streams = %v", streams)
	}
	m := streams[0]
	cur := m["current"].(map[string]any)
	opts := m["options"].(map[string]any)
	if m["configToken"] != "vp1" || m["inUse"] != "main" || m["encoding"] != "H264" || cur["width"] != 1920.0 || cur["fps"] != 25.0 || cur["bitrate"] != 4096.0 ||
		len(opts["resolutions"].([]any)) != 3 || opts["fps"].(map[string]any)["max"] != 30.0 {
		t.Fatalf("main = %v", m)
	}
	if streams[1]["inUse"] != "sub" {
		t.Fatalf("sub inUse = %v", streams[1]["inUse"])
	}

	// Zero-valued fields keep the camera's values; main-stream fps is stored.
	r := do(t, f.h, "PUT", f.path("vp1"), f.admin, `{"width":1280,"height":720,"fps":15}`)
	if r.Code != 204 {
		t.Fatalf("PUT: %d %s", r.Code, r.Body)
	}
	st, _ := f.cam.Stream("p1")
	if st.Width != 1280 || st.Height != 720 || st.FPS != 15 || st.Bitrate != 4096 || st.GovLength != 50 || st.Quality != 5 {
		t.Fatalf("camera main = %+v", st)
	}
	if sub, _ := f.cam.Stream("p2"); sub.Width != 640 || sub.FPS != 10 {
		t.Fatalf("sub changed: %+v", sub)
	}
	cam, err := f.s.Store.Camera(context.Background(), f.id)
	if err != nil || cam.FPS != 15 {
		t.Fatalf("stored fps = %d err=%v", cam.FPS, err)
	}
	// Sub-stream changes leave the stored (main) fps alone.
	if r := do(t, f.h, "PUT", f.path("vp2"), f.admin, `{"fps":5}`); r.Code != 204 {
		t.Fatalf("PUT sub: %d %s", r.Code, r.Body)
	}
	if cam, _ := f.s.Store.Camera(context.Background(), f.id); cam.FPS != 15 {
		t.Fatalf("stored fps after sub change = %d", cam.FPS)
	}
	if got := getStreams(t, f, f.path(""))[1]["current"].(map[string]any)["fps"]; got != 5.0 {
		t.Fatalf("sub fps via GET = %v", got)
	}
	// Audited with what changed.
	logs, err := f.s.Store.AuditLog(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range logs {
		found = found || (e.Action == "Changed stream settings" && strings.Contains(e.Detail, "Dock") && strings.Contains(e.Detail, "1920x1080 → 1280x720"))
	}
	if !found {
		t.Fatalf("no audit entry in %+v", logs)
	}
}

func TestStreamSettingsValidation(t *testing.T) {
	f := newEncFixture(t)
	for _, body := range []string{
		`{}`,                          // nothing to change
		`{"fps":31}`,                  // above the camera's range
		`{"bitrate":99999}`,           // above
		`{"govLength":500}`,           // above
		`{"quality":11}`,              // above
		`{"width":1234,"height":567}`, // not offered
		`{"width":1280}`,              // half a resolution
		`{"fps":-1}`,                  // negative
		`{"fpss":5}`,                  // unknown field
	} {
		if r := do(t, f.h, "PUT", f.path("vp1"), f.admin, body); r.Code != 400 {
			t.Errorf("PUT %s = %d %s, want 400", body, r.Code, r.Body)
		}
	}
	if len(f.cam.EncoderSets()) != 0 {
		t.Fatal("an invalid change reached the camera")
	}
	if r := do(t, f.h, "PUT", f.path("vnope"), f.admin, `{"fps":5}`); r.Code != 404 {
		t.Fatalf("unknown stream = %d", r.Code)
	}
}

func TestStreamSettingsPermissionsAndScope(t *testing.T) {
	f := newEncFixture(t)
	for _, tok := range []string{f.op, f.viewer} {
		if r := do(t, f.h, "GET", f.path(""), tok, ""); r.Code != 403 {
			t.Errorf("GET as non-admin = %d", r.Code)
		}
		if r := do(t, f.h, "PUT", f.path("vp1"), tok, `{"fps":5}`); r.Code != 403 {
			t.Errorf("PUT as non-admin = %d", r.Code)
		}
	}
	if r := do(t, f.h, "GET", f.path(""), "", ""); r.Code != 401 {
		t.Errorf("anonymous = %d", r.Code)
	}
	if r := do(t, f.h, "GET", "/api/v2/cameras/nope/stream-settings", f.admin, ""); r.Code != 404 {
		t.Errorf("unknown camera = %d", r.Code)
	}
	// Out of scope is a 404 via inScope (scope.go). Admin is the only role
	// with ManageCameras and always sees every camera, so a camera that
	// doesn't exist is the reachable case; the wrapper itself is covered by
	// scope_test.go and the PTZ tests.
}

func TestStreamSettingsNoONVIF409(t *testing.T) {
	f := newEncFixture(t)
	c, err := f.s.Store.SaveCamera(context.Background(), "", store.CameraInput{Name: "Dumb", RTSPURL: "rtsp://10.0.0.7/s"})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v2/cameras/" + c.ID + "/stream-settings"
	if r := do(t, f.h, "GET", base, f.admin, ""); r.Code != 409 || !strings.Contains(r.Body.String(), "without ONVIF") {
		t.Fatalf("GET = %d %s", r.Code, r.Body)
	}
	if r := do(t, f.h, "PUT", base+"/vp1", f.admin, `{"fps":5}`); r.Code != 409 {
		t.Fatalf("PUT = %d %s", r.Code, r.Body)
	}
}

func TestStreamSettingsCameraErrorsSurface(t *testing.T) {
	f := newEncFixture(t)
	// Wrong stored password: 502 with a sign-in message, not a 500.
	pw := "different"
	cam, _ := f.s.Store.Camera(context.Background(), f.id)
	onvifURL := cam.ONVIFURL
	if _, err := f.s.Store.SaveCamera(context.Background(), f.id, store.CameraInput{Name: "Dock", RTSPURL: cam.RTSPURL, Username: "admin", Password: &pw, ONVIFURL: &onvifURL}); err != nil {
		t.Fatal(err)
	}
	r := do(t, f.h, "GET", f.path(""), f.admin, "")
	if r.Code != 502 || !strings.Contains(r.Body.String(), "username or password") {
		t.Fatalf("wrong password = %d %s", r.Code, r.Body)
	}
}

// A camera on a remote recording server is reached through that server's
// control link (here the in-process link, as in multiserver_test.go).
func TestStreamSettingsRemoteRecorder(t *testing.T) {
	f := newEncFixture(t)
	ctx := context.Background()
	addRecorder(t, f.s, "rs2", "Depot")
	if err := f.s.Store.SetCameraRecorder(ctx, f.id, "rs2"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ApplyRecorder(ctx, "rs2"); err != nil { // the recorder learns the camera
		t.Fatal(err)
	}

	streams := getStreams(t, f, f.path(""))
	if len(streams) != 2 || streams[0]["inUse"] != "main" {
		t.Fatalf("remote streams = %v", streams)
	}
	if r := do(t, f.h, "PUT", f.path("vp1"), f.admin, `{"width":1280,"height":720,"bitrate":2000}`); r.Code != 204 {
		t.Fatalf("remote PUT = %d %s", r.Code, r.Body)
	}
	if st, _ := f.cam.Stream("p1"); st.Width != 1280 || st.Bitrate != 2000 || st.FPS != 25 {
		t.Fatalf("camera = %+v", st)
	}
	if r := do(t, f.h, "PUT", f.path("vp1"), f.admin, `{"fps":99}`); r.Code != 400 {
		t.Fatalf("remote out of range = %d", r.Code)
	}
}

func TestStreamSettingsRecorderOffline502(t *testing.T) {
	f := newEncFixture(t)
	ctx := context.Background()
	if err := f.s.Store.AddRecordingServer(ctx, &store.RecordingServer{ID: "rs9", Name: "Gone"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Store.SetCameraRecorder(ctx, f.id, "rs9"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", f.path(""), ""}, {"PUT", f.path("vp1"), `{"fps":5}`},
	} {
		r := do(t, f.h, c.method, c.path, f.admin, c.body)
		if r.Code != 502 || !strings.Contains(r.Body.String(), "isn't connected") {
			t.Errorf("%s offline = %d %s, want 502", c.method, r.Code, r.Body)
		}
	}
	if len(f.cam.EncoderSets()) != 0 {
		t.Fatal("offline recorder: camera was driven directly")
	}
}

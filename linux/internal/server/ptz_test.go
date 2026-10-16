package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"sentinel-linux/internal/onvif/fakecam"
	"sentinel-linux/internal/store"
)

// ptzFixture: a PTZ-capable fake camera added to the store, an Admin, an
// Operator scoped to a group that does not contain it, an unscoped Operator
// and a Viewer.
type ptzFixture struct {
	s                           *Server
	h                           http.Handler
	cam                         *fakecam.Camera
	id                          string
	admin, op, scopedOp, viewer string
}

func newPTZFixture(t *testing.T) *ptzFixture {
	t.Helper()
	f := &ptzFixture{s: newCompanionServer(t)}
	f.cam = &fakecam.Camera{User: "admin", Pass: "pw", PTZ: true, Streams: []fakecam.Stream{
		{Token: "sub", Name: "Sub", Encoding: "H264", Width: 640, Height: 360, FPS: 10, RTSPURL: "rtsp://10.0.0.5/s2"},
		{Token: "main", Name: "Main", Encoding: "H264", Width: 1920, Height: 1080, FPS: 20, RTSPURL: "rtsp://10.0.0.5/s1"},
	}}
	f.cam.SeedPreset("1", "Gate")
	srv := httptest.NewServer(f.cam)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	pw, onvifURL := "pw", srv.URL+"/onvif/device_service"
	c, err := f.s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Dome", RTSPURL: "rtsp://10.0.0.5/s1", Username: "admin", Password: &pw, ONVIFURL: &onvifURL})
	if err != nil {
		t.Fatal(err)
	}
	f.id = c.ID
	other, err := f.s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Other", RTSPURL: "rtsp://10.0.0.6/s"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := f.s.Store.SaveCameraGroup(ctx, "", "Other only", []string{other.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.admin = v2Token(t, f.s, "boss", "Admin")
	f.op = v2Token(t, f.s, "op", "Operator")
	f.scopedOp = v2Token(t, f.s, "scoped", "Operator")
	f.viewer = v2Token(t, f.s, "viewer", "Viewer")
	u, _ := f.s.Store.UserByName(ctx, "scoped")
	if err := f.s.Store.SetUserScope(ctx, u.ID, store.UserScope{GroupIDs: []string{g.ID}}); err != nil {
		t.Fatal(err)
	}
	f.h = f.s.Handler()
	return f
}

func (f *ptzFixture) path(suffix string) string { return "/api/v2/cameras/" + f.id + "/ptz" + suffix }

func TestPTZRoundTrip(t *testing.T) {
	f := newPTZFixture(t)
	r := do(t, f.h, "GET", f.path(""), f.op, "")
	if r.Code != 200 || !contains(r.Body.String(), `"supported":true`) || !contains(r.Body.String(), `"name":"Gate"`) {
		t.Fatalf("GET ptz: %d %s", r.Code, r.Body)
	}
	// Velocities outside -1..1 are clamped before they reach the camera.
	if r := do(t, f.h, "POST", f.path("/move"), f.op, `{"pan":3,"tilt":-0.5,"zoom":0}`); r.Code != 200 {
		t.Fatalf("move: %d %s", r.Code, r.Body)
	}
	if r := do(t, f.h, "POST", f.path("/stop"), f.op, ""); r.Code != 204 {
		t.Fatalf("stop: %d %s", r.Code, r.Body)
	}
	r = do(t, f.h, "POST", f.path("/presets"), f.op, `{"name":"Lobby"}`)
	if r.Code != 201 || !contains(r.Body.String(), `"token":"2"`) {
		t.Fatalf("save preset: %d %s", r.Code, r.Body)
	}
	if r := do(t, f.h, "POST", f.path("/presets"), f.op, `{"name":"  "}`); r.Code != 400 {
		t.Fatalf("blank preset name: %d", r.Code)
	}
	if r := do(t, f.h, "POST", f.path("/presets/2/goto"), f.op, ""); r.Code != 204 {
		t.Fatalf("goto: %d %s", r.Code, r.Body)
	}
	if r := do(t, f.h, "DELETE", f.path("/presets/1"), f.op, ""); r.Code != 204 {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	want := []string{"move 1.000 -0.500 0.000", "stop", "set Lobby", "goto 2", "remove 1"}
	if got := f.cam.PTZLog(); !reflect.DeepEqual(got, want) {
		t.Fatalf("camera saw %q, want %q", got, want)
	}
	acts := auditActions(t, f.s, "Cameras")
	if !acts["Saved PTZ preset"] || !acts["Removed PTZ preset"] {
		t.Fatalf("preset changes not audited: %v", acts)
	}
}

func TestPTZUnsupportedCamera(t *testing.T) {
	f := newPTZFixture(t)
	f.cam.PTZ = false
	r := do(t, f.h, "GET", f.path(""), f.op, "")
	if r.Code != 200 || !contains(r.Body.String(), `"supported":false`) || !contains(r.Body.String(), `"presets":[]`) {
		t.Fatalf("GET ptz: %d %s", r.Code, r.Body)
	}
	if r := do(t, f.h, "POST", f.path("/stop"), f.op, ""); r.Code != 400 {
		t.Fatalf("stop on a fixed camera: %d, want 400", r.Code)
	}
}

func TestPTZProbeCachedAndInvalidatedOnSave(t *testing.T) {
	f := newPTZFixture(t)
	if r := do(t, f.h, "GET", f.path(""), f.op, ""); !contains(r.Body.String(), `"supported":true`) {
		t.Fatalf("first probe: %s", r.Body)
	}
	f.cam.PTZ = false // the camera changes; the cached answer still stands
	if r := do(t, f.h, "GET", f.path(""), f.op, ""); !contains(r.Body.String(), `"supported":true`) {
		t.Fatalf("expected cached answer: %s", r.Body)
	}
	name := "Dome 2"
	if r := do(t, f.h, "PATCH", "/api/v2/cameras/"+f.id, f.admin, `{"name":"`+name+`"}`); r.Code != 200 {
		t.Fatalf("patch camera: %d %s", r.Code, r.Body)
	}
	if r := do(t, f.h, "GET", f.path(""), f.op, ""); !contains(r.Body.String(), `"supported":false`) {
		t.Fatalf("saving the camera should drop the cache: %s", r.Body)
	}
}

func TestPTZPermissionViewerForbidden(t *testing.T) {
	f := newPTZFixture(t)
	for _, c := range []struct{ method, suffix, body string }{
		{"GET", "", ""}, {"POST", "/move", `{"pan":1,"tilt":0,"zoom":0}`}, {"POST", "/stop", ""},
		{"POST", "/presets", `{"name":"x"}`}, {"POST", "/presets/1/goto", ""}, {"DELETE", "/presets/1", ""},
	} {
		if r := do(t, f.h, c.method, f.path(c.suffix), f.viewer, c.body); r.Code != 403 {
			t.Errorf("Viewer %s ptz%s = %d, want 403", c.method, c.suffix, r.Code)
		}
	}
	if len(f.cam.PTZLog()) != 0 {
		t.Fatalf("a Viewer moved the camera: %q", f.cam.PTZLog())
	}
}

func TestPTZScopeHidesCamera(t *testing.T) {
	f := newPTZFixture(t)
	for _, c := range []struct{ method, suffix, body string }{
		{"GET", "", ""}, {"POST", "/move", `{"pan":1,"tilt":0,"zoom":0}`}, {"POST", "/stop", ""},
		{"POST", "/presets", `{"name":"x"}`}, {"POST", "/presets/1/goto", ""}, {"DELETE", "/presets/1", ""},
	} {
		if r := do(t, f.h, c.method, f.path(c.suffix), f.scopedOp, c.body); r.Code != 404 {
			t.Errorf("out-of-scope %s ptz%s = %d, want 404", c.method, c.suffix, r.Code)
		}
	}
	if len(f.cam.PTZLog()) != 0 {
		t.Fatalf("out-of-scope user moved the camera: %q", f.cam.PTZLog())
	}
	if r := do(t, f.h, "GET", f.path(""), f.admin, ""); r.Code != 200 {
		t.Fatalf("admin: %d", r.Code)
	}
}

func TestPTZRemoteRecorder502(t *testing.T) {
	f := newPTZFixture(t)
	if err := f.s.Store.SetCameraRecorder(context.Background(), f.id, "rs2"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, suffix, body string }{
		{"GET", "", ""}, {"POST", "/move", `{"pan":1,"tilt":0,"zoom":0}`}, {"POST", "/stop", ""},
	} {
		r := do(t, f.h, c.method, f.path(c.suffix), f.op, c.body)
		if r.Code != 502 || !contains(r.Body.String(), "only available for cameras on the management server") {
			t.Errorf("%s ptz%s = %d %s, want 502", c.method, c.suffix, r.Code, r.Body)
		}
	}
	if len(f.cam.PTZLog()) != 0 {
		t.Fatalf("remote camera was driven locally: %q", f.cam.PTZLog())
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

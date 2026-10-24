package server

import (
	"context"
	"testing"

	"sentinel-linux/internal/store"
)

// WAVE24 D1: JSON null on PATCH restores the server default, 0 is literal,
// an omitted field is unchanged.
func TestV2CameraRollNullable(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "admin", "Admin")
	_, cam := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"Door","rtspUrl":"rtsp://10.0.0.5/s"}`)
	id := cam["id"].(string)
	patch := func(body string) map[string]any {
		code, m := v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, body)
		if code != 200 {
			t.Fatalf("patch %s: %d %v", body, code, m)
		}
		return m
	}
	if m := patch(`{"preRollSeconds":0,"postRollSeconds":0}`); m["preRollSeconds"] != 0.0 || m["postRollSeconds"] != 0.0 {
		t.Fatalf("literal 0: %v", m)
	}
	if m := patch(`{"name":"Door 2"}`); m["preRollSeconds"] != 0.0 || m["postRollSeconds"] != 0.0 {
		t.Fatalf("omitted keeps: %v", m)
	}
	if m := patch(`{"preRollSeconds":null}`); m["preRollSeconds"] != nil || m["postRollSeconds"] != 0.0 {
		t.Fatalf("null resets only pre: %v", m)
	}
	if m := patch(`{"postRollSeconds":120}`); m["postRollSeconds"] != 120.0 {
		t.Fatalf("120: %v", m)
	}
	cams, _ := s.Store.Cameras(context.Background())
	snap := s.snapshot(context.Background(), store.LocalRecorderID, cams)
	if snap.Cameras[0].PreRollSeconds != nil || snap.Cameras[0].PostRollSeconds == nil || *snap.Cameras[0].PostRollSeconds != 120 {
		t.Fatalf("snapshot %+v", snap.Cameras[0])
	}
}

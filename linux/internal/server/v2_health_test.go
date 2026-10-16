package server

import (
	"context"
	"testing"

	"sentinel-linux/internal/store"
)

func healthServers(t *testing.T, m map[string]any) map[string]map[string]any {
	t.Helper()
	raw, _ := m["servers"].([]any)
	out := map[string]map[string]any{}
	for _, r := range raw {
		sv := r.(map[string]any)
		out[sv["id"].(string)] = sv
	}
	return out
}

func healthCams(sv map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, c := range sv["cameras"].([]any) {
		cm := c.(map[string]any)
		out[cm["name"].(string)] = cm
	}
	return out
}

func TestSystemHealthScopeAndMetrics(t *testing.T) {
	f := newScopeFixture(t)
	if err := f.s.ApplyRecorder(context.Background(), store.LocalRecorderID); err != nil {
		t.Fatal(err)
	}

	if code, _ := v2Call(t, f.h, "GET", "/api/v2/system/health", "", ""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	code, m := v2Call(t, f.h, "GET", "/api/v2/system/health", f.admin, "")
	if code != 200 {
		t.Fatalf("admin: %d %v", code, m)
	}
	local := healthServers(t, m)["local"]
	if local == nil || local["online"] != true || local["version"] == "" {
		t.Fatalf("local server: %v", local)
	}
	if local["cpu"] == nil || local["disk"] == nil || local["uptimeSec"] == nil || local["dbBytes"] == nil || local["latencyMs"] == nil {
		t.Fatalf("local metrics missing: %v", local)
	}
	cams := healthCams(local)
	if len(cams) != 2 {
		t.Fatalf("admin camera rows: %v", cams)
	}
	// The fixture wrote a recorded segment for each camera: its age is reported.
	if cams["Front door"]["lastSegmentAgeSec"] == nil {
		t.Fatalf("no last segment age: %v", cams["Front door"])
	}

	// A camera-scoped operator sees the server but only their camera's row.
	code, m = v2Call(t, f.h, "GET", "/api/v2/system/health", f.op, "")
	if code != 200 {
		t.Fatalf("scoped: %d %v", code, m)
	}
	scoped := healthCams(healthServers(t, m)["local"])
	if len(scoped) != 1 || scoped["Front door"] == nil || scoped["Back gate"] != nil {
		t.Fatalf("scoped rows leak: %v", scoped)
	}
}

func TestSystemHealthOfflineRecorderDegrades(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	rs2 := addRecorder(t, s, "rs2", "Depot")
	admin := v2Token(t, s, "admin", "Admin")
	viewer := v2Token(t, s, "viewer", "Viewer")
	cam := addCam(t, s)
	if err := s.Store.SetCameraRecorder(ctx, cam.ID, "rs2"); err != nil {
		t.Fatal(err)
	}
	_ = s.Store.AddRecordingServer(ctx, &store.RecordingServer{ID: "gone", Name: "Gone"})
	_ = s.Store.RevokeRecordingServer(ctx, "gone")

	// Online: a remote recorder answers over the link with its own reading.
	_ = rs2
	code, m := v2Call(t, h, "GET", "/api/v2/system/health", viewer, "")
	if code != 200 {
		t.Fatalf("viewer: %d %v", code, m)
	}
	srv := healthServers(t, m)
	if srv["gone"] != nil {
		t.Fatal("revoked server listed")
	}
	if sv := srv["rs2"]; sv["online"] != true || sv["version"] != "test-rs2" || sv["cpu"] == nil || healthCams(sv)["Door"] == nil {
		t.Fatalf("online rs2: %v", sv)
	}

	// Offline: still listed, flagged, with its last-known cameras and no metrics.
	s.ConnectRecorder("rs2", nil)
	code, m = v2Call(t, h, "GET", "/api/v2/system/health", admin, "")
	if code != 200 {
		t.Fatalf("offline: %d %v", code, m)
	}
	sv := healthServers(t, m)["rs2"]
	if sv == nil || sv["online"] != false || sv["cpu"] != nil || sv["disk"] != nil || sv["uptimeSec"] != nil || sv["latencyMs"] != nil {
		t.Fatalf("offline rs2: %v", sv)
	}
	door := healthCams(sv)["Door"]
	if door == nil || door["online"] != false || door["recording"] != false {
		t.Fatalf("offline camera row: %v", door)
	}
}

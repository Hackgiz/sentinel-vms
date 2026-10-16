package server

// The network scan itself isn't run in tests (it would multicast on the
// build machine); these cover routing, validation and the control-link
// error paths, the same way the log endpoint's tests do.

import (
	"context"
	"testing"

	"sentinel-linux/internal/store"
)

func TestV2DiscoverRouting(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	admin := v2Token(t, s, "admin", "Admin")
	viewer := v2Token(t, s, "viewer", "Viewer")

	// Unknown server.
	if code, m := v2Call(t, h, "POST", "/api/v2/recording-servers/nope/discover", admin, `{}`); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("unknown server: %d %v", code, m)
	}
	// A registered but never-connected server: 503, not a hang.
	if err := s.Store.AddRecordingServer(ctx, &store.RecordingServer{ID: "rs9", Name: "Depot"}); err != nil {
		t.Fatal(err)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/recording-servers/rs9/discover", admin, `{}`); code != 503 || errCode(m) != "unavailable" {
		t.Fatalf("offline server: %d %v", code, m)
	}
	// Permissions: scanning needs ManageCameras.
	if code, _ := v2Call(t, h, "POST", "/api/v2/recording-servers/rs9/discover", viewer, `{}`); code != 403 {
		t.Fatalf("viewer scan allowed: %d", code)
	}
	// Unknown body fields are rejected.
	if code, _ := v2Call(t, h, "POST", "/api/v2/recording-servers/rs9/discover", admin, `{"sweeep":true}`); code != 400 {
		t.Fatalf("bad body accepted: %d", code)
	}
}

func TestV2DiscoverProbeValidation(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	admin := v2Token(t, s, "admin", "Admin")
	if err := s.Store.AddRecordingServer(ctx, &store.RecordingServer{ID: "rs9", Name: "Depot"}); err != nil {
		t.Fatal(err)
	}

	bad := []string{
		`{"host":""}`,
		`{"host":"not a host!"}`,
		`{"host":"10.0.0.9","serviceURL":"ftp://10.0.0.9/x"}`,
		`{"host":"10.0.0.9","serviceURL":"http://10.0.0.8/onvif"}`, // different host
	}
	for _, body := range bad {
		if code, m := v2Call(t, h, "POST", "/api/v2/recording-servers/rs9/probe-or-404", admin, body); code != 404 {
			t.Fatalf("expected 404 for bad route, got %d %v", code, m)
		}
		if code, m := v2Call(t, h, "POST", "/api/v2/recording-servers/rs9/discover/probe", admin, body); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("probe %s: %d %v", body, code, m)
		}
	}
	// A valid target on a disconnected server fails on the link, not validation.
	if code, m := v2Call(t, h, "POST", "/api/v2/recording-servers/rs9/discover/probe", admin, `{"host":"10.0.0.9"}`); code != 503 || errCode(m) != "unavailable" {
		t.Fatalf("offline probe: %d %v", code, m)
	}
}

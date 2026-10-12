package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.4.2", "0.4.1", true},
		{"0.5.0", "0.4.9", true},
		{"0.10.0", "0.9.0", true},
		{"1.0", "0.9.9", true},
		{"0.4.1", "0.4.1", false},
		{"0.4.1", "0.4.2", false},
		{"0.4.1", "0.4.1-3-gabc123", false},
		{"0.4.2", "v0.4.1-3-gabc123", true},
		{"0.4.1.1", "0.4.1", true},
		{"0.4.1", "dev", false},
		{"", "0.4.1", false},
		{"garbage", "0.4.1", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.latest, c.current); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestUpdateCheck(t *testing.T) {
	var hits int
	var ua string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		ua = r.Header.Get("User-Agent")
		io.WriteString(w, `{"mac":{"version":"9.9.9"},"linux":{"version":"0.5.0","notes":"Camera analytics","url":"https://sentvms.com/?get=linux-amd64","urlArm64":"https://sentvms.com/?get=linux-arm64"}}`)
	}))
	defer gw.Close()

	s := newCompanionServer(t)
	s.Version = "0.4.1"
	s.UpdateURL = gw.URL
	ctx := context.Background()
	admin, _ := s.Store.CreateUser(ctx, "admin", "Admin", "x")
	viewer, _ := s.Store.CreateUser(ctx, "viewer", "Viewer", "x")
	adminTok, _ := s.Store.CreateSession(ctx, admin.ID, time.Hour)
	viewerTok, _ := s.Store.CreateSession(ctx, viewer.ID, time.Hour)
	h := s.Handler()
	call := func(method, body, tok string) (int, map[string]any) {
		r := httptest.NewRequest(method, "/api/v1/update", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}

	if _, m := call("GET", "", viewerTok); m["available"] != false || m["checkEnabled"] != true || m["docker"] != false {
		t.Fatalf("before any check: %v", m)
	}
	t.Setenv("SENTINEL_DOCKER", "1") // set by the Docker image
	if _, m := call("GET", "", viewerTok); m["docker"] != true {
		t.Fatalf("in Docker: %v", m)
	}
	if c, _ := call("PUT", `{"checkNow":true}`, viewerTok); c != http.StatusForbidden {
		t.Fatalf("viewer check now = %d, want 403", c)
	}
	c, m := call("PUT", `{"checkNow":true}`, adminTok)
	if c != 200 || m["available"] != true || m["latest"] != "0.5.0" || m["notes"] != "Camera analytics" {
		t.Fatalf("after check: %d %v", c, m)
	}
	if ua != "SentinelLinux/0.4.1" || hits != 1 {
		t.Fatalf("request: ua=%q hits=%d", ua, hits)
	}
	if _, m := call("PUT", `{"checkEnabled":false}`, adminTok); m["checkEnabled"] != false {
		t.Fatalf("turn off: %v", m)
	}
	if s.updateChecksOn(ctx) {
		t.Fatal("setting not stored")
	}

	// Unreachable gateway: no update, a readable error, nothing else breaks.
	s.UpdateURL = "http://127.0.0.1:1/latest"
	if _, m := call("PUT", `{"checkNow":true}`, adminTok); m["error"] == "" || m["latest"] != "0.5.0" {
		t.Fatalf("offline check: %v", m)
	}
}

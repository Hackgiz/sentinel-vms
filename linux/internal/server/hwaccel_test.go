package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/hwinfo"
)

func TestHWAccelSetting(t *testing.T) {
	s := newCompanionServer(t)
	// No ffmpeg → no readers; the probe result and mode are still reported.
	rec := recOf(s)
	rec.Detect = &detect.Manager{}
	rec.Detect.ConfigureAccel(detect.AccelAuto, detect.DecodeCaps{
		Hwaccels: []string{"vaapi", "qsv"}, Usable: []string{"vaapi"}, Device: "/dev/dri/renderD128", Notes: []string{},
		Hardware: hwinfo.Info{Accelerators: []hwinfo.Accelerator{{Kind: "coral-usb", Name: "Coral USB Accelerator"}}},
	}, nil)
	ctx := context.Background()
	admin, _ := s.Store.CreateUser(ctx, "admin", "Admin", "x")
	viewer, _ := s.Store.CreateUser(ctx, "viewer", "Viewer", "x")
	adminTok, _ := s.Store.CreateSession(ctx, admin.ID, time.Hour)
	viewerTok, _ := s.Store.CreateSession(ctx, viewer.ID, time.Hour)
	h := s.Handler()
	call := func(method, body, tok string) (int, map[string]any) {
		r := httptest.NewRequest(method, "/api/v1/hwaccel", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}

	c, m := call("GET", "", viewerTok)
	if c != 200 || m["mode"] != "auto" {
		t.Fatalf("get: %d %v", c, m)
	}
	dec := m["decode"].(map[string]any)
	if dec["device"] != "/dev/dri/renderD128" || len(dec["usable"].([]any)) != 1 {
		t.Fatalf("decode %v", dec)
	}
	acc := m["accelerators"].([]any)
	if len(acc) != 1 || acc[0].(map[string]any)["supported"] != false || !strings.Contains(acc[0].(map[string]any)["note"].(string), "Edge TPU") {
		t.Fatalf("accelerators %v", acc)
	}

	if c, _ := call("PUT", `{"mode":"off"}`, viewerTok); c != http.StatusForbidden {
		t.Fatalf("viewer put = %d", c)
	}
	if c, _ := call("PUT", `{"mode":"fast"}`, adminTok); c != http.StatusBadRequest {
		t.Fatalf("bad mode = %d", c)
	}
	if c, m := call("PUT", `{"mode":"off"}`, adminTok); c != 200 || m["mode"] != "off" {
		t.Fatalf("put off: %d %v", c, m)
	}
	if s.Store.Setting(ctx, "hwaccel", "") != "off" {
		t.Fatal("setting not saved")
	}
	log, _ := s.Store.AuditLog(ctx, 10)
	found := false
	for _, e := range log {
		if e.Action == "Changed hardware acceleration" && strings.Contains(e.Detail, "auto → off") && e.User == "admin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit entry: %+v", log)
	}
}

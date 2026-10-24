package server

import (
	"context"
	"strings"
	"testing"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
)

func TestV2CameraRecordingModes(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "admin", "Admin")
	ctx := context.Background()

	code, cam := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"Door","rtspUrl":"rtsp://10.0.0.5/s"}`)
	if code != 201 || cam["recordingMode"] != "default" || cam["effectiveRecordingMode"] != "continuous" || cam["recording"] != true ||
		cam["recordTrigger"] != "default" || cam["preRollSeconds"] != nil || cam["postRollSeconds"] != nil || cam["lowResRetentionDays"] != 0.0 {
		t.Fatalf("create: %d %v", code, cam)
	}
	id := cam["id"].(string)
	patch := func(body string) (int, map[string]any) {
		return v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, body)
	}

	// dual needs a sub-stream.
	if code, m := patch(`{"recordingMode":"dual"}`); code != 400 || errCode(m) != "bad_request" || !strings.Contains(m["error"].(map[string]any)["message"].(string), "sub-stream") {
		t.Fatalf("dual without sub: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"X","rtspUrl":"rtsp://10.0.0.7/s","recordingMode":"dual"}`); code != 400 {
		t.Fatalf("create dual without sub: %d %v", code, m)
	}
	for _, bad := range []string{`{"recordingMode":"sometimes"}`, `{"recordTrigger":"wind"}`, `{"preRollSeconds":31}`, `{"postRollSeconds":121}`, `{"lowResRetentionDays":366}`} {
		if code, m := patch(bad); code != 400 {
			t.Fatalf("patch %s: %d %v", bad, code, m)
		}
	}
	code, m := patch(`{"subRtspUrl":"rtsp://10.0.0.5/sub","recordingMode":"dual","recordTrigger":"objects","preRollSeconds":10,"postRollSeconds":30,"lowResRetentionDays":21}`)
	if code != 200 || m["recordingMode"] != "dual" || m["effectiveRecordingMode"] != "dual" || m["recordTrigger"] != "objects" ||
		m["preRollSeconds"] != 10.0 || m["postRollSeconds"] != 30.0 || m["lowResRetentionDays"] != 21.0 || m["recording"] != true {
		t.Fatalf("set dual: %d %v", code, m)
	}
	// Removing the sub-stream of a dual camera is refused too.
	if code, m := patch(`{"subRtspUrl":""}`); code != 400 {
		t.Fatalf("drop sub of dual: %d %v", code, m)
	}
	// The snapshot carries the settings and the server defaults.
	cams, _ := s.Store.Cameras(ctx)
	snap := s.snapshot(ctx, store.LocalRecorderID, cams)
	if len(snap.Cameras) != 1 || snap.Cameras[0].RecordingMode != "dual" || snap.Cameras[0].RecordTrigger != "objects" ||
		snap.Cameras[0].PreRollSeconds == nil || *snap.Cameras[0].PreRollSeconds != 10 || snap.Cameras[0].LowResRetentionDays != 21 || !snap.Cameras[0].Recording ||
		snap.RecordingDefaults.DefaultMode != link.RecordContinuous {
		t.Fatalf("snapshot %+v / %+v", snap.Cameras, snap.RecordingDefaults)
	}
	// recording bool without recordingMode: off, then back on → default.
	if _, m := patch(`{"recording":false}`); m["recordingMode"] != "off" || m["effectiveRecordingMode"] != "off" || m["recording"] != false {
		t.Fatalf("recording=false: %v", m)
	}
	if _, m := patch(`{"location":"Lobby"}`); m["recordingMode"] != "off" {
		t.Fatalf("unrelated patch changed the mode: %v", m)
	}
	if _, m := patch(`{"recording":true}`); m["recordingMode"] != "default" || m["recording"] != true {
		t.Fatalf("recording=true: %v", m)
	}
	patch(`{"recordingMode":"events"}`)
	if _, m := patch(`{"recording":true}`); m["recordingMode"] != "events" {
		t.Fatalf("recording=true reset an events camera: %v", m)
	}
	// recordingMode wins over recording.
	if _, m := patch(`{"recording":false,"recordingMode":"continuous"}`); m["recordingMode"] != "continuous" {
		t.Fatalf("recordingMode vs recording: %v", m)
	}
	if !auditActions(t, s, "Cameras")["Changed recording mode"] {
		t.Error("no recording mode audit entry")
	}
	// v1 sees the fields too, and its recording switch works the same way.
	web := signIn(t, s, "web", "Admin")
	rec := doWeb(t, h, web, "PUT", "/api/v1/cameras/"+id, `{"name":"Door","rtspURL":"rtsp://10.0.0.5/s","subRTSPURL":"rtsp://10.0.0.5/sub","recording":false}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"recordingMode":"off"`) {
		t.Fatalf("v1 save: %d %s", rec.Code, rec.Body)
	}
}

func TestMacImportRecordingMode(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		cam  macImportCamera
		want string
	}{
		{macImportCamera{RecordingMode: "events"}, "events"},
		{macImportCamera{Recording: &yes, RecordingMode: "dual", SubRTSPURL: "rtsp://h/sub"}, "dual"},
		{macImportCamera{Recording: &yes, RecordingMode: "dual"}, "events"},
		{macImportCamera{Recording: &no, RecordingMode: "continuous"}, ""},
		{macImportCamera{Recording: &yes}, ""},
	} {
		got := ""
		if m := macImportRecordingMode(c.cam); m != nil {
			got = *m
		}
		if got != c.want {
			t.Errorf("%+v → %q, want %q", c.cam, got, c.want)
		}
	}
}

func TestV2RecordingSettings(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin, op := v2Token(t, s, "admin", "Admin"), v2Token(t, s, "op", "Operator")
	ctx := context.Background()

	code, m := v2Call(t, h, "GET", "/api/v2/settings/recording", op, "")
	if code != 200 || m["defaultMode"] != "continuous" || m["defaultTrigger"] != "motion" || m["defaultPreRollSeconds"] != 5.0 ||
		m["defaultPostRollSeconds"] != 15.0 || m["defaultRetentionDays"] != 7.0 || m["defaultLowResRetentionDays"] != 14.0 || m["minFreePercent"] != 0.0 {
		t.Fatalf("get: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "PATCH", "/api/v2/settings/recording", op, `{"defaultMode":"events"}`); code != 403 {
		t.Fatalf("operator patch: %d %v", code, m)
	}
	for _, bad := range []string{`{"defaultMode":"default"}`, `{"defaultTrigger":"default"}`, `{"defaultPostRollSeconds":2}`,
		`{"defaultRetentionDays":0}`, `{"defaultLowResRetentionDays":400}`, `{"minFreePercent":3}`, `{"minFreePercent":51}`, `{"bogus":1}`} {
		if code, m := v2Call(t, h, "PATCH", "/api/v2/settings/recording", admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("patch %s: %d %v", bad, code, m)
		}
	}
	_, cam := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"Door","rtspUrl":"rtsp://10.0.0.5/s"}`)
	rev := s.cfgRev.Load()
	code, m = v2Call(t, h, "PATCH", "/api/v2/settings/recording", admin, `{"defaultMode":"dual","defaultTrigger":"objects","defaultRetentionDays":30,"minFreePercent":10}`)
	if code != 200 || m["defaultMode"] != "dual" || m["defaultTrigger"] != "objects" || m["defaultRetentionDays"] != 30.0 || m["minFreePercent"] != 10.0 || m["defaultPreRollSeconds"] != 5.0 {
		t.Fatalf("patch: %d %v", code, m)
	}
	if s.cfgRev.Load() == rev {
		t.Fatal("recording defaults did not reach the recorders")
	}
	// The v1 setting is the same value.
	web := signIn(t, s, "web", "Admin")
	if rec := doWeb(t, h, web, "GET", "/api/v1/settings", ""); !strings.Contains(rec.Body.String(), `"defaultRetentionDays":30`) {
		t.Fatalf("v1 settings: %s", rec.Body)
	}
	doWeb(t, h, web, "PUT", "/api/v1/settings", `{"defaultRetentionDays":9}`)
	if d := s.Store.RecordingDefaults(ctx); d.DefaultRetentionDays != 9 {
		t.Fatalf("v1 write not seen: %+v", d)
	}
	// A default camera without a sub-stream: dual falls back to events.
	_, c := v2Call(t, h, "GET", "/api/v2/cameras/"+cam["id"].(string), admin, "")
	if c["recordingMode"] != "default" || c["effectiveRecordingMode"] != "events" {
		t.Fatalf("camera under dual default: %v", c)
	}
	cams, _ := s.Store.Cameras(ctx)
	snap := s.snapshot(ctx, store.LocalRecorderID, cams)
	if snap.RecordingDefaults.DefaultMode != "dual" || snap.RecordingDefaults.MinFreePercent != 10 || snap.DefaultRetentionDays != 9 {
		t.Fatalf("snapshot defaults %+v", snap.RecordingDefaults)
	}
	if !auditActions(t, s, "Storage")["Changed recording defaults"] {
		t.Error("no audit entry")
	}
}

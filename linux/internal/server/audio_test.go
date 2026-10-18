package server

import (
	"context"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

func TestV2CameraAudioSettings(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin, op := v2Token(t, s, "admin", "Admin"), v2Token(t, s, "op", "Operator")

	code, cam := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"Door","rtspUrl":"rtsp://10.0.0.5/s"}`)
	if code != 201 || cam["audioDetect"] != false || cam["audioThresholdDb"] != 25.0 {
		t.Fatalf("default audio settings: %d %v", code, cam)
	}
	id := cam["id"].(string)
	patch := func(tok, body string) (int, map[string]any) {
		return v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, tok, body)
	}

	// Permissions: ManageCameras only.
	if code, m := patch(op, `{"audioDetect":true}`); code != 403 || errCode(m) != "forbidden" {
		t.Fatalf("operator patch: %d %v", code, m)
	}
	if c, _ := s.Store.Camera(context.Background(), id); c.AudioDetect {
		t.Fatal("forbidden request changed the camera")
	}

	// Validation leaves everything untouched.
	for _, bad := range []string{`{"audioDetect":true,"audioThresholdDb":5}`, `{"audioThresholdDb":61}`, `{"audioDetect":"yes"}`} {
		if code, m := patch(admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("patch %s: %d %v", bad, code, m)
		}
	}
	if c, _ := s.Store.Camera(context.Background(), id); c.AudioDetect || c.AudioThresholdDB != 25 {
		t.Fatalf("rejected patch changed audio: %+v", c)
	}

	rev := s.cfgRev.Load()
	code, m := patch(admin, `{"audioDetect":true,"audioThresholdDb":30}`)
	if code != 200 || m["audioDetect"] != true || m["audioThresholdDb"] != 30.0 {
		t.Fatalf("enable: %d %v", code, m)
	}
	if s.cfgRev.Load() == rev {
		t.Fatal("enabling audio did not push the configuration to recorders")
	}
	// An unrelated patch keeps the audio settings.
	if _, m := patch(admin, `{"location":"Lobby"}`); m["audioDetect"] != true || m["audioThresholdDb"] != 30.0 {
		t.Fatalf("unrelated patch lost audio settings: %v", m)
	}
	// The recorder snapshot carries the threshold; off sends 0.
	cams, _ := s.Store.Cameras(context.Background())
	snap := s.snapshot(context.Background(), store.LocalRecorderID, cams)
	if len(snap.Cameras) != 1 || snap.Cameras[0].AudioDetect != 30 {
		t.Fatalf("snapshot audio = %+v", snap.Cameras)
	}
	if _, m := patch(admin, `{"audioDetect":false}`); m["audioDetect"] != false || m["audioThresholdDb"] != 30.0 {
		t.Fatalf("disable: %v", m)
	}
	cams, _ = s.Store.Cameras(context.Background())
	if snap := s.snapshot(context.Background(), store.LocalRecorderID, cams); snap.Cameras[0].AudioDetect != 0 {
		t.Fatalf("off camera still sends a threshold: %+v", snap.Cameras[0])
	}

	// Audited.
	acts := auditActions(t, s, "Cameras")
	for _, want := range []string{"Turned on audio detection", "Turned off audio detection"} {
		if !acts[want] {
			t.Errorf("no %q audit entry in %v", want, acts)
		}
	}
	patch(admin, `{"audioThresholdDb":40}`)
	if !auditActions(t, s, "Cameras")["Changed audio detection threshold"] {
		t.Error("threshold change not audited")
	}

	// Create with audio on.
	code, c2 := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"Yard","rtspUrl":"rtsp://10.0.0.6/s","audioDetect":true}`)
	if code != 201 || c2["audioDetect"] != true || c2["audioThresholdDb"] != 25.0 {
		t.Fatalf("create with audio: %d %v", code, c2)
	}
}

func TestAudioEventAlarmFollowsAlarmMode(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	cam, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.5/s"})
	if err != nil {
		t.Fatal(err)
	}
	fire := func(label, eventID string) {
		s.onDetection(ctx, cam.ID, link.Detection{Source: link.SourceAudio, Kind: "audio", Label: label, Score: 0.8,
			EventID: eventID, CreatedAt: time.Now().Unix(), At: time.Now().UnixMilli(), MergeSince: time.Now().Add(time.Second).UnixMilli()})
	}
	alarms := func() []store.Alarm {
		a, _ := s.Store.Alarms(ctx, false, 50)
		return a
	}

	// Alarm mode "people only": the loudness baseline is logged, no alarm
	// (as for motion) — but a classified threat alarms whatever the mode.
	people := 3
	if _, err := s.Store.SaveCamera(ctx, cam.ID, store.CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.5/s", AlertOn: &people}); err != nil {
		t.Fatal(err)
	}
	fire("Loud noise", "EVAUDIO0000000000000000000000001")
	if n := len(alarms()); n != 0 {
		t.Fatalf("baseline alarm in people-only mode: %d", n)
	}
	fire("Gunshot", "EVAUDIO0000000000000000000000003")
	a := alarms()
	if len(a) != 1 || a[0].Severity != "Critical" || a[0].Title != "Gunshot heard" {
		t.Fatalf("classified threat in people-only mode: %+v", a)
	}
	// "Any motion": the baseline alarms too.
	any := 1
	if _, err := s.Store.SaveCamera(ctx, cam.ID, store.CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.5/s", AlertOn: &any}); err != nil {
		t.Fatal(err)
	}
	fire("Glass break", "EVAUDIO0000000000000000000000002")
	a = alarms()
	var glass *store.Alarm
	for i := range a {
		if a[i].Title == "Glass break heard" {
			glass = &a[i]
		}
	}
	if len(a) != 2 || glass == nil || glass.Kind != "audio" || glass.Severity != "Critical" {
		t.Fatalf("alarms = %+v", a)
	}
	fire("Loud noise", "EVAUDIO0000000000000000000000004")
	if n := len(alarms()); n != 3 {
		t.Fatalf("baseline in any-motion mode: %d alarms", n)
	}
}

func TestAudioKindIsAValidRuleKind(t *testing.T) {
	r := rules.Rule{Name: "Sounds", Kinds: []string{"audio"}, MinSeverity: "Warning", Delivery: rules.DeliveryDashboard}
	r.Normalize()
	if err := r.Validate(); err != nil {
		t.Fatalf("audio rule rejected: %v", err)
	}
}

func TestAudioAlarmText(t *testing.T) {
	for label, want := range map[string]string{"Gunshot": "Critical", "Scream": "Critical", "Glass break": "Critical", "Alarm": "Warning", "Loud noise": "Warning"} {
		if _, sev := audioAlarm(label); sev != want {
			t.Errorf("%s severity %s, want %s", label, sev, want)
		}
	}
	if store.AudioDefaultThresholdDB != 25 {
		t.Fatal("default threshold drifted from the contract's 25 dB")
	}
}

package server

import (
	"context"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
)

func TestV2CameraLoiterSeconds(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin, op := v2Token(t, s, "admin", "Admin"), v2Token(t, s, "op", "Operator")
	ctx := context.Background()

	code, cam := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"Door","rtspUrl":"rtsp://10.0.0.5/s"}`)
	if code != 201 || cam["loiterSeconds"] != 0.0 {
		t.Fatalf("default loiterSeconds: %d %v", code, cam)
	}
	id := cam["id"].(string)
	patch := func(tok, body string) (int, map[string]any) {
		return v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, tok, body)
	}
	if code, m := patch(op, `{"loiterSeconds":30}`); code != 403 || errCode(m) != "forbidden" {
		t.Fatalf("operator patch: %d %v", code, m)
	}
	for _, bad := range []string{`{"loiterSeconds":-1}`, `{"loiterSeconds":3601}`, `{"loiterSeconds":"30"}`} {
		if code, m := patch(admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Fatalf("patch %s: %d %v", bad, code, m)
		}
	}
	if c, _ := s.Store.Camera(ctx, id); c.LoiterSeconds != 0 {
		t.Fatalf("rejected patch changed loitering: %+v", c)
	}
	rev := s.cfgRev.Load()
	if code, m := patch(admin, `{"loiterSeconds":45}`); code != 200 || m["loiterSeconds"] != 45.0 {
		t.Fatalf("set: %d %v", code, m)
	}
	if s.cfgRev.Load() == rev {
		t.Fatal("loiterSeconds did not reach the recorders")
	}
	if _, m := patch(admin, `{"location":"Lobby"}`); m["loiterSeconds"] != 45.0 {
		t.Fatalf("unrelated patch lost loiterSeconds: %v", m)
	}
	cams, _ := s.Store.Cameras(ctx)
	if snap := s.snapshot(ctx, store.LocalRecorderID, cams); len(snap.Cameras) != 1 || snap.Cameras[0].LoiterSeconds != 45 {
		t.Fatalf("snapshot = %+v", snap.Cameras)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/cameras/"+id, admin, ""); code != 200 || m["loiterSeconds"] != 45.0 {
		t.Fatalf("get: %d %v", code, m)
	}
	patch(admin, `{"loiterSeconds":0}`)
	acts := auditActions(t, s, "Cameras")
	for _, want := range []string{"Turned on loitering detection", "Turned off loitering detection"} {
		if !acts[want] {
			t.Errorf("no %q audit entry in %v", want, acts)
		}
	}
	// Create with loitering on.
	if code, c2 := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"Yard","rtspUrl":"rtsp://10.0.0.6/s","loiterSeconds":120}`); code != 201 || c2["loiterSeconds"] != 120.0 {
		t.Fatalf("create with loitering: %d %v", code, c2)
	}
}

// A recorder's Loitering event (source objects) raises a Warning alarm in
// any alert mode, and object events keep their trackId.
func TestLoiteringEventAlarmAndTrackID(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	people := 3
	cam, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.5/s", AlertOn: &people})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.onDetection(ctx, cam.ID, link.Detection{Source: link.SourceObjects, Kind: "Loitering", Label: "person", Score: 0.8,
		Box: "[0.1,0.1,0.2,0.5]", TrackID: cam.ID + ":7", Summary: "a person stayed more than 30 s",
		EventID: "EVLOITER000000000000000000000001", CreatedAt: now.Unix(), At: now.UnixMilli(), MergeSince: now.Add(time.Second).UnixMilli()})
	a, _ := s.Store.Alarms(ctx, false, 50)
	if len(a) != 1 || a[0].Kind != "Loitering" || a[0].Severity != "Warning" || a[0].Title != "Loitering detected" {
		t.Fatalf("alarms = %+v", a)
	}
	ev, err := s.Store.Event(ctx, "EVLOITER000000000000000000000001")
	if err != nil || ev.TrackID != cam.ID+":7" || ev.AlarmID != a[0].ID {
		t.Fatalf("event %+v %v", ev, err)
	}
	if v := toV2Event(ev); v.TrackID != cam.ID+":7" {
		t.Fatalf("v2 event %+v", v)
	}
	// An Animal (a bird) never alarms.
	s.onDetection(ctx, cam.ID, link.Detection{Source: link.SourceObjects, Kind: "Animal", Label: "bird", Score: 0.7, TrackID: cam.ID + ":8",
		EventID: "EVBIRD00000000000000000000000001", CreatedAt: now.Unix(), At: now.UnixMilli(), MergeSince: now.Add(time.Second).UnixMilli()})
	if a, _ := s.Store.Alarms(ctx, false, 50); len(a) != 1 {
		t.Fatalf("bird alarmed: %+v", a)
	}
}

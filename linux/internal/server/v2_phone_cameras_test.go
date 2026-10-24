package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/cloud"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/videogw"
	"sentinel-linux/internal/videotoken"
)

// setCloudPlan makes the site linked to Sentinel Cloud with plan/status.
func setCloudPlan(t *testing.T, s *Server, plan, status string) {
	t.Helper()
	ctx := context.Background()
	sealed, err := s.Store.Seal("srv_test")
	if err != nil {
		t.Fatal(err)
	}
	ent, _ := json.Marshal(cloud.Entitlement{ServerID: "k7f3q9x2ab", Plan: plan, Status: status, Features: []string{}})
	for k, v := range map[string]string{
		cloud.KeyToken: sealed, cloud.KeyServerID: "k7f3q9x2ab", cloud.KeyEntitlement: string(ent),
		cloud.KeyEntitlementAt: time.Now().UTC().Format(time.RFC3339),
	} {
		if err := s.Store.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
}

// freshPushLimits gives a test its own push-token rate limits.
func freshPushLimits(t *testing.T) {
	t.Helper()
	pc, pk := pushTokenPerCamera, pushTokenPerClient
	pushTokenPerCamera = &videogw.SessionLimiter{N: 10, Window: time.Minute}
	pushTokenPerClient = &videogw.SessionLimiter{N: 30, Window: time.Minute}
	t.Cleanup(func() { pushTokenPerCamera, pushTokenPerClient = pc, pk })
}

func pushToken(t *testing.T, h http.Handler, camID, key string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v2/cameras/"+camID+"/push-token", nil)
	if key != "" {
		r.Header.Set(pushKeyHeader, key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

// newPhoneCamera adds a phone camera on a paid site; returns its id and key.
func newPhoneCamera(t *testing.T, s *Server, admin string) (string, string) {
	t.Helper()
	code, m := v2Call(t, s.Handler(), "POST", "/api/v2/phone-cameras", admin, `{"name":"Kitchen iPhone"}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, m)
	}
	return m["camera"].(map[string]any)["id"].(string), m["pushKey"].(string)
}

func TestPhoneCameraCreateRotateRevoke(t *testing.T) {
	freshPushLimits(t)
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	op := v2Token(t, s, "op", "Operator")

	// Sentinel Cloud only.
	code, m := v2Call(t, h, "POST", "/api/v2/phone-cameras", admin, `{"name":"Kitchen iPhone"}`)
	if code != 402 || errCode(m) != "cloud_required" || errMessage(m) != "Using an iPhone as a camera is part of Sentinel Cloud." {
		t.Fatalf("unpaid: %d %v", code, m)
	}
	setCloudPlan(t, s, "cloud", "active")
	if code, _ := v2Call(t, h, "POST", "/api/v2/phone-cameras", op, `{"name":"x"}`); code != 403 {
		t.Fatalf("operator: %d", code)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/phone-cameras", admin, `{"name":"x","recordingMode":"dual"}`); code != 400 || !strings.Contains(errMessage(m), "dual") {
		t.Fatalf("dual: %d %v", code, m)
	}
	code, m = v2Call(t, h, "POST", "/api/v2/phone-cameras", admin, `{"name":"Kitchen iPhone","recordingMode":"events"}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, m)
	}
	key := m["pushKey"].(string)
	cam := m["camera"].(map[string]any)
	id := cam["id"].(string)
	phone, _ := cam["phone"].(map[string]any)
	if len(key) != 43 || cam["source"] != "phone" || cam["rtspUrl"] != "" || phone == nil || phone["hasPushKey"] != true || phone["streaming"] != false ||
		cam["recordingMode"] != "events" {
		t.Fatalf("created %v", m)
	}
	// The recording server got a publisher path.
	if b, err := os.ReadFile(recOf(s).Media.ConfigPath); err != nil || !strings.Contains(string(b), "  "+id+":\n    source: publisher") {
		t.Fatalf("media config: %v\n%s", err, b)
	}
	// Edits: no RTSP fields, no dual, no source change; empty strings are fine.
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"recordingMode":"dual"}`, 400},
		{`{"rtspUrl":"rtsp://10.0.0.1/x"}`, 400},
		{`{"source":"rtsp"}`, 400},
		{`{"name":"Kitchen","rtspUrl":"","username":"","subRtspUrl":"","source":"phone","recordingMode":"continuous"}`, 200},
	} {
		if code, m := v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, tc.body); code != tc.code {
			t.Errorf("patch %s: %d %v", tc.body, code, m)
		}
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"y","rtspUrl":"rtsp://10.0.0.2/s","source":"phone"}`); code != 400 {
		t.Errorf("POST /cameras source phone: %d %v", code, m)
	}

	// The key buys a push token: this camera, the push right only.
	code, m = pushToken(t, h, id, key)
	if code != 200 || m["recorderId"] != "local" || m["cameraId"] != id || !strings.HasPrefix(m["baseUrl"].(string), "/v/") {
		t.Fatalf("push token: %d %v", code, m)
	}
	signer, _ := s.videoSigner(context.Background())
	claims, err := videotoken.NewVerifier("local", signer.PublicKey()).Verify(m["token"].(string))
	if err != nil || claims.Camera != id || !claims.Has(videotoken.Push) || claims.Has(videotoken.Live) || len(claims.Rights) != 1 {
		t.Fatalf("claims %+v %v", claims, err)
	}
	if d := claims.ExpiresAt().Sub(time.Now()); d < 4*time.Minute || d > 6*time.Minute {
		t.Errorf("ttl %v", d)
	}
	rtsp := addCam(t, s)
	for _, tc := range []struct{ cam, key string }{{id, ""}, {id, key + "x"}, {id, strings.Repeat("A", 43)}, {rtsp.ID, key}, {"nope", key}} {
		if code, m := pushToken(t, h, tc.cam, tc.key); code != 401 || errCode(m) != "unauthorized" {
			t.Errorf("bad key %q for %s: %d %v", tc.key, tc.cam, code, m)
		}
	}
	// video-token never hands out push.
	if code, _ := videoToken(t, s, admin, id, `{"rights":["push"]}`); code != 400 {
		t.Errorf("video-token push: %d", code)
	}

	// Rotate: the old key stops at once.
	if code, _ := v2Call(t, h, "POST", "/api/v2/cameras/"+id+"/push-key", op, ""); code != 403 {
		t.Errorf("operator rotate: %d", code)
	}
	code, m = v2Call(t, h, "POST", "/api/v2/cameras/"+id+"/push-key", admin, "")
	newKey, _ := m["pushKey"].(string)
	if code != 200 || newKey == "" || newKey == key || m["camera"].(map[string]any)["id"] != id {
		t.Fatalf("rotate: %d %v", code, m)
	}
	if code, _ := pushToken(t, h, id, key); code != 401 {
		t.Errorf("old key after rotate: %d", code)
	}
	if code, _ := pushToken(t, h, id, newKey); code != 200 {
		t.Errorf("new key: %d", code)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/cameras/"+rtsp.ID+"/push-key", admin, ""); code != 404 {
		t.Errorf("rotate an RTSP camera: %d", code)
	}

	// Subscription ends: no new tokens, status says why; managing still works.
	setCloudPlan(t, s, "cloud", "canceled")
	if code, m := pushToken(t, h, id, newKey); code != 402 || errCode(m) != "cloud_required" {
		t.Errorf("canceled: %d %v", code, m)
	}
	// (The test site has no media engine, so build the view as if it ran.)
	stored, _ := s.Store.Camera(context.Background(), id)
	view := func() string {
		running := link.ServerStatus{Media: link.MediaStatus{Running: true}, Cameras: map[string]link.CameraStatus{id: {Push: &link.PushStatus{}}}}
		v := []v2Camera{toV2Camera(stored, running, true)}
		s.fillPhoneStatus(context.Background(), v)
		return v[0].Status.Message
	}
	if msg := view(); msg != "Needs Sentinel Cloud" {
		t.Errorf("status when unpaid: %q", msg)
	}
	setCloudPlan(t, s, "business", "grace")
	if msg := view(); msg != "Phone not streaming" {
		t.Errorf("status in grace: %q", msg)
	}
	setCloudPlan(t, s, "free", "inactive")

	// Revoke (not gated).
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/cameras/"+id+"/push-key", admin, ""); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	setCloudPlan(t, s, "cloud", "active")
	if code, _ := pushToken(t, h, id, newKey); code != 401 {
		t.Errorf("revoked key: %d", code)
	}
	if _, m := v2Call(t, h, "GET", "/api/v2/cameras/"+id, admin, ""); m["phone"].(map[string]any)["hasPushKey"] != false {
		t.Errorf("hasPushKey after revoke: %v", m["phone"])
	}

	// Audited, and the key never is.
	entries, _ := s.Store.AuditLog(context.Background(), 100)
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
		if strings.Contains(e.Detail, key) || strings.Contains(e.Detail, newKey) {
			t.Errorf("key in audit: %+v", e)
		}
	}
	for _, want := range []string{"Added iPhone camera", "Rotated iPhone camera key", "Revoked iPhone camera key"} {
		if !strings.Contains(strings.Join(actions, "|"), want) {
			t.Errorf("audit lacks %q: %v", want, actions)
		}
	}
}

func TestPushTokenRateLimit(t *testing.T) {
	freshPushLimits(t)
	s := newCompanionServer(t)
	setCloudPlan(t, s, "cloud", "active")
	id, _ := newPhoneCamera(t, s, v2Token(t, s, "boss", "Admin"))
	for i := 0; i < 10; i++ {
		if code, _ := pushToken(t, s.Handler(), id, "wrong"); code != 401 {
			t.Fatalf("try %d: %d", i, code)
		}
	}
	if code, _ := pushToken(t, s.Handler(), id, "wrong"); code != 429 {
		t.Fatalf("11th: %d", code)
	}
}

func TestPushThroughGateway(t *testing.T) {
	freshPushLimits(t)
	s := newCompanionServer(t)
	h := s.Handler()
	setCloudPlan(t, s, "cloud", "active")
	admin := v2Token(t, s, "boss", "Admin")
	id, key := newPhoneCamera(t, s, admin)
	_, m := pushToken(t, h, id, key)
	base := m["baseUrl"].(string)
	do := func(method, path string, body []byte) (int, map[string]any) {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, m := do("POST", base+"/push/"+id+"/sessions", []byte(`{"video":"h264","width":1280,"height":720,"fps":30,"audio":"aac","device":"EJ's iPhone"}`))
	if code != 200 || m["maxSegmentBytes"] != float64(8<<20) {
		t.Fatalf("start through the gateway: %d %v", code, m)
	}
	sid := m["sessionId"].(string)
	// Before the init segment: 409 with what's expected.
	moof := append([]byte{0, 0, 0, 12}, []byte("moofxxxx")...)
	if code, m := do("PUT", base+"/push/"+id+"/sessions/"+sid+"/segments/0", moof); code != 409 || m["expected"] != float64(0) {
		t.Fatalf("segment before init: %d %v", code, m)
	}
	// The gateway refuses bodies over 8 MiB.
	if code, _ := do("PUT", base+"/push/"+id+"/sessions/"+sid+"/segments/0", make([]byte, 8<<20+1)); code != 413 {
		t.Fatalf("big segment: %d", code)
	}
	// The recording server reports the session in its status.
	if p := recOf(s).Status().Cameras[id].Push; p == nil || !p.Streaming || p.Device != "EJ's iPhone" {
		t.Fatalf("recorder status %+v", p)
	}
	// A push token can't watch, a live token can't push.
	if code, _ := do("GET", base+"/live/"+id+"/index.m3u8", nil); code != 403 {
		t.Errorf("push token reading live: %d", code)
	}
	live := mustVideoToken(t, s, admin, id)
	if code, _ := do("POST", live+"/push/"+id+"/sessions", []byte(`{"video":"h264"}`)); code != 403 {
		t.Errorf("live token pushing: %d", code)
	}
	if code, _ := do("DELETE", base+"/push/"+id+"/sessions/"+sid, nil); code != 204 {
		t.Errorf("stop: %d", code)
	}
}

func TestPhoneCameraStatusMessages(t *testing.T) {
	cam := &store.Camera{ID: "CAM", Name: "Kitchen", Source: link.SourcePhone, HasPushKey: true}
	rs := link.ServerStatus{Media: link.MediaStatus{Running: true}, Cameras: map[string]link.CameraStatus{
		"CAM": {Ready: true, Push: &link.PushStatus{Streaming: true, Device: "EJ's iPhone", LastSeenAt: 1234}},
	}}
	v := toV2Camera(cam, rs, true)
	if v.Status.Message != "Streaming from EJ's iPhone" || !v.Status.Online || v.Source != "phone" || v.LastSeenAt != 1234 || v.Phone.LastSeenAt != 1234 || !v.Phone.Streaming {
		t.Fatalf("streaming: %+v %+v", v.Status, v.Phone)
	}
	rs.Cameras["CAM"] = link.CameraStatus{Push: &link.PushStatus{Device: "EJ's iPhone", LastSeenAt: 1234}}
	if v := toV2Camera(cam, rs, true); v.Status.Message != "Phone not streaming" || v.Status.Online || v.LastSeenAt != 1234 {
		t.Fatalf("stopped: %+v", v.Status)
	}
	if v := toV2Camera(cam, rs, false); v.Status.Message != "Recording server offline" || v.Phone.Streaming {
		t.Fatalf("server offline: %+v", v.Status)
	}
	rtsp := &store.Camera{ID: "R", Name: "Door", Source: "rtsp"}
	if v := toV2Camera(rtsp, rs, true); v.Source != "rtsp" || v.Phone != nil || v.Status.Message != "No signal" {
		t.Fatalf("rtsp: %+v", v)
	}
}

func TestCloudStatusSaysWhatThePlanAllows(t *testing.T) {
	s := newCompanionServer(t)
	viewer := v2Token(t, s, "vera", "Viewer")
	for _, tc := range []struct {
		plan, status string
		want         bool
	}{{"", "", false}, {"free", "inactive", false}, {"cloud", "canceled", false}, {"cloud", "inactive", false}, {"cloud", "active", true}, {"business", "grace", true}} {
		if tc.plan != "" {
			setCloudPlan(t, s, tc.plan, tc.status)
		}
		code, m := v2Call(t, s.Handler(), "GET", "/api/v2/cloud", viewer, "")
		if code != 200 || m["uploadsAvailable"] != tc.want || m["phoneCamerasAvailable"] != tc.want {
			t.Errorf("%s/%s: %d uploads %v phone %v", tc.plan, tc.status, code, m["uploadsAvailable"], m["phoneCamerasAvailable"])
		}
	}
}

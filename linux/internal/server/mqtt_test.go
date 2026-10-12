package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/store"
)

func testBroker(t *testing.T) (string, int) {
	t.Helper()
	srv := mochi.New(nil)
	_ = srv.AddHook(new(auth.AllowHook), nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := srv.AddListener(listeners.NewTCP(listeners.Config{ID: "t", Address: addr})); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })
	host, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)
	return host, port
}

type mqttWatch struct {
	mu   sync.Mutex
	last map[string]string
	c    paho.Client
}

func watchBroker(t *testing.T, host string, port int) *mqttWatch {
	t.Helper()
	w := &mqttWatch{last: map[string]string{}}
	w.c = paho.NewClient(paho.NewClientOptions().AddBroker(fmt.Sprintf("tcp://%s:%d", host, port)).SetClientID("ha-test"))
	if tok := w.c.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal("watch connect failed")
	}
	w.c.Subscribe("#", 1, func(_ paho.Client, m paho.Message) {
		w.mu.Lock()
		w.last[m.Topic()] = string(m.Payload())
		w.mu.Unlock()
	}).WaitTimeout(5 * time.Second)
	t.Cleanup(func() { w.c.Disconnect(50) })
	return w
}

func (w *mqttWatch) wait(t *testing.T, topic string, ok func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		v, have := w.last[topic]
		w.mu.Unlock()
		if have && ok(v) {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", topic)
	return ""
}

func TestMQTTSettingsAPI(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	admin, _ := s.Store.CreateUser(ctx, "admin", "Admin", "x")
	op, _ := s.Store.CreateUser(ctx, "op", "Operator", "x")
	adminTok, _ := s.Store.CreateSession(ctx, admin.ID, time.Hour)
	opTok, _ := s.Store.CreateSession(ctx, op.ID, time.Hour)
	h := s.Handler()
	call := func(method, path, body, tok string) (int, map[string]any) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}

	if c, _ := call("GET", "/api/v1/mqtt", "", opTok); c != http.StatusForbidden {
		t.Fatalf("operator GET = %d", c)
	}
	if c, _ := call("PUT", "/api/v1/mqtt", `{"enabled":false}`, opTok); c != http.StatusForbidden {
		t.Fatalf("operator PUT = %d", c)
	}
	c, m := call("GET", "/api/v1/mqtt", "", adminTok)
	if c != 200 || m["enabled"] != false || m["topicPrefix"] != "sentinel" || m["discoveryPrefix"] != "homeassistant" || m["port"] != float64(1883) {
		t.Fatalf("defaults: %d %v", c, m)
	}
	if c, m := call("PUT", "/api/v1/mqtt", `{"enabled":true,"host":"tcp://x"}`, adminTok); c != http.StatusBadRequest {
		t.Fatalf("bad host accepted: %d %v", c, m)
	}
	// Point at a closed port: saving works; the bridge just reports an error.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	body := fmt.Sprintf(`{"enabled":true,"host":"127.0.0.1","port":%d,"username":"ha","password":"hunter2-secret","topicPrefix":"vms"}`, port)
	c, m = call("PUT", "/api/v1/mqtt", body, adminTok)
	if c != 200 || m["hasPassword"] != true || m["username"] != "ha" || m["topicPrefix"] != "vms" {
		t.Fatalf("save: %d %v", c, m)
	}
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "hunter2") {
		t.Fatal("password returned to the client")
	}
	if strings.Contains(s.Store.Setting(ctx, mqttSetting, ""), "hunter2") || strings.Contains(s.Store.Setting(ctx, mqttPasswordSetting, ""), "hunter2") {
		t.Fatal("password stored in plain text")
	}
	if s.mqttConfig(ctx).Password != "hunter2-secret" {
		t.Fatal("sealed password doesn't round-trip")
	}
	// Omitting the password keeps it.
	if _, m = call("PUT", "/api/v1/mqtt", `{"tls":false}`, adminTok); m["hasPassword"] != true {
		t.Fatalf("password dropped: %v", m)
	}
	if _, m = call("POST", "/api/v1/mqtt/test", "", adminTok); m["ok"] != false || m["error"] == "" {
		t.Fatalf("test against closed port: %v", m)
	}
	entries, _ := s.Store.AuditLog(ctx, 50)
	var actions []string
	for _, e := range entries {
		if e.Area == "MQTT" {
			actions = append(actions, e.Action)
			if strings.Contains(e.Detail, "hunter2") {
				t.Fatal("password in audit log")
			}
		}
	}
	joined := strings.Join(actions, "|")
	if !strings.Contains(joined, "Changed MQTT settings") || !strings.Contains(joined, "Changed MQTT password") || !strings.Contains(joined, "Tested MQTT connection") {
		t.Fatalf("audit = %v", actions)
	}
	call("PUT", "/api/v1/mqtt", `{"enabled":false}`, adminTok)
	if st := s.mqttBridge().Status(); st.State != "disabled" {
		t.Fatalf("status after disable = %+v", st)
	}
}

func TestMQTTPipelineAndCommands(t *testing.T) {
	host, port := testBroker(t)
	watch := watchBroker(t, host, port)
	s := newCompanionServer(t)
	dir := t.TempDir()
	recOf(s).Media = &media.Supervisor{ConfigPath: filepath.Join(dir, "mediamtx.yml"), RecordingsRoot: filepath.Join(dir, "rec"), Ports: media.DefaultPorts}
	ctx := context.Background()
	cam, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Gate", RTSPURL: "rtsp://10.0.0.9/s", Recording: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"enabled": true, "host": host, "port": port, "allowCommands": true})
	_ = s.Store.SetSetting(ctx, mqttSetting, string(cfg))
	if err := s.ApplyMedia(ctx); err != nil { // starts the bridge and publishes the camera
		t.Fatal(err)
	}
	t.Cleanup(s.mqttBridge().Close)
	watch.wait(t, "sentinel/available", func(v string) bool { return v == "online" })
	watch.wait(t, "homeassistant/switch/"+"sentinel_"+strings.ToLower(strings.ReplaceAll(cam.ID, "-", ""))+"/recording/config", func(v string) bool { return v != "" })
	watch.wait(t, "sentinel/"+cam.ID+"/recording/state", func(v string) bool { return v == "ON" })

	// Motion: binary sensor ON, event + alarm published.
	recOf(s).OnMotion(detect.Motion{CameraID: cam.ID, Score: 0.2, At: time.Now()})
	watch.wait(t, "sentinel/"+cam.ID+"/motion", func(v string) bool { return v == "ON" })
	ev := watch.wait(t, "sentinel/events", func(v string) bool { return strings.Contains(v, `"kind":"Motion"`) })
	if !strings.Contains(ev, `"camera":"Gate"`) || !strings.Contains(ev, `"type":"new"`) {
		t.Fatalf("event = %s", ev)
	}
	watch.wait(t, "sentinel/"+cam.ID+"/last_event", func(v string) bool { return strings.Contains(v, `"Motion"`) })
	watch.wait(t, "sentinel/alarms", func(v string) bool {
		return strings.Contains(v, `"title":"Motion detected"`) && strings.Contains(v, `"type":"new"`)
	})

	// Home Assistant switches recording off: same save path + audit, state republished.
	watch.c.Publish("sentinel/"+cam.ID+"/recording/set", 1, false, "OFF").WaitTimeout(time.Second)
	watch.wait(t, "sentinel/"+cam.ID+"/recording/state", func(v string) bool { return v == "OFF" })
	got, _ := s.Store.Camera(ctx, cam.ID)
	if got.Recording {
		t.Fatal("recording still on")
	}
	entries, _ := s.Store.AuditLog(ctx, 10)
	found := false
	for _, e := range entries {
		if e.User == mqttAuditUser && e.Action == "Turned recording off" && strings.Contains(e.Detail, "Gate") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit entry for the MQTT command: %+v", entries)
	}
	// Detection off then on restores the previous sensitivity.
	_, _ = s.Store.SaveCamera(ctx, cam.ID, store.CameraInput{Name: "Gate", RTSPURL: "rtsp://10.0.0.9/s", MotionLevel: mqttPtr(3)})
	_ = s.ApplyMedia(ctx)
	watch.c.Publish("sentinel/"+cam.ID+"/detection/set", 1, false, "OFF").WaitTimeout(time.Second)
	watch.wait(t, "sentinel/"+cam.ID+"/detection/state", func(v string) bool { return v == "OFF" })
	watch.c.Publish("sentinel/"+cam.ID+"/detection/set", 1, false, "ON").WaitTimeout(time.Second)
	watch.wait(t, "sentinel/"+cam.ID+"/detection/state", func(v string) bool { return v == "ON" })
	if got, _ := s.Store.Camera(ctx, cam.ID); got.MotionLevel != 3 {
		t.Fatalf("motion level = %d, want 3 restored", got.MotionLevel)
	}
}

func mqttPtr[T any](v T) *T { return &v }

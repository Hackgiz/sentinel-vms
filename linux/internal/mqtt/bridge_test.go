package mqtt

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
)

const camID = "0A1B2C3D-0000-4000-8000-00000000CAFE"

// startBroker runs an in-process MQTT broker on a free loopback port.
func startBroker(t *testing.T, ledger *auth.Ledger) (string, int) {
	t.Helper()
	srv := mochi.New(&mochi.Options{InlineClient: true})
	var err error
	if ledger == nil {
		err = srv.AddHook(new(auth.AllowHook), nil)
	} else {
		err = srv.AddHook(new(auth.Hook), &auth.Options{Ledger: ledger})
	}
	if err != nil {
		t.Fatal(err)
	}
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
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	return host, p
}

type got struct {
	payload  []byte
	retained bool
	at       time.Time
}

// recorder is a plain MQTT client subscribed to everything (like Home Assistant).
type recorder struct {
	mu     sync.Mutex
	last   map[string]got
	all    map[string][]got
	client paho.Client
}

func newRecorder(t *testing.T, host string, port int) *recorder {
	t.Helper()
	r := &recorder{last: map[string]got{}, all: map[string][]got{}}
	o := paho.NewClientOptions().AddBroker("tcp://" + net.JoinHostPort(host, strconv.Itoa(port))).SetClientID("recorder")
	r.client = paho.NewClient(o)
	if tok := r.client.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("recorder connect: %v", tok.Error())
	}
	tok := r.client.Subscribe("#", 1, func(_ paho.Client, m paho.Message) {
		r.mu.Lock()
		defer r.mu.Unlock()
		g := got{payload: append([]byte(nil), m.Payload()...), retained: m.Retained(), at: time.Now()}
		r.last[m.Topic()] = g
		r.all[m.Topic()] = append(r.all[m.Topic()], g)
	})
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal("recorder subscribe failed")
	}
	t.Cleanup(func() { r.client.Disconnect(50) })
	return r
}

func (r *recorder) wait(t *testing.T, topic string, ok func([]byte) bool) got {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		g, have := r.last[topic]
		r.mu.Unlock()
		if have && ok(g.payload) {
			return g
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var topics []string
	for k := range r.last {
		topics = append(topics, k)
	}
	t.Fatalf("timed out waiting for %s (last: %q); have %v", topic, r.last[topic].payload, topics)
	return got{}
}

// retained connects a fresh client and returns the broker's retained message.
func retained(t *testing.T, host string, port int, topic string) ([]byte, bool) {
	t.Helper()
	o := paho.NewClientOptions().AddBroker("tcp://" + net.JoinHostPort(host, strconv.Itoa(port))).SetClientID("late-" + strconv.FormatInt(time.Now().UnixNano(), 36))
	c := paho.NewClient(o)
	if tok := c.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal("late subscriber connect failed")
	}
	defer c.Disconnect(50)
	ch := make(chan []byte, 1)
	c.Subscribe(topic, 1, func(_ paho.Client, m paho.Message) {
		if m.Retained() {
			select {
			case ch <- append([]byte(nil), m.Payload()...):
			default:
			}
		}
	}).WaitTimeout(5 * time.Second)
	select {
	case p := <-ch:
		return p, true
	case <-time.After(300 * time.Millisecond):
		return nil, false
	}
}

func equals(s string) func([]byte) bool { return func(b []byte) bool { return string(b) == s } }
func nonEmpty(b []byte) bool            { return len(b) > 0 }

func newBridge(t *testing.T, host string, port int, mutate func(*Config)) *Bridge {
	t.Helper()
	b := &Bridge{Version: "1.2.3", OffAfter: 300 * time.Millisecond}
	b.SyncCameras([]Camera{{ID: camID, Name: "Front Door", Recording: true, Detection: true}})
	cfg := Config{Enabled: true, Host: host, Port: port, ClientID: "sentinel-test"}
	if mutate != nil {
		mutate(&cfg)
	}
	b.Apply(context.Background(), cfg)
	t.Cleanup(b.Close)
	return b
}

func TestDiscoveryPayloads(t *testing.T) {
	host, port := startBroker(t, nil)
	rec := newRecorder(t, host, port)
	b := newBridge(t, host, port, nil)
	rec.wait(t, "sentinel/available", equals("online"))
	if st := b.Status(); st.State != "connected" {
		t.Fatalf("status = %+v", st)
	}

	node := nodeID(camID)
	if node != "sentinel_0a1b2c3d00004000800000000000cafe" {
		t.Fatalf("node id = %s", node)
	}
	g := rec.wait(t, "homeassistant/binary_sensor/"+node+"/motion/config", nonEmpty)
	if p, ok := retained(t, host, port, "homeassistant/binary_sensor/"+node+"/motion/config"); !ok || !bytes.Equal(p, g.payload) {
		t.Error("discovery must be retained")
	}
	var cfg map[string]any
	if err := json.Unmarshal(g.payload, &cfg); err != nil {
		t.Fatal(err)
	}
	dev, _ := cfg["device"].(map[string]any)
	ids, _ := dev["identifiers"].([]any)
	if cfg["state_topic"] != "sentinel/"+camID+"/motion" || cfg["device_class"] != "motion" ||
		cfg["unique_id"] != node+"_motion" || cfg["availability_topic"] != "sentinel/available" ||
		dev["name"] != "Front Door" || len(ids) != 1 || ids[0] != node || dev["sw_version"] != "1.2.3" {
		t.Fatalf("motion config = %s", g.payload)
	}
	for _, topic := range []string{
		"homeassistant/binary_sensor/" + node + "/person/config",
		"homeassistant/binary_sensor/" + node + "/vehicle/config",
		"homeassistant/binary_sensor/" + node + "/online/config",
		"homeassistant/sensor/" + node + "/last_event/config",
	} {
		rec.wait(t, topic, nonEmpty)
	}
	img := rec.wait(t, "homeassistant/image/"+node+"/snapshot/config", nonEmpty)
	if !bytes.Contains(img.payload, []byte(`"image_topic":"sentinel/`+camID+`/snapshot"`)) || !bytes.Contains(img.payload, []byte(`"content_type":"image/jpeg"`)) {
		t.Fatalf("image config = %s", img.payload)
	}
	// Commands are off by default: no switches.
	time.Sleep(100 * time.Millisecond)
	rec.mu.Lock()
	sw, has := rec.last["homeassistant/switch/"+node+"/recording/config"]
	rec.mu.Unlock()
	if has && len(sw.payload) > 0 {
		t.Fatalf("switch published with commands off: %s", sw.payload)
	}
	rec.wait(t, "sentinel/"+camID+"/recording/state", equals("ON"))
	rec.wait(t, "sentinel/"+camID+"/motion", equals("OFF"))

	// Deleting the camera removes its entities (empty retained configs).
	b.SyncCameras(nil)
	rec.wait(t, "homeassistant/binary_sensor/"+node+"/motion/config", func(p []byte) bool { return len(p) == 0 })
	rec.wait(t, "homeassistant/image/"+node+"/snapshot/config", func(p []byte) bool { return len(p) == 0 })
}

func TestBinarySensorOffTimeout(t *testing.T) {
	host, port := startBroker(t, nil)
	rec := newRecorder(t, host, port)
	b := newBridge(t, host, port, nil)
	topic := "sentinel/" + camID + "/person"
	rec.wait(t, topic, equals("OFF"))

	start := time.Now()
	b.Touch(camID, "Person")
	on := rec.wait(t, topic, equals("ON"))
	// Keep it alive past one timeout: it must stay ON.
	time.Sleep(200 * time.Millisecond)
	b.Touch(camID, "Person")
	time.Sleep(200 * time.Millisecond)
	rec.mu.Lock()
	if string(rec.last[topic].payload) != "ON" {
		rec.mu.Unlock()
		t.Fatal("went OFF while detections continued")
	}
	rec.mu.Unlock()
	off := rec.wait(t, topic, equals("OFF"))
	if d := off.at.Sub(start); d < 480*time.Millisecond {
		t.Fatalf("turned off after %v; want ≥ ~200ms + 300ms timeout", d)
	}
	_ = on
	if p, ok := retained(t, host, port, topic); !ok || string(p) != "OFF" {
		t.Errorf("state must be retained, got %q", p)
	}
	// Repeated touches while ON publish nothing extra.
	rec.mu.Lock()
	n := len(rec.all[topic])
	rec.mu.Unlock()
	if n > 4 { // OFF (initial, maybe twice with bootstrap) + ON + OFF
		t.Fatalf("too many state messages: %d", n)
	}
	// Unknown kinds and cameras are ignored.
	b.Touch(camID, "Animal")
	b.Touch("NOPE", "Person")
}

func TestEventsSnapshotAlarmsAndOnline(t *testing.T) {
	host, port := startBroker(t, nil)
	rec := newRecorder(t, host, port)
	b := newBridge(t, host, port, nil)
	rec.wait(t, "sentinel/available", equals("online"))

	b.CameraOnline(camID, true)
	rec.wait(t, "sentinel/"+camID+"/online", equals("ON"))
	b.CameraOnline(camID, false)
	rec.wait(t, "sentinel/"+camID+"/online", equals("OFF"))

	b.Event(Event{Type: "new", ID: "E1", CameraID: camID, Camera: "Front Door", Kind: "Person", Label: "person", Score: 0.91, Time: 1760000000})
	ev := rec.wait(t, "sentinel/events", nonEmpty)
	var e Event
	if err := json.Unmarshal(ev.payload, &e); err != nil || e.ID != "E1" || e.Kind != "Person" || e.Camera != "Front Door" || e.TimeISO == "" {
		t.Fatalf("event = %s (retained %v)", ev.payload, ev.retained)
	}
	if !b.WantsSnapshot(camID, "E1") || b.WantsSnapshot(camID, "OTHER") {
		t.Fatal("WantsSnapshot")
	}
	b.Event(Event{Type: "update", ID: "E1", CameraID: camID, Camera: "Front Door", Kind: "Person", Score: 0.91, Time: 1760000000,
		Description: "A courier leaves a parcel.", Snapshot: "/api/v1/events/E1/thumbnail.jpg"})
	last := rec.wait(t, "sentinel/"+camID+"/last_event", func(p []byte) bool { return bytes.Contains(p, []byte("courier")) })
	if p, ok := retained(t, host, port, "sentinel/"+camID+"/last_event"); !ok || !bytes.Equal(p, last.payload) {
		t.Error("last_event must be retained")
	}
	if !bytes.Contains(last.payload, []byte(`"snapshot":"/api/v1/events/E1/thumbnail.jpg"`)) {
		t.Fatalf("last_event = %s", last.payload)
	}
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3, 0xFF, 0xD9}
	b.Snapshot(camID, "E1", jpeg)
	snap := rec.wait(t, "sentinel/"+camID+"/snapshot", nonEmpty)
	if p, ok := retained(t, host, port, "sentinel/"+camID+"/snapshot"); !bytes.Equal(snap.payload, jpeg) || !ok || !bytes.Equal(p, jpeg) {
		t.Fatal("snapshot payload")
	}
	if b.WantsSnapshot(camID, "E1") {
		t.Fatal("snapshot sent twice")
	}
	// An older event arriving late doesn't replace last_event.
	b.Event(Event{Type: "update", ID: "E0", CameraID: camID, Kind: "Motion", Time: 1759999000})
	rec.wait(t, "sentinel/events", func(p []byte) bool { return bytes.Contains(p, []byte(`"E0"`)) })
	time.Sleep(50 * time.Millisecond)
	rec.mu.Lock()
	if !bytes.Contains(rec.last["sentinel/"+camID+"/last_event"].payload, []byte(`"E1"`)) {
		t.Error("last_event rolled back to an older event")
	}
	rec.mu.Unlock()

	b.Alarm(Alarm{Type: "new", ID: "A1", CameraID: camID, Camera: "Front Door", Kind: "Person", Title: "Person detected", Severity: "Critical", State: "New"})
	al := rec.wait(t, "sentinel/alarms", nonEmpty)
	if !bytes.Contains(al.payload, []byte(`"title":"Person detected"`)) {
		t.Fatalf("alarm = %s", al.payload)
	}
	for _, topic := range []string{"sentinel/events", "sentinel/alarms"} {
		if _, ok := retained(t, host, port, topic); ok {
			t.Errorf("%s must not be retained", topic)
		}
	}
}

func TestCommandsAndBirthMessage(t *testing.T) {
	host, port := startBroker(t, nil)
	rec := newRecorder(t, host, port)
	type cmd struct {
		cam, what string
		on        bool
	}
	cmds := make(chan cmd, 10)
	b := &Bridge{OnCommand: func(c, w string, on bool) { cmds <- cmd{c, w, on} }}
	b.SyncCameras([]Camera{{ID: camID, Name: "Front Door", Recording: true}})
	b.Apply(context.Background(), Config{Enabled: true, Host: host, Port: port, AllowCommands: true, TopicPrefix: "vms", DiscoveryPrefix: "ha"})
	t.Cleanup(b.Close)

	node := nodeID(camID)
	sw := rec.wait(t, "ha/switch/"+node+"/recording/config", nonEmpty)
	if !bytes.Contains(sw.payload, []byte(`"command_topic":"vms/`+camID+`/recording/set"`)) {
		t.Fatalf("switch config = %s", sw.payload)
	}
	rec.wait(t, "ha/switch/"+node+"/detection/config", nonEmpty)
	rec.wait(t, "vms/"+camID+"/detection/state", equals("OFF"))

	pub := func(topic, payload string) {
		rec.client.Publish(topic, 1, false, payload).WaitTimeout(time.Second)
	}
	pub("vms/"+camID+"/recording/set", "garbage")
	pub("vms/UNKNOWN-CAMERA/recording/set", "OFF")
	pub("vms/"+camID+"/format/set", "ON")
	pub("vms/"+camID+"/recording/set", "OFF")
	select {
	case c := <-cmds:
		if c != (cmd{camID, "recording", false}) {
			t.Fatalf("command = %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no command")
	}
	select {
	case c := <-cmds:
		t.Fatalf("invalid command passed through: %+v", c)
	case <-time.After(200 * time.Millisecond):
	}

	// Home Assistant restarting (birth message) gets discovery again.
	rec.mu.Lock()
	before := len(rec.all["ha/sensor/"+node+"/last_event/config"])
	rec.mu.Unlock()
	pub("ha/status", "online")
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec.mu.Lock()
		n := len(rec.all["ha/sensor/"+node+"/last_event/config"])
		rec.mu.Unlock()
		if n > before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("discovery not republished after HA birth message")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCommandsIgnoredWhenNotAllowed(t *testing.T) {
	host, port := startBroker(t, nil)
	rec := newRecorder(t, host, port)
	var calls atomic.Int32
	b := &Bridge{OnCommand: func(string, string, bool) { calls.Add(1) }}
	b.SyncCameras([]Camera{{ID: camID, Name: "Front Door"}})
	b.Apply(context.Background(), Config{Enabled: true, Host: host, Port: port})
	t.Cleanup(b.Close)
	rec.wait(t, "sentinel/available", equals("online"))
	rec.client.Publish("sentinel/"+camID+"/recording/set", 1, false, "OFF").WaitTimeout(time.Second)
	time.Sleep(200 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("command handled while commands are off")
	}
}

func TestOfflineOnCloseAndWill(t *testing.T) {
	host, port := startBroker(t, nil)
	rec := newRecorder(t, host, port)
	b := newBridge(t, host, port, nil)
	rec.wait(t, "sentinel/available", equals("online"))
	b.Close()
	rec.wait(t, "sentinel/available", equals("offline"))
	if st := b.Status(); st.State != "disabled" {
		t.Fatalf("status after close = %+v", st)
	}
}

func TestQueueIsBoundedAndNeverBlocks(t *testing.T) {
	// Nothing listens on this port: the bridge keeps retrying in the background.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	b := &Bridge{QueueSize: 5}
	b.SyncCameras([]Camera{{ID: camID, Name: "Front Door"}})
	b.Apply(context.Background(), Config{Enabled: true, Host: "127.0.0.1", Port: port})
	t.Cleanup(b.Close)
	start := time.Now()
	for i := 0; i < 1000; i++ {
		b.Event(Event{ID: strconv.Itoa(i), CameraID: camID, Kind: "Motion", Time: int64(i)})
		b.Touch(camID, "motion")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("publishing while disconnected took %v", d)
	}
	b.mu.Lock()
	n := len(b.queue)
	b.mu.Unlock()
	if n != 5 {
		t.Fatalf("queue length = %d, want 5", n)
	}
	if st := b.Status(); st.Dropped < 1000 {
		t.Fatalf("dropped = %d", st.Dropped)
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.Status().State != "error" {
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v", b.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReconnectsAfterBrokerRestart(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	start := func() *mochi.Server {
		srv := mochi.New(nil)
		_ = srv.AddHook(new(auth.AllowHook), nil)
		_ = srv.AddListener(listeners.NewTCP(listeners.Config{ID: "t", Address: addr}))
		go func() { _ = srv.Serve() }()
		return srv
	}
	srv := start()
	host, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)
	b := newBridge(t, host, port, nil)
	waitState := func(want string) {
		deadline := time.Now().Add(8 * time.Second)
		for b.Status().State != want {
			if time.Now().After(deadline) {
				t.Fatalf("status = %+v, want %s", b.Status(), want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitState("connected")
	_ = srv.Close()
	waitState("error")
	srv = start()
	defer srv.Close()
	waitState("connected")
	rec := newRecorder(t, host, port)
	b.Touch(camID, "motion")
	rec.wait(t, "sentinel/"+camID+"/motion", equals("ON"))
}

func TestCredentialsAndTestConnection(t *testing.T) {
	ledger := &auth.Ledger{Auth: auth.AuthRules{{Username: "ha", Password: "s3cret", Allow: true}}}
	host, port := startBroker(t, ledger)
	ctx := context.Background()
	if err := Test(ctx, Config{Host: host, Port: port, Username: "ha", Password: "s3cret"}); err != nil {
		t.Fatalf("good login: %v", err)
	}
	err := Test(ctx, Config{Host: host, Port: port, Username: "ha", Password: "wrong"})
	if err == nil || !strings.Contains(err.Error(), "refused the username or password") {
		t.Fatalf("bad login: %v", err)
	}
	b := newBridge(t, host, port, func(c *Config) { c.Username, c.Password = "ha", "nope" })
	deadline := time.Now().Add(5 * time.Second)
	for b.Status().State != "error" {
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v", b.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(b.Status().Error, "nope") {
		t.Fatal("status leaks the password")
	}
}

func TestConfigValidation(t *testing.T) {
	ok := Config{Enabled: true, Host: "broker.lan"}.WithDefaults()
	if err := ok.Validate(); err != nil || ok.Port != 1883 || ok.TopicPrefix != "sentinel" || ok.DiscoveryPrefix != "homeassistant" {
		t.Fatalf("defaults: %+v %v", ok, err)
	}
	if c := (Config{TLS: true, Host: "x"}).WithDefaults(); c.Port != 8883 {
		t.Fatalf("tls port = %d", c.Port)
	}
	for _, c := range []Config{
		{Enabled: true},
		{Enabled: true, Host: "tcp://broker"},
		{Enabled: true, Host: "broker/x"},
		{Enabled: true, Host: "b", Port: 70000},
		{Enabled: true, Host: "b", TopicPrefix: "sent#inel"},
		{Enabled: true, Host: "b", TopicPrefix: "a/+"},
		{Enabled: true, Host: "b", DiscoveryPrefix: "x", TopicPrefix: "x"},
	} {
		if err := c.WithDefaults().Validate(); err == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

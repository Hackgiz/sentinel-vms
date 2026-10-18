package mqtt

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

const (
	DefaultOffAfter  = 30 * time.Second // same quiet gap that ends a motion episode
	DefaultQueueSize = 512
	publishTimeout   = 10 * time.Second
	connectTimeout   = 10 * time.Second
	maxBackoff       = 60 * time.Second
	maxSnapshotBytes = 4 << 20
)

// Status is the connection state shown on the dashboard.
type Status struct {
	State       string `json:"state"` // disabled | connecting | connected | error
	Error       string `json:"error,omitempty"`
	ConnectedAt int64  `json:"connectedAt,omitempty"`
	Dropped     uint64 `json:"dropped"` // messages dropped because the queue was full
}

type message struct {
	topic    string
	payload  []byte
	retained bool
}

// Bridge keeps per-camera state and mirrors it to the broker. The zero value
// is not usable; set the exported fields, then call Apply.
type Bridge struct {
	Version   string
	OffAfter  time.Duration // binary sensors turn OFF this long after the last detection
	QueueSize int
	// OnCommand runs (in its own goroutine) for a validated switch command:
	// what is "recording" or "detection". Only called when AllowCommands is on.
	OnCommand func(cameraID, what string, on bool)

	mu        sync.Mutex
	cfg       Config
	sess      *session
	cams      map[string]Camera
	seen      map[string]map[string]time.Time // camera → binary kind → last seen (present = ON)
	online    map[string]bool
	lastEvent map[string]lastEvent
	snapshot  map[string]snapshot
	queue     []message
	dropped   uint64
	lastDrop  time.Time
	status    Status
	wake      chan struct{}
}

type lastEvent struct {
	id      string
	at      int64
	payload []byte
}

type snapshot struct {
	eventID string
	jpeg    []byte
}

type session struct {
	cfg         Config
	cancel      context.CancelFunc
	done        chan struct{}
	lost        chan struct{}
	rebootstrap chan struct{}
}

func (b *Bridge) init() {
	if b.cams == nil {
		b.cams = map[string]Camera{}
		b.seen = map[string]map[string]time.Time{}
		b.online = map[string]bool{}
		b.lastEvent = map[string]lastEvent{}
		b.snapshot = map[string]snapshot{}
		b.wake = make(chan struct{}, 1)
		b.status = Status{State: "disabled"}
	}
}

func (b *Bridge) offAfter() time.Duration {
	if b.OffAfter > 0 {
		return b.OffAfter
	}
	return DefaultOffAfter
}

// Apply (re)connects with a new config. An unchanged config is a no-op; a
// disabled one disconnects (publishing "offline" first).
func (b *Bridge) Apply(ctx context.Context, cfg Config) {
	cfg = cfg.WithDefaults()
	b.mu.Lock()
	b.init()
	old := b.sess
	if old != nil && old.cfg.same(cfg) {
		b.mu.Unlock()
		return
	}
	if old == nil && !cfg.Enabled {
		b.cfg = cfg
		b.mu.Unlock()
		return
	}
	b.sess = nil
	b.cfg = cfg
	b.queue = nil // topics may have moved; the new session republishes state
	for _, kinds := range b.seen {
		clear(kinds)
	}
	b.status = Status{State: "disabled", Dropped: b.dropped}
	var s *session
	if cfg.Enabled {
		sctx, cancel := context.WithCancel(ctx)
		s = &session{cfg: cfg, cancel: cancel, done: make(chan struct{}),
			lost: make(chan struct{}, 1), rebootstrap: make(chan struct{}, 1)}
		b.sess = s
		b.status = Status{State: "connecting", Dropped: b.dropped}
		go b.run(sctx, s)
	}
	b.mu.Unlock()
	if old != nil {
		old.cancel()
		select {
		case <-old.done:
		case <-time.After(3 * time.Second):
		}
	}
}

// Close disconnects (publishing "offline").
func (b *Bridge) Close() {
	b.mu.Lock()
	cfg := b.cfg
	b.mu.Unlock()
	cfg.Enabled = false
	b.Apply(context.Background(), cfg)
}

func (b *Bridge) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	st := b.status
	st.Dropped = b.dropped
	return st
}

// Enabled reports whether a broker is configured and on (cheap; lets callers
// skip building payloads).
func (b *Bridge) Enabled() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sess != nil
}

func (b *Bridge) setStatus(s *session, st Status) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == s {
		b.status = st
	}
}

// enqueueLocked appends; when full the oldest message is dropped.
func (b *Bridge) enqueueLocked(msgs ...message) {
	if b.sess == nil {
		return
	}
	limit := b.QueueSize
	if limit <= 0 {
		limit = DefaultQueueSize
	}
	b.queue = append(b.queue, msgs...)
	if over := len(b.queue) - limit; over > 0 {
		b.dropped += uint64(over)
		b.queue = append(b.queue[:0:0], b.queue[over:]...)
		if time.Since(b.lastDrop) > 10*time.Second {
			b.lastDrop = time.Now()
			slog.Warn("mqtt queue full; dropped oldest messages", "dropped_total", b.dropped)
		}
	}
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// MARK: - Inputs from the server pipeline (never block)

// SyncCameras publishes discovery for new or changed cameras and removes
// cameras that were deleted.
func (b *Bridge) SyncCameras(cams []Camera) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	keep := map[string]bool{}
	for _, c := range cams {
		if !validCameraID(c.ID) {
			continue
		}
		keep[c.ID] = true
		old, known := b.cams[c.ID]
		b.cams[c.ID] = c
		if known && old == c {
			continue
		}
		if !known || old.Name != c.Name {
			b.enqueueLocked(b.cfg.discovery(c, b.Version)...)
		}
		b.enqueueLocked(
			message{topic: b.cfg.CameraTopic(c.ID, "recording", "state"), payload: onOff(c.Recording), retained: true},
			message{topic: b.cfg.CameraTopic(c.ID, "detection", "state"), payload: onOff(c.Detection), retained: true})
		if !known {
			for _, k := range BinaryKinds {
				b.enqueueLocked(message{topic: b.cfg.CameraTopic(c.ID, k), payload: onOff(false), retained: true})
			}
		}
	}
	for id, c := range b.cams {
		if keep[id] {
			continue
		}
		delete(b.cams, id)
		delete(b.seen, id)
		delete(b.online, id)
		delete(b.lastEvent, id)
		delete(b.snapshot, id)
		b.enqueueLocked(b.cfg.removal(c)...)
	}
}

// Touch marks a binary sensor (motion, person, vehicle, audio) as detected now. It
// turns ON at once and OFF after OffAfter without another Touch.
func (b *Bridge) Touch(cameraID, kind string) {
	kind = strings.ToLower(kind)
	if !isBinaryKind(kind) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return
	}
	if _, ok := b.cams[cameraID]; !ok {
		return
	}
	kinds := b.seen[cameraID]
	if kinds == nil {
		kinds = map[string]time.Time{}
		b.seen[cameraID] = kinds
	}
	_, on := kinds[kind]
	kinds[kind] = time.Now()
	if !on {
		b.enqueueLocked(message{topic: b.cfg.CameraTopic(cameraID, kind), payload: onOff(true), retained: true})
	}
}

// expire turns OFF binary sensors that haven't been touched for OffAfter.
func (b *Bridge) expire(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	off := b.offAfter()
	for cam, kinds := range b.seen {
		for k, last := range kinds {
			if now.Sub(last) >= off {
				delete(kinds, k)
				b.enqueueLocked(message{topic: b.cfg.CameraTopic(cam, k), payload: onOff(false), retained: true})
			}
		}
	}
}

// CameraOnline publishes a camera's connectivity when it changes.
func (b *Bridge) CameraOnline(cameraID string, online bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return
	}
	if _, ok := b.cams[cameraID]; !ok {
		return
	}
	if prev, ok := b.online[cameraID]; ok && prev == online {
		return
	}
	b.online[cameraID] = online
	b.enqueueLocked(message{topic: b.cfg.CameraTopic(cameraID, "online"), payload: onOff(online), retained: true})
}

// Event publishes a detection event; the camera's newest event is also
// retained as its last_event.
func (b *Bridge) Event(e Event) {
	if e.TimeISO == "" {
		e.TimeISO = isoTime(e.Time)
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return
	}
	b.enqueueLocked(message{topic: b.cfg.EventsTopic(), payload: payload})
	if _, ok := b.cams[e.CameraID]; !ok {
		return
	}
	if last, ok := b.lastEvent[e.CameraID]; ok && last.id != e.ID && last.at > e.Time {
		return // an older event's update; don't roll last_event back
	}
	b.lastEvent[e.CameraID] = lastEvent{id: e.ID, at: e.Time, payload: payload}
	b.enqueueLocked(message{topic: b.cfg.CameraTopic(e.CameraID, "last_event"), payload: payload, retained: true})
}

// WantsSnapshot reports whether Snapshot would publish this event's image,
// so the caller can skip reading the file.
func (b *Bridge) WantsSnapshot(cameraID, eventID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return false
	}
	if last, ok := b.lastEvent[cameraID]; !ok || last.id != eventID {
		return false
	}
	return b.snapshot[cameraID].eventID != eventID
}

// Snapshot publishes the JPEG of the camera's newest event to its image
// topic (Home Assistant image entity), retained.
func (b *Bridge) Snapshot(cameraID, eventID string, jpeg []byte) {
	if len(jpeg) == 0 || len(jpeg) > maxSnapshotBytes {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return
	}
	if last, ok := b.lastEvent[cameraID]; !ok || last.id != eventID {
		return
	}
	b.snapshot[cameraID] = snapshot{eventID: eventID, jpeg: jpeg}
	b.enqueueLocked(message{topic: b.cfg.CameraTopic(cameraID, "snapshot"), payload: jpeg, retained: true})
}

// Alarm publishes an alarm being raised or changing state.
func (b *Bridge) Alarm(a Alarm) {
	payload, err := json.Marshal(a)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enqueueLocked(message{topic: b.cfg.AlarmsTopic(), payload: payload})
}

// MARK: - Connection worker

func (b *Bridge) clientOptions(cfg Config, s *session) *paho.ClientOptions {
	o := paho.NewClientOptions().
		AddBroker(cfg.brokerURL()).
		SetClientID(cfg.ClientID).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetCleanSession(true).
		SetAutoReconnect(false). // run() reconnects with its own backoff and status
		SetConnectRetry(false).
		SetConnectTimeout(connectTimeout).
		SetKeepAlive(30*time.Second).
		SetOrderMatters(false).
		SetWill(cfg.AvailabilityTopic(), "offline", 1, true)
	if cfg.TLS {
		o.SetTLSConfig(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
	}
	if s != nil {
		o.SetConnectionLostHandler(func(_ paho.Client, err error) {
			b.setStatus(s, Status{State: "error", Error: "connection lost: " + err.Error()})
			slog.Warn("mqtt connection lost", "broker", cfg.brokerURL(), "err", err)
			select {
			case s.lost <- struct{}{}:
			default:
			}
		})
	}
	return o
}

func (b *Bridge) run(ctx context.Context, s *session) {
	defer close(s.done)
	cfg := s.cfg
	client := paho.NewClient(b.clientOptions(cfg, s))
	backoff := time.Second
	for ctx.Err() == nil {
		select { // drop a stale "lost" from the previous connection
		case <-s.lost:
		default:
		}
		tok := client.Connect()
		err := errors.New("timed out connecting to the broker")
		if tok.WaitTimeout(connectTimeout + 5*time.Second) {
			err = tok.Error()
		}
		if err != nil {
			b.setStatus(s, Status{State: "error", Error: friendlyError(err)})
			slog.Warn("mqtt connect failed", "broker", cfg.brokerURL(), "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = time.Second
		b.setStatus(s, Status{State: "connected", ConnectedAt: time.Now().Unix()})
		slog.Info("mqtt connected", "broker", cfg.brokerURL())
		b.bootstrap(client, s)
		b.pump(ctx, client, s)
		if ctx.Err() != nil {
			if client.IsConnectionOpen() {
				client.Publish(cfg.AvailabilityTopic(), 1, true, "offline").WaitTimeout(time.Second)
				client.Disconnect(250)
			}
			return
		}
	}
}

// bootstrap (re)publishes availability, discovery and every retained state,
// then subscribes to commands and Home Assistant's birth message.
func (b *Bridge) bootstrap(client paho.Client, s *session) {
	cfg := s.cfg
	b.mu.Lock()
	if b.sess != s {
		b.mu.Unlock()
		return
	}
	msgs := []message{{topic: cfg.AvailabilityTopic(), payload: []byte("online"), retained: true}}
	for id, c := range b.cams {
		msgs = append(msgs, cfg.discovery(c, b.Version)...)
		for _, k := range BinaryKinds {
			_, on := b.seen[id][k]
			msgs = append(msgs, message{topic: cfg.CameraTopic(id, k), payload: onOff(on), retained: true})
		}
		msgs = append(msgs,
			message{topic: cfg.CameraTopic(id, "recording", "state"), payload: onOff(c.Recording), retained: true},
			message{topic: cfg.CameraTopic(id, "detection", "state"), payload: onOff(c.Detection), retained: true})
		if on, ok := b.online[id]; ok {
			msgs = append(msgs, message{topic: cfg.CameraTopic(id, "online"), payload: onOff(on), retained: true})
		}
		if le, ok := b.lastEvent[id]; ok {
			msgs = append(msgs, message{topic: cfg.CameraTopic(id, "last_event"), payload: le.payload, retained: true})
		}
		if sn, ok := b.snapshot[id]; ok {
			msgs = append(msgs, message{topic: cfg.CameraTopic(id, "snapshot"), payload: sn.jpeg, retained: true})
		}
	}
	// Queued retained states are superseded by the snapshot above; keep only
	// the event/alarm stream.
	kept := b.queue[:0]
	for _, m := range b.queue {
		if !m.retained {
			kept = append(kept, m)
		}
	}
	b.queue = kept
	b.mu.Unlock()

	for _, m := range msgs {
		b.publish(client, m)
	}
	subs := map[string]byte{cfg.DiscoveryPrefix + "/status": 1}
	if cfg.AllowCommands {
		subs[cfg.topic("+", "recording", "set")] = 1
		subs[cfg.topic("+", "detection", "set")] = 1
	}
	tok := client.SubscribeMultiple(subs, func(_ paho.Client, m paho.Message) { b.onMessage(s, m) })
	if tok.WaitTimeout(publishTimeout) && tok.Error() != nil {
		slog.Warn("mqtt subscribe failed", "err", tok.Error())
	}
}

func (b *Bridge) pump(ctx context.Context, client paho.Client, s *session) {
	tick := time.NewTicker(min(max(b.offAfter()/4, 10*time.Millisecond), time.Second))
	defer tick.Stop()
	for {
		b.drain(ctx, client)
		select {
		case <-ctx.Done():
			return
		case <-s.lost:
			return
		case now := <-tick.C:
			b.expire(now)
		case <-b.wake:
		case <-s.rebootstrap:
			b.bootstrap(client, s)
		}
	}
}

func (b *Bridge) drain(ctx context.Context, client paho.Client) {
	for ctx.Err() == nil && client.IsConnectionOpen() {
		b.mu.Lock()
		if len(b.queue) == 0 {
			b.mu.Unlock()
			return
		}
		m := b.queue[0]
		b.queue[0] = message{}
		b.queue = b.queue[1:]
		b.mu.Unlock()
		b.publish(client, m)
	}
}

func (b *Bridge) publish(client paho.Client, m message) {
	tok := client.Publish(m.topic, 1, m.retained, m.payload)
	if !tok.WaitTimeout(publishTimeout) {
		slog.Warn("mqtt publish timed out", "topic", m.topic)
	} else if err := tok.Error(); err != nil {
		slog.Warn("mqtt publish failed", "topic", m.topic, "err", err)
	}
}

// onMessage handles Home Assistant's birth message and switch commands. It
// runs on paho's goroutine, so it only validates and hands off.
func (b *Bridge) onMessage(s *session, m paho.Message) {
	cfg := s.cfg
	topic, payload := m.Topic(), strings.ToUpper(strings.TrimSpace(string(m.Payload())))
	if topic == cfg.DiscoveryPrefix+"/status" {
		if payload == "ONLINE" {
			select { // Home Assistant restarted: send discovery again
			case s.rebootstrap <- struct{}{}:
			default:
			}
		}
		return
	}
	if !cfg.AllowCommands || !strings.HasPrefix(topic, cfg.TopicPrefix+"/") {
		return
	}
	parts := strings.Split(strings.TrimPrefix(topic, cfg.TopicPrefix+"/"), "/")
	if len(parts) != 3 || parts[2] != "set" || (parts[1] != "recording" && parts[1] != "detection") {
		return
	}
	if payload != "ON" && payload != "OFF" {
		slog.Warn("mqtt command ignored: payload must be ON or OFF", "topic", topic)
		return
	}
	cameraID, what := parts[0], parts[1]
	b.mu.Lock()
	_, known := b.cams[cameraID]
	current := b.sess == s
	b.mu.Unlock()
	if !known || !current || b.OnCommand == nil {
		return
	}
	go b.OnCommand(cameraID, what, payload == "ON")
}

// Test connects once with cfg and disconnects. It does not touch a running
// bridge (it uses a different client ID).
func Test(ctx context.Context, cfg Config) error {
	cfg = cfg.WithDefaults()
	cfg.ClientID += "-test"
	var b Bridge
	client := paho.NewClient(b.clientOptions(cfg, nil))
	tok := client.Connect()
	done := make(chan bool, 1)
	go func() { done <- tok.WaitTimeout(connectTimeout + 5*time.Second) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case ok := <-done:
		if !ok {
			return errors.New("timed out connecting to the broker")
		}
	}
	if err := tok.Error(); err != nil {
		return errors.New(friendlyError(err))
	}
	client.Disconnect(100)
	return nil
}

func friendlyError(err error) string {
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "not authorized"), strings.Contains(low, "bad user name or password"):
		return "the broker refused the username or password (" + msg + ")"
	case strings.Contains(low, "connection refused"):
		return "nothing is listening at that host and port (" + msg + ")"
	case strings.Contains(low, "certificate"), strings.Contains(low, "tls"):
		return "TLS failed — check the port and that the broker's certificate is valid for this host (" + msg + ")"
	case strings.Contains(low, "no such host"):
		return "host name not found (" + msg + ")"
	}
	return msg
}

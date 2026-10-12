package server

// API v2: /events/stream (WebSocket): live notifications from the event bus.
// Contract: api/openapi.yaml (StreamMessage).
//
// One bus subscriber per handler (streamHub) turns bus messages into
// encoded StreamMessages once and offers them to every connection. Each
// connection has its own bounded queue drained by its own writer, so a slow
// client only ever loses its own messages (and is told so with `lagged`);
// the bus subscriber, and so every publisher, never waits on a socket.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/store"
)

// Stream timing and sizes. Variables so tests can shorten them; a handler
// copies them when it is built (s.Handler()).
var (
	streamRecheckEvery = 60 * time.Second // re-validate the sign-in
	streamAuthTimeout  = 10 * time.Second // first-message auth deadline
	streamPingEvery    = 30 * time.Second
	streamPongWait     = 60 * time.Second // no pong (or anything) for this long: dead peer
	streamWriteWait    = 10 * time.Second
	streamQueueLen     = 256 // messages per connection before dropping
)

// WebSocket close codes (api/openapi.yaml, /events/stream).
const (
	closeSignInEnded = 4401
	closeForbidden   = 4403
)

// Stream topics a client can pick, in hello order.
var streamTopics = []string{"alarm", "event", "camera", "config", "servers", "activity"}

var streamDefaultTopics = map[string]bool{"alarm": true, "event": true, "camera": true, "config": true, "servers": true}

var streamUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	// streamAuth already refused cross-site cookie upgrades; bearer and
	// first-message auth carry no ambient credentials, so any origin is fine.
	CheckOrigin: func(*http.Request) bool { return true },
}

func (s *Server) registerV2Events(mux *http.ServeMux) {
	hub := newStreamHub(s)
	s.v2Route(mux, "GET /api/v2/events/stream", publicRoute, hub.serve)
}

// streamMessage is the wire shape (StreamMessage).
type streamMessage struct {
	Type string `json:"type"`
	At   int64  `json:"at"`
	Data any    `json:"data,omitempty"`
}

func encodeStream(typ string, data any) []byte {
	b, err := json.Marshal(streamMessage{Type: typ, At: time.Now().UnixMilli(), Data: data})
	if err != nil {
		slog.Error("event stream: encode", "type", typ, "err", err)
		return nil
	}
	return b
}

// parseStreamTopics reads the topics query: "" = the defaults.
func parseStreamTopics(q string) (map[string]bool, error) {
	if strings.TrimSpace(q) == "" {
		return streamDefaultTopics, nil
	}
	out := map[string]bool{}
	for _, t := range strings.Split(q, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		known := false
		for _, k := range streamTopics {
			known = known || k == t
		}
		if !known {
			return nil, fmt.Errorf("unknown topic %q; use %s", t, strings.Join(streamTopics, ","))
		}
		out[t] = true
	}
	return out, nil
}

// MARK: - Hub

// streamHub fans bus messages out to the connections of one handler. It
// subscribes to the bus on the first connection and stays subscribed until
// s.Ctx ends, so the camera online states it tracks stay current.
type streamHub struct {
	s *Server

	// settings copied from the package vars when the handler was built
	recheckEvery, authTimeout, pingEvery, pongWait, writeWait time.Duration
	queueLen                                                  int

	once   sync.Once
	mu     sync.Mutex
	conns  map[*streamConn]struct{}
	online map[string]bool // camera ID → last online state sent
}

func newStreamHub(s *Server) *streamHub {
	return &streamHub{
		s: s, recheckEvery: streamRecheckEvery, authTimeout: streamAuthTimeout, pingEvery: streamPingEvery,
		pongWait: streamPongWait, writeWait: streamWriteWait, queueLen: streamQueueLen,
		conns: map[*streamConn]struct{}{}, online: map[string]bool{},
	}
}

func (h *streamHub) subscribe() {
	s := h.s
	sub := s.Bus.Subscribe("events-stream", 0)
	bus.Handle(sub, TopicAlarmNew, func(a store.Alarm) {
		h.broadcast("alarm", "alarm.new", a.CameraID, toV2Alarm(&a, nil))
	})
	bus.Handle(sub, TopicAlarmState, func(a store.Alarm) {
		h.broadcast("alarm", "alarm.updated", a.CameraID, toV2Alarm(&a, nil))
	})
	bus.Handle(sub, TopicEventNew, func(m EventMessage) {
		h.broadcast("event", "event.new", m.Event.CameraID, toV2Event(&m.Event))
	})
	bus.Handle(sub, TopicEventUpdated, func(m EventMessage) {
		// The message only names the event: re-read it.
		if !h.anyWants("event") {
			return
		}
		e, err := s.Store.Event(s.ctxOrBackground(), m.Event.ID)
		if err != nil {
			return
		}
		h.broadcast("event", "event.updated", e.CameraID, toV2Event(e))
	})
	bus.Handle(sub, TopicCameraOnline, func(m CameraOnline) {
		// Recording servers report every camera every 15 s; pass on changes.
		h.mu.Lock()
		last, seen := h.online[m.CameraID]
		h.online[m.CameraID] = m.Online
		h.mu.Unlock()
		if !seen || last != m.Online {
			h.broadcast("camera", "camera.status", m.CameraID, map[string]any{"cameraId": m.CameraID, "online": m.Online})
		}
	})
	bus.Handle(sub, TopicCameraActivity, func(m CameraActivity) {
		h.broadcast("activity", "camera.activity", m.CameraID, map[string]any{"cameraId": m.CameraID, "kind": m.Kind})
	})
	bus.Handle(sub, TopicConfigChanged, func(m ConfigChanged) {
		h.configChanged(m.Cameras)
	})
	bus.Handle(sub, TopicRecorderOnline, func(m RecorderOnline) {
		h.broadcast("servers", "recorder.status", "", map[string]any{"recorderId": m.ID, "online": m.Online})
	})
	if done := s.ctxOrBackground().Done(); done != nil {
		go func() { <-done; sub.Close() }()
	}
}

func (h *streamHub) add(c *streamConn) {
	h.once.Do(h.subscribe)
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

func (h *streamHub) remove(c *streamConn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

func (h *streamHub) anyWants(topic string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if c.topics[topic] {
			return true
		}
	}
	return false
}

// broadcast encodes a message once and queues it for every connection that
// asked for topic and may see camera cam ("" = site-wide, for connections
// that see every camera; scope.go). Runs on the bus subscriber's
// goroutine; never blocks.
func (h *streamHub) broadcast(topic, typ, cam string, data any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var msg []byte
	for c := range h.conns {
		if !c.topics[topic] || !c.scope.Load().camera(cam) {
			continue
		}
		if msg == nil {
			if msg = encodeStream(typ, data); msg == nil {
				return
			}
		}
		c.queue.push(msg)
	}
}

// configChanged re-reads every connection's camera scope (groups or scopes
// may be what changed) and sends each the cameras it may see.
func (h *streamHub) configChanged(cams []store.Camera) {
	h.mu.Lock()
	conns := make([]*streamConn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	ctx := h.s.ctxOrBackground()
	for _, c := range conns {
		sc := h.s.scopeOf(ctx, c.user.Load())
		c.scope.Store(sc)
		if !c.topics["config"] {
			continue
		}
		ids := make([]string, 0, len(cams))
		for _, cam := range cams {
			if sc.camera(cam.ID) {
				ids = append(ids, cam.ID)
			}
		}
		if msg := encodeStream("config.changed", map[string]any{"cameraIds": ids}); msg != nil {
			c.queue.push(msg)
		}
	}
}

// MARK: - Send queue

// sendQueue is a connection's bounded outbox. When it overflows it drops
// everything until the writer has caught up, then hands out one `lagged`
// message in place of the gap, so the client knows to re-read state.
type sendQueue struct {
	mu      sync.Mutex
	msgs    [][]byte
	max     int
	dropped int
	wake    chan struct{}
}

func newSendQueue(max int) *sendQueue {
	return &sendQueue{max: max, wake: make(chan struct{}, 1)}
}

func (q *sendQueue) push(msg []byte) {
	q.mu.Lock()
	if q.dropped > 0 || len(q.msgs) >= q.max {
		q.dropped++
		q.mu.Unlock()
		return
	}
	q.msgs = append(q.msgs, msg)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// next returns the next frame to write, or nil when there is none.
func (q *sendQueue) next() []byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.msgs) > 0 {
		m := q.msgs[0]
		q.msgs[0] = nil
		q.msgs = q.msgs[1:]
		return m
	}
	if q.dropped > 0 {
		n := q.dropped
		q.dropped = 0
		return encodeStream("lagged", map[string]int{"dropped": n})
	}
	return nil
}

// MARK: - Connections

// streamCred is how a connection signed in, for the periodic re-check.
type streamCred struct {
	accessToken string // bearer or first-message token
	session     string // dashboard session cookie
}

func (s *Server) credUser(ctx context.Context, c streamCred) (*store.User, error) {
	if c.accessToken != "" {
		return s.Store.AccessTokenUser(ctx, c.accessToken)
	}
	return s.Store.SessionUser(ctx, c.session)
}

type streamConn struct {
	ws     *websocket.Conn
	user   atomic.Pointer[store.User]
	scope  atomic.Pointer[cameraScope] // the cameras user may see
	cred   streamCred
	topics map[string]bool
	queue  *sendQueue
}

// streamAuth authenticates the upgrade request: bearer header, else a
// same-origin session cookie. ok=false means it already answered with an
// error; a zero cred with ok=true means "authenticate with the first
// message".
func (s *Server) streamAuth(w http.ResponseWriter, r *http.Request) (u *store.User, cred streamCred, ok bool) {
	if t := bearerToken(r); t != "" {
		u, err := s.Store.AccessTokenUser(r.Context(), t)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sentinel"`)
			writeV2Error(w, http.StatusUnauthorized, codeUnauthorized, "sign in required, or the token expired")
			return nil, cred, false
		}
		return u, streamCred{accessToken: t}, true
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if u, err := s.Store.SessionUser(r.Context(), c.Value); err == nil {
			// Browsers send the cookie on cross-site WebSocket upgrades
			// too: only this host's own pages may use it.
			o, err := url.Parse(r.Header.Get("Origin"))
			if err != nil || o.Host == "" || o.Host != r.Host {
				writeV2Error(w, http.StatusForbidden, codeForbidden, "cross-site request refused")
				return nil, cred, false
			}
			return u, streamCred{session: c.Value}, true
		}
	}
	return nil, cred, true
}

func (h *streamHub) serve(w http.ResponseWriter, r *http.Request) {
	s := h.s
	topics, err := parseStreamTopics(r.URL.Query().Get("topics"))
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	u, cred, ok := s.streamAuth(w, r)
	if !ok {
		return
	}
	if u != nil && !auth.Can(u.Role, auth.ViewLive) {
		writeV2Error(w, http.StatusForbidden, codeForbidden, "your role ("+u.Role+") can't do this")
		return
	}
	ws, err := streamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader already answered
	}
	defer ws.Close()
	ws.SetReadLimit(4096)
	if u == nil {
		if u, cred = h.firstMessageAuth(ws); u == nil {
			return
		}
	}
	c := &streamConn{ws: ws, cred: cred, topics: topics, queue: newSendQueue(h.queueLen)}
	c.user.Store(u)
	c.scope.Store(s.scopeOf(r.Context(), u))
	names := []string{}
	for _, t := range streamTopics {
		if topics[t] {
			names = append(names, t)
		}
	}
	c.queue.push(encodeStream("hello", map[string]any{"user": s.v2UserView(r.Context(), u), "topics": names, "serverVersion": s.Version}))
	h.add(c)
	defer h.remove(c)
	h.run(c)
}

// firstMessageAuth waits for {"type":"auth","accessToken"} and closes with
// 4401 (or 4403) if it doesn't come or doesn't check out.
func (h *streamHub) firstMessageAuth(ws *websocket.Conn) (*store.User, streamCred) {
	_ = ws.SetReadDeadline(time.Now().Add(h.authTimeout))
	var in struct {
		Type        string `json:"type"`
		AccessToken string `json:"accessToken"`
	}
	_, data, err := ws.ReadMessage()
	if err == nil {
		err = json.Unmarshal(data, &in)
	}
	if err != nil || in.Type != "auth" || in.AccessToken == "" {
		h.closeWith(ws, closeSignInEnded, "sign in required")
		return nil, streamCred{}
	}
	u, err := h.s.Store.AccessTokenUser(context.Background(), in.AccessToken)
	if err != nil {
		h.closeWith(ws, closeSignInEnded, "the token expired or was revoked")
		return nil, streamCred{}
	}
	if !auth.Can(u.Role, auth.ViewLive) {
		h.closeWith(ws, closeForbidden, "your role can't view live video")
		return nil, streamCred{}
	}
	return u, streamCred{accessToken: in.AccessToken}
}

func (h *streamHub) closeWith(ws *websocket.Conn, code int, reason string) {
	_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(h.writeWait))
}

// run is the connection's writer loop (on the handler goroutine) plus a
// reader goroutine that only handles pongs and notices the peer leaving.
func (h *streamHub) run(c *streamConn) {
	gone := make(chan struct{})
	go h.read(c, gone)
	ping := time.NewTicker(h.pingEvery)
	defer ping.Stop()
	recheck := time.NewTicker(h.recheckEvery)
	defer recheck.Stop()
	shutdown := h.s.ctxOrBackground().Done()
	for {
		select {
		case <-c.queue.wake:
			for m := c.queue.next(); m != nil; m = c.queue.next() {
				_ = c.ws.SetWriteDeadline(time.Now().Add(h.writeWait))
				if err := c.ws.WriteMessage(websocket.TextMessage, m); err != nil {
					return
				}
			}
		case <-ping.C:
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(h.writeWait)); err != nil {
				return
			}
		case <-recheck.C:
			if code, reason := h.recheck(c); code != 0 {
				h.closeWith(c.ws, code, reason)
				return
			}
		case <-shutdown:
			h.closeWith(c.ws, websocket.CloseGoingAway, "server shutting down")
			return
		case <-gone:
			return
		}
	}
}

// recheck re-validates the sign-in: a close code, or 0 if all is well.
func (h *streamHub) recheck(c *streamConn) (int, string) {
	u, err := h.s.credUser(h.s.ctxOrBackground(), c.cred)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return closeSignInEnded, "the sign-in ended"
	case err != nil:
		return 0, "" // a database hiccup is no reason to drop the client
	case !auth.Can(u.Role, auth.ViewLive):
		return closeForbidden, "your role can't view live video"
	}
	c.user.Store(u)
	c.scope.Store(h.s.scopeOf(h.s.ctxOrBackground(), u))
	return 0, ""
}

func (h *streamHub) read(c *streamConn, gone chan<- struct{}) {
	defer close(gone)
	_ = c.ws.SetReadDeadline(time.Now().Add(h.pongWait))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(h.pongWait))
	})
	for {
		// Clients have nothing to say after auth; anything they send is
		// read and ignored, which also keeps the deadline fresh.
		if _, _, err := c.ws.ReadMessage(); err != nil {
			return
		}
		_ = c.ws.SetReadDeadline(time.Now().Add(h.pongWait))
	}
}

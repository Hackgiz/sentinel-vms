package cloud

// The relay client against a loopback fake of the relay's Durable Object:
// a Go WebSocket server on 127.0.0.1:0 speaking section 3, in front of a
// loopback origin. Nothing leaves the machine.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const testToken = "scs_k7f3q9x2ab_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA_"

// fakeDO is the relay side: it accepts home connections and hands each
// to the test.
type fakeDO struct {
	t        *testing.T
	srv      *httptest.Server
	token    string
	hello    func() []byte // HELLO payload per connection; nil = none
	reject   atomic.Int32  // answer the handshake with this status instead
	conns    chan *doConn
	accepted atomic.Int32
	versions chan string
	attempts chan time.Time // every handshake attempt
}

type doConn struct {
	t      *testing.T
	ws     *websocket.Conn
	frames chan Frame
	wmu    sync.Mutex
}

func newFakeDO(t *testing.T) *fakeDO {
	t.Helper()
	d := &fakeDO{t: t, token: testToken, conns: make(chan *doConn, 16), versions: make(chan string, 16), attempts: make(chan time.Time, 64)}
	up := websocket.Upgrader{}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/relay" {
			http.NotFound(w, r)
			return
		}
		select {
		case d.attempts <- time.Now():
		default:
		}
		if code := d.reject.Load(); code != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(int(code))
			_, _ = io.WriteString(w, `{"error":"rejected by the test"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+d.token {
			http.Error(w, `{"error":"bad token"}`, http.StatusUnauthorized)
			return
		}
		select {
		case d.versions <- r.Header.Get("X-Sentinel-Version"):
		default:
		}
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		d.accepted.Add(1)
		c := &doConn{t: t, ws: ws, frames: make(chan Frame, 256)}
		if d.hello != nil {
			c.send(FrameHello, 0, d.hello())
		}
		go func() {
			defer close(c.frames)
			for {
				mt, data, err := ws.ReadMessage()
				if err != nil {
					return
				}
				if mt != websocket.BinaryMessage {
					t.Errorf("home sent message type %d", mt)
					return
				}
				f, err := DecodeFrame(data)
				if err != nil {
					t.Errorf("home sent a bad frame: %v", err)
					return
				}
				c.frames <- f
			}
		}()
		d.conns <- c
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *fakeDO) url() string { return "ws" + strings.TrimPrefix(d.srv.URL, "http") + "/v1/relay" }

func (d *fakeDO) next(t *testing.T) *doConn {
	t.Helper()
	select {
	case c := <-d.conns:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("home never connected")
		return nil
	}
}

func (c *doConn) send(typ byte, id uint32, payload []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.ws.WriteMessage(websocket.BinaryMessage, EncodeFrame(typ, id, payload)); err != nil {
		c.t.Errorf("DO send: %v", err)
	}
}

func (c *doConn) sendJSON(typ byte, id uint32, v any) {
	b, _ := json.Marshal(v)
	c.send(typ, id, b)
}

func (c *doConn) closeWith(code int, reason string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason))
	time.Sleep(20 * time.Millisecond)
	c.ws.Close()
}

// expect reads the next frame for stream id (frames for other streams are
// a test failure).
func (c *doConn) expect(t *testing.T, id uint32, typ byte) Frame {
	t.Helper()
	select {
	case f, ok := <-c.frames:
		if !ok {
			t.Fatalf("connection closed waiting for type %d on stream %d", typ, id)
		}
		if f.Stream != id || f.Type != typ {
			t.Fatalf("got type %d on stream %d (%q), want type %d on stream %d", f.Type, f.Stream, f.Payload, typ, id)
		}
		return f
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for type %d on stream %d", typ, id)
		return Frame{}
	}
}

// body collects DATA frames until END.
func (c *doConn) body(t *testing.T, id uint32) string {
	t.Helper()
	var sb strings.Builder
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				t.Fatal("connection closed in a body")
			}
			if f.Stream != id {
				t.Fatalf("frame for stream %d inside stream %d's body", f.Stream, id)
			}
			switch f.Type {
			case FrameData:
				sb.Write(f.Payload)
			case FrameEnd:
				return sb.String()
			default:
				t.Fatalf("type %d inside a body: %s", f.Type, f.Payload)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out in a body")
		}
	}
}

// testOrigin is the loopback origin with handlers the tests drive.
type testOrigin struct {
	srv      *httptest.Server
	gotFirst chan struct{} // POST /echo read its first chunk
	release  chan struct{} // GET /slow may write its second part
	canceled chan struct{} // GET /hang saw its request canceled
	wsClosed chan int      // close code the /ws echo received
	stop     chan struct{} // closed before the server shuts down
}

func newTestOrigin(t *testing.T) *testOrigin {
	t.Helper()
	o := &testOrigin{gotFirst: make(chan struct{}, 1), release: make(chan struct{}), canceled: make(chan struct{}, 1), wsClosed: make(chan int, 1), stop: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.Header().Set("X-Host", r.Host)
		w.Header().Set("X-Got-Forwarded", r.Header.Get("X-Forwarded-For"))
		w.Header().Set("X-Query", r.URL.RawQuery)
		_, _ = io.WriteString(w, "hello from the origin")
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "part one;")
		w.(http.Flusher).Flush()
		select {
		case <-o.release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "part two")
	})
	mux.HandleFunc("GET /hang", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		select {
		case o.canceled <- struct{}{}:
		default:
		}
	})
	mux.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) {
		first := make([]byte, 6)
		if _, err := io.ReadFull(r.Body, first); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		o.gotFirst <- struct{}{}
		rest, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Content-Type", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(append(first, rest...))
	})
	mux.HandleFunc("HEAD /empty", func(w http.ResponseWriter, r *http.Request) { w.Header().Set("X-Empty", "1") })
	mux.HandleFunc("GET /big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(3<<20))
		chunk := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
		for i := 0; i < 48; i++ {
			_, _ = w.Write(chunk)
		}
	})
	mux.HandleFunc("POST /stall", func(w http.ResponseWriter, r *http.Request) {
		select { // never reads the body (so the server can't notice the client leaving)
		case <-r.Context().Done():
		case <-o.stop:
		}
	})
	mux.HandleFunc("GET /nocontent", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	up := websocket.Upgrader{Subprotocols: []string{"sentinel.v2"}, CheckOrigin: func(*http.Request) bool { return true }}
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		ws, err := up.Upgrade(w, r, http.Header{"X-Host": {r.Host}})
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				if ce, ok := err.(*websocket.CloseError); ok {
					o.wsClosed <- ce.Code
				}
				return
			}
			if string(data) == "bye" {
				_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4000, "done"))
				return
			}
			_ = ws.WriteMessage(mt, append([]byte("echo:"), data...))
		}
	})
	o.srv = httptest.NewServer(mux)
	t.Cleanup(o.srv.Close)
	t.Cleanup(func() { close(o.stop) }) // runs first
	return o
}

func helloJSON(features ...string) []byte {
	b, _ := json.Marshal(Entitlement{ServerID: "k7f3q9x2ab", Plan: "cloud", Status: "active", Features: features, RelayHost: "k7f3q9x2ab-cloud.example"})
	return b
}

// startRelay runs a relay against d and o until the test ends.
func startRelay(t *testing.T, d *fakeDO, o *testOrigin, tune func(*Relay)) (*Relay, <-chan struct{}) {
	t.Helper()
	r := &Relay{URL: d.url(), Token: testToken, Version: "9.9.9-test", Origin: o.srv.URL,
		MinBackoff: 10 * time.Millisecond, MaxBackoff: 80 * time.Millisecond, InactiveWait: 50 * time.Millisecond,
		ReplacedWait: 50 * time.Millisecond, PingEvery: time.Second}
	if tune != nil {
		tune(r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("relay did not stop")
		}
	})
	return r, done
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
		var zero T
		return zero
	}
}

func headerOf(h headerPairs, name string) []string {
	var out []string
	for _, kv := range h {
		if kv[0] == name {
			out = append(out, kv[1])
		}
	}
	return out
}

func TestRelayGetStreamsResponse(t *testing.T) {
	d, o := newFakeDO(t), newTestOrigin(t)
	var hellos atomic.Int32
	d.hello = func() []byte { return helloJSON("relay") }
	startRelay(t, d, o, func(r *Relay) { r.OnHello = func(e Entitlement) { hellos.Add(1) } })
	c := d.next(t)
	if v := recv(t, d.versions); v != "9.9.9-test" {
		t.Fatalf("X-Sentinel-Version %q", v)
	}

	c.sendJSON(FrameReq, 1, reqHead{M: "GET", P: "/hello?x=1&y=2", H: headerPairs{
		{"accept", "text/plain"}, {"host", "k7f3q9x2ab-cloud.example"}, {"x-forwarded-for", "203.0.113.7"},
		{"connection", "keep-alive"}, {"x-forwarded-proto", "https"},
	}})
	var res resHead
	_ = json.Unmarshal(c.expect(t, 1, FrameRes).Payload, &res)
	if res.S != 200 || !res.Body {
		t.Fatalf("RES %+v", res)
	}
	if got := headerOf(res.H, "set-cookie"); len(got) != 2 {
		t.Fatalf("set-cookie pairs %v", got)
	}
	if h := headerOf(res.H, "x-host"); len(h) != 1 || h[0] != "k7f3q9x2ab-cloud.example" {
		t.Fatalf("origin saw host %v", h)
	}
	if h := headerOf(res.H, "x-got-forwarded"); len(h) != 1 || h[0] != "203.0.113.7" {
		t.Fatalf("x-forwarded-for %v", h)
	}
	if h := headerOf(res.H, "x-query"); h[0] != "x=1&y=2" {
		t.Fatalf("query %v", h)
	}
	for _, kv := range res.H {
		if kv[0] != strings.ToLower(kv[0]) || hopByHop[kv[0]] {
			t.Fatalf("header %q crossed the relay", kv[0])
		}
	}
	if b := c.body(t, 1); b != "hello from the origin" {
		t.Fatalf("body %q", b)
	}
	if hellos.Load() != 1 {
		t.Fatalf("HELLO handled %d times", hellos.Load())
	}

	// The response streams: part one arrives before the origin finishes.
	c.sendJSON(FrameReq, 3, reqHead{M: "GET", P: "/slow"})
	_ = json.Unmarshal(c.expect(t, 3, FrameRes).Payload, &res)
	if f := c.expect(t, 3, FrameData); string(f.Payload) != "part one;" {
		t.Fatalf("first chunk %q", f.Payload)
	}
	close(o.release)
	if rest := c.body(t, 3); rest != "part two" {
		t.Fatalf("rest %q", rest)
	}

	// No body for HEAD and 204.
	c.sendJSON(FrameReq, 5, reqHead{M: "HEAD", P: "/empty"})
	_ = json.Unmarshal(c.expect(t, 5, FrameRes).Payload, &res)
	if res.S != 200 || res.Body || headerOf(res.H, "x-empty")[0] != "1" {
		t.Fatalf("HEAD %+v", res)
	}
	c.sendJSON(FrameReq, 7, reqHead{M: "GET", P: "/nocontent"})
	_ = json.Unmarshal(c.expect(t, 7, FrameRes).Payload, &res)
	if res.S != 204 || res.Body {
		t.Fatalf("204 %+v", res)
	}
	// An unknown path is the origin's own 404, streamed like any answer.
	c.sendJSON(FrameReq, 9, reqHead{M: "GET", P: "/nope"})
	_ = json.Unmarshal(c.expect(t, 9, FrameRes).Payload, &res)
	if res.S != 404 {
		t.Fatalf("404 %+v", res)
	}
	c.body(t, 9)
}

func TestRelayPostStreamsRequestBody(t *testing.T) {
	d, o := newFakeDO(t), newTestOrigin(t)
	startRelay(t, d, o, nil)
	c := d.next(t)
	c.sendJSON(FrameReq, 1, reqHead{M: "POST", P: "/echo", Body: true, H: headerPairs{{"content-type", "application/json"}}})
	c.send(FrameData, 1, []byte("chunk1"))
	// The origin reads the first chunk before the rest exists: no buffering.
	select {
	case <-o.gotFirst:
	case <-time.After(5 * time.Second):
		t.Fatal("origin never saw the first chunk")
	}
	c.send(FrameData, 1, []byte("+chunk2"))
	c.send(FrameEnd, 1, nil)
	var res resHead
	_ = json.Unmarshal(c.expect(t, 1, FrameRes).Payload, &res)
	if res.S != 201 || headerOf(res.H, "x-content-type")[0] != "application/json" {
		t.Fatalf("RES %+v", res)
	}
	if b := c.body(t, 1); b != "chunk1+chunk2" {
		t.Fatalf("echo %q", b)
	}

	// With a content-length the origin gets a sized body.
	c.sendJSON(FrameReq, 3, reqHead{M: "POST", P: "/echo", Body: true, H: headerPairs{{"content-length", "12"}}})
	c.send(FrameData, 3, []byte("abcdefghijkl"))
	c.send(FrameEnd, 3, nil)
	recv(t, o.gotFirst)
	_ = json.Unmarshal(c.expect(t, 3, FrameRes).Payload, &res)
	if b := c.body(t, 3); res.S != 201 || b != "abcdefghijkl" {
		t.Fatalf("sized: %d %q", res.S, b)
	}
}

func TestRelayRstAndBadRequests(t *testing.T) {
	d, o := newFakeDO(t), newTestOrigin(t)
	startRelay(t, d, o, nil)
	c := d.next(t)

	// RST from the relay cancels the origin request.
	c.sendJSON(FrameReq, 1, reqHead{M: "GET", P: "/hang"})
	time.Sleep(50 * time.Millisecond)
	c.sendJSON(FrameRst, 1, rstBody{Reason: "client went away"})
	select {
	case <-o.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("origin request not canceled")
	}

	// Bad request lines are refused with RST, the connection stays up.
	for id, h := range map[uint32]reqHead{
		3: {M: "CONNECT", P: "/"},
		5: {M: "GET", P: "http://evil.example/"},
		7: {M: "GET", P: "//evil.example/x"},
	} {
		c.sendJSON(FrameReq, id, h)
		var rb rstBody
		_ = json.Unmarshal(c.expect(t, id, FrameRst).Payload, &rb)
		if rb.Reason == "" {
			t.Fatalf("RST without a reason for %+v", h)
		}
	}
	c.send(FrameReq, 9, []byte("{not json"))
	c.expect(t, 9, FrameRst)
	// DATA for a request without a body is a stream error.
	c.sendJSON(FrameReq, 11, reqHead{M: "GET", P: "/hang"})
	c.send(FrameData, 11, []byte("x"))
	c.expect(t, 11, FrameRst)

	// Still working afterwards.
	c.sendJSON(FrameReq, 13, reqHead{M: "GET", P: "/hello"})
	c.expect(t, 13, FrameRes)
	c.body(t, 13)
	if d.accepted.Load() != 1 {
		t.Fatalf("reconnected %d times", d.accepted.Load())
	}
}

func TestRelayUnknownStreams(t *testing.T) {
	d, o := newFakeDO(t), newTestOrigin(t)
	startRelay(t, d, o, nil)
	c := d.next(t)
	// END, RST and WSCLOSE on an unknown stream are dropped silently …
	c.send(FrameEnd, 41, nil)
	c.sendJSON(FrameRst, 43, rstBody{Reason: "x"})
	c.sendJSON(FrameWSClose, 45, wsCloseBody{Code: 1000})
	// … anything else gets an RST.
	c.send(FrameData, 47, []byte("x"))
	c.expect(t, 47, FrameRst)
	c.send(FrameWSMsg, 49, []byte{wsText, 'x'})
	c.expect(t, 49, FrameRst)
	c.sendJSON(FrameRes, 51, resHead{S: 200})
	c.expect(t, 51, FrameRst)
	// Nothing else came back: the next frame answers this request.
	c.sendJSON(FrameReq, 53, reqHead{M: "GET", P: "/hello"})
	c.expect(t, 53, FrameRes)
	c.body(t, 53)
}

func TestRelayLargeBodyFrames(t *testing.T) {
	d, o := newFakeDO(t), newTestOrigin(t)
	startRelay(t, d, o, nil)
	c := d.next(t)
	c.sendJSON(FrameReq, 1, reqHead{M: "GET", P: "/big"})
	c.expect(t, 1, FrameRes)
	total, frames := 0, 0
	for {
		f := <-c.frames
		if f.Type == FrameEnd {
			break
		}
		if f.Type != FrameData || len(f.Payload) > MaxDataChunk {
			t.Fatalf("frame type %d of %d bytes", f.Type, len(f.Payload))
		}
		total += len(f.Payload)
		frames++
	}
	if total != 3<<20 {
		t.Fatalf("%d bytes", total)
	}
	t.Logf("3 MiB in %d DATA frames", frames)
}

func TestRelayResetsSlowConsumers(t *testing.T) {
	old := slowConsumer
	slowConsumer = 50 * time.Millisecond
	t.Cleanup(func() { slowConsumer = old })
	d, o := newFakeDO(t), newTestOrigin(t)
	startRelay(t, d, o, nil)
	c := d.next(t)
	c.sendJSON(FrameReq, 1, reqHead{M: "POST", P: "/stall", Body: true})
	chunk := make([]byte, MaxDataChunk)
	reset := make(chan string, 1)
	go func() {
		for f := range c.frames {
			if f.Stream == 1 && f.Type == FrameRst {
				var rb rstBody
				_ = json.Unmarshal(f.Payload, &rb)
				reset <- rb.Reason
				return
			}
		}
	}()
	for i := 0; i < 256; i++ {
		select {
		case reason := <-reset:
			if !strings.Contains(reason, "too slow") {
				t.Fatalf("reason %q", reason)
			}
			return
		default:
		}
		c.send(FrameData, 1, chunk)
	}
	select {
	case reason := <-reset:
		if !strings.Contains(reason, "too slow") {
			t.Fatalf("reason %q", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled request body never got reset")
	}
}

func TestRelayStreamLimit(t *testing.T) {
	d, o := newFakeDO(t), newTestOrigin(t)
	startRelay(t, d, o, nil)
	c := d.next(t)
	for i := uint32(0); i < MaxStreams; i++ {
		c.sendJSON(FrameReq, 2*i+1, reqHead{M: "GET", P: "/hang"})
	}
	c.sendJSON(FrameReq, 2*MaxStreams+1, reqHead{M: "GET", P: "/hang"})
	var rb rstBody
	_ = json.Unmarshal(c.expect(t, 2*MaxStreams+1, FrameRst).Payload, &rb)
	if !strings.Contains(rb.Reason, "too many") {
		t.Fatalf("reason %q", rb.Reason)
	}
	// Reusing an open id is refused too.
	c.sendJSON(FrameReq, 1, reqHead{M: "GET", P: "/hang"})
	c.expect(t, 1, FrameRst)
}

func TestRelayBridgesWebSocket(t *testing.T) {
	d, o := newFakeDO(t), newTestOrigin(t)
	startRelay(t, d, o, nil)
	c := d.next(t)

	// Refused upgrade: the origin's status comes back in WSOK.
	c.sendJSON(FrameWSOpen, 1, wsOpenHead{P: "/ws", H: headerPairs{{"authorization", "Bearer bad"}}})
	var ok wsOKHead
	_ = json.Unmarshal(c.expect(t, 1, FrameWSOK).Payload, &ok)
	if ok.S != 401 {
		t.Fatalf("refused WSOK %+v", ok)
	}

	c.sendJSON(FrameWSOpen, 3, wsOpenHead{P: "/ws?topics=alarm", H: headerPairs{
		{"authorization", "Bearer good"}, {"sec-websocket-protocol", "sentinel.v2"}, {"host", "k7f3q9x2ab-cloud.example"},
		{"sec-websocket-key", "x"}, {"upgrade", "websocket"},
	}})
	_ = json.Unmarshal(c.expect(t, 3, FrameWSOK).Payload, &ok)
	if ok.S != 101 || len(ok.H) != 1 || ok.H[0] != [2]string{"sec-websocket-protocol", "sentinel.v2"} {
		t.Fatalf("WSOK %+v", ok)
	}
	c.send(FrameWSMsg, 3, append([]byte{wsText}, "ping"...))
	if f := c.expect(t, 3, FrameWSMsg); f.Payload[0] != wsText || string(f.Payload[1:]) != "echo:ping" {
		t.Fatalf("text echo %q", f.Payload)
	}
	c.send(FrameWSMsg, 3, []byte{wsBinary, 0, 1, 2})
	if f := c.expect(t, 3, FrameWSMsg); f.Payload[0] != wsBinary || string(f.Payload[1:]) != "echo:\x00\x01\x02" {
		t.Fatalf("binary echo %q", f.Payload)
	}
	// The origin closes: WSCLOSE with its code.
	c.send(FrameWSMsg, 3, append([]byte{wsText}, "bye"...))
	var cl wsCloseBody
	_ = json.Unmarshal(c.expect(t, 3, FrameWSClose).Payload, &cl)
	if cl.Code != 4000 || cl.Reason != "done" {
		t.Fatalf("WSCLOSE %+v", cl)
	}

	// The client closes: the origin gets the close code.
	c.sendJSON(FrameWSOpen, 5, wsOpenHead{P: "/ws", H: headerPairs{{"authorization", "Bearer good"}}})
	c.expect(t, 5, FrameWSOK)
	c.sendJSON(FrameWSClose, 5, wsCloseBody{Code: 1001, Reason: "tab closed"})
	select {
	case code := <-o.wsClosed:
		if code != 1001 {
			t.Fatalf("origin got close %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("origin never saw the close")
	}
}

func TestRelayReconnectsWithBackoff(t *testing.T) {
	d, o := newFakeDO(t), newTestOrigin(t)
	r, _ := startRelay(t, d, o, nil)
	for i := 0; i < 3; i++ {
		c := d.next(t)
		waitFor(t, "connected", func() bool { s, _ := r.State(); return s == StateConnected })
		c.ws.Close() // abrupt: no close frame
	}
	c := d.next(t)
	if n := d.accepted.Load(); n < 4 {
		t.Fatalf("accepted %d connections", n)
	}

	// A server that refuses with 503 is retried, state retrying.
	d.reject.Store(http.StatusServiceUnavailable)
	c.ws.Close()
	waitFor(t, "retrying", func() bool { s, e := r.State(); return s == StateRetrying && strings.Contains(e, "503") })
}

func TestRelayBackoffGrowsAndCaps(t *testing.T) {
	d, o := newFakeDO(t), newTestOrigin(t)
	d.reject.Store(http.StatusBadGateway)
	startRelay(t, d, o, func(r *Relay) { r.MinBackoff, r.MaxBackoff = 20*time.Millisecond, 160*time.Millisecond })
	var times []time.Time
	for len(times) < 8 {
		select {
		case at := <-d.attempts:
			times = append(times, at)
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d attempts", len(times))
		}
	}
	first, last := times[1].Sub(times[0]), times[7].Sub(times[6])
	if first > 60*time.Millisecond {
		t.Errorf("first retry after %v", first)
	}
	if last < 70*time.Millisecond || last > 400*time.Millisecond {
		t.Errorf("capped retry after %v (want 80–160 ms plus scheduling)", last)
	}
}

func TestRelayCloseCodes(t *testing.T) {
	t.Run("4001 revoked stops", func(t *testing.T) {
		d, o := newFakeDO(t), newTestOrigin(t)
		revoked := make(chan struct{}, 1)
		r, done := startRelay(t, d, o, func(r *Relay) { r.OnRevoked = func() { revoked <- struct{}{} } })
		d.next(t).closeWith(CloseTokenRevoked, "token revoked")
		select {
		case <-revoked:
		case <-time.After(5 * time.Second):
			t.Fatal("OnRevoked not called")
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("relay kept running after 4001")
		}
		if s, e := r.State(); s != StateOff || e == "" {
			t.Fatalf("state %q %q", s, e)
		}
		time.Sleep(50 * time.Millisecond)
		if d.accepted.Load() != 1 {
			t.Fatal("reconnected after 4001")
		}
	})
	t.Run("4003 inactive waits then rechecks", func(t *testing.T) {
		d, o := newFakeDO(t), newTestOrigin(t)
		var inactive, rechecks atomic.Int32
		r, _ := startRelay(t, d, o, func(r *Relay) {
			r.InactiveWait = 300 * time.Millisecond
			r.OnInactive = func() { inactive.Add(1) }
			r.RetryInactive = func(context.Context) bool { rechecks.Add(1); return true }
		})
		d.next(t).closeWith(ClosePlanInactive, "plan inactive")
		waitFor(t, "inactive", func() bool { s, _ := r.State(); return s == StateInactive })
		if rechecks.Load() != 0 || d.accepted.Load() != 1 {
			t.Fatal("retried before the inactive wait")
		}
		d.next(t) // after the wait and the recheck
		if inactive.Load() != 1 || rechecks.Load() != 1 {
			t.Fatalf("inactive %d rechecks %d", inactive.Load(), rechecks.Load())
		}
	})
	t.Run("4003 recheck says stop", func(t *testing.T) {
		d, o := newFakeDO(t), newTestOrigin(t)
		_, done := startRelay(t, d, o, func(r *Relay) { r.RetryInactive = func(context.Context) bool { return false } })
		d.next(t).closeWith(ClosePlanInactive, "")
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("relay kept running")
		}
	})
	t.Run("402 at the handshake is inactive", func(t *testing.T) {
		d, o := newFakeDO(t), newTestOrigin(t)
		d.reject.Store(http.StatusPaymentRequired)
		var inactive atomic.Int32
		r, _ := startRelay(t, d, o, func(r *Relay) { r.InactiveWait = time.Hour; r.OnInactive = func() { inactive.Add(1) } })
		waitFor(t, "inactive", func() bool { s, _ := r.State(); return s == StateInactive && inactive.Load() == 1 })
	})
	t.Run("4009 replaced backs off", func(t *testing.T) {
		d, o := newFakeDO(t), newTestOrigin(t)
		r, _ := startRelay(t, d, o, func(r *Relay) { r.ReplacedWait = 300 * time.Millisecond })
		d.next(t).closeWith(CloseReplaced, "replaced")
		waitFor(t, "retrying", func() bool {
			s, e := r.State()
			return s == StateRetrying && strings.Contains(e, "another server")
		})
		start := time.Now()
		d.next(t)
		if waited := time.Since(start); waited < 200*time.Millisecond {
			t.Fatalf("reconnected after %v", waited)
		}
	})
	t.Run("401 at the handshake stops until the token changes", func(t *testing.T) {
		d, o := newFakeDO(t), newTestOrigin(t)
		d.token = "scs_k7f3q9x2ab_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
		unauthorized := make(chan struct{}, 4)
		r, done := startRelay(t, d, o, func(r *Relay) { r.OnUnauthorized = func() { unauthorized <- struct{}{} } })
		recv(t, unauthorized)
		recv(t, done)
		if s, e := r.State(); s != StateOff || !strings.Contains(e, "token") {
			t.Fatalf("state %q %q", s, e)
		}
		time.Sleep(50 * time.Millisecond)
		if n := len(d.attempts); n != 1 {
			t.Fatalf("%d handshake attempts", n)
		}
	})
	t.Run("protocol error reconnects", func(t *testing.T) {
		d, o := newFakeDO(t), newTestOrigin(t)
		startRelay(t, d, o, nil)
		c := d.next(t)
		c.wmu.Lock()
		_ = c.ws.WriteMessage(websocket.TextMessage, []byte("hello?"))
		c.wmu.Unlock()
		for range c.frames { // the home closes this connection …
		}
		d.next(t) // … and connects again
	})
}

func TestRelayHelloUpdatesEntitlement(t *testing.T) {
	d, o := newFakeDO(t), newTestOrigin(t)
	d.hello = func() []byte { return helloJSON("relay", "push") }
	got := make(chan Entitlement, 4)
	startRelay(t, d, o, func(r *Relay) { r.OnHello = func(e Entitlement) { got <- e } })
	c := d.next(t)
	e := recv(t, got)
	if e.ServerID != "k7f3q9x2ab" || !e.Has(FeaturePush) || e.RelayHost != "k7f3q9x2ab-cloud.example" {
		t.Fatalf("hello %+v", e)
	}
	// A later HELLO (the entitlement changed) is handled the same way.
	c.send(FrameHello, 0, helloJSON("relay", "push", "backup"))
	if e := recv(t, got); !e.Has(FeatureBackup) {
		t.Fatalf("second hello %+v", e)
	}
	// An unreadable HELLO is ignored, not fatal.
	c.send(FrameHello, 0, []byte("{"))
	c.sendJSON(FrameReq, 1, reqHead{M: "GET", P: "/hello"})
	c.expect(t, 1, FrameRes)
}

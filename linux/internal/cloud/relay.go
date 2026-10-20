package cloud

// The relay client (contract section 3): one outbound WebSocket to
// wss://<control plane>/v1/relay carrying many streams. Each REQ becomes a
// plain HTTP request against the loopback origin (Server.RemoteHandler),
// each WSOPEN a WebSocket dial to it. Bodies stream both ways through
// io.Pipe; nothing buffers a whole body.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Relay keeps the connection to the relay up until its context ends, the
// token is revoked (4001) or a RetryInactive check says to stop.
type Relay struct {
	URL     string // wss://cloud.sentvms.com/v1/relay
	Token   string // scs_…
	Version string // sent as X-Sentinel-Version
	Origin  string // http://127.0.0.1:<port>, serving RemoteHandler

	Dialer       *websocket.Dialer // to the relay; nil = default
	OriginClient *http.Client      // to the origin; nil = default

	OnHello        func(Entitlement)              // a HELLO arrived
	OnRevoked      func()                         // close 4001: the token was revoked
	OnInactive     func()                         // close 4003 (or 402/403 at the handshake)
	OnUnauthorized func()                         // 401 at the handshake: the caller checks with the control plane
	RetryInactive  func(ctx context.Context) bool // after InactiveWait; false stops the relay

	MinBackoff   time.Duration // first retry delay (1 s)
	MaxBackoff   time.Duration // longest retry delay (60 s)
	InactiveWait time.Duration // after 4003 (1 h)
	ReplacedWait time.Duration // after 4009 or a handshake 401 (5 min)
	PingEvery    time.Duration // transport pings (25 s); the read deadline is 3×

	mu      sync.Mutex
	state   string
	lastErr string
}

const (
	defaultMinBackoff   = time.Second
	defaultMaxBackoff   = time.Minute
	defaultInactiveWait = time.Hour
	defaultReplacedWait = 5 * time.Minute
	defaultPingEvery    = 25 * time.Second
	relayWriteWait      = 30 * time.Second
	bodyChunk           = 128 << 10
)

// codeHandshakeUnauthorized marks a 401 at the handshake (no close code).
const codeHandshakeUnauthorized = -401

// State is the relay's state and last error.
func (r *Relay) State() (state, lastErr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == "" {
		return StateConnecting, ""
	}
	return r.state, r.lastErr
}

func (r *Relay) setState(state, msg string) {
	r.mu.Lock()
	r.state, r.lastErr = state, msg
	r.mu.Unlock()
}

func dur(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

// Run connects and reconnects until ctx ends.
func (r *Relay) Run(ctx context.Context) {
	minB, maxB := dur(r.MinBackoff, defaultMinBackoff), dur(r.MaxBackoff, defaultMaxBackoff)
	backoff := minB
	for ctx.Err() == nil {
		r.setState(StateConnecting, "")
		start := time.Now()
		code, connected, err := r.session(ctx)
		if ctx.Err() != nil {
			r.setState(StateOff, "")
			return
		}
		if connected && time.Since(start) > maxB {
			backoff = minB // it was up for a while: retry soon
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		switch code {
		case CloseTokenRevoked:
			r.setState(StateOff, "Sentinel Cloud revoked this server's link")
			slog.Warn("sentinel cloud: link revoked")
			if r.OnRevoked != nil {
				r.OnRevoked()
			}
			return
		case ClosePlanInactive:
			r.setState(StateInactive, "Sentinel Cloud is not active for this server")
			slog.Info("sentinel cloud: plan inactive; retrying in an hour")
			if r.OnInactive != nil {
				r.OnInactive()
			}
			if !sleep(ctx, dur(r.InactiveWait, defaultInactiveWait)) {
				r.setState(StateOff, "")
				return
			}
			if r.RetryInactive != nil && !r.RetryInactive(ctx) {
				return
			}
			backoff = minB
			continue
		case CloseReplaced:
			wait = dur(r.ReplacedWait, defaultReplacedWait)
			err = errors.New("another server using this Sentinel Cloud link took over remote access")
		case codeHandshakeUnauthorized:
			// A missing or malformed token: retrying can't help until the
			// token changes (a new link restarts the relay).
			msg := "Sentinel Cloud refused this server's token; unlink and link again"
			if err != nil {
				msg += " (" + err.Error() + ")"
			}
			r.setState(StateOff, msg)
			slog.Warn("sentinel cloud relay: token refused; not retrying", "err", err)
			if r.OnUnauthorized != nil {
				go r.OnUnauthorized()
			}
			return
		default:
			backoff = min(backoff*2, maxB)
		}
		msg := "connection lost"
		if err != nil {
			msg = err.Error()
		}
		r.setState(StateRetrying, msg)
		slog.Warn("sentinel cloud relay down; retrying", "in", wait.Round(time.Millisecond), "err", msg)
		if !sleep(ctx, wait) {
			r.setState(StateOff, "")
			return
		}
	}
	r.setState(StateOff, "")
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// session runs one connection. code is the close code (or a handshake
// pseudo-code), connected whether the handshake succeeded.
func (r *Relay) session(ctx context.Context) (code int, connected bool, err error) {
	d := r.Dialer
	if d == nil {
		// Large write buffer: a 1 MiB DATA frame goes out in few WebSocket
		// fragments.
		d = &websocket.Dialer{HandshakeTimeout: 20 * time.Second, Proxy: http.ProxyFromEnvironment, ReadBufferSize: 64 << 10, WriteBufferSize: 256 << 10}
	}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+r.Token)
	hdr.Set("X-Sentinel-Version", r.Version)
	ws, resp, err := d.DialContext(ctx, r.URL, hdr)
	if err != nil {
		if resp != nil {
			msg := errorText(resp.Body)
			resp.Body.Close()
			e := fmt.Errorf("Sentinel Cloud answered %s%s", resp.Status, msg)
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				return codeHandshakeUnauthorized, false, e
			case http.StatusPaymentRequired, http.StatusForbidden:
				return ClosePlanInactive, false, e
			}
			return 0, false, e
		}
		return 0, false, fmt.Errorf("couldn't reach Sentinel Cloud: %w", err)
	}
	r.setState(StateConnected, "")
	slog.Info("sentinel cloud relay connected")
	c := newRelayConn(ctx, r, ws)
	code, err = c.run()
	return code, true, err
}

// errorText is ": <message>" from a JSON {"error"} body, or "".
func errorText(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, 4096))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return ": " + e.Error
	}
	return ""
}

// MARK: - One connection

type relayConn struct {
	r      *Relay
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	origin *http.Client
	wsDial *websocket.Dialer

	wmu sync.Mutex // one writer at a time

	mu      sync.Mutex
	streams map[uint32]*stream
}

// stream is one REQ or WSOPEN.
type stream struct {
	id     uint32
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	// HTTP: request body chunks from the relay (nil without a body).
	// bodyEnded is touched only by the read loop.
	body      chan []byte
	bodyEnded bool

	// WebSocket: messages from the relay for the origin.
	wsIn chan wsMessage
}

type wsMessage struct {
	kind int // websocket.TextMessage | BinaryMessage
	data []byte
}

func (st *stream) close() {
	st.once.Do(func() {
		st.cancel()
		close(st.done)
	})
}

func (st *stream) closed() bool {
	select {
	case <-st.done:
		return true
	default:
		return false
	}
}

func newRelayConn(ctx context.Context, r *Relay, ws *websocket.Conn) *relayConn {
	cctx, cancel := context.WithCancel(ctx)
	origin := r.OriginClient
	if origin == nil {
		origin = &http.Client{
			Transport: &http.Transport{
				Proxy:               nil, // the origin is loopback
				DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				DisableCompression:  true, // pass bodies through as the origin sent them
				MaxIdleConnsPerHost: MaxStreams,
				ReadBufferSize:      64 << 10,
				IdleConnTimeout:     90 * time.Second,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &relayConn{
		r: r, ws: ws, ctx: cctx, cancel: cancel, origin: origin,
		wsDial:  &websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: nil},
		streams: map[uint32]*stream{},
	}
}

// run reads until the connection ends and returns its close code.
func (c *relayConn) run() (int, error) {
	defer c.shutdown()
	ws := c.ws
	ws.SetReadLimit(maxFrameBytes)
	ping := dur(c.r.PingEvery, defaultPingEvery)
	extend := func() { _ = ws.SetReadDeadline(time.Now().Add(3 * ping)) }
	extend()
	ws.SetPongHandler(func(string) error { extend(); return nil })
	go func() {
		t := time.NewTicker(ping)
		defer t.Stop()
		for {
			select {
			case <-c.ctx.Done():
				_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
				ws.Close()
				return
			case <-t.C:
				if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(relayWriteWait)); err != nil {
					ws.Close()
					return
				}
			}
		}
	}()
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) {
				reason := ce.Text
				if reason == "" {
					reason = "closed with code " + strconv.Itoa(ce.Code)
				}
				return ce.Code, errors.New("Sentinel Cloud closed the relay: " + reason)
			}
			return 0, err
		}
		extend()
		if mt != websocket.BinaryMessage {
			c.protocolError("text message")
			return websocket.CloseProtocolError, errors.New("relay protocol error: text message")
		}
		f, err := DecodeFrame(data)
		if err != nil {
			c.protocolError(err.Error())
			return websocket.CloseProtocolError, err
		}
		c.dispatch(f)
	}
}

func (c *relayConn) protocolError(msg string) {
	slog.Warn("sentinel cloud relay protocol error", "err", msg)
	_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseProtocolError, "protocol error"), time.Now().Add(time.Second))
}

// shutdown ends every stream with the connection.
func (c *relayConn) shutdown() {
	c.cancel()
	c.ws.Close()
	c.mu.Lock()
	all := c.streams
	c.streams = map[uint32]*stream{}
	c.mu.Unlock()
	for _, st := range all {
		st.close()
	}
}

// send writes one frame whose payload is the parts in order.
func (c *relayConn) send(typ byte, id uint32, parts ...[]byte) error {
	var hdr [frameHeader]byte
	putHeader(hdr[:], typ, id)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(relayWriteWait))
	w, err := c.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return err
	}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
	return w.Close()
}

func (c *relayConn) sendJSON(typ byte, id uint32, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.send(typ, id, b)
}

// reset aborts a stream from this side: RST to the relay, then forget it.
func (c *relayConn) reset(id uint32, reason string) {
	_ = c.sendJSON(FrameRst, id, rstBody{Reason: reason})
	c.remove(id)
}

func (c *relayConn) remove(id uint32) {
	c.mu.Lock()
	st := c.streams[id]
	delete(c.streams, id)
	c.mu.Unlock()
	if st != nil {
		st.close()
	}
}

func (c *relayConn) lookup(id uint32) *stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams[id]
}

// open registers a new stream; false (after an RST) when the id is taken
// or too many are open.
func (c *relayConn) open(id uint32) (*stream, bool) {
	c.mu.Lock()
	_, taken := c.streams[id]
	full := len(c.streams) >= MaxStreams
	var st *stream
	if !taken && !full {
		ctx, cancel := context.WithCancel(c.ctx)
		st = &stream{id: id, ctx: ctx, cancel: cancel, done: make(chan struct{})}
		c.streams[id] = st
	}
	c.mu.Unlock()
	switch {
	case taken:
		_ = c.sendJSON(FrameRst, id, rstBody{Reason: "stream id already open"})
		return nil, false
	case full:
		_ = c.sendJSON(FrameRst, id, rstBody{Reason: "too many open streams"})
		return nil, false
	}
	return st, true
}

// dispatch handles one frame on the read loop. It blocks only to hand a
// body chunk or message to a stream (backpressure).
func (c *relayConn) dispatch(f Frame) {
	switch f.Type {
	case FrameHello:
		var e Entitlement
		if err := json.Unmarshal(f.Payload, &e); err != nil {
			slog.Warn("sentinel cloud: unreadable HELLO", "err", err)
			return
		}
		if c.r.OnHello != nil {
			c.r.OnHello(e)
		}
	case FrameReq:
		var h reqHead
		if err := json.Unmarshal(f.Payload, &h); err != nil {
			_ = c.sendJSON(FrameRst, f.Stream, rstBody{Reason: "bad request head"})
			return
		}
		h.M = strings.ToUpper(h.M)
		if !methods[h.M] || !validPath(h.P) {
			_ = c.sendJSON(FrameRst, f.Stream, rstBody{Reason: "bad request line"})
			return
		}
		st, ok := c.open(f.Stream)
		if !ok {
			return
		}
		if h.Body {
			st.body = make(chan []byte, 16)
		}
		go c.serveHTTP(st, h)
	case FrameData:
		st := c.lookup(f.Stream)
		if st == nil {
			_ = c.sendJSON(FrameRst, f.Stream, rstBody{Reason: "unknown stream"})
			return
		}
		if st.body == nil || st.bodyEnded {
			c.reset(f.Stream, "DATA without an open request body")
			return
		}
		if !handOver(st.body, f.Payload, st.done) {
			c.reset(f.Stream, "origin too slow")
		}
	case FrameEnd:
		if st := c.lookup(f.Stream); st != nil && st.body != nil && !st.bodyEnded {
			st.bodyEnded = true
			close(st.body)
		}
	case FrameRst:
		c.remove(f.Stream)
	case FrameWSOpen:
		var h wsOpenHead
		if err := json.Unmarshal(f.Payload, &h); err != nil || !validPath(h.P) {
			_ = c.sendJSON(FrameWSOK, f.Stream, wsOKHead{S: http.StatusBadRequest})
			return
		}
		st, ok := c.open(f.Stream)
		if !ok {
			return
		}
		st.wsIn = make(chan wsMessage, 64)
		go c.serveWS(st, h)
	case FrameWSMsg:
		st := c.lookup(f.Stream)
		if st == nil || st.wsIn == nil {
			_ = c.sendJSON(FrameRst, f.Stream, rstBody{Reason: "unknown stream"})
			return
		}
		kind := websocket.TextMessage
		if f.Payload[0] == wsBinary {
			kind = websocket.BinaryMessage
		}
		if !handOver(st.wsIn, wsMessage{kind: kind, data: f.Payload[1:]}, st.done) {
			c.reset(f.Stream, "origin too slow")
		}
	case FrameWSClose:
		st := c.lookup(f.Stream)
		if st == nil {
			return
		}
		var b wsCloseBody
		_ = json.Unmarshal(f.Payload, &b)
		if st.wsIn != nil {
			select {
			case st.wsIn <- wsMessage{kind: websocket.CloseMessage, data: websocket.FormatCloseMessage(wireCloseCode(b.Code), b.Reason)}:
			case <-st.done:
			}
		} else {
			c.remove(f.Stream)
		}
	default: // RES, WSOK: only the home sends these
		c.reset(f.Stream, "unexpected frame type "+strconv.Itoa(int(f.Type)))
	}
}

// slowConsumer is how long the read loop waits for a stream that has
// stopped taking chunks before resetting it (the loop serves every stream).
var slowConsumer = 2 * time.Second

// handOver gives v to a stream's queue: at once if there is room, else
// within slowConsumer. false means the stream is too slow (or gone).
func handOver[T any](ch chan T, v T, done <-chan struct{}) bool {
	select {
	case ch <- v:
		return true
	case <-done:
		return true
	default:
	}
	t := time.NewTimer(slowConsumer)
	defer t.Stop()
	select {
	case ch <- v:
		return true
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// MARK: - HTTP streams

var errStreamReset = errors.New("stream reset")

// serveHTTP runs one REQ against the origin and streams the answer back.
func (c *relayConn) serveHTTP(st *stream, h reqHead) {
	defer c.remove(st.id)
	var body io.Reader = http.NoBody
	if st.body != nil {
		pr, pw := io.Pipe()
		body = pr
		go pumpBody(st, pw)
		defer pr.CloseWithError(errStreamReset) // unblocks the pump if the origin left the body unread
	}
	req, err := http.NewRequestWithContext(st.ctx, h.M, strings.TrimRight(c.r.Origin, "/")+h.P, body)
	if err != nil {
		c.reset(st.id, "bad request")
		return
	}
	if st.body == nil {
		req.ContentLength = 0
	} else {
		req.ContentLength = -1
	}
	for _, kv := range h.H {
		name, value := strings.ToLower(kv[0]), kv[1]
		if hopByHop[name] || !validHeader(name, value) {
			continue
		}
		switch name {
		case "host":
			req.Host = value
		case "content-length":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 && st.body != nil {
				req.ContentLength = n
			}
		default:
			req.Header.Add(name, value)
		}
	}
	resp, err := c.origin.Do(req)
	if err != nil {
		if st.ctx.Err() == nil {
			c.reset(st.id, "origin: "+err.Error())
		}
		return
	}
	defer resp.Body.Close()
	hasBody := h.M != http.MethodHead && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified
	if err := c.sendJSON(FrameRes, st.id, resHead{S: resp.StatusCode, H: pairsOf(resp.Header), Body: hasBody}); err != nil || !hasBody {
		return
	}
	c.streamBody(st, resp.Body)
}

var chunkPool = sync.Pool{New: func() any { b := make([]byte, bodyChunk); return &b }}

// streamBody sends a response body as DATA then END. A reader goroutine
// queues chunks as the origin produces them; each frame carries whatever
// is queued, up to 1 MiB, so a fast body goes out in few large frames and
// a slow one (live playlists, long polls) without delay.
func (c *relayConn) streamBody(st *stream, body io.Reader) {
	chunks := make(chan *[]byte, MaxDataChunk/bodyChunk)
	var readErr error
	go func() {
		defer close(chunks)
		for {
			bp := chunkPool.Get().(*[]byte)
			n, err := body.Read(*bp)
			if n > 0 {
				b := (*bp)[:n]
				select {
				case chunks <- &b:
				case <-st.done:
					return
				}
			} else {
				chunkPool.Put(bp)
			}
			if err != nil {
				readErr = err
				return
			}
		}
	}()
	var carry *[]byte
	for {
		first := carry
		carry = nil
		if first == nil {
			var ok bool
			select {
			case first, ok = <-chunks:
			case <-st.done:
				return
			}
			if !ok {
				if readErr == io.EOF {
					_ = c.send(FrameEnd, st.id)
				} else if readErr != nil && st.ctx.Err() == nil {
					c.reset(st.id, "origin: "+readErr.Error())
				}
				return
			}
		}
		frame := []*[]byte{first}
		total := len(*first)
	gather:
		for total < MaxDataChunk {
			select {
			case more, ok := <-chunks:
				if !ok {
					break gather
				}
				if total+len(*more) > MaxDataChunk {
					carry = more
					break gather
				}
				frame = append(frame, more)
				total += len(*more)
			default:
				break gather
			}
		}
		parts := make([][]byte, len(frame))
		for i, b := range frame {
			parts[i] = *b
		}
		if st.closed() {
			return
		}
		err := c.send(FrameData, st.id, parts...)
		for _, b := range frame {
			full := (*b)[:cap(*b)]
			chunkPool.Put(&full)
		}
		if err != nil {
			return
		}
	}
}

// pumpBody copies request body chunks into the pipe the origin request
// reads. After the origin stops reading, chunks are discarded so the read
// loop never blocks on a finished stream.
func pumpBody(st *stream, pw *io.PipeWriter) {
	failed := false
	for {
		select {
		case chunk, ok := <-st.body:
			if !ok {
				pw.Close()
				return
			}
			if !failed {
				if _, err := pw.Write(chunk); err != nil {
					failed = true
				}
			}
		case <-st.done:
			pw.CloseWithError(errStreamReset)
			return
		}
	}
}

// pairsOf turns response headers into lowercase pairs, hop-by-hop removed.
func pairsOf(h http.Header) headerPairs {
	out := headerPairs{}
	for name, values := range h {
		n := strings.ToLower(name)
		if hopByHop[n] {
			continue
		}
		for _, v := range values {
			out = append(out, [2]string{n, v})
		}
	}
	return out
}

// MARK: - WebSocket streams

// serveWS bridges one client WebSocket (WSOPEN) to the origin.
func (c *relayConn) serveWS(st *stream, h wsOpenHead) {
	defer c.remove(st.id)
	u := "ws" + strings.TrimPrefix(strings.TrimRight(c.r.Origin, "/"), "http") + h.P
	hdr := http.Header{}
	var protocols []string
	for _, kv := range h.H {
		name, value := strings.ToLower(kv[0]), kv[1]
		if hopByHop[name] || wsHandshake[name] || !validHeader(name, value) {
			continue
		}
		switch name {
		case "host":
			hdr.Set("Host", value)
		case "sec-websocket-protocol":
			for _, p := range strings.Split(value, ",") {
				if p = strings.TrimSpace(p); p != "" {
					protocols = append(protocols, p)
				}
			}
		default:
			hdr.Add(name, value)
		}
	}
	d := *c.wsDial
	d.Subprotocols = protocols
	oc, resp, err := d.DialContext(st.ctx, u, hdr)
	if err != nil {
		status := http.StatusBadGateway
		if resp != nil {
			status = resp.StatusCode
			resp.Body.Close()
		}
		_ = c.sendJSON(FrameWSOK, st.id, wsOKHead{S: status})
		return
	}
	defer oc.Close()
	ok := wsOKHead{S: http.StatusSwitchingProtocols}
	if p := oc.Subprotocol(); p != "" {
		ok.H = headerPairs{{"sec-websocket-protocol", p}}
	}
	if err := c.sendJSON(FrameWSOK, st.id, ok); err != nil {
		return
	}
	// Relay → origin.
	go func() {
		for {
			select {
			case m := <-st.wsIn:
				_ = oc.SetWriteDeadline(time.Now().Add(relayWriteWait))
				if m.kind == websocket.CloseMessage {
					_ = oc.WriteMessage(websocket.CloseMessage, m.data)
					st.close()
					return
				}
				if err := oc.WriteMessage(m.kind, m.data); err != nil {
					st.close()
					return
				}
			case <-st.done:
				return
			}
		}
	}()
	go func() {
		<-st.done
		oc.Close()
	}()
	// Origin → relay.
	for {
		kind, data, err := oc.ReadMessage()
		if err != nil {
			if st.closed() {
				return
			}
			code, reason := websocket.CloseGoingAway, "origin connection lost"
			var ce *websocket.CloseError
			if errors.As(err, &ce) {
				code, reason = ce.Code, ce.Text
			}
			_ = c.sendJSON(FrameWSClose, st.id, wsCloseBody{Code: wireCloseCode(code), Reason: reason})
			return
		}
		k := wsText
		if kind == websocket.BinaryMessage {
			k = wsBinary
		}
		msg := make([]byte, 1+len(data))
		msg[0] = k
		copy(msg[1:], data)
		if err := c.send(FrameWSMsg, st.id, msg); err != nil {
			return
		}
	}
}

// wireCloseCode maps codes that may not be sent in a close frame (1005,
// 1006, 1015, out of range) to ones that may.
func wireCloseCode(code int) int {
	switch {
	case code == websocket.CloseNoStatusReceived:
		return websocket.CloseNormalClosure
	case code == websocket.CloseAbnormalClosure || code == websocket.CloseTLSHandshake:
		return websocket.CloseGoingAway
	case code < 1000 || code >= 5000 || code == 1004:
		return websocket.CloseNormalClosure
	}
	return code
}

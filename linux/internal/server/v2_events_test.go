package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"sentinel-linux/internal/store"
)

type streamMsg struct {
	Type string         `json:"type"`
	At   int64          `json:"at"`
	Data map[string]any `json:"data"`
}

// setStreamVar shortens one of the stream's package settings for a test.
// Set before s.Handler(): each handler copies them when built.
func setStreamVar[T any](t *testing.T, v *T, val T) {
	t.Helper()
	old := *v
	*v = val
	t.Cleanup(func() { *v = old })
}

// streamServer serves s over real HTTP and returns the stream's ws:// URL.
func streamServer(t *testing.T, s *Server) (srv *httptest.Server, wsURL string) {
	t.Helper()
	srv = httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v2/events/stream"
}

func dialStream(t *testing.T, u string, h http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial(u, h)
	if c != nil {
		t.Cleanup(func() { c.Close() })
	}
	return c, resp, err
}

func bearerHeader(tok string) http.Header {
	return http.Header{"Authorization": {"Bearer " + tok}}
}

// dialBearer signs in an Operator and opens a stream with its token.
func dialBearer(t *testing.T, s *Server, wsURL, query string) (*websocket.Conn, string) {
	t.Helper()
	tok := v2SignIn(t, s, "op", "Operator")["accessToken"].(string)
	c, resp, err := dialStream(t, wsURL+query, bearerHeader(tok))
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	if m := readStream(t, c); m.Type != "hello" {
		t.Fatalf("first message %+v", m)
	}
	return c, tok
}

func readStream(t *testing.T, c *websocket.Conn) streamMsg {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m streamMsg
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	if m.At == 0 {
		t.Fatalf("message without at: %s", data)
	}
	return m
}

// expectClose reads until the server closes the stream and checks the code.
func expectClose(t *testing.T, c *websocket.Conn, code int) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := c.ReadMessage()
		if err == nil {
			t.Logf("message before close: %s", data)
			continue
		}
		if !websocket.IsCloseError(err, code) {
			t.Fatalf("want close %d, got %v", code, err)
		}
		return
	}
}

func TestV2StreamBearerHello(t *testing.T) {
	s := newCompanionServer(t)
	_, wsURL := streamServer(t, s)
	tok := v2SignIn(t, s, "op", "Operator")["accessToken"].(string)
	c, resp, err := dialStream(t, wsURL, bearerHeader(tok))
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	m := readStream(t, c)
	user, _ := m.Data["user"].(map[string]any)
	if m.Type != "hello" || user["name"] != "op" || user["role"] != "Operator" || m.Data["serverVersion"] != "test" {
		t.Fatalf("hello %+v", m)
	}
	if topics, _ := json.Marshal(m.Data["topics"]); string(topics) != `["alarm","event","camera","config","servers"]` {
		t.Fatalf("default topics %s", topics)
	}

	// A bad bearer token is refused before upgrading.
	if _, resp, err := dialStream(t, wsURL, bearerHeader("sat_nope")); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("bad token: %v %v", err, resp)
	}
}

func TestV2StreamCookieNeedsSameOrigin(t *testing.T) {
	s := newCompanionServer(t)
	srv, wsURL := streamServer(t, s)
	ctx := context.Background()
	u, err := s.Store.CreateUser(ctx, "viewer", "Viewer", "x")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Store.CreateSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	header := func(origin string) http.Header {
		h := http.Header{"Cookie": {sessionCookie + "=" + sess}}
		if origin != "" {
			h.Set("Origin", origin)
		}
		return h
	}
	c, resp, err := dialStream(t, wsURL, header(srv.URL))
	if err != nil {
		t.Fatalf("same origin: %v %v", err, resp)
	}
	if m := readStream(t, c); m.Type != "hello" || m.Data["user"].(map[string]any)["name"] != "viewer" {
		t.Fatalf("hello %+v", m)
	}
	for _, origin := range []string{"https://evil.example", ""} {
		if _, resp, err := dialStream(t, wsURL, header(origin)); err == nil || resp == nil || resp.StatusCode != 403 {
			t.Fatalf("origin %q: %v %v", origin, err, resp)
		}
	}
}

func TestV2StreamFirstMessageAuth(t *testing.T) {
	setStreamVar(t, &streamAuthTimeout, 200*time.Millisecond)
	s := newCompanionServer(t)
	_, wsURL := streamServer(t, s)
	tok := v2SignIn(t, s, "op", "Operator")["accessToken"].(string)

	c, _, err := dialStream(t, wsURL+"?topics=alarm", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteJSON(map[string]string{"type": "auth", "accessToken": tok}); err != nil {
		t.Fatal(err)
	}
	if m := readStream(t, c); m.Type != "hello" || m.Data["user"].(map[string]any)["name"] != "op" {
		t.Fatalf("hello %+v", m)
	}

	bad, _, err := dialStream(t, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = bad.WriteJSON(map[string]string{"type": "auth", "accessToken": "sat_nope"})
	expectClose(t, bad, 4401)

	silent, _, err := dialStream(t, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectClose(t, silent, 4401)
}

func TestV2StreamBadTopics(t *testing.T) {
	s := newCompanionServer(t)
	tok := v2SignIn(t, s, "op", "Operator")["accessToken"].(string)
	if code, m := v2Call(t, s.Handler(), "GET", "/api/v2/events/stream?topics=alarm,weather", tok, ""); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("unknown topic: %d %v", code, m)
	}
}

func TestV2StreamAlarmsAndEvents(t *testing.T) {
	s := newCompanionServer(t)
	_, wsURL := streamServer(t, s)
	c, _ := dialBearer(t, s, wsURL, "")
	ctx := context.Background()

	a, isNew, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: "cam1", CameraName: "Door", Kind: "Person",
		Title: "Person detected", Severity: "High", At: time.Now()})
	if err != nil || !isNew {
		t.Fatal(err, isNew)
	}
	if m := readStream(t, c); m.Type != "alarm.new" || m.Data["id"] != a.ID || m.Data["cameraName"] != "Door" {
		t.Fatalf("alarm.new %+v", m)
	}
	upd, err := s.Store.SetAlarmState(ctx, a.ID, "Acknowledged", "op", "on it")
	if err != nil {
		t.Fatal(err)
	}
	s.publishAlarmState(upd)
	if m := readStream(t, c); m.Type != "alarm.updated" || m.Data["state"] != "Acknowledged" || m.Data["owner"] != "op" {
		t.Fatalf("alarm.updated %+v", m)
	}

	e := &store.Event{CameraID: "cam1", Kind: "Person", Score: 0.9, CreatedAt: time.Now().Unix()}
	if err := s.Store.AddEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	s.publishEventNew(nil, e)
	if m := readStream(t, c); m.Type != "event.new" || m.Data["id"] != e.ID || m.Data["cameraId"] != "cam1" {
		t.Fatalf("event.new %+v", m)
	}
	if err := s.Store.SetEventAnalysis(ctx, e.ID, "A courier at the door", "low", false, "", "courier"); err != nil {
		t.Fatal(err)
	}
	s.publishEventUpdated(e.ID)
	if m := readStream(t, c); m.Type != "event.updated" || m.Data["id"] != e.ID || m.Data["description"] != "A courier at the door" {
		t.Fatalf("event.updated %+v", m)
	}
}

func TestV2StreamCameraStatusActivityConfig(t *testing.T) {
	s := newCompanionServer(t)
	_, wsURL := streamServer(t, s)
	def, tok := dialBearer(t, s, wsURL, "")
	act, resp, err := dialStream(t, wsURL+"?topics=activity", bearerHeader(tok))
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	if m := readStream(t, act); m.Type != "hello" {
		t.Fatalf("hello %+v", m)
	}

	s.publishCameraOnline("cam1", true)
	s.publishCameraOnline("cam1", true) // no change: not sent
	s.publishActivity("cam1", "motion") // default topics leave activity out
	s.publishCameraOnline("cam1", false)
	s.publishConfigChanged([]store.Camera{{ID: "cam1"}, {ID: "cam2"}})

	want := []string{`camera.status {"cameraId":"cam1","online":true}`, `camera.status {"cameraId":"cam1","online":false}`,
		`config.changed {"cameraIds":["cam1","cam2"]}`}
	for _, w := range want {
		m := readStream(t, def)
		data, _ := json.Marshal(m.Data)
		if got := m.Type + " " + string(data); got != w {
			t.Fatalf("got %s, want %s", got, w)
		}
	}
	if m := readStream(t, act); m.Type != "camera.activity" || m.Data["cameraId"] != "cam1" || m.Data["kind"] != "motion" {
		t.Fatalf("activity %+v", m)
	}
}

func TestV2StreamClosesWhenSignInEnds(t *testing.T) {
	setStreamVar(t, &streamRecheckEvery, 50*time.Millisecond)
	s := newCompanionServer(t)
	_, wsURL := streamServer(t, s)
	c, tok := dialBearer(t, s, wsURL, "")
	if err := s.Store.RevokeAccessTokenFamily(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	expectClose(t, c, 4401)
}

func TestV2StreamClosesOnShutdown(t *testing.T) {
	s := newCompanionServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Ctx = ctx
	_, wsURL := streamServer(t, s)
	c, _ := dialBearer(t, s, wsURL, "")
	cancel()
	expectClose(t, c, websocket.CloseGoingAway)
}

func TestV2StreamLaggedOverSocket(t *testing.T) {
	setStreamVar(t, &streamQueueLen, 4)
	s := newCompanionServer(t)
	_, wsURL := streamServer(t, s)
	c, _ := dialBearer(t, s, wsURL, "?topics=activity")
	// With the client not reading, the socket buffers fill, the writer
	// blocks and the connection's queue overflows.
	kind := strings.Repeat("x", 200)
	for range 20000 {
		s.publishActivity("cam1", kind)
	}
	for {
		m := readStream(t, c)
		if m.Type == "lagged" {
			if n, _ := m.Data["dropped"].(float64); n < 1 {
				t.Fatalf("lagged %+v", m)
			}
			return
		}
		if m.Type != "camera.activity" {
			t.Fatalf("unexpected %+v", m)
		}
	}
}

func TestStreamSendQueueLags(t *testing.T) {
	q := newSendQueue(2)
	for _, m := range []string{"a", "b", "c", "d"} {
		q.push([]byte(m))
	}
	if m := q.next(); string(m) != "a" {
		t.Fatalf("got %s", m)
	}
	q.push([]byte("e")) // still lagging: dropped until the queue drains
	if m := q.next(); string(m) != "b" {
		t.Fatalf("got %s", m)
	}
	var lag streamMsg
	if err := json.Unmarshal(q.next(), &lag); err != nil || lag.Type != "lagged" || lag.Data["dropped"] != float64(3) {
		t.Fatalf("lagged %+v %v", lag, err)
	}
	if m := q.next(); m != nil {
		t.Fatalf("queue not empty: %s", m)
	}
	q.push([]byte("f"))
	if m := q.next(); string(m) != "f" {
		t.Fatalf("after lag: %s", m)
	}
}

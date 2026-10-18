package server

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/onvif/fakecam"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/talkback"
)

// talkFixture: a fake ONVIF camera whose main stream is a fake RTSP
// endpoint with a backchannel (digest-protected), stored as camera "Door";
// a second camera "Fixed" whose backchannel is off; the usual users.
type talkFixture struct {
	s                           *Server
	srv                         *httptest.Server
	bc, offBC                   *fakecam.Backchannel
	id, fixedID                 string
	offONVIF                    string // ONVIF address of a camera without a backchannel
	admin, op, scopedOp, viewer string
}

func newTalkFixture(t *testing.T) *talkFixture {
	t.Helper()
	f := &talkFixture{s: newCompanionServer(t)}
	ctx := context.Background()
	camWith := func(bc *fakecam.Backchannel) string {
		if err := bc.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { bc.Close() })
		cam := &fakecam.Camera{User: "admin", Pass: "pw", Streams: []fakecam.Stream{
			{Token: "main", Name: "Main", Encoding: "H264", Width: 1920, Height: 1080, FPS: 20, RTSPURL: bc.URL()},
		}}
		srv := httptest.NewServer(cam)
		t.Cleanup(srv.Close)
		return srv.URL + "/onvif/device_service"
	}
	f.bc = &fakecam.Backchannel{User: "admin", Pass: "pw"}
	f.offBC = &fakecam.Backchannel{User: "admin", Pass: "pw", Off: true}
	onvifURL := camWith(f.bc)
	f.offONVIF = camWith(f.offBC)
	pw := "pw"
	c, err := f.s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Door", RTSPURL: f.bc.URL(), Username: "admin", Password: &pw, ONVIFURL: &onvifURL})
	if err != nil {
		t.Fatal(err)
	}
	f.id = c.ID
	fixed, err := f.s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Fixed", RTSPURL: f.offBC.URL(), Username: "admin", Password: &pw, ONVIFURL: &f.offONVIF})
	if err != nil {
		t.Fatal(err)
	}
	f.fixedID = fixed.ID
	other, err := f.s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Other", RTSPURL: "rtsp://127.0.0.1:1/x"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := f.s.Store.SaveCameraGroup(ctx, "", "Other only", []string{other.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.admin = v2Token(t, f.s, "boss", "Admin")
	f.op = v2Token(t, f.s, "op", "Operator")
	f.scopedOp = v2Token(t, f.s, "scoped", "Operator")
	f.viewer = v2Token(t, f.s, "viewer", "Viewer")
	u, _ := f.s.Store.UserByName(ctx, "scoped")
	if err := f.s.Store.SetUserScope(ctx, u.ID, store.UserScope{GroupIDs: []string{g.ID}}); err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewServer(f.s.Handler())
	t.Cleanup(f.srv.Close)
	return f
}

func (f *talkFixture) wsURL(id, query string) string {
	return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/api/v2/cameras/" + id + "/talkback" + query
}

func (f *talkFixture) dial(t *testing.T, id, query, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	var h http.Header
	if token != "" {
		h = bearerHeader(token)
	}
	return dialStream(t, f.wsURL(id, query), h)
}

// readReady reads the first message and checks it is "ready".
func readReady(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var m map[string]any
	if err := c.ReadJSON(&m); err != nil {
		t.Fatalf("no ready message: %v", err)
	}
	if m["type"] != "ready" || m["codec"] != "PCMU/8000" {
		t.Fatalf("first message %v", m)
	}
	return m
}

// closeCode reads until the server closes and returns the close code.
func closeCode(t *testing.T, c *websocket.Conn) int {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := c.ReadMessage()
		if err == nil {
			continue
		}
		var ce *websocket.CloseError
		if errors.As(err, &ce) {
			return ce.Code
		}
		t.Fatalf("connection ended without a close frame: %v", err)
	}
}

func pcmTone(n int, rate float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(9000 * math.Sin(2*math.Pi*523*float64(i)/rate))
	}
	return out
}

func le16(samples []int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

func talkWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// talkAudit returns the Cameras-area talk-down audit entries, oldest first.
func talkAudit(t *testing.T, s *Server) []store.AuditEntry {
	t.Helper()
	all, err := s.Store.AuditLog(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEntry
	for i := len(all) - 1; i >= 0; i-- {
		if e := all[i]; e.Area == "Cameras" && strings.Contains(e.Action, "talk-down") {
			out = append(out, e)
		}
	}
	return out
}

func TestTalkbackEndToEnd(t *testing.T) {
	f := newTalkFixture(t)
	c, resp, err := f.dial(t, f.id, "", f.op)
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	ready := readReady(t, c)
	if ready["idleTimeoutMs"] != float64(30000) || ready["maxDurationMs"] != float64(300000) || ready["rate"] != float64(8000) {
		t.Fatalf("ready %v", ready)
	}

	// One session per camera: a second caller gets 409 before any upgrade.
	if _, resp, err := f.dial(t, f.id, "", f.admin); err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("second session: err=%v resp=%v, want 409", err, resp)
	}

	// 25 frames of 20 ms, sent as 160-sample binary messages plus one odd
	// split to show framing doesn't depend on message boundaries.
	pcm := pcmTone(25*talkback.FrameSamples, 8000)
	if err := c.WriteMessage(websocket.BinaryMessage, le16(pcm[:100])); err != nil {
		t.Fatal(err)
	}
	for i := 100; i < len(pcm); i += talkback.FrameSamples {
		end := min(i+talkback.FrameSamples, len(pcm))
		if err := c.WriteMessage(websocket.BinaryMessage, le16(pcm[i:end])); err != nil {
			t.Fatal(err)
		}
	}
	talkWait(t, "25 packets at the camera", func() bool { return f.bc.Stats().Packets == 25 })
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"stop"}`)); err != nil {
		t.Fatal(err)
	}
	if code := closeCode(t, c); code != websocket.CloseNormalClosure {
		t.Fatalf("close code %d", code)
	}
	talkWait(t, "teardown", func() bool { return f.bc.Stats().Teardowns == 1 })
	st := f.bc.Stats()
	want := talkback.EncodeMuLaw(nil, pcm)
	if st.Packets != 25 || st.PayloadBytes != len(want) || st.Checksum != crc32.ChecksumIEEE(want) {
		t.Fatalf("camera got %d packets / %d bytes / crc %08x, want 25 / %d / %08x", st.Packets, st.PayloadBytes, st.Checksum, len(want), crc32.ChecksumIEEE(want))
	}
	if st.SeqGaps != 0 || st.TSGaps != 0 || st.Markers != 1 || st.PayloadTypes[0] != 25 || st.Setups != 1 || st.Plays != 1 {
		t.Fatalf("stream shape %+v", st)
	}

	talkWait(t, "audit of the end", func() bool { return len(talkAudit(t, f.s)) == 2 })
	a := talkAudit(t, f.s)
	if a[0].Action != "Started talk-down" || a[0].User != "op" || a[0].Detail != "Door" {
		t.Fatalf("start audit %+v", a[0])
	}
	if a[1].Action != "Ended talk-down" || a[1].User != "op" || !strings.HasPrefix(a[1].Detail, "Door: ") || !strings.HasSuffix(a[1].Detail, "s (hung up)") {
		t.Fatalf("end audit %+v", a[1])
	}

	// The camera is free again.
	talkWait(t, "slot released", func() bool { return f.s.talk.mgr.Active(f.id) == "" })
	c2, resp, err := f.dial(t, f.id, "", f.admin)
	if err != nil {
		t.Fatalf("after hang-up: %v %v", err, resp)
	}
	readReady(t, c2)
	c2.Close() // dropping the socket also tears down
	talkWait(t, "second teardown", func() bool { return f.bc.Stats().Teardowns == 2 })
}

func TestTalkback16kInput(t *testing.T) {
	f := newTalkFixture(t)
	c, resp, err := f.dial(t, f.id, "?rate=16000", f.op)
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	if r := readReady(t, c); r["rate"] != float64(16000) {
		t.Fatalf("ready %v", r)
	}
	pcm := pcmTone(20*2*talkback.FrameSamples, 16000) // 400 ms at 16 kHz
	for i := 0; i < len(pcm); i += 2 * talkback.FrameSamples {
		if err := c.WriteMessage(websocket.BinaryMessage, le16(pcm[i:i+2*talkback.FrameSamples])); err != nil {
			t.Fatal(err)
		}
	}
	talkWait(t, "20 packets", func() bool { return f.bc.Stats().Packets == 20 })
	c.Close()
	talkWait(t, "teardown", func() bool { return f.bc.Stats().Teardowns == 1 })
	var h talkback.Halver
	want := talkback.EncodeMuLaw(nil, h.Process(nil, pcm))
	if st := f.bc.Stats(); st.Checksum != crc32.ChecksumIEEE(want) || st.PayloadBytes != len(want) {
		t.Fatalf("16 kHz path: %d bytes crc %08x, want %d %08x", st.PayloadBytes, st.Checksum, len(want), crc32.ChecksumIEEE(want))
	}
}

func TestTalkbackIdleTimeout(t *testing.T) {
	setStreamVar(t, &talkIdleTimeout, 200*time.Millisecond)
	f := newTalkFixture(t)
	c, resp, err := f.dial(t, f.id, "", f.op)
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	readReady(t, c)
	_ = c.WriteMessage(websocket.BinaryMessage, le16(pcmTone(talkback.FrameSamples, 8000)))
	if code := closeCode(t, c); code != closeTalkIdle {
		t.Fatalf("close code %d, want %d", code, closeTalkIdle)
	}
	talkWait(t, "teardown", func() bool { return f.bc.Stats().Teardowns == 1 })
	talkWait(t, "end audit", func() bool { return len(talkAudit(t, f.s)) == 2 })
	if d := talkAudit(t, f.s)[1].Detail; !strings.HasSuffix(d, "(idle timeout)") {
		t.Fatalf("end audit %q", d)
	}
}

func TestTalkbackHardCap(t *testing.T) {
	setStreamVar(t, &talkMaxDuration, 300*time.Millisecond)
	f := newTalkFixture(t)
	c, resp, err := f.dial(t, f.id, "", f.op)
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	readReady(t, c)
	stop := make(chan struct{})
	defer close(stop)
	go func() { // keep talking: only the cap can end this
		frame := le16(pcmTone(talkback.FrameSamples, 8000))
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				if c.WriteMessage(websocket.BinaryMessage, frame) != nil {
					return
				}
			}
		}
	}()
	if code := closeCode(t, c); code != closeTalkLimit {
		t.Fatalf("close code %d, want %d", code, closeTalkLimit)
	}
	talkWait(t, "teardown", func() bool { return f.bc.Stats().Teardowns == 1 })
	talkWait(t, "end audit", func() bool { return len(talkAudit(t, f.s)) == 2 })
	if d := talkAudit(t, f.s)[1].Detail; !strings.HasSuffix(d, "(time limit)") {
		t.Fatalf("end audit %q", d)
	}
	if f.bc.Stats().Packets == 0 {
		t.Fatal("no audio reached the camera before the cap")
	}
}

func TestTalkbackAuthPermissionScope(t *testing.T) {
	setStreamVar(t, &talkAuthTimeout, 300*time.Millisecond)
	f := newTalkFixture(t)
	status := func(id, query, tok string) int {
		t.Helper()
		_, resp, err := f.dial(t, id, query, tok)
		if err == nil {
			t.Fatalf("%s %s: upgrade accepted", id, query)
		}
		if resp == nil {
			t.Fatalf("no response: %v", err)
		}
		return resp.StatusCode
	}
	if s := status(f.id, "", f.viewer); s != http.StatusForbidden {
		t.Errorf("Viewer: %d, want 403", s)
	}
	if s := status(f.id, "", f.scopedOp); s != http.StatusNotFound {
		t.Errorf("out of scope: %d, want 404", s)
	}
	if s := status(f.id, "", "not-a-token"); s != http.StatusUnauthorized {
		t.Errorf("bad token: %d, want 401", s)
	}
	if s := status(f.id, "?rate=44100", f.op); s != http.StatusBadRequest {
		t.Errorf("bad rate: %d, want 400", s)
	}
	if s := status(f.fixedID, "", f.op); s != http.StatusBadRequest {
		t.Errorf("camera without backchannel: %d, want 400", s)
	}
	if s := status("nope", "", f.admin); s != http.StatusNotFound {
		t.Errorf("unknown camera: %d, want 404", s)
	}

	// Token mode: first message auth.
	c, _, err := f.dial(t, f.id, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code := closeCode(t, c); code != closeSignInEnded {
		t.Errorf("no auth message: close %d, want 4401", code)
	}
	firstMsg := func(tok string) *websocket.Conn {
		c, _, err := f.dial(t, f.id, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := c.WriteJSON(map[string]string{"type": "auth", "accessToken": tok}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if code := closeCode(t, firstMsg(f.viewer)); code != closeForbidden {
		t.Errorf("Viewer by first message: close %d, want 4403", code)
	}
	if code := closeCode(t, firstMsg(f.scopedOp)); code != closeTalkNotFound {
		t.Errorf("out of scope by first message: close %d, want 4404", code)
	}
	ok := firstMsg(f.op)
	readReady(t, ok)
	if code := closeCode(t, firstMsg(f.admin)); code != closeTalkBusy {
		t.Errorf("second session by first message: close %d, want 4409", code)
	}
	ok.Close()
	talkWait(t, "teardown", func() bool { return f.bc.Stats().Teardowns == 1 })
	if f.bc.Stats().Setups != 1 {
		t.Fatalf("refused callers reached the camera: %+v", f.bc.Stats())
	}

	// Remote recording server: 502, nothing sent to the camera.
	if err := f.s.Store.SetCameraRecorder(context.Background(), f.id, "rs2"); err != nil {
		t.Fatal(err)
	}
	if s := status(f.id, "", f.op); s != http.StatusBadGateway {
		t.Errorf("remote recorder: %d, want 502", s)
	}
}

func TestTalkbackCameraRefuses(t *testing.T) {
	f := newTalkFixture(t)
	f.s.talk.dialHook = func(context.Context, string, string, string) (talkback.Sink, error) {
		return nil, errors.New("RTSP 453 Not Enough Bandwidth")
	}
	c, resp, err := f.dial(t, f.id, "", f.op)
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	var m map[string]any
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := c.ReadJSON(&m); err != nil || m["type"] != "error" || !strings.Contains(m["message"].(string), "453") {
		t.Fatalf("error message %v %v", m, err)
	}
	if code := closeCode(t, c); code != closeTalkCamera {
		t.Fatalf("close %d, want 4502", code)
	}
	talkWait(t, "slot released", func() bool { return f.s.talk.mgr.Active(f.id) == "" })
	if len(talkAudit(t, f.s)) != 0 {
		t.Fatal("a session that never started was audited")
	}
}

func TestTalkbackOnCameraPayload(t *testing.T) {
	f := newTalkFixture(t)
	sub := f.s.Bus.Subscribe("talk-test", 0)
	changed := make(chan struct{}, 4)
	bus.Handle(sub, TopicConfigChanged, func(ConfigChanged) {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	talkbackOf := func(id string) bool {
		r := do(t, f.s.Handler(), "GET", "/api/v2/cameras/"+id, f.op, "")
		if r.Code != 200 {
			t.Fatalf("GET camera: %d %s", r.Code, r.Body)
		}
		return contains(r.Body.String(), `"talkback":true`)
	}
	// The first look starts a background probe; the answer lands in memory
	// and config.changed tells clients to re-read.
	talkbackOf(f.id)
	talkWait(t, "talkback on the payload", func() bool { return talkbackOf(f.id) })
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no config.changed after the probe found talkback")
	}
	if talkbackOf(f.fixedID) {
		t.Fatal("camera without backchannel reports talkback")
	}
	// The list carries it too.
	r := do(t, f.s.Handler(), "GET", "/api/v2/cameras", f.op, "")
	if !contains(r.Body.String(), `"talkback":true`) || !contains(r.Body.String(), `"talkback":false`) {
		t.Fatalf("list: %s", r.Body)
	}
	// Saving the camera with addresses that have no backchannel re-probes
	// (both: the probe falls back to the stored RTSP address).
	if r := do(t, f.s.Handler(), "PATCH", "/api/v2/cameras/"+f.id, f.admin, `{"onvifUrl":"`+f.offONVIF+`","rtspUrl":"`+f.offBC.URL()+`"}`); r.Code != 200 {
		t.Fatalf("patch: %d %s", r.Code, r.Body)
	}
	talkWait(t, "talkback cleared after save", func() bool { return !talkbackOf(f.id) })
}

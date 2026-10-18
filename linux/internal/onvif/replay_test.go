package onvif

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// rtspClient plays ffmpeg's part: one TCP connection, requests in order.
type rtspClient struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
	seq  int
}

func (c *rtspClient) do(method, uri string, extra ...string) *rtspMessage {
	c.t.Helper()
	c.seq++
	req := fmt.Sprintf("%s %s RTSP/1.0\r\nCSeq: %d\r\nUser-Agent: Lavf61\r\n", method, uri, c.seq)
	for _, e := range extra {
		req += e + "\r\n"
	}
	if _, err := io.WriteString(c.conn, req+"\r\n"); err != nil {
		c.t.Fatal(err)
	}
	resp, err := readRTSPResponse(c.br)
	if err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
	if got := resp.get("CSeq"); got != fmt.Sprint(c.seq) {
		c.t.Fatalf("%s: CSeq %q, want %d", method, got, c.seq)
	}
	return resp
}

func replayFixture(t *testing.T) (uri string, cam interface{ ReplayLog() []string }) {
	t.Helper()
	g := newCam()
	g.EnableProfileG(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	ln, err := net.Listen("tcp", "127.0.0.1:0") // the OS picks a free port
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	g.ServeReplay(ln)
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	e, err := OpenEdge(context.Background(), srv.URL+"/onvif/device_service", "admin", "s3cret&<pw")
	if err != nil {
		t.Fatal(err)
	}
	uri, err = e.ReplayURI(context.Background(), "rec1")
	if err != nil {
		t.Fatal(err)
	}
	return uri, g
}

func TestReplayProxyAddsReplayHeadersAndAuth(t *testing.T) {
	uri, cam := replayFixture(t)
	from := time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)
	p, err := StartReplayProxy(uri, "admin", "s3cret&<pw", from, from.Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !strings.HasPrefix(p.URL(), "rtsp://127.0.0.1:") || !strings.HasSuffix(p.URL(), "/replay/rec1") || p.URL() == uri {
		t.Fatalf("proxy URL %q (camera %q)", p.URL(), uri)
	}
	conn, err := net.Dial("tcp", strings.TrimPrefix(strings.SplitN(p.URL(), "/replay", 2)[0], "rtsp://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	c := &rtspClient{t: t, conn: conn, br: bufio.NewReader(conn)}

	if r := c.do("OPTIONS", p.URL()); r.status() != 200 {
		t.Fatalf("OPTIONS %s", r.start)
	}
	// The camera challenges; the proxy answers with Digest over the
	// camera's URL, so the client never sees the 401.
	d := c.do("DESCRIBE", p.URL(), "Accept: application/sdp")
	if d.status() != 200 || !strings.Contains(string(d.body), "m=video") {
		t.Fatalf("DESCRIBE %s %q", d.start, d.body)
	}
	base := d.get("Content-Base")
	if base != uri+"/" {
		t.Fatalf("Content-Base %q, want the camera's %q", base, uri+"/")
	}
	if r := c.do("SETUP", base+"trackID=1", "Transport: RTP/AVP/TCP;unicast;interleaved=0-1"); r.status() != 200 {
		t.Fatalf("SETUP %s", r.start)
	}
	// ffmpeg asks for npt; the camera must get the clock range instead.
	if r := c.do("PLAY", base, "Session: 4711", "Range: npt=0.000-"); r.status() != 200 {
		t.Fatalf("PLAY %s", r.start)
	}
	// Then the RTP stream flows through untouched until the recording ends.
	got, _ := io.ReadAll(c.br)
	if want := "$\x00\x00\x04rtp0$\x00\x00\x04rtp1$\x00\x00\x04rtp2"; string(got) != want {
		t.Fatalf("stream %q, want %q", got, want)
	}
	want := []string{"PLAY clock=20261016T030000.000Z-20261016T031500.000Z rate-control=no"}
	if log := cam.ReplayLog(); !reflect.DeepEqual(log, want) {
		t.Fatalf("camera saw %q, want %q", log, want)
	}
}

func TestReplayProxyWrongPassword(t *testing.T) {
	uri, _ := replayFixture(t)
	p, err := StartReplayProxy(uri, "admin", "wrong", time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(strings.SplitN(p.URL(), "/replay", 2)[0], "rtsp://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	c := &rtspClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	if r := c.do("DESCRIBE", p.URL()); r.status() != 401 {
		t.Fatalf("DESCRIBE with a wrong password: %s", r.start)
	}
}

func TestReplayRewrite(t *testing.T) {
	p := &ReplayProxy{localPrefix: "rtsp://127.0.0.1:5000", camPrefix: "rtsp://10.0.0.9:554",
		from: time.Date(2026, 10, 16, 1, 2, 3, 0, time.UTC), to: time.Date(2026, 10, 16, 1, 17, 3, 0, time.UTC)}
	m := &rtspMessage{start: "PLAY rtsp://127.0.0.1:5000/replay/x?a=1 RTSP/1.0",
		headers: [][2]string{{"CSeq", "5"}, {"Range", "npt=0.000-"}, {"Scale", "1"}, {"Authorization", "Basic stale"}}}
	p.rewrite(m)
	if m.start != "PLAY rtsp://10.0.0.9:554/replay/x?a=1 RTSP/1.0" {
		t.Errorf("start line %q", m.start)
	}
	if m.get("Range") != "clock=20261016T010203.000Z-20261016T011703.000Z" || m.get("Require") != "onvif-replay" ||
		m.get("Rate-Control") != "no" || m.get("Scale") != "" || m.get("Authorization") != "" || m.get("CSeq") != "5" {
		t.Errorf("headers %v", m.headers)
	}
	// Absolute camera URLs (from Content-Base) pass unchanged; TEARDOWN gets no Require.
	m = &rtspMessage{start: "TEARDOWN rtsp://10.0.0.9:554/replay/x/ RTSP/1.0"}
	p.rewrite(m)
	if m.start != "TEARDOWN rtsp://10.0.0.9:554/replay/x/ RTSP/1.0" || m.get("Require") != "" {
		t.Errorf("teardown %q %v", m.start, m.headers)
	}
	if _, err := StartReplayProxy("http://10.0.0.9/x", "", "", time.Now(), time.Now()); err == nil {
		t.Error("non-RTSP replay address accepted")
	}
}

package recorder

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
)

const phoneID = "5A4F7C1E-3B2D-4E6F-8A9B-0C1D2E3F4A5B"

// recSink is a fake ingest: it records what it was fed.
type recSink struct {
	mu     sync.Mutex
	chunks [][]byte
	closed bool
	fail   bool
}

func (s *recSink) Write(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return io.ErrClosedPipe
	}
	s.chunks = append(s.chunks, append([]byte(nil), p...))
	return nil
}

func (s *recSink) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *recSink) fed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.chunks {
		out = append(out, string(c[8:]))
	}
	return out
}

func (s *recSink) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func box(typ, payload string) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[4:], typ)
	copy(b[8:], payload)
	return b
}

type pushFixture struct {
	r     *Recorder
	h     http.Handler
	clock *fakeClock
	mu    sync.Mutex
	sinks []*recSink
}

func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	r, _ := newTestRecorder(t)
	f := &pushFixture{r: r, h: r.Video(), clock: &fakeClock{t: time.Unix(1_800_000_000, 0)}}
	r.push.now, r.push.tick = f.clock.now, 2*time.Millisecond
	r.push.newSink = func(string) (pushSink, error) {
		s := &recSink{}
		f.mu.Lock()
		f.sinks = append(f.sinks, s)
		f.mu.Unlock()
		return s, nil
	}
	if _, err := r.ApplyConfig(context.Background(), link.Snapshot{Rev: 2, Cameras: []link.CameraConfig{
		{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", Recording: true},
		{ID: phoneID, Name: "Phone", Source: link.SourcePhone, Recording: true, RecordingMode: link.RecordEvents},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.endAllPush)
	return f
}

func (f *pushFixture) sink(i int) *recSink {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.sinks) {
		return nil
	}
	return f.sinks[i]
}

func (f *pushFixture) do(method, path string, body []byte) (int, map[string]any) {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func (f *pushFixture) start(t *testing.T) string {
	t.Helper()
	code, m := f.do("POST", "/push/"+phoneID+"/sessions", []byte(`{"video":"h264","width":1280,"height":720,"fps":30,"audio":null,"device":"EJ's iPhone"}`))
	if code != 200 || m["maxSegmentBytes"] != float64(8<<20) {
		t.Fatalf("start: %d %v", code, m)
	}
	return m["sessionId"].(string)
}

func (f *pushFixture) seg(sid string, seq int, payload string) (int, map[string]any) {
	return f.do("PUT", fmt.Sprintf("/push/%s/sessions/%s/segments/%d", phoneID, sid, seq), box("moof", payload))
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestPushOrderDuplicatesAndTooFarAhead(t *testing.T) {
	f := newPushFixture(t)
	sid := f.start(t)
	base := "/push/" + phoneID + "/sessions/" + sid
	// A segment before the init is refused with what's expected.
	if code, m := f.seg(sid, 0, "s0"); code != 409 || m["expected"] != float64(0) {
		t.Fatalf("before init: %d %v", code, m)
	}
	if code, m := f.do("PUT", base+"/init", box("moov", "x")); code != 400 {
		t.Fatalf("init without ftyp: %d %v", code, m)
	}
	init := box("ftyp", "iso5")
	if code, m := f.do("PUT", base+"/init", init); code != 204 {
		t.Fatalf("init: %d %v", code, m)
	}
	if code, _ := f.do("PUT", base+"/init", init); code != 204 {
		t.Fatalf("same init again: %d", code)
	}
	if code, _ := f.do("PUT", base+"/init", box("ftyp", "iso6")); code != 409 {
		t.Fatalf("different init: %d", code)
	}
	for _, step := range []struct {
		seq  int
		code int
	}{{0, 204}, {2, 204}, {1, 204}, {1, 204}, {0, 204}, {3, 204}, {13, 409}, {12, 204}} {
		code, m := f.seg(sid, step.seq, fmt.Sprintf("s%d", step.seq))
		if code != step.code {
			t.Fatalf("seq %d: %d %v", step.seq, code, m)
		}
		if code == 409 && m["expected"] != float64(4) {
			t.Fatalf("too far ahead: %v", m)
		}
	}
	if code, _ := f.do("PUT", base+"/segments/5", box("mdat", "x")); code != 400 {
		t.Fatalf("segment not starting with moof: %d", code)
	}
	got := strings.Join(f.sink(0).fed(), ",")
	if got != "iso5,s0,s1,s2,s3" { // ftyp payload, then the media in order, once each
		t.Fatalf("fed %s", got)
	}
	st := f.r.Status().Cameras[phoneID]
	if st.Push == nil || !st.Push.Streaming || st.Push.Device != "EJ's iPhone" || st.Push.LastSeenAt != f.clock.now().UnixMilli() {
		t.Fatalf("status %+v", st.Push)
	}
	if f.r.Status().Cameras[camID].Push != nil {
		t.Fatal("an RTSP camera has no push status")
	}
	// Not a phone camera, or no such session: 404.
	if code, _ := f.do("POST", "/push/"+camID+"/sessions", []byte(`{"video":"h264"}`)); code != 404 {
		t.Fatalf("rtsp camera: %d", code)
	}
	if code, _ := f.do("PUT", "/push/"+phoneID+"/sessions/"+strings.Repeat("0", 32)+"/segments/4", box("moof", "x")); code != 404 {
		t.Fatalf("unknown session: %d", code)
	}
	if code, _ := f.do("PUT", base+"/segments/4", make([]byte, 8<<20+1)); code != 413 {
		t.Fatalf("too big: %d", code)
	}
	// DELETE ends it and closes the ingest.
	if code, _ := f.do("DELETE", base, nil); code != 204 || !f.sink(0).isClosed() {
		t.Fatalf("stop: %d", code)
	}
	if code, _ := f.seg(sid, 4, "s4"); code != 404 {
		t.Fatalf("after stop: %d", code)
	}
	if st := f.r.Status().Cameras[phoneID].Push; st.Streaming || st.Device != "EJ's iPhone" || st.LastSeenAt == 0 {
		t.Fatalf("status after stop %+v", st)
	}
}

func TestPushGapSkipIdleEndAndOneSessionPerCamera(t *testing.T) {
	f := newPushFixture(t)
	sid := f.start(t)
	base := "/push/" + phoneID + "/sessions/" + sid
	if code, _ := f.do("PUT", base+"/init", box("ftyp", "i")); code != 204 {
		t.Fatal(code)
	}
	f.seg(sid, 0, "s0")
	f.seg(sid, 2, "s2") // 1 is missing
	f.seg(sid, 3, "s3")
	f.clock.add(2 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if got := strings.Join(f.sink(0).fed(), ","); got != "i,s0" {
		t.Fatalf("held before 3 s: %s", got)
	}
	f.clock.add(1100 * time.Millisecond)
	waitFor(t, "gap skip", func() bool { return strings.Join(f.sink(0).fed(), ",") == "i,s0,s2,s3" })
	// The late segment is now behind: acknowledged, not fed.
	if code, _ := f.seg(sid, 1, "s1"); code != 204 || strings.Join(f.sink(0).fed(), ",") != "i,s0,s2,s3" {
		t.Fatalf("late segment: %d", code)
	}
	// A new session ends the old one.
	sid2 := f.start(t)
	if !f.sink(0).isClosed() {
		t.Fatal("old ingest still open")
	}
	if code, _ := f.seg(sid, 4, "s4"); code != 404 {
		t.Fatalf("old session: %d", code)
	}
	f.do("PUT", "/push/"+phoneID+"/sessions/"+sid2+"/init", box("ftyp", "j"))
	f.clock.add(14 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if !f.r.pushStatus(phoneID).Streaming {
		t.Fatal("ended before 15 s idle")
	}
	f.clock.add(2 * time.Second)
	waitFor(t, "idle end", func() bool { return !f.r.pushStatus(phoneID).Streaming && f.sink(1).isClosed() })
	// Removing the camera ends its session.
	f.start(t)
	if _, err := f.r.ApplyConfig(context.Background(), link.Snapshot{Rev: 3}); err != nil {
		t.Fatal(err)
	}
	if f.r.pushStatus(phoneID).Streaming {
		t.Fatal("session survived the camera's removal")
	}
}

func TestPushIngestFailureEndsSession(t *testing.T) {
	f := newPushFixture(t)
	sid := f.start(t)
	f.do("PUT", "/push/"+phoneID+"/sessions/"+sid+"/init", box("ftyp", "i"))
	s := f.sink(0)
	s.mu.Lock()
	s.fail = true
	s.mu.Unlock()
	if code, _ := f.seg(sid, 0, "s0"); code != 503 {
		t.Fatalf("broken ingest: %d", code)
	}
	if f.r.pushStatus(phoneID).Streaming {
		t.Fatal("session still open")
	}
}

func TestMediaConfigForPhoneCamera(t *testing.T) {
	f := newPushFixture(t)
	b, err := os.ReadFile(f.r.Media.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b)
	i := strings.Index(cfg, "  "+phoneID+":\n")
	if i < 0 {
		t.Fatalf("no path for the phone camera:\n%s", cfg)
	}
	path := cfg[i:]
	if j := strings.Index(path[4:], "\n  "+camID); j >= 0 {
		path = path[:j+4]
	}
	for _, want := range []string{"source: publisher", "record: yes", "/.buffer/"} {
		if !strings.Contains(path, want) {
			t.Errorf("phone path lacks %q:\n%s", want, path)
		}
	}
	if !strings.Contains(cfg, "- action: publish") || !strings.Contains(cfg, "user: sentinel-push") {
		t.Errorf("publishing isn't restricted:\n%s", cfg)
	}
	if !strings.HasPrefix(f.r.Media.PublishURL(phoneID), "rtsp://sentinel-push:") {
		t.Error(f.r.Media.PublishURL(phoneID))
	}
	if got := redactURL("x rtsp://sentinel-push:secret@127.0.0.1:1/a y", ""); strings.Contains(got, "secret") {
		t.Error(got)
	}
}

// MARK: - Integration: real ffmpeg into a real MediaMTX

func lookTool(env, name string) string {
	if p := os.Getenv(env); p != "" {
		return p
	}
	p, _ := exec.LookPath(name)
	return p
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// splitFMP4 cuts a fragmented MP4 into its init segment (everything before
// the first moof) and media segments (each moof with what follows it).
func splitFMP4(t *testing.T, b []byte) ([]byte, [][]byte) {
	t.Helper()
	var initSeg []byte
	var segs [][]byte
	for off := 0; off+8 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[off:]))
		if n < 8 || off+n > len(b) {
			t.Fatalf("bad box at %d", off)
		}
		typ := string(b[off+4 : off+8])
		switch {
		case typ == "moof":
			segs = append(segs, append([]byte(nil), b[off:off+n]...))
		case len(segs) == 0:
			initSeg = append(initSeg, b[off:off+n]...)
		default:
			segs[len(segs)-1] = append(segs[len(segs)-1], b[off:off+n]...)
		}
		off += n
	}
	return initSeg, segs
}

func TestPushIntoMediaMTX(t *testing.T) {
	ffmpeg, mtx := lookTool("SENTINEL_FFMPEG", "ffmpeg"), lookTool("SENTINEL_MEDIAMTX", "mediamtx")
	if ffmpeg == "" || mtx == "" {
		t.Skip("needs ffmpeg and mediamtx (PATH, or SENTINEL_FFMPEG / SENTINEL_MEDIAMTX)")
	}
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mp4")
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=15",
		"-t", "6", "-c:v", "mpeg4", "-g", "15", "-f", "mp4", "-movflags", "+frag_keyframe+empty_moov+default_base_moof", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't make a test clip: %v %s", err, out)
	}
	raw, err := os.ReadFile(clip)
	if err != nil {
		t.Fatal(err)
	}
	initSeg, segs := splitFMP4(t, raw)
	if len(segs) < 4 {
		t.Fatalf("only %d fragments", len(segs))
	}

	st, err := OpenStore(filepath.Join(dir, "recorder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	sup := &media.Supervisor{Binary: mtx, ConfigPath: filepath.Join(dir, "mediamtx.yml"), LogPath: filepath.Join(dir, "mediamtx.log"),
		RecordingsRoot: filepath.Join(dir, "recordings"), Ports: media.Ports{RTSP: freePort(t), HLS: freePort(t), API: freePort(t)}}
	ctx, cancel := context.WithCancel(context.Background())
	r := New(dir, sup, st, "test", "")
	r.Ctx = ctx
	t.Setenv("SENTINEL_FFMPEG", ffmpeg)
	if _, err := r.ApplyConfig(ctx, link.Snapshot{Rev: 1, Cameras: []link.CameraConfig{
		{ID: phoneID, Name: "Phone", Source: link.SourcePhone, Recording: true, RecordingMode: link.RecordContinuous},
	}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()
	t.Cleanup(func() {
		r.endAllPush()
		cancel()
		<-done
		r.Wait()
	})
	api := fmt.Sprintf("http://127.0.0.1:%d/v3/paths/get/%s", sup.Ports.API, phoneID)
	pathState := func() (bool, bool) { // exists, ready
		resp, err := http.Get(api)
		if err != nil {
			return false, false
		}
		defer resp.Body.Close()
		var p struct {
			Ready bool `json:"ready"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&p)
		return resp.StatusCode == 200, p.Ready
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if ok, _ := pathState(); ok {
			break
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(sup.LogPath)
			t.Fatalf("MediaMTX never listed the publisher path:\n%s", log)
		}
		time.Sleep(100 * time.Millisecond)
	}

	h := r.Video()
	do := func(method, path string, body []byte) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(body)))
		return w.Code
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/push/"+phoneID+"/sessions", strings.NewReader(`{"video":"h264","width":320,"height":240,"fps":15,"audio":null}`)))
	var started struct{ SessionID string }
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil || w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	base := "/push/" + phoneID + "/sessions/" + started.SessionID
	if code := do("PUT", base+"/init", initSeg); code != 204 {
		t.Fatalf("init: %d", code)
	}
	ready := false
	for i, s := range segs {
		if code := do("PUT", fmt.Sprintf("%s/segments/%d", base, i), s); code != 204 {
			t.Fatalf("segment %d: %d", i, code)
		}
		time.Sleep(150 * time.Millisecond)
		if _, ok := pathState(); ok {
			ready = true
		}
	}
	for end := time.Now().Add(5 * time.Second); !ready && time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		_, ready = pathState()
	}
	if !ready {
		log, _ := os.ReadFile(sup.LogPath)
		t.Fatalf("MediaMTX never saw the pushed stream:\n%s", log)
	}
	if code := do("DELETE", base, nil); code != 204 {
		t.Fatalf("stop: %d", code)
	}
	// Nobody else may publish to the path, even from loopback.
	rogue := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-re", "-i", clip, "-t", "1", "-c", "copy", "-f", "rtsp", "-rtsp_transport", "tcp",
		fmt.Sprintf("rtsp://127.0.0.1:%d/%s", sup.Ports.RTSP, phoneID))
	if out, err := rogue.CombinedOutput(); err == nil {
		t.Fatalf("a publisher without the credential was accepted: %s", out)
	}
}

package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/onvif/fakecam"
	"sentinel-linux/internal/store"
)

const streamsPass = "Sekr3t-Pa55"

type streamsFixture struct {
	*encFixture
	describe *fakeDescriber
}

// fakeDescriber stands in for RTSP DESCRIBE: video only at the listed
// addresses; it records what it was asked with.
type fakeDescriber struct {
	mu    sync.Mutex
	video map[string]string
	calls []string
	creds []string
	pass  string // when set, any other password is ErrAuth
}

func (f *fakeDescriber) fn(_ context.Context, u, user, pass string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, u)
	f.creds = append(f.creds, user+":"+pass)
	if f.pass != "" && pass != f.pass {
		return "", onvif.ErrAuth
	}
	if c, ok := f.video[u]; ok {
		return c, nil
	}
	return "", onvif.ErrStreamRefused
}

// newStreamsFixture: a saved camera (no sub-stream stored) whose ONVIF
// service offers the given streams.
func newStreamsFixture(t *testing.T, mainURL string, streams ...fakecam.Stream) *streamsFixture {
	t.Helper()
	f := &encFixture{s: newCompanionServer(t)}
	f.cam = &fakecam.Camera{User: "admin", Pass: streamsPass, Streams: streams}
	srv := httptest.NewServer(f.cam)
	t.Cleanup(srv.Close)
	pw, onvifURL := streamsPass, srv.URL+"/onvif/device_service"
	c, err := f.s.Store.SaveCamera(context.Background(), "", store.CameraInput{Name: "Gate", RTSPURL: mainURL, Username: "admin", Password: &pw, ONVIFURL: &onvifURL})
	if err != nil {
		t.Fatal(err)
	}
	f.id = c.ID
	f.admin = v2Token(t, f.s, "boss", "Admin")
	f.op = v2Token(t, f.s, "op", "Operator")
	f.viewer = v2Token(t, f.s, "viewer", "Viewer")
	d := &fakeDescriber{video: map[string]string{}}
	f.s.DescribeStream = d.fn
	f.h = f.s.Handler()
	return &streamsFixture{encFixture: f, describe: d}
}

func (f *streamsFixture) url(op string) string { return "/api/v2/cameras/" + f.id + "/streams/" + op }

func decodeDiscovery(t *testing.T, body []byte) onvif.StreamDiscovery {
	t.Helper()
	var d onvif.StreamDiscovery
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func noSecrets(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, streamsPass) || strings.Contains(body, "admin:") || strings.Contains(body, "password") {
		t.Fatalf("response leaks credentials: %s", body)
	}
}

func TestStreamsDiscoverONVIF(t *testing.T) {
	f := newStreamsFixture(t, "rtsp://10.0.0.5/s1",
		fakecam.Stream{Token: "p1", Name: "Main", Encoding: "H264", Width: 1920, Height: 1080, FPS: 25, RTSPURL: "rtsp://admin:" + streamsPass + "@10.0.0.5/s1"},
		fakecam.Stream{Token: "p2", Name: "Sub", Encoding: "H264", Width: 1280, Height: 720, FPS: 15, RTSPURL: "rtsp://10.0.0.5/s2"},
	)
	r := do(t, f.h, "POST", f.url("discover"), f.admin, "")
	if r.Code != 200 {
		t.Fatalf("discover = %d %s", r.Code, r.Body)
	}
	noSecrets(t, r.Body.String())
	// The wire shape: explicit nulls, every field present.
	var raw map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &raw)
	if _, ok := raw["tooSmallSub"]; !ok || raw["tooSmallSub"] != nil {
		t.Fatalf("tooSmallSub should be null: %s", r.Body)
	}
	d := decodeDiscovery(t, r.Body.Bytes())
	if len(d.Streams) != 2 || d.Streams[0].Role != "main" || d.Streams[1].Role != "" || d.Streams[1].EncoderToken != "vp2" || d.Streams[1].Label != "1280×720 · H.264 · 15 fps" {
		t.Fatalf("streams = %+v", d.Streams)
	}
	if d.RecommendedSub == nil || *d.RecommendedSub != "rtsp://10.0.0.5/s2" {
		t.Fatalf("recommended = %v", d.RecommendedSub)
	}
	if len(f.describe.calls) != 0 {
		t.Fatalf("guessed although ONVIF had a second stream: %v", f.describe.calls)
	}
}

func TestStreamsDiscoverGuessesFiltered(t *testing.T) {
	main := "rtsp://10.0.0.5/h264/ch1/main/stream1"
	f := newStreamsFixture(t, main, fakecam.Stream{Token: "p1", Encoding: "H264", Width: 1920, Height: 1080, FPS: 25, RTSPURL: main})
	f.describe.video["rtsp://10.0.0.5/h264/ch1/sub/stream1"] = "H264"
	r := do(t, f.h, "POST", f.url("discover"), f.admin, "")
	if r.Code != 200 {
		t.Fatalf("discover = %d %s", r.Code, r.Body)
	}
	d := decodeDiscovery(t, r.Body.Bytes())
	if len(f.describe.calls) != 2 { // stream1→stream2 and main→sub
		t.Fatalf("describe calls = %v", f.describe.calls)
	}
	for _, c := range f.describe.creds {
		if c != "admin:"+streamsPass {
			t.Fatalf("guesses not checked with the saved login: %v", f.describe.creds)
		}
	}
	if len(d.Streams) != 2 || d.Streams[1].Source != "guess" || d.Streams[1].URL != "rtsp://10.0.0.5/h264/ch1/sub/stream1" {
		t.Fatalf("streams = %+v (refused guess must be left out)", d.Streams)
	}
	if d.RecommendedSub == nil || *d.RecommendedSub != d.Streams[1].URL {
		t.Fatalf("recommended = %v", d.RecommendedSub)
	}
	noSecrets(t, r.Body.String())
}

func TestStreamsDiscoverLoginAndAuthFailure(t *testing.T) {
	f := newStreamsFixture(t, "rtsp://10.0.0.5/live", fakecam.Stream{Token: "p1", Encoding: "H264", Width: 1920, Height: 1080, RTSPURL: "rtsp://10.0.0.5/live"})
	r := do(t, f.h, "POST", f.url("discover"), f.admin, `{"password":"wrong-one"}`)
	if r.Code != 422 {
		t.Fatalf("wrong login = %d %s", r.Code, r.Body)
	}
	var e struct {
		Error      struct{ Code, Message string }
		AuthFailed bool
	}
	_ = json.Unmarshal(r.Body.Bytes(), &e)
	if !e.AuthFailed || e.Error.Code != "camera_auth" || strings.Contains(r.Body.String(), "wrong-one") {
		t.Fatalf("body = %s", r.Body)
	}
	// The saved login still works; a camera with one stream is an empty answer.
	r = do(t, f.h, "POST", f.url("discover"), f.admin, `{}`)
	if r.Code != 200 {
		t.Fatalf("saved login = %d %s", r.Code, r.Body)
	}
	if d := decodeDiscovery(t, r.Body.Bytes()); d.RecommendedSub != nil || len(d.Streams) != 1 {
		t.Fatalf("d = %+v", d)
	}
	if r := do(t, f.h, "POST", f.url("discover"), f.admin, `{"user":"x"}`); r.Code != 400 {
		t.Fatalf("unknown field = %d", r.Code)
	}
}

func TestStreamsPermissions(t *testing.T) {
	f := newStreamsFixture(t, "rtsp://10.0.0.5/s1", fakecam.Stream{Token: "p1", Encoding: "H264", Width: 1920, Height: 1080, RTSPURL: "rtsp://10.0.0.5/s1"})
	for _, tok := range []string{f.op, f.viewer} {
		if r := do(t, f.h, "POST", f.url("discover"), tok, ""); r.Code != 403 {
			t.Errorf("discover as non-admin = %d", r.Code)
		}
		if r := do(t, f.h, "POST", f.url("resize"), tok, `{"encoderToken":"vp1","width":1280,"height":720}`); r.Code != 403 {
			t.Errorf("resize as non-admin = %d", r.Code)
		}
	}
	if r := do(t, f.h, "POST", f.url("discover"), "", ""); r.Code != 401 {
		t.Errorf("anonymous = %d", r.Code)
	}
	if r := do(t, f.h, "POST", "/api/v2/cameras/nope/streams/discover", f.admin, ""); r.Code != 404 {
		t.Errorf("unknown camera = %d", r.Code)
	}
	if len(f.cam.EncoderSets()) != 0 {
		t.Fatal("a refused resize reached the camera")
	}
}

func TestStreamsResize(t *testing.T) {
	f := newStreamsFixture(t, "rtsp://10.0.0.5/s1",
		fakecam.Stream{Token: "p1", Encoding: "H264", Width: 1920, Height: 1080, FPS: 25, RTSPURL: "rtsp://10.0.0.5/s1"},
		fakecam.Stream{Token: "p2", Encoding: "H264", Width: 320, Height: 180, FPS: 10, RTSPURL: "rtsp://10.0.0.5/s2"},
	)
	r := do(t, f.h, "POST", f.url("discover"), f.admin, "")
	d := decodeDiscovery(t, r.Body.Bytes())
	if d.TooSmallSub == nil || d.TooSmallSub.EncoderToken != "vp2" || d.TooSmallSub.Width != 320 {
		t.Fatalf("tooSmallSub = %+v", d.TooSmallSub)
	}

	for _, body := range []string{`{"width":1280,"height":720}`, `{"encoderToken":" ","width":1280,"height":720}`, `{"encoderToken":"vp2"}`, `{"encoderToken":"vp2","width":1234,"height":567}`} {
		if r := do(t, f.h, "POST", f.url("resize"), f.admin, body); r.Code != 400 {
			t.Errorf("resize %s = %d %s, want 400", body, r.Code, r.Body)
		}
	}
	if r := do(t, f.h, "POST", f.url("resize"), f.admin, `{"encoderToken":"vnope","width":1280,"height":720}`); r.Code != 404 {
		t.Errorf("unknown token = %d", r.Code)
	}
	if len(f.cam.EncoderSets()) != 0 {
		t.Fatal("an invalid resize reached the camera")
	}

	r = do(t, f.h, "POST", f.url("resize"), f.admin, `{"encoderToken":"vp2","width":1280,"height":720}`)
	if r.Code != 200 {
		t.Fatalf("resize = %d %s", r.Code, r.Body)
	}
	noSecrets(t, r.Body.String())
	var st onvif.FoundStream
	_ = json.Unmarshal(r.Body.Bytes(), &st)
	if st.URL != "rtsp://10.0.0.5/s2" || st.Width != 1280 || st.Height != 720 || st.EncoderToken != "vp2" || st.Source != "onvif" {
		t.Fatalf("stream = %+v", st)
	}
	if cs, _ := f.cam.Stream("p2"); cs.Width != 1280 || cs.Height != 720 || cs.FPS != 10 {
		t.Fatalf("camera = %+v", cs)
	}
	logs, _ := f.s.Store.AuditLog(context.Background(), 50)
	found := false
	for _, e := range logs {
		found = found || (e.Action == "Changed stream settings" && strings.Contains(e.Detail, "Gate") && strings.Contains(e.Detail, "320x180 → 1280x720"))
	}
	if !found {
		t.Fatalf("resize not audited: %+v", logs)
	}
}

// A camera on a remote recording server is discovered by that server
// (link.CmdStreamsDiscover), with its own DESCRIBE checker.
func TestStreamsDiscoverRemoteRecorder(t *testing.T) {
	main := "rtsp://10.0.0.5/stream1"
	f := newStreamsFixture(t, main, fakecam.Stream{Token: "p1", Encoding: "H264", Width: 1920, Height: 1080, RTSPURL: main})
	ctx := context.Background()
	rec := addRecorder(t, f.s, "rs2", "Depot")
	remote := &fakeDescriber{video: map[string]string{"rtsp://10.0.0.5/stream2": "H265"}, pass: streamsPass}
	rec.DescribeStream = remote.fn
	if err := f.s.Store.SetCameraRecorder(ctx, f.id, "rs2"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ApplyRecorder(ctx, "rs2"); err != nil {
		t.Fatal(err)
	}
	r := do(t, f.h, "POST", f.url("discover"), f.admin, "")
	if r.Code != 200 {
		t.Fatalf("remote discover = %d %s", r.Code, r.Body)
	}
	d := decodeDiscovery(t, r.Body.Bytes())
	if len(d.Streams) != 2 || d.Streams[1].Codec != "H265" || d.RecommendedSub == nil || *d.RecommendedSub != "rtsp://10.0.0.5/stream2" {
		t.Fatalf("d = %+v", d)
	}
	if len(f.describe.calls) != 0 || len(remote.calls) != 1 || remote.creds[0] != "admin:"+streamsPass {
		t.Fatalf("local=%v remote=%v creds=%v", f.describe.calls, remote.calls, remote.creds)
	}
	// A one-off login travels to the recorder for this discovery only.
	r = do(t, f.h, "POST", f.url("discover"), f.admin, `{"password":"bad"}`)
	if r.Code != 422 || !strings.Contains(r.Body.String(), `"authFailed":true`) {
		t.Fatalf("remote wrong login = %d %s", r.Code, r.Body)
	}
}

func TestStreamsDiscoverRecorderOffline(t *testing.T) {
	f := newStreamsFixture(t, "rtsp://10.0.0.5/s1", fakecam.Stream{Token: "p1", Encoding: "H264", Width: 1920, Height: 1080, RTSPURL: "rtsp://10.0.0.5/s1"})
	ctx := context.Background()
	if err := f.s.Store.AddRecordingServer(ctx, &store.RecordingServer{ID: "rs9", Name: "Gone"}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Store.SetCameraRecorder(ctx, f.id, "rs9"); err != nil {
		t.Fatal(err)
	}
	if r := do(t, f.h, "POST", f.url("discover"), f.admin, ""); r.Code != 502 || !strings.Contains(r.Body.String(), "isn't connected") {
		t.Fatalf("offline = %d %s", r.Code, r.Body)
	}
}

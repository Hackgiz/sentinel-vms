package onvif

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"sentinel-linux/internal/onvif/fakecam"
)

func TestGuessSubURLsPatterns(t *testing.T) {
	for _, c := range []struct{ main, want string }{
		{"rtsp://10.0.0.5:554/stream1", "rtsp://10.0.0.5:554/stream2"},
		{"rtsp://10.0.0.5/Stream1?x=1", "rtsp://10.0.0.5/Stream2?x=1"},
		{"rtsp://10.0.0.5/101", "rtsp://10.0.0.5/102"},
		{"rtsp://10.0.0.5:554/Streaming/Channels/101", "rtsp://10.0.0.5:554/Streaming/Channels/102"},
		{"rtsp://10.0.0.5/Streaming/Channels/201?transportmode=unicast", "rtsp://10.0.0.5/Streaming/Channels/202?transportmode=unicast"},
		{"rtsp://10.0.0.5/cam/realmonitor?channel=1&subtype=0", "rtsp://10.0.0.5/cam/realmonitor?channel=1&subtype=1"},
		{"rtsp://10.0.0.5/live_main.sdp", "rtsp://10.0.0.5/live_sub.sdp"},
		{"rtsp://10.0.0.5/h264/ch1/main/av_stream", "rtsp://10.0.0.5/h264/ch1/sub/av_stream"},
		{"rtsp://10.0.0.5/live/main", "rtsp://10.0.0.5/live/sub"},
		{"rtsp://10.0.0.5/profile1/media.smp", "rtsp://10.0.0.5/profile2/media.smp"},
		{"rtsp://10.0.0.5/ch0_0.h264", "rtsp://10.0.0.5/ch0_1.h264"},
		{"rtsp://10.0.0.5/videoMain/av0_0", "rtsp://10.0.0.5/videoMain/av0_1"},
	} {
		got := GuessSubURLs(c.main)
		if len(got) == 0 || got[0] != c.want {
			t.Errorf("GuessSubURLs(%q) = %q, want first %q", c.main, got, c.want)
		}
	}
}

func TestGuessSubURLsRules(t *testing.T) {
	// No guess for an address no pattern fits, nor for a bare host.
	for _, m := range []string{"rtsp://10.0.0.5/live", "rtsp://10.0.0.5", "rtsp://10.0.0.5/stream10", "rtsp://10.0.0.5/1010", "not a url"} {
		if g := GuessSubURLs(m); len(g) != 0 {
			t.Errorf("GuessSubURLs(%q) = %q, want none", m, g)
		}
	}
	// Host and credentials are never rewritten; credentials are dropped.
	got := GuessSubURLs("rtsp://admin:main1@main.cam.local:554/stream1")
	if !reflect.DeepEqual(got, []string{"rtsp://main.cam.local:554/stream2"}) {
		t.Fatalf("got %q", got)
	}
	// Several patterns on one address: each once, never the main itself.
	main := "rtsp://10.0.0.5/h264_main/stream1"
	got = GuessSubURLs(main)
	seen := map[string]bool{}
	for _, g := range got {
		if g == main || seen[g] {
			t.Fatalf("duplicate or main in %q", got)
		}
		seen[g] = true
	}
	if !seen["rtsp://10.0.0.5/h264_main/stream2"] || !seen["rtsp://10.0.0.5/h264_sub/stream1"] || len(got) != 2 {
		t.Fatalf("got %q", got)
	}
}

func TestChooseStreamsPrefersSmallestUsableSub(t *testing.T) {
	ps := []Profile{
		{Token: "m", Encoding: "H264", Width: 2560, Height: 1440, RTSPURL: "rtsp://c/1"},
		{Token: "tiny", Encoding: "H264", Width: 352, Height: 288, RTSPURL: "rtsp://c/3"},
		{Token: "hd", Encoding: "H264", Width: 1280, Height: 720, RTSPURL: "rtsp://c/4"},
		{Token: "vga", Encoding: "H264", Width: 640, Height: 360, RTSPURL: "rtsp://c/2"},
		{Token: "mjpeg", Encoding: "JPEG", Width: 800, Height: 600, RTSPURL: "rtsp://c/5"},
	}
	main, sub := ChooseStreams(ps)
	if main.Token != "m" || sub == nil || sub.Token != "vga" {
		t.Fatalf("main=%v sub=%v, want m + vga (smallest ≥ 640×360)", main, sub)
	}
	// Nothing reaches 640×360: fall back to the smallest.
	main, sub = ChooseStreams([]Profile{ps[0], ps[1], {Token: "qvga", Encoding: "H264", Width: 320, Height: 180, RTSPURL: "rtsp://c/6"}})
	if sub == nil || sub.Token != "qvga" {
		t.Fatalf("fallback sub = %v", sub)
	}
	if _, sub = ChooseStreams(ps[:1]); sub != nil {
		t.Fatalf("single profile sub = %v", sub)
	}
}

func TestSDPVideoCodec(t *testing.T) {
	for _, c := range []struct {
		sdp, codec string
		ok         bool
	}{
		{"v=0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n", "H264", true},
		{"m=audio 0 RTP/AVP 0\r\nm=video 0 RTP/AVP 98\r\na=rtpmap:98 H265/90000\r\n", "H265", true},
		{"m=video 0 RTP/AVP 26\r\n", "MJPEG", true},
		{"m=video 0 RTP/AVP 97\r\n", "", true},
		{"m=audio 0 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n", "", false},
	} {
		codec, ok := sdpVideoCodec(c.sdp)
		if codec != c.codec || ok != c.ok {
			t.Errorf("sdpVideoCodec(%q) = %q %v", c.sdp, codec, ok)
		}
	}
}

func TestDescribeVideoAgainstFakeRTSP(t *testing.T) {
	b := &fakecam.Backchannel{User: "admin", Pass: "pw"}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	codec, err := DescribeVideo(context.Background(), b.URL(), "admin", "pw")
	if err != nil || codec != "H264" {
		t.Fatalf("codec=%q err=%v", codec, err)
	}
	if _, err := DescribeVideo(context.Background(), b.URL(), "admin", "nope"); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong password: %v", err)
	}
}

// fakeDescribe answers video for the listed addresses and records calls.
type fakeDescribe struct {
	mu    sync.Mutex
	video map[string]string
	err   error // for every other address (default ErrStreamRefused)
	calls []string
	creds []string
}

func (f *fakeDescribe) fn(_ context.Context, u, user, pass string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, u)
	f.creds = append(f.creds, user+":"+pass)
	if c, ok := f.video[u]; ok {
		return c, nil
	}
	if f.err != nil {
		return "", f.err
	}
	return "", ErrStreamRefused
}

func discoverCam(streams ...fakecam.Stream) (*fakecam.Camera, string, func()) {
	cam := &fakecam.Camera{User: "admin", Pass: "pw", Streams: streams}
	srv := httptest.NewServer(cam)
	return cam, srv.URL + "/onvif/device_service", srv.Close
}

func TestDiscoverStreamsONVIFRecommends(t *testing.T) {
	_, svc, done := discoverCam(
		fakecam.Stream{Token: "p1", Name: "Main", Encoding: "H264", Width: 1920, Height: 1080, FPS: 25, RTSPURL: "rtsp://admin:pw@10.0.0.5/s1"},
		fakecam.Stream{Token: "p2", Name: "Sub", Encoding: "H264", Width: 640, Height: 360, FPS: 15, RTSPURL: "rtsp://10.0.0.5/s2"},
		fakecam.Stream{Token: "p3", Name: "Third", Encoding: "H264", Width: 320, Height: 180, FPS: 10, RTSPURL: "rtsp://10.0.0.5/s3"},
	)
	defer done()
	f := &fakeDescribe{}
	d, err := DiscoverStreams(context.Background(), StreamTarget{ONVIFURL: svc, MainURL: "rtsp://10.0.0.5/s1", Username: "admin", Password: "pw"}, f.fn)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Streams) != 3 || len(f.calls) != 0 {
		t.Fatalf("streams=%+v describes=%v", d.Streams, f.calls)
	}
	m := d.Streams[0]
	if m.URL != "rtsp://10.0.0.5/s1" || m.Role != "main" || m.Source != "onvif" || m.EncoderToken != "vp1" || m.Label != "1920×1080 · H.264 · 25 fps" || m.Codec != "H264" {
		t.Fatalf("main = %+v (credentials must be stripped)", m)
	}
	if d.RecommendedSub == nil || *d.RecommendedSub != "rtsp://10.0.0.5/s2" || d.TooSmallSub != nil {
		t.Fatalf("recommended=%v tooSmall=%+v", d.RecommendedSub, d.TooSmallSub)
	}
}

func TestDiscoverStreamsTooSmallSub(t *testing.T) {
	_, svc, done := discoverCam(
		fakecam.Stream{Token: "p1", Encoding: "H264", Width: 1920, Height: 1080, FPS: 25, RTSPURL: "rtsp://10.0.0.5/s1"},
		fakecam.Stream{Token: "p2", Encoding: "H264", Width: 352, Height: 288, FPS: 15, RTSPURL: "rtsp://10.0.0.5/s2"},
	)
	defer done()
	d, err := DiscoverStreams(context.Background(), StreamTarget{ONVIFURL: svc, MainURL: "rtsp://10.0.0.5/s1", SubURL: "rtsp://10.0.0.5/s2", Username: "admin", Password: "pw"}, (&fakeDescribe{}).fn)
	if err != nil {
		t.Fatal(err)
	}
	want := &TooSmallSub{URL: "rtsp://10.0.0.5/s2", EncoderToken: "vp2", Width: 352, Height: 288}
	if !reflect.DeepEqual(d.TooSmallSub, want) || d.RecommendedSub == nil || *d.RecommendedSub != want.URL || d.Streams[1].Role != "sub" {
		t.Fatalf("got %+v / %+v", d.TooSmallSub, d)
	}
}

func TestDiscoverStreamsGuessesWhenONVIFHasNoSecond(t *testing.T) {
	_, svc, done := discoverCam(fakecam.Stream{Token: "p1", Encoding: "H264", Width: 1920, Height: 1080, FPS: 25, RTSPURL: "rtsp://10.0.0.5/Streaming/Channels/101"})
	defer done()
	f := &fakeDescribe{video: map[string]string{"rtsp://10.0.0.5/Streaming/Channels/102": "H264"}}
	d, err := DiscoverStreams(context.Background(), StreamTarget{ONVIFURL: svc, MainURL: "rtsp://10.0.0.5/Streaming/Channels/101", Username: "admin", Password: "pw"}, f.fn)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Streams) != 2 || d.Streams[1].Source != "guess" || d.Streams[1].URL != "rtsp://10.0.0.5/Streaming/Channels/102" || d.Streams[1].Label != "H.264" {
		t.Fatalf("streams = %+v", d.Streams)
	}
	if d.RecommendedSub == nil || *d.RecommendedSub != d.Streams[1].URL || d.TooSmallSub != nil {
		t.Fatalf("recommended = %v", d.RecommendedSub)
	}
	if !reflect.DeepEqual(f.creds, []string{"admin:pw"}) {
		t.Fatalf("describe creds = %v", f.creds)
	}
}

func TestDiscoverStreamsWithoutONVIF(t *testing.T) {
	// No ONVIF address: the by-host attempt fails (netguard), guesses run;
	// at most MaxStreamGuesses, refused ones are left out.
	main := "rtsp://10.0.0.5/stream1/main/profile1/ch0_0/av0_0/101?subtype=0"
	f := &fakeDescribe{video: map[string]string{}}
	guesses := GuessSubURLs(main)
	if len(guesses) <= MaxStreamGuesses {
		t.Fatalf("fixture should exceed the cap: %d", len(guesses))
	}
	f.video[guesses[1]] = "MJPEG"
	d, err := DiscoverStreams(context.Background(), StreamTarget{MainURL: main}, f.fn)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != MaxStreamGuesses || len(d.Streams) != 1 || d.Streams[0].Codec != "MJPEG" {
		t.Fatalf("calls=%d streams=%+v", len(f.calls), d.Streams)
	}
	if d.RecommendedSub != nil {
		t.Fatalf("MJPEG recommended: %v", *d.RecommendedSub)
	}
	// Nothing fits a pattern and no ONVIF: empty, not an error.
	d, err = DiscoverStreams(context.Background(), StreamTarget{MainURL: "rtsp://10.0.0.5/live"}, f.fn)
	if err != nil || len(d.Streams) != 0 || d.RecommendedSub != nil {
		t.Fatalf("d=%+v err=%v", d, err)
	}
}

func TestDiscoverStreamsFailures(t *testing.T) {
	_, svc, done := discoverCam(fakecam.Stream{Token: "p1", Encoding: "H264", Width: 1920, Height: 1080, RTSPURL: "rtsp://10.0.0.5/stream1"})
	defer done()
	// Wrong password over ONVIF and RTSP: ErrAuth.
	f := &fakeDescribe{err: ErrAuth}
	_, err := DiscoverStreams(context.Background(), StreamTarget{ONVIFURL: svc, MainURL: "rtsp://10.0.0.5/stream1", Username: "admin", Password: "bad"}, f.fn)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if strings.Contains(err.Error(), "bad") {
		t.Fatalf("error leaks the password: %v", err)
	}
	// Unreachable: no ONVIF answer, guesses can't connect.
	f = &fakeDescribe{err: errors.New("connection refused")}
	_, err = DiscoverStreams(context.Background(), StreamTarget{MainURL: "rtsp://10.0.0.5/stream1"}, f.fn)
	if err == nil || errors.Is(err, ErrAuth) {
		t.Fatalf("unreachable err = %v", err)
	}
	// Reached but no second stream: empty success.
	f = &fakeDescribe{}
	d, err := DiscoverStreams(context.Background(), StreamTarget{MainURL: "rtsp://10.0.0.5/stream1"}, f.fn)
	if err != nil || len(d.Streams) != 0 {
		t.Fatalf("d=%+v err=%v", d, err)
	}
}

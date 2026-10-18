package onvif

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"sentinel-linux/internal/onvif/fakecam"
)

func TestParseBackchannelSDP(t *testing.T) {
	const base = "rtsp://cam/onvif/stream1"
	cases := []struct {
		name string
		sdp  string
		want *BackchannelTrack
	}{
		{"onvif example", "v=0\r\ns=x\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:video\r\na=recvonly\r\n" +
			"m=audio 0 RTP/AVP 0\r\na=control:audio\r\na=recvonly\r\nm=audio 0 RTP/AVP 0\r\na=control:audioback\r\na=rtpmap:0 PCMU/8000\r\na=sendonly\r\n",
			&BackchannelTrack{Control: base + "/audioback", PayloadType: 0, Encoding: "PCMU", ClockRate: 8000}},
		{"static payload, absolute control", "m=audio 0 RTP/AVP 0\na=control:rtsp://other/back\na=sendonly\n",
			&BackchannelTrack{Control: "rtsp://other/back", PayloadType: 0, Encoding: "PCMU", ClockRate: 8000}},
		{"prefers PCMU among several", "m=audio 0 RTP/AVP 8 97 0\na=rtpmap:97 MPEG4-GENERIC/16000\na=control:trackID=5\na=sendonly\n",
			&BackchannelTrack{Control: base + "/trackID=5", PayloadType: 0, Encoding: "PCMU", ClockRate: 8000}},
		{"PCMA only", "m=audio 0 RTP/AVP 8\na=control:back\na=sendonly\n",
			&BackchannelTrack{Control: base + "/back", PayloadType: 8, Encoding: "PCMA", ClockRate: 8000}},
		{"session control base", "a=control:rtsp://cam/agg/\nm=audio 0 RTP/AVP 0\na=control:b\na=sendonly\n",
			&BackchannelTrack{Control: "rtsp://cam/agg/b", PayloadType: 0, Encoding: "PCMU", ClockRate: 8000}},
		{"receive-only audio is not a backchannel", "m=audio 0 RTP/AVP 0\na=control:a\na=recvonly\n", nil},
		{"sendonly video is not a backchannel", "m=video 0 RTP/AVP 96\na=control:v\na=sendonly\n", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		got := ParseBackchannelSDP(c.sdp, base)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: got %+v, want none", c.name, got)
		case c.want != nil && (got == nil || *got != *c.want):
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	if (&BackchannelTrack{Encoding: "PCMA", ClockRate: 8000}).PCMU() || !(&BackchannelTrack{Encoding: "PCMU", ClockRate: 8000}).PCMU() {
		t.Fatal("PCMU() wrong")
	}
	var none *BackchannelTrack
	if none.PCMU() {
		t.Fatal("nil track is not PCMU")
	}
}

func startBackchannel(t *testing.T, b *fakecam.Backchannel) *fakecam.Backchannel {
	t.Helper()
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func TestProbeBackchannel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	b := startBackchannel(t, &fakecam.Backchannel{User: "admin", Pass: "pw"})
	if ok, err := ProbeBackchannel(ctx, b.URL(), "admin", "pw"); err != nil || !ok {
		t.Fatalf("digest-protected backchannel: ok=%v err=%v", ok, err)
	}
	if _, err := ProbeBackchannel(ctx, b.URL(), "admin", "wrong"); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong password: %v, want ErrAuth", err)
	}
	off := startBackchannel(t, &fakecam.Backchannel{Off: true})
	if ok, err := ProbeBackchannel(ctx, off.URL(), "", ""); err != nil || ok {
		t.Fatalf("video-only camera: ok=%v err=%v", ok, err)
	}
	refuse := startBackchannel(t, &fakecam.Backchannel{Refuse551: true})
	if ok, err := ProbeBackchannel(ctx, refuse.URL(), "", ""); err != nil || ok {
		t.Fatalf("551 camera: ok=%v err=%v", ok, err)
	}
	aac := startBackchannel(t, &fakecam.Backchannel{Encoding: "MPEG4-GENERIC/16000"})
	if ok, err := ProbeBackchannel(ctx, aac.URL(), "", ""); err != nil || ok {
		t.Fatalf("AAC-only backchannel must not count: ok=%v err=%v", ok, err)
	}
	if _, err := ProbeBackchannel(ctx, "http://nope", "", ""); err == nil {
		t.Fatal("non-rtsp URL accepted")
	}
}

func TestTalkbackSupportedViaONVIF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b := startBackchannel(t, &fakecam.Backchannel{User: "admin", Pass: "pw"})
	cam := &fakecam.Camera{User: "admin", Pass: "pw", Streams: []fakecam.Stream{
		{Token: "main", Name: "Main", Encoding: "H264", Width: 1920, Height: 1080, FPS: 20, RTSPURL: b.URL()},
	}}
	srv := httptest.NewServer(cam)
	defer srv.Close()

	// The stored address is wrong; GetStreamUri finds the right one.
	ok, uri, err := TalkbackSupported(ctx, srv.URL+"/onvif/device_service", "rtsp://127.0.0.1:1/none", "admin", "pw")
	if err != nil || !ok || uri != b.URL() {
		t.Fatalf("got ok=%v uri=%q err=%v, want %q", ok, uri, err, b.URL())
	}
	// No ONVIF: the stored address alone.
	if ok, uri, err := TalkbackSupported(ctx, "", b.URL(), "admin", "pw"); err != nil || !ok || uri != b.URL() {
		t.Fatalf("rtsp only: ok=%v uri=%q err=%v", ok, uri, err)
	}
	// A camera that answers without a backchannel is (false, nil).
	b.Off = true
	if ok, _, err := TalkbackSupported(ctx, srv.URL+"/onvif/device_service", "", "admin", "pw"); err != nil || ok {
		t.Fatalf("off: ok=%v err=%v", ok, err)
	}
	// Bad ONVIF credentials are an error, not "unsupported".
	if _, _, err := TalkbackSupported(ctx, srv.URL+"/onvif/device_service", "", "admin", "bad"); !errors.Is(err, ErrAuth) {
		t.Fatalf("bad creds: %v", err)
	}
	// Nothing reachable is an error.
	if _, _, err := TalkbackSupported(ctx, "", "rtsp://127.0.0.1:1/none", "", ""); err == nil {
		t.Fatal("unreachable camera reported no error")
	}
}

func TestRTSPSessionHeader(t *testing.T) {
	c := &RTSPConn{}
	h := map[string][]string{"Session": {"12345678;timeout=30"}}
	if d := c.SetSession(h); d != 30*time.Second || c.Session() != "12345678" {
		t.Fatalf("got %v %q", d, c.Session())
	}
	h = map[string][]string{"Session": {"abc"}}
	if d := c.SetSession(h); d != 60*time.Second || c.Session() != "abc" {
		t.Fatalf("default timeout: got %v %q", d, c.Session())
	}
}

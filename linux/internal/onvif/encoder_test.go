package onvif

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"sentinel-linux/internal/onvif/fakecam"
)

const encPass = "s3cret&<pw"

func encCam() *fakecam.Camera {
	return &fakecam.Camera{User: "admin", Pass: encPass, Manufacturer: "Acme", Model: "X100",
		Streams: []fakecam.Stream{
			{Token: "main", Name: "Main", Encoding: "H264", Width: 1920, Height: 1080, FPS: 25, Bitrate: 4096, GovLength: 50, Quality: 5, RTSPURL: "rtsp://10.0.0.5:554/stream1"},
			{Token: "sub", Name: "Sub", Encoding: "H264", Width: 640, Height: 360, FPS: 10, RTSPURL: "rtsp://10.0.0.5:554/stream2?a=1&b=2"},
		}}
}

func TestGetEncodersParsesConfigAndOptions(t *testing.T) {
	cam := encCam()
	cam.EncoderOptions = fakecam.EncoderOptions{FPSMax: 25, BitrateMax: 8192, GovMax: 100, QualityMax: 6}
	srv := httptest.NewServer(cam)
	defer srv.Close()

	// Host differs from the stored URLs on purpose: only path+query count.
	got, err := GetEncoders(context.Background(), srv.URL+"/onvif/device_service", "admin", encPass,
		"rtsp://192.168.1.9/stream1", "rtsp://192.168.1.9/stream2?a=1&b=2")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("streams = %+v", got)
	}
	m := got[0]
	if m.ConfigToken != "vmain" || m.ProfileToken != "main" || m.InUse != "main" || m.Encoding != "H264" ||
		m.Width != 1920 || m.Height != 1080 || m.FPS != 25 || m.Bitrate != 4096 || m.GovLength != 50 || m.Quality != 5 {
		t.Fatalf("main = %+v", m)
	}
	if got[1].InUse != "sub" {
		t.Fatalf("sub inUse = %q", got[1].InUse)
	}
	o := m.Options
	if len(o.Resolutions) != 3 || o.Resolutions[1] != (Resolution{1280, 720}) ||
		o.FPSMin != 1 || o.FPSMax != 25 || o.BitrateMin != 64 || o.BitrateMax != 8192 ||
		o.GovMin != 1 || o.GovMax != 100 || o.QualityMin != 1 || o.QualityMax != 6 {
		t.Fatalf("options = %+v", o)
	}
}

func TestGetEncodersMissingBitrateExtension(t *testing.T) {
	cam := encCam()
	cam.NoBitrateRange = true
	srv := httptest.NewServer(cam)
	defer srv.Close()
	got, err := GetEncoders(context.Background(), srv.URL+"/onvif/device_service", "admin", encPass, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if o := got[0].Options; o.BitrateMin != 0 || o.BitrateMax != 0 || o.FPSMax == 0 {
		t.Fatalf("options = %+v, want unbounded bitrate and bounded fps", o)
	}
	if got[0].InUse != "" {
		t.Fatalf("inUse = %q with no stored URLs", got[0].InUse)
	}
}

func TestSetEncoderEchoesFullConfiguration(t *testing.T) {
	cam := encCam()
	srv := httptest.NewServer(cam)
	defer srv.Close()
	ctx := context.Background()
	svc := srv.URL + "/onvif/device_service"

	err := SetEncoder(ctx, svc, "admin", encPass, EncoderConfig{ConfigToken: "vmain", Width: 1280, Height: 720, FPS: 15, Bitrate: 2048, GovLength: 30, Quality: 4})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := cam.Stream("main")
	if st.Width != 1280 || st.Height != 720 || st.FPS != 15 || st.Bitrate != 2048 || st.GovLength != 30 || st.Quality != 4 {
		t.Fatalf("camera now %+v", st)
	}
	sets := cam.EncoderSets()
	if len(sets) != 1 {
		t.Fatalf("sets = %d", len(sets))
	}
	for _, want := range []string{
		`token="vmain"`, `GuaranteedFrameRate="false"`, "<tt:Encoding>H264</tt:Encoding>", "<tt:H264Profile>Main</tt:H264Profile>",
		"<tt:SessionTimeout>PT60S</tt:SessionTimeout>", "<tt:IPv4Address>0.0.0.0</tt:IPv4Address>", "<tt:UseCount>1</tt:UseCount>",
		"<trt:ForcePersistence>true</trt:ForcePersistence>",
	} {
		if !strings.Contains(sets[0], want) {
			t.Errorf("set request lacks %s:\n%s", want, sets[0])
		}
	}
	// Zero fields keep the camera's values.
	if err := SetEncoder(ctx, svc, "admin", encPass, EncoderConfig{ConfigToken: "vmain", FPS: 12}); err != nil {
		t.Fatal(err)
	}
	st, _ = cam.Stream("main")
	if st.Width != 1280 || st.FPS != 12 || st.Bitrate != 2048 || st.GovLength != 30 {
		t.Fatalf("partial set clobbered fields: %+v", st)
	}
	// The sub stream was never touched.
	if sub, _ := cam.Stream("sub"); sub.Width != 640 || sub.FPS != 10 {
		t.Fatalf("sub changed: %+v", sub)
	}
}

func TestSetEncoderCameraErrorAndAuth(t *testing.T) {
	cam := encCam()
	srv := httptest.NewServer(cam)
	defer srv.Close()
	ctx := context.Background()
	svc := srv.URL + "/onvif/device_service"

	err := SetEncoder(ctx, svc, "admin", encPass, EncoderConfig{ConfigToken: "vmain", Width: 123, Height: 45})
	if err == nil || !strings.Contains(err.Error(), "resolution 123x45 is not supported") {
		t.Fatalf("camera refusal = %v", err)
	}
	if err := SetEncoder(ctx, svc, "admin", "wrong", EncoderConfig{ConfigToken: "vmain", FPS: 5}); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong password: %v, want ErrAuth", err)
	}
	if _, err := GetEncoders(ctx, svc, "admin", "wrong", "", ""); !errors.Is(err, ErrAuth) {
		t.Fatalf("GetEncoders wrong password: %v, want ErrAuth", err)
	}
	if err := SetEncoder(ctx, svc, "admin", encPass, EncoderConfig{ConfigToken: "vnope", FPS: 5}); err == nil {
		t.Fatal("unknown token accepted")
	}
}

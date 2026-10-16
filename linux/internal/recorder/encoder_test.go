package recorder

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/onvif/fakecam"
)

func TestEncoderCommands(t *testing.T) {
	r, _ := newTestRecorder(t)
	cam := &fakecam.Camera{User: "admin", Pass: "p@ss", Streams: []fakecam.Stream{
		{Token: "main", Name: "Main", Encoding: "H264", Width: 1920, Height: 1080, FPS: 25, RTSPURL: "rtsp://10.0.0.9/s"},
	}}
	srv := httptest.NewServer(cam)
	defer srv.Close()
	svc := srv.URL + "/onvif/device_service"
	other := "0F1B3D6A-0000-4000-8000-000000000001"
	if _, err := r.ApplyConfig(context.Background(), link.Snapshot{Rev: 2, Cameras: []link.CameraConfig{
		{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", Username: "admin", Password: "p@ss", ONVIFURL: svc},
		{ID: other, Name: "NoOnvif", RTSPURL: "rtsp://10.0.0.8/s"},
	}}); err != nil {
		t.Fatal(err)
	}

	res := command(t, r, link.CmdEncoderGet, link.EncoderGetArgs{CameraID: camID})
	var got link.EncoderGetResult
	if err := res.Decode(&got); err != nil || got.Error != "" {
		t.Fatalf("get: %v %+v", err, got)
	}
	var streams []onvif.EncoderStream
	if err := json.Unmarshal(got.Streams, &streams); err != nil || len(streams) != 1 || streams[0].InUse != "main" || streams[0].Width != 1920 {
		t.Fatalf("streams = %+v err=%v", streams, err)
	}

	res = command(t, r, link.CmdEncoderSet, link.EncoderSetArgs{CameraID: camID, ConfigToken: "vmain", Width: 1280, Height: 720, FPS: 10})
	var set link.EncoderSetResult
	if err := res.Decode(&set); err != nil || set.Error != "" {
		t.Fatalf("set: %v %+v", err, set)
	}
	if st, _ := cam.Stream("main"); st.Width != 1280 || st.FPS != 10 {
		t.Fatalf("camera = %+v", st)
	}

	// The camera's own refusal is an ok result carrying its text.
	res = command(t, r, link.CmdEncoderSet, link.EncoderSetArgs{CameraID: camID, ConfigToken: "vmain", Width: 7, Height: 7})
	if err := res.Decode(&set); err != nil || !strings.Contains(set.Error, "not supported") {
		t.Fatalf("refusal: %v %+v", err, set)
	}

	// Command errors: unknown camera, no ONVIF, bad id, missing token.
	for _, c := range []struct{ id, token, code string }{
		{"0F1B3D6A-0000-4000-8000-0000000000FF", "vmain", link.CodeNotFound},
		{other, "vmain", link.CodeBadRequest},
		{"../x", "vmain", link.CodeBadRequest},
		{camID, "", link.CodeBadRequest},
	} {
		if res := command(t, r, link.CmdEncoderSet, link.EncoderSetArgs{CameraID: c.id, ConfigToken: c.token, FPS: 5}); res.Code != c.code {
			t.Errorf("set %q/%q: code %q, want %q", c.id, c.token, res.Code, c.code)
		}
		if c.token == "" {
			continue
		}
		if res := command(t, r, link.CmdEncoderGet, link.EncoderGetArgs{CameraID: c.id}); res.Code != c.code {
			t.Errorf("get %q: code %q, want %q", c.id, res.Code, c.code)
		}
	}
}

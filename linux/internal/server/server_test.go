package server

import (
	"context"
	"testing"

	"sentinel-linux/internal/store"
)

func TestNormalizeCameraMovesCredentialsOutOfURL(t *testing.T) {
	in := store.CameraInput{Name: " Dock ", RTSPURL: "rtsp://admin:s3cret@10.0.0.9:554/stream1"}
	if err := normalizeCamera(&in); err != nil {
		t.Fatal(err)
	}
	if in.RTSPURL != "rtsp://10.0.0.9:554/stream1" || in.Username != "admin" || in.Password == nil || *in.Password != "s3cret" || in.Name != "Dock" {
		t.Fatalf("got %+v", in)
	}
}

func TestNormalizeCameraRejectsBadInput(t *testing.T) {
	for _, in := range []store.CameraInput{
		{Name: "", RTSPURL: "rtsp://10.0.0.9/s"},
		{Name: "x", RTSPURL: "http://10.0.0.9/s"},
		{Name: "x", RTSPURL: "rtsp:///nohost"},
		{Name: "x", RTSPURL: "rtsp://10.0.0.9/s", RetentionDays: -1},
	} {
		in := in
		if err := normalizeCamera(&in); err == nil {
			t.Errorf("expected error for %+v", in)
		}
	}
}

func TestCameraHostsMarksDiscoveredCamerasAdded(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.SaveCamera(ctx, "", store.CameraInput{Name: "Door", RTSPURL: "rtsp://192.168.2.221:554/stream1", SubRTSPURL: "rtsp://192.168.2.222:554/stream2"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	used := s.cameraHosts(ctx)
	if !used["192.168.2.221"] || !used["192.168.2.222"] || used["192.168.2.85"] {
		t.Fatalf("hosts = %v", used)
	}
}

package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/onvif/fakecam"
	"sentinel-linux/internal/store"
)

func TestCameraAnalyticsSubscribesAndRaisesAlarms(t *testing.T) {
	cam := &fakecam.Camera{User: "admin", Pass: "pw", Manufacturer: "Acme", Model: "M1"}
	srv := httptest.NewServer(cam)
	defer srv.Close()

	s := newCompanionServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Ctx = ctx
	pw, onvifURL := "pw", srv.URL+"/onvif/device_service"
	c, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Gate", RTSPURL: "rtsp://10.0.0.9/s", Username: "admin", Password: &pw, ONVIFURL: &onvifURL})
	if err != nil {
		t.Fatal(err)
	}
	if !c.CameraAnalytics {
		t.Fatal("camera analytics should default on")
	}
	cams, _ := s.Store.Cameras(ctx)
	_ = s.ApplyMedia(ctx)
	waitUntil(t, func() bool { return recOf(s).AnalyticsStatus(c.ID) == "Connected" })

	cam.Emit("tns1:RuleEngine/LineDetector/Crossed", "", map[string]string{"Rule": "Driveway"}, map[string]string{"ClassTypes": "Human"})
	waitUntil(t, func() bool { a, _ := s.Store.Alarms(ctx, true, 10); return len(a) == 1 })
	alarms, _ := s.Store.Alarms(ctx, true, 10)
	if a := alarms[0]; a.Kind != "Line crossing" || a.Severity != "Critical" || a.CameraID != c.ID {
		t.Fatalf("alarm %+v", a)
	}
	events, _ := s.Store.Events(ctx, c.ID, 10)
	if len(events) != 1 || events[0].Label != "person" || events[0].AlarmID == "" {
		t.Fatalf("events %+v", events)
	}

	// Turning camera analytics off stops the subscription.
	off := false
	if _, err := s.Store.SaveCamera(ctx, c.ID, store.CameraInput{Name: "Gate", RTSPURL: "rtsp://10.0.0.9/s", Username: "admin", CameraAnalytics: &off}); err != nil {
		t.Fatal(err)
	}
	cams, _ = s.Store.Cameras(ctx)
	if cams[0].ONVIFURL != onvifURL {
		t.Fatalf("an edit without onvifURL must keep it, got %q", cams[0].ONVIFURL)
	}
	_ = s.ApplyMedia(ctx)
	if st := recOf(s).AnalyticsStatus(c.ID); st != "" {
		t.Fatalf("still subscribed: %q", st)
	}
}

func TestCameraEventAlarmFollowsAlertMode(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	people, motion := alertPeople, alertMotion
	lot, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Lot", RTSPURL: "rtsp://10.0.0.9/s", AlertOn: &people})
	hall, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Hall", RTSPURL: "rtsp://10.0.0.8/s", AlertOn: &motion})
	auto, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.7/s"})
	t0 := time.Now()

	syncCams(t, s)
	recOf(s).OnCameraEvent(lot.ID, onvif.AnalyticsEvent{Kind: "Vehicle", Label: "car", Active: true, At: t0})
	recOf(s).OnCameraEvent(lot.ID, onvif.AnalyticsEvent{Kind: "Intrusion", Rule: "Fence", Active: true, At: t0})
	recOf(s).OnCameraEvent(hall.ID, onvif.AnalyticsEvent{Kind: "Person", Label: "person", Active: true, At: t0})
	// "Auto" alarms on people even with no detector installed here: the camera is the detector.
	recOf(s).OnCameraEvent(auto.ID, onvif.AnalyticsEvent{Kind: "Person", Label: "person", Active: true, At: t0})
	// Ignored: an ending state, and camera motion.
	recOf(s).OnCameraEvent(auto.ID, onvif.AnalyticsEvent{Kind: "Intrusion", Active: false, At: t0})
	recOf(s).OnCameraEvent(auto.ID, onvif.AnalyticsEvent{Kind: "Motion", Active: true, At: t0})

	alarms, _ := s.Store.Alarms(ctx, true, 10)
	got := map[string]string{}
	for _, a := range alarms {
		got[a.CameraName] += a.Kind + ";"
	}
	if got["Lot"] != "Intrusion;" || got["Hall"] != "" || got["Door"] != "Person;" {
		t.Fatalf("alarms by camera %v", got)
	}
	events, _ := s.Store.Events(ctx, "", 20)
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4 (vehicle, intrusion, two people)", len(events))
	}
}

func TestCameraAndBuiltInDetectionShareEpisodes(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	c, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Porch", RTSPURL: "rtsp://10.0.0.9/s"})
	t0 := time.Now()
	syncCams(t, s)
	recOf(s).OnObjects(detect.Detections{CameraID: c.ID, Objects: []detect.Object{{Kind: "Person", Label: "person", Score: 0.8, Box: [4]float64{0.1, 0.1, 0.2, 0.5}}}, At: t0})
	recOf(s).OnCameraEvent(c.ID, onvif.AnalyticsEvent{Kind: "Person", Label: "person", Active: true, At: t0.Add(2 * time.Second)})
	if events, _ := s.Store.Events(ctx, "", 10); len(events) != 1 {
		t.Fatalf("got %d events for one person seen by both detectors", len(events))
	}
}

func TestNormalizeCameraONVIFAddress(t *testing.T) {
	for raw, ok := range map[string]bool{
		" http://10.0.0.9/onvif/device_service ": true,
		"":                                       true,
		"rtsp://10.0.0.9/":                       false,
		"http://admin:pw@10.0.0.9/onvif":         false,
		"http:///onvif":                          false,
	} {
		v := raw
		in := store.CameraInput{Name: "x", RTSPURL: "rtsp://10.0.0.9/s", ONVIFURL: &v}
		if err := normalizeCamera(&in); (err == nil) != ok {
			t.Errorf("%q: err = %v", raw, err)
		}
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

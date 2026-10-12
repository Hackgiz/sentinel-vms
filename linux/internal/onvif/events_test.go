package onvif

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestWatchEventsDeliversCameraAnalytics(t *testing.T) {
	for _, axisStyle := range []bool{false, true} {
		cam := newCam()
		cam.ClockSkew = 2 * time.Hour
		cam.RequireSubscriptionID = axisStyle
		srv := httptest.NewServer(cam)

		ctx, cancel := context.WithCancel(context.Background())
		var mu sync.Mutex
		var got []AnalyticsEvent
		var statuses []string
		done := make(chan struct{})
		go func() {
			defer close(done)
			WatchEvents(ctx, srv.URL+"/onvif/device_service", "admin", "s3cret&<pw",
				func(e AnalyticsEvent) { mu.Lock(); got = append(got, e); mu.Unlock() },
				func(s string) { mu.Lock(); statuses = append(statuses, s); mu.Unlock() })
		}()

		waitFor(t, func() bool { s, _ := cam.Stats(); return s > 0 })
		// State replayed on subscribe must not count as a new detection.
		cam.Emit("tns1:RuleEngine/FieldDetector/ObjectsInside", "Initialized", map[string]string{"Rule": "Yard"}, map[string]string{"IsInside": "true"})
		cam.Emit("tns1:RuleEngine/MyRuleDetector/PeopleDetect", "Changed", map[string]string{"Rule": "Door"}, map[string]string{"State": "true"})
		cam.Emit("tns1:RuleEngine/LineDetector/Crossed", "", map[string]string{"Rule": "Gate"}, map[string]string{"ObjectId": "15"})
		cam.Emit("tns1:Device/Trigger/DigitalInput", "Changed", nil, map[string]string{"LogicalState": "true"})

		waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) >= 2 })
		cancel()
		<-done
		srv.Close()

		if len(got) != 2 {
			t.Fatalf("axis=%v: got %+v, want person + line crossing only", axisStyle, got)
		}
		if got[0].Kind != "Person" || !got[0].Active || got[0].Rule != "Door" {
			t.Errorf("axis=%v: first = %+v", axisStyle, got[0])
		}
		if got[1].Kind != "Line crossing" || !got[1].Active || got[1].Rule != "Gate" {
			t.Errorf("axis=%v: second = %+v", axisStyle, got[1])
		}
		if len(statuses) == 0 || statuses[0] != "Connected" {
			t.Errorf("axis=%v: statuses %v", axisStyle, statuses)
		}
	}
}

func TestWatchEventsWrongPassword(t *testing.T) {
	srv := httptest.NewServer(newCam())
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	status := make(chan string, 4)
	go WatchEvents(ctx, srv.URL+"/onvif/device_service", "admin", "nope", func(AnalyticsEvent) {}, func(s string) { status <- s })
	select {
	case s := <-status:
		if s != "Camera rejected the login for analytics events" {
			t.Fatalf("status %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no status")
	}
	cancel()
}

func TestClassifyVendorTopics(t *testing.T) {
	cases := []struct {
		topic  string
		data   map[string]string
		kind   string
		label  string
		active bool
	}{
		{"tns1:RuleEngine/CellMotionDetector/Motion", map[string]string{"IsMotion": "true"}, "Motion", "", true},
		{"tns1:RuleEngine/CellMotionDetector/Motion", map[string]string{"IsMotion": "false"}, "Motion", "", false},
		{"tns1:VideoSource/MotionAlarm", map[string]string{"State": "1"}, "Motion", "", true},
		{"tns1:RuleEngine/FieldDetector/ObjectsInside", map[string]string{"IsInside": "true", "ClassTypes": "Human"}, "Intrusion", "person", true},
		{"tns1:RuleEngine/LineDetector/Crossed", map[string]string{"ObjectId": "1", "ClassTypes": "Vehicle"}, "Line crossing", "vehicle", true},
		{"tns1:RuleEngine/ObjectDetection/Object", map[string]string{"ObjectType": "Car", "Likelihood": "87"}, "Vehicle", "car", true},
		{"tns1:RuleEngine/HumanDetector/Detect", map[string]string{"IsHuman": "true"}, "Person", "person", true},
		{"tns1:RuleEngine/Loitering/Alert", map[string]string{"State": "true"}, "Loitering", "", true},
		{"tns1:VideoSource/GlobalSceneChange/ImagingService", map[string]string{"State": "true"}, "Tamper", "", true},
		{"tns1:RuleEngine/FaceDetector/Face", nil, "Face", "", true},
		{"tns1:RuleEngine/LicensePlateDetector/Plate", map[string]string{"PlateNumber": "ABC123"}, "License plate", "", true},
	}
	for _, c := range cases {
		ev, ok := classify(notification{topic: cleanTopic(c.topic), op: "Changed", source: map[string]string{}, data: c.data})
		if !ok || ev.Kind != c.kind || ev.Label != c.label || ev.Active != c.active {
			t.Errorf("%s %v → %+v ok=%v; want %s/%q active=%v", c.topic, c.data, ev, ok, c.kind, c.label, c.active)
		}
	}
	for _, topic := range []string{"tns1:Device/Trigger/DigitalInput", "tns1:Monitoring/ProcessorUsage", "tns1:Media/ConfigurationChanged"} {
		if ev, ok := classify(notification{topic: cleanTopic(topic), data: map[string]string{"State": "true"}}); ok {
			t.Errorf("%s classified as %+v; want ignored", topic, ev)
		}
	}
	if ev, _ := classify(notification{topic: "RuleEngine/ObjectDetection/Object", data: map[string]string{"ObjectType": "Human", "Likelihood": "0.92"}}); ev.Score != 0.92 {
		t.Errorf("likelihood not parsed: %+v", ev)
	}
}

func TestParseProfileMObjectClass(t *testing.T) {
	msg := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tev:PullMessagesResponse xmlns:tev="x" xmlns:wsnt="y" xmlns:tt="z"><wsnt:NotificationMessage><wsnt:Topic>tns1:RuleEngine/ObjectDetection/Object</wsnt:Topic><wsnt:Message><tt:Message UtcTime="2026-10-09T10:00:00Z" PropertyOperation="Changed"><tt:Source><tt:SimpleItem Name="Rule" Value="Front"/></tt:Source><tt:Data><tt:ElementItem Name="Object"><tt:Object ObjectId="3"><tt:Appearance><tt:Class><tt:Type Likelihood="0.8">Human</tt:Type></tt:Class></tt:Appearance></tt:Object></tt:ElementItem></tt:Data></tt:Message></wsnt:Message></wsnt:NotificationMessage></tev:PullMessagesResponse></s:Body></s:Envelope>`
	ns := parseNotifications(parseXML([]byte(msg)))
	if len(ns) != 1 {
		t.Fatalf("got %d notifications", len(ns))
	}
	ev, ok := classify(ns[0])
	if !ok || ev.Kind != "Person" || ev.Score != 0.8 || ev.Rule != "Front" {
		t.Fatalf("%+v ok=%v", ev, ok)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

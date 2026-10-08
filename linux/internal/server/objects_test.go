package server

import (
	"context"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/store"
)

func TestObjectsModeAlarmsAndParkedCars(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	mode := alertObjects
	c, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Drive", RTSPURL: "rtsp://10.0.0.9/s", Recording: true, AlertOn: &mode})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	parked := detect.Object{Kind: "Vehicle", Label: "car", Score: 0.9, Box: [4]float64{0.1, 0.5, 0.4, 0.8}}
	person := detect.Object{Kind: "Person", Label: "person", Score: 0.8, Box: [4]float64{0.6, 0.3, 0.7, 0.9}}

	s.OnObjects(detect.Detections{CameraID: c.ID, Objects: []detect.Object{parked}, At: t0})
	// 5 minutes later someone walks past the same parked car.
	s.OnObjects(detect.Detections{CameraID: c.ID, Objects: []detect.Object{parked, person}, At: t0.Add(5 * time.Minute)})
	// A different car pulls in elsewhere.
	moved := detect.Object{Kind: "Vehicle", Label: "truck", Score: 0.85, Box: [4]float64{0.55, 0.4, 0.95, 0.85}}
	s.OnObjects(detect.Detections{CameraID: c.ID, Objects: []detect.Object{parked, moved}, At: t0.Add(10 * time.Minute)})

	events, _ := s.Store.Events(ctx, "", 50)
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	if kinds["Vehicle"] != 2 || kinds["Person"] != 1 {
		t.Fatalf("events by kind %v; want 2 vehicles (first sighting + the truck) and 1 person — the parked car must not repeat", kinds)
	}
	alarms, _ := s.Store.Alarms(ctx, true, 10)
	sev := map[string]string{}
	for _, a := range alarms {
		sev[a.Kind] = a.Severity
	}
	if sev["Person"] != "Critical" || sev["Vehicle"] != "Warning" {
		t.Fatalf("alarm severities %v", sev)
	}

	// Motion alone in this mode: event yes, alarm no.
	before := len(alarms)
	s.OnMotion(detect.Motion{CameraID: c.ID, Score: 0.2, At: t0.Add(20 * time.Minute)})
	if after, _ := s.Store.Alarms(ctx, true, 10); len(after) != before {
		t.Fatal("motion raised an alarm in people & vehicles mode")
	}
}

func TestPeopleOnlyModeIgnoresVehicles(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	mode := alertPeople
	c, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Lot", RTSPURL: "rtsp://10.0.0.9/s", AlertOn: &mode})
	s.OnObjects(detect.Detections{CameraID: c.ID, At: time.Now(), Objects: []detect.Object{{Kind: "Vehicle", Label: "car", Score: 0.9, Box: [4]float64{0, 0, 0.3, 0.3}}}})
	if a, _ := s.Store.Alarms(ctx, true, 10); len(a) != 0 {
		t.Fatalf("people-only camera raised %d alarms for a vehicle", len(a))
	}
}

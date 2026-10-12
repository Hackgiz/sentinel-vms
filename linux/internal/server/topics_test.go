package server

import (
	"context"
	"testing"
	"time"

	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/store"
)

// The detection and alarm code publishes on the bus, in pipeline order, and
// every new alarm (whoever raised it) is announced once.
func TestBusCarriesTheEventPipeline(t *testing.T) {
	s := newCompanionServer(t)
	c := addCam(t, s)
	sub := s.Bus.Subscribe("test", 0)
	var got []string
	bus.Handle(sub, TopicCameraActivity, func(m CameraActivity) { got = append(got, "activity:"+m.Kind) })
	bus.Handle(sub, TopicEventNew, func(m EventMessage) {
		if m.Camera == nil || m.Camera.ID != c.ID {
			t.Errorf("event without its camera: %+v", m)
		}
		got = append(got, "event.new:"+m.Event.Kind)
	})
	bus.Handle(sub, TopicAlarmNew, func(a store.Alarm) { got = append(got, "alarm.new:"+a.Kind) })
	bus.Handle(sub, TopicAlarmState, func(a store.Alarm) { got = append(got, "alarm.state:"+a.State) })

	syncCams(t, s)
	recOf(s).OnMotion(detect.Motion{CameraID: c.ID, Score: 0.2, At: time.Now()})
	sub.Drain()
	want := []string{"activity:motion", "event.new:Motion", "alarm.new:Motion"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	// A storage alarm is a new alarm too (MQTT skips it, push does not).
	got = nil
	ctx := context.Background()
	if _, _, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{Kind: "Storage", Title: "Storage almost full", Severity: "Critical", At: time.Now(), Origin: "test"}); err != nil {
		t.Fatal(err)
	}
	alarms, _ := s.Store.Alarms(ctx, true, 10)
	admin := signIn(t, s, "admin", "Admin")
	if rec := doWeb(t, s.Handler(), admin, "POST", "/api/v1/alarms/"+alarms[0].ID+"/state", `{"state":"Resolved"}`); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	sub.Drain()
	if len(got) != 2 || got[0] != "alarm.new:Storage" || got[1] != "alarm.state:Resolved" {
		t.Fatalf("got %v", got)
	}
}

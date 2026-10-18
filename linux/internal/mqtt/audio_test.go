package mqtt

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// WAVE9: the per-camera audio binary sensor — Home Assistant discovery,
// ON at a touch, OFF after the quiet gap, retained, removed with the camera;
// audio events ride the existing event stream.
func TestAudioSensor(t *testing.T) {
	host, port := startBroker(t, nil)
	rec := newRecorder(t, host, port)
	b := newBridge(t, host, port, nil)
	node := nodeID(camID)
	topic := "sentinel/" + camID + "/audio"

	g := rec.wait(t, "homeassistant/binary_sensor/"+node+"/audio/config", nonEmpty)
	var cfg map[string]any
	if err := json.Unmarshal(g.payload, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["state_topic"] != topic || cfg["device_class"] != "sound" || cfg["unique_id"] != node+"_audio" ||
		cfg["payload_on"] != "ON" || cfg["payload_off"] != "OFF" || cfg["availability_topic"] != "sentinel/available" {
		t.Fatalf("audio config = %s", g.payload)
	}
	rec.wait(t, topic, equals("OFF"))

	start := time.Now()
	b.Touch(camID, "audio")
	rec.wait(t, topic, equals("ON"))
	if p, ok := retained(t, host, port, topic); !ok || string(p) != "ON" {
		t.Errorf("audio state must be retained, got %q", p)
	}
	off := rec.wait(t, topic, equals("OFF"))
	if d := off.at.Sub(start); d < 280*time.Millisecond {
		t.Fatalf("audio turned off after %v; want at least the 300ms timeout", d)
	}

	b.Event(Event{Type: "new", ID: "AU1", CameraID: camID, Camera: "Front Door", Kind: "audio", Label: "Glass break", Score: 0.8, Time: 1760000000})
	ev := rec.wait(t, "sentinel/events", func(p []byte) bool { return bytes.Contains(p, []byte(`"AU1"`)) })
	var e Event
	if err := json.Unmarshal(ev.payload, &e); err != nil || e.Kind != "audio" || e.Label != "Glass break" || ev.retained {
		t.Fatalf("audio event = %s (retained %v)", ev.payload, ev.retained)
	}

	b.SyncCameras(nil)
	rec.wait(t, "homeassistant/binary_sensor/"+node+"/audio/config", func(p []byte) bool { return len(p) == 0 })
	rec.wait(t, topic, func(p []byte) bool { return len(p) == 0 })
}

func TestDefaultOffAfterIs30s(t *testing.T) {
	if DefaultOffAfter != 30*time.Second || (&Bridge{}).offAfter() != 30*time.Second {
		t.Fatalf("default off-timeout %v", DefaultOffAfter)
	}
}

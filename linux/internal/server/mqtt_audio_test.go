package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/audiodetect"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/store"
)

// WAVE9: an audio detection reaches MQTT as an event on sentinel/events and
// turns the camera's audio binary sensor ON (motion's sensor stays OFF).
func TestMQTTAudioEventAndSensor(t *testing.T) {
	host, port := testBroker(t)
	watch := watchBroker(t, host, port)
	s := newCompanionServer(t)
	dir := t.TempDir()
	recOf(s).Media = &media.Supervisor{ConfigPath: filepath.Join(dir, "mediamtx.yml"), RecordingsRoot: filepath.Join(dir, "rec"), Ports: media.DefaultPorts}
	ctx := context.Background()
	cam, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Porch", RTSPURL: "rtsp://10.0.0.9/s"})
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if _, _, err := s.Store.SetCameraAudio(ctx, cam.ID, &yes, nil); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"enabled": true, "host": host, "port": port})
	_ = s.Store.SetSetting(ctx, mqttSetting, string(cfg))
	if err := s.ApplyMedia(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.mqttBridge().Close)
	node := "sentinel_" + strings.ToLower(strings.ReplaceAll(cam.ID, "-", ""))
	watch.wait(t, "homeassistant/binary_sensor/"+node+"/audio/config", func(v string) bool { return strings.Contains(v, `"device_class":"sound"`) })
	watch.wait(t, "sentinel/"+cam.ID+"/audio", func(v string) bool { return v == "OFF" })

	recOf(s).OnAudio(audiodetect.Event{CameraID: cam.ID, Kind: "audio", Label: "Glass break", Score: 0.9, At: time.Now()})
	watch.wait(t, "sentinel/"+cam.ID+"/audio", func(v string) bool { return v == "ON" })
	ev := watch.wait(t, "sentinel/events", func(v string) bool { return strings.Contains(v, `"kind":"audio"`) })
	if !strings.Contains(ev, `"label":"Glass break"`) || !strings.Contains(ev, `"camera":"Porch"`) || !strings.Contains(ev, `"type":"new"`) {
		t.Fatalf("event = %s", ev)
	}
	watch.mu.Lock()
	motion := watch.last["sentinel/"+cam.ID+"/motion"]
	watch.mu.Unlock()
	if motion == "ON" {
		t.Fatal("an audio event turned the motion sensor on")
	}
}

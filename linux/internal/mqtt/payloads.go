package mqtt

import (
	"encoding/json"
	"time"
)

// Camera is what the bridge needs to know about one camera.
type Camera struct {
	ID        string
	Name      string
	Recording bool
	Detection bool // motion detection on (sensitivity > off)
}

// Event is published to <prefix>/events and, when it is the camera's newest,
// retained on <prefix>/<camera>/last_event.
type Event struct {
	Type        string  `json:"type"` // new | update
	ID          string  `json:"id"`
	CameraID    string  `json:"cameraId"`
	Camera      string  `json:"camera"`
	Kind        string  `json:"kind"` // Motion | Person | Vehicle | Animal | Line crossing | …
	Label       string  `json:"label,omitempty"`
	Score       float64 `json:"score"`
	Time        int64   `json:"time"` // unix seconds
	TimeISO     string  `json:"timeISO"`
	AlarmID     string  `json:"alarmId,omitempty"`
	Description string  `json:"description,omitempty"` // AI scene description, when AI ran
	Threat      string  `json:"threat,omitempty"`
	Snapshot    string  `json:"snapshot,omitempty"` // dashboard path of the JPEG (needs a signed-in session)
}

// Alarm is published to <prefix>/alarms whenever one is raised or changes state.
type Alarm struct {
	Type        string `json:"type"` // new | state
	ID          string `json:"id"`
	CameraID    string `json:"cameraId,omitempty"`
	Camera      string `json:"camera,omitempty"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Detail      string `json:"detail"`
	Severity    string `json:"severity"`
	State       string `json:"state"`
	Owner       string `json:"owner"`
	CreatedAt   int64  `json:"createdAt"`
	LastEventAt int64  `json:"lastEventAt"`
	EventCount  int    `json:"eventCount"`
}

// BinaryKinds are the per-camera binary_sensors driven by Touch.
var BinaryKinds = []string{"motion", "person", "vehicle"}

func isBinaryKind(k string) bool {
	for _, b := range BinaryKinds {
		if b == k {
			return true
		}
	}
	return false
}

func onOff(b bool) []byte {
	if b {
		return []byte("ON")
	}
	return []byte("OFF")
}

func isoTime(unix int64) string { return time.Unix(unix, 0).UTC().Format(time.RFC3339) }

// MARK: - Home Assistant discovery

type entity struct {
	component, object string
	config            map[string]any // nil = remove (empty retained payload)
}

// discovery returns the Home Assistant config messages for one camera.
// Switches are published only when commands are allowed; otherwise their
// config topics get an empty payload so Home Assistant removes them.
func (c Config) discovery(cam Camera, version string) []message {
	node := nodeID(cam.ID)
	device := map[string]any{
		"identifiers":  []string{node},
		"name":         cam.Name,
		"manufacturer": "Sentinel VMS",
		"model":        "Camera",
	}
	if version != "" {
		device["sw_version"] = version
	}
	base := func(object, name string) map[string]any {
		return map[string]any{
			"name":                  name,
			"unique_id":             node + "_" + object,
			"availability_topic":    c.AvailabilityTopic(),
			"payload_available":     "online",
			"payload_not_available": "offline",
			"device":                device,
			"origin":                map[string]any{"name": "Sentinel VMS", "sw": version, "url": "https://sentvms.com"},
		}
	}
	with := func(m map[string]any, kv ...any) map[string]any {
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	ents := []entity{
		{"binary_sensor", "motion", with(base("motion", "Motion"),
			"state_topic", c.CameraTopic(cam.ID, "motion"), "device_class", "motion", "payload_on", "ON", "payload_off", "OFF")},
		{"binary_sensor", "person", with(base("person", "Person"),
			"state_topic", c.CameraTopic(cam.ID, "person"), "device_class", "occupancy", "icon", "mdi:account", "payload_on", "ON", "payload_off", "OFF")},
		{"binary_sensor", "vehicle", with(base("vehicle", "Vehicle"),
			"state_topic", c.CameraTopic(cam.ID, "vehicle"), "icon", "mdi:car", "payload_on", "ON", "payload_off", "OFF")},
		{"binary_sensor", "online", with(base("online", "Online"),
			"state_topic", c.CameraTopic(cam.ID, "online"), "device_class", "connectivity", "entity_category", "diagnostic", "payload_on", "ON", "payload_off", "OFF")},
		{"sensor", "last_event", with(base("last_event", "Last event"),
			"state_topic", c.CameraTopic(cam.ID, "last_event"), "value_template", "{{ value_json.kind }}",
			"json_attributes_topic", c.CameraTopic(cam.ID, "last_event"), "icon", "mdi:cctv")},
		{"image", "snapshot", with(base("snapshot", "Last event snapshot"),
			"image_topic", c.CameraTopic(cam.ID, "snapshot"), "content_type", "image/jpeg")},
	}
	if c.AllowCommands {
		ents = append(ents,
			entity{"switch", "recording", with(base("recording", "Recording"),
				"state_topic", c.CameraTopic(cam.ID, "recording", "state"), "command_topic", c.CameraTopic(cam.ID, "recording", "set"),
				"payload_on", "ON", "payload_off", "OFF", "icon", "mdi:record-rec", "entity_category", "config")},
			entity{"switch", "detection", with(base("detection", "Detection"),
				"state_topic", c.CameraTopic(cam.ID, "detection", "state"), "command_topic", c.CameraTopic(cam.ID, "detection", "set"),
				"payload_on", "ON", "payload_off", "OFF", "icon", "mdi:motion-sensor", "entity_category", "config")})
	} else {
		ents = append(ents, entity{"switch", "recording", nil}, entity{"switch", "detection", nil})
	}
	out := make([]message, 0, len(ents))
	for _, e := range ents {
		var payload []byte
		if e.config != nil {
			payload, _ = json.Marshal(e.config)
		}
		out = append(out, message{topic: c.discoveryTopic(e.component, cam.ID, e.object), payload: payload, retained: true})
	}
	return out
}

// removal clears every retained topic Sentinel published for a camera.
func (c Config) removal(cam Camera) []message {
	var out []message
	for _, m := range c.discovery(Camera{ID: cam.ID, Name: cam.Name}, "") {
		out = append(out, message{topic: m.topic, retained: true})
	}
	for _, t := range []string{"motion", "person", "vehicle", "online", "last_event", "snapshot", "recording/state", "detection/state"} {
		out = append(out, message{topic: c.CameraTopic(cam.ID, t), retained: true})
	}
	return out
}

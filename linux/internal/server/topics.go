package server

import (
	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/store"
)

// Event bus topics. Everything that reacts to what happens on the site (MQTT,
// iPhone push, and in API v2 the WebSocket event stream) subscribes here
// instead of being called from the detection/alarm code. Publishing never
// blocks: see package bus.
var (
	// TopicEventNew: a detection event was stored.
	TopicEventNew = bus.NewTopic[EventMessage]("event.new")
	// TopicEventUpdated: an event got its snapshot or AI description.
	TopicEventUpdated = bus.NewTopic[EventMessage]("event.updated")
	// TopicAlarmNew: an alarm was opened (not a merge into an open one).
	// Published by the store hook after commit, for every alarm kind.
	TopicAlarmNew = bus.NewTopic[store.Alarm]("alarm.new")
	// TopicAlarmState: an operator (dashboard or iPhone) changed an alarm's state.
	TopicAlarmState = bus.NewTopic[store.Alarm]("alarm.state")
	// TopicCameraOnline: a camera's stream state, every 15 s per camera.
	TopicCameraOnline = bus.NewTopic[CameraOnline]("camera.online")
	// TopicCameraActivity: a detector saw something (motion, a person …) on
	// a camera, whether or not an event was recorded. High rate.
	TopicCameraActivity = bus.NewTopic[CameraActivity]("camera.activity")
	// TopicRecorderOnline: a recording server connected or disconnected.
	TopicRecorderOnline = bus.NewTopic[RecorderOnline]("recorder.online")
	// TopicConfigChanged: the camera configuration was (re)applied.
	TopicConfigChanged = bus.NewTopic[ConfigChanged]("config.changed")
)

type EventMessage struct {
	Camera *store.Camera // nil if the camera is gone
	Event  store.Event
}

type CameraOnline struct {
	CameraID string
	Online   bool
}

type CameraActivity struct {
	CameraID string
	Kind     string // "motion", "Person", "Vehicle", …
}

type RecorderOnline struct {
	ID     string
	Online bool
}

type ConfigChanged struct {
	Cameras []store.Camera
}

func (s *Server) publishEventNew(cam *store.Camera, e *store.Event) {
	bus.Publish(s.Bus, TopicEventNew, EventMessage{Camera: cam, Event: *e})
}

// publishEventUpdated announces that an event changed; subscribers re-read it.
func (s *Server) publishEventUpdated(eventID string) {
	bus.Publish(s.Bus, TopicEventUpdated, EventMessage{Event: store.Event{ID: eventID}})
}

func (s *Server) publishActivity(cameraID, kind string) {
	bus.Publish(s.Bus, TopicCameraActivity, CameraActivity{CameraID: cameraID, Kind: kind})
}

func (s *Server) publishCameraOnline(cameraID string, online bool) {
	bus.Publish(s.Bus, TopicCameraOnline, CameraOnline{CameraID: cameraID, Online: online})
}

func (s *Server) publishConfigChanged(cams []store.Camera) {
	bus.Publish(s.Bus, TopicConfigChanged, ConfigChanged{Cameras: cams})
}

func (s *Server) publishAlarmState(a *store.Alarm) {
	if a != nil {
		bus.Publish(s.Bus, TopicAlarmState, *a)
	}
}

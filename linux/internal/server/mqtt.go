package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/mqtt"
	"sentinel-linux/internal/store"
)

// MQTT + Home Assistant. The bridge is fed by an event-bus subscriber
// (subscribeMQTT): camera config, activity, online state, events and alarms.
// Neither the bus nor the bridge ever blocks the publisher.

const (
	mqttSetting         = "mqtt"          // JSON mqtt.Config (no password)
	mqttPasswordSetting = "mqtt_password" // sealed with install.key
	mqttClientIDSetting = "mqtt_client_id"
	mqttAuditUser       = "Home Assistant (MQTT)"
)

type mqttState struct {
	once   sync.Once
	bridge *mqtt.Bridge
	cmdMu  sync.Mutex // one switch command at a time
}

func (s *Server) ctxOrBackground() context.Context {
	if s.Ctx != nil {
		return s.Ctx
	}
	return context.Background()
}

// mqttBridge starts the bridge on first use with the saved settings.
func (s *Server) mqttBridge() *mqtt.Bridge {
	s.mqtt.once.Do(func() {
		b := &mqtt.Bridge{Version: s.Version, OnCommand: s.onMQTTCommand}
		s.mqtt.bridge = b
		ctx := s.ctxOrBackground()
		if cams, err := s.Store.Cameras(ctx); err == nil {
			b.SyncCameras(mqttCameras(cams))
		}
		b.Apply(ctx, s.mqttConfig(ctx))
	})
	return s.mqtt.bridge
}

func (s *Server) mqttConfig(ctx context.Context) mqtt.Config {
	var cfg mqtt.Config
	_ = json.Unmarshal([]byte(s.Store.Setting(ctx, mqttSetting, "{}")), &cfg)
	if sealed := s.Store.Setting(ctx, mqttPasswordSetting, ""); sealed != "" {
		if pw, err := s.Store.Unseal(sealed); err == nil {
			cfg.Password = pw
		}
	}
	cfg.ClientID = s.Store.Setting(ctx, mqttClientIDSetting, "")
	if cfg.ClientID == "" {
		cfg.ClientID = "sentinel-" + strings.ToLower(strings.ReplaceAll(store.NewID(), "-", "")[:12])
		_ = s.Store.SetSetting(ctx, mqttClientIDSetting, cfg.ClientID)
	}
	return cfg.WithDefaults()
}

func mqttCameras(cams []store.Camera) []mqtt.Camera {
	out := make([]mqtt.Camera, 0, len(cams))
	for _, c := range cams {
		out = append(out, mqtt.Camera{ID: c.ID, Name: c.Name, Recording: c.Recording, Detection: c.MotionLevel > 0})
	}
	return out
}

// MARK: - Event bus subscriber

// subscribeMQTT feeds the bridge from the event bus. The handlers run on the
// subscriber's goroutine; the bridge itself only queues, so a slow broker
// never holds up detection.
func (s *Server) subscribeMQTT() {
	sub := s.Bus.Subscribe("mqtt", 0)
	bus.Handle(sub, TopicConfigChanged, func(m ConfigChanged) {
		s.mqttBridge().SyncCameras(mqttCameras(m.Cameras))
	})
	// The motion/person/vehicle binary sensors (OFF after a quiet gap).
	bus.Handle(sub, TopicCameraActivity, func(m CameraActivity) { s.mqttBridge().Touch(m.CameraID, m.Kind) })
	bus.Handle(sub, TopicCameraOnline, func(m CameraOnline) { s.mqttBridge().CameraOnline(m.CameraID, m.Online) })
	bus.Handle(sub, TopicEventNew, func(m EventMessage) {
		if b := s.mqttBridge(); b.Enabled() {
			b.Event(mqttEventPayload("new", m.Camera, &m.Event))
		}
	})
	bus.Handle(sub, TopicEventUpdated, func(m EventMessage) { s.mqttEventUpdated(m.Event.ID) })
	bus.Handle(sub, TopicAlarmNew, func(a store.Alarm) {
		// Storage alarms were never published on sentinel/alarms; kept that
		// way so moving to the bus changes nothing (see ROADMAP, phase 1).
		if a.Kind != "Storage" {
			s.mqttAlarm(&a, "new")
		}
	})
	bus.Handle(sub, TopicAlarmState, func(a store.Alarm) { s.mqttAlarm(&a, "state") })
}

func mqttEventPayload(typ string, cam *store.Camera, e *store.Event) mqtt.Event {
	out := mqtt.Event{Type: typ, ID: e.ID, CameraID: e.CameraID, Kind: e.Kind, Label: e.Label, Score: e.Score,
		Time: e.CreatedAt, AlarmID: e.AlarmID, Description: e.Description, Threat: e.Threat}
	if cam != nil {
		out.Camera = cam.Name
	}
	if e.HasThumb || e.Thumbnail != "" {
		out.Snapshot = "/api/v1/events/" + e.ID + "/thumbnail.jpg"
	}
	return out
}

// mqttEventUpdated republishes an event once its snapshot or AI description
// is saved, and sends the snapshot JPEG to the camera's image topic.
func (s *Server) mqttEventUpdated(eventID string) {
	b := s.mqttBridge()
	if !b.Enabled() {
		return
	}
	ctx := s.ctxOrBackground()
	e, err := s.Store.Event(ctx, eventID)
	if err != nil {
		return
	}
	cam, _ := s.Store.Camera(ctx, e.CameraID)
	b.Event(mqttEventPayload("update", cam, e))
	if e.Thumbnail != "" && b.WantsSnapshot(e.CameraID, e.ID) {
		if jpeg, ok := s.eventThumbnail(ctx, e, 4<<20+1); ok {
			b.Snapshot(e.CameraID, e.ID, jpeg)
		}
	}
}

// mqttAlarm publishes an alarm being raised ("new") or changing state ("state").
func (s *Server) mqttAlarm(a *store.Alarm, typ string) {
	if a == nil {
		return
	}
	b := s.mqttBridge()
	if !b.Enabled() {
		return
	}
	b.Alarm(mqtt.Alarm{Type: typ, ID: a.ID, CameraID: a.CameraID, Camera: a.CameraName, Kind: a.Kind, Title: a.Title,
		Detail: a.Detail, Severity: a.Severity, State: a.State, Owner: a.Owner, CreatedAt: a.CreatedAt,
		LastEventAt: a.LastEventAt, EventCount: a.EventCount})
}

// MARK: - Switch commands from Home Assistant

// onMQTTCommand applies a recording/detection switch from the broker with the
// same validation and audit trail as an edit in the dashboard. The bridge has
// already checked the topic, payload (ON/OFF) and that the camera exists.
func (s *Server) onMQTTCommand(cameraID, what string, on bool) {
	s.mqtt.cmdMu.Lock()
	defer s.mqtt.cmdMu.Unlock()
	ctx := s.ctxOrBackground()
	if !s.mqttConfig(ctx).AllowCommands {
		return
	}
	cam, err := s.Store.Camera(ctx, cameraID)
	if err != nil {
		return
	}
	in := store.CameraInput{Name: cam.Name, Location: cam.Location, RTSPURL: cam.RTSPURL, SubRTSPURL: cam.SubRTSPURL,
		Username: cam.Username, Recording: cam.Recording, RetentionDays: cam.RetentionDays}
	var action string
	levelKey := "mqtt_detection_level_" + cam.ID
	switch what {
	case "recording":
		if cam.Recording == on {
			return
		}
		in.Recording = on
		action = "Turned recording " + onOffWord(on)
	case "detection":
		if (cam.MotionLevel > 0) == on {
			return
		}
		level := 0
		if on {
			level = 2 // medium, unless we remember what it was before Home Assistant turned it off
			if v, err := strconv.Atoi(s.Store.Setting(ctx, levelKey, "")); err == nil && v >= 1 && v <= 3 {
				level = v
			}
		} else {
			_ = s.Store.SetSetting(ctx, levelKey, fmt.Sprint(cam.MotionLevel))
		}
		in.MotionLevel = &level
		action = "Turned detection " + onOffWord(on)
	default:
		return
	}
	if err := normalizeCamera(&in); err != nil {
		slog.Warn("mqtt command rejected", "camera", cam.Name, "what", what, "err", err)
		return
	}
	if _, err := s.Store.SaveCamera(ctx, cam.ID, in); err != nil {
		slog.Error("mqtt command failed", "camera", cam.Name, "err", err)
		return
	}
	if err := s.Store.Audit(ctx, mqttAuditUser, "Cameras", action, cam.Name+" (via MQTT)"); err != nil {
		slog.Error("audit write failed", "err", err)
	}
	slog.Info("mqtt command", "camera", cam.Name, "action", action)
	if err := s.ApplyMedia(ctx); err != nil {
		slog.Error("media apply failed", "err", err)
	}
}

func onOffWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// MARK: - Settings API

type mqttView struct {
	mqtt.Config
	HasPassword bool        `json:"hasPassword"`
	Status      mqtt.Status `json:"status"`
}

func (s *Server) mqttView(ctx context.Context) mqttView {
	cfg := s.mqttConfig(ctx)
	return mqttView{Config: cfg, HasPassword: cfg.Password != "", Status: s.mqttBridge().Status()}
}

func (s *Server) handleGetMQTT(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mqttView(r.Context()))
}

type mqttInput struct {
	Enabled         *bool   `json:"enabled"`
	Host            *string `json:"host"`
	Port            *int    `json:"port"`
	TLS             *bool   `json:"tls"`
	Username        *string `json:"username"`
	Password        *string `json:"password"` // nil keeps the saved one; "" clears it
	TopicPrefix     *string `json:"topicPrefix"`
	DiscoveryPrefix *string `json:"discoveryPrefix"`
	AllowCommands   *bool   `json:"allowCommands"`
}

// merged applies the input over the saved config.
func (in mqttInput) merged(cfg mqtt.Config) mqtt.Config {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	set(&cfg.Host, in.Host)
	if in.Port != nil {
		cfg.Port = *in.Port
	}
	if in.TLS != nil {
		cfg.TLS = *in.TLS
	}
	set(&cfg.Username, in.Username)
	if in.Password != nil {
		cfg.Password = *in.Password
	}
	set(&cfg.TopicPrefix, in.TopicPrefix)
	set(&cfg.DiscoveryPrefix, in.DiscoveryPrefix)
	if in.AllowCommands != nil {
		cfg.AllowCommands = *in.AllowCommands
	}
	return cfg.WithDefaults()
}

func (s *Server) handlePutMQTT(w http.ResponseWriter, r *http.Request) {
	var in mqttInput
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	old := s.mqttConfig(ctx)
	cfg := in.merged(old)
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, _ := json.Marshal(cfg) // Password and ClientID are json:"-"
	if err := s.Store.SetSetting(ctx, mqttSetting, string(saved)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if in.Password != nil {
		sealed, err := s.Store.Seal(cfg.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := s.Store.SetSetting(ctx, mqttPasswordSetting, sealed); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if cfg.Password != old.Password {
			if cfg.Password == "" {
				s.audit(r, "MQTT", "Removed MQTT password", "")
			} else {
				s.audit(r, "MQTT", "Changed MQTT password", "")
			}
		}
	}
	if desc, prev := mqttDescribe(cfg), mqttDescribe(old); desc != prev {
		s.audit(r, "MQTT", "Changed MQTT settings", prev+" → "+desc)
	}
	s.mqttBridge().Apply(s.ctxOrBackground(), cfg)
	writeJSON(w, http.StatusOK, s.mqttView(ctx))
}

// mqttDescribe is the audit summary of a config (never the password).
func mqttDescribe(c mqtt.Config) string {
	if !c.Enabled {
		return "off"
	}
	tls, user, cmds := "", "", "commands off"
	if c.TLS {
		tls = " (TLS)"
	}
	if c.Username != "" {
		user = ", user " + c.Username
	}
	if c.AllowCommands {
		cmds = "commands on"
	}
	return fmt.Sprintf("on, %s:%d%s%s, topics %s/…, discovery %s/…, %s", c.Host, c.Port, tls, user, c.TopicPrefix, c.DiscoveryPrefix, cmds)
}

// handleTestMQTT tries a connection with the posted settings (the saved
// password unless one is given) without saving anything.
func (s *Server) handleTestMQTT(w http.ResponseWriter, r *http.Request) {
	var in mqttInput
	if r.ContentLength != 0 && !readJSON(w, r, &in) {
		return
	}
	cfg := in.merged(s.mqttConfig(r.Context()))
	cfg.Enabled = true
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	err := mqtt.Test(ctx, cfg)
	result := "ok"
	if err != nil {
		result = err.Error()
	}
	s.audit(r, "MQTT", "Tested MQTT connection", fmt.Sprintf("%s:%d — %s", cfg.Host, cfg.Port, result))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

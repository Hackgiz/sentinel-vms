package recorder

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/onvif"
)

// Camera analytics: cameras that run their own detection (ONVIF Profile M)
// report people, vehicles, line crossings and zone intrusions as ONVIF
// events. Each such camera gets one long-lived PullPoint subscription, and
// what it reports joins the same event pipeline as our own detector.

type cameraEventsState struct {
	mu   sync.Mutex
	subs map[string]*cameraEventSub // camera id →
}

type cameraEventSub struct {
	key    [32]byte // address + login it was started with; a change restarts it
	cancel context.CancelFunc
	status string
}

// syncCameraAnalytics starts, restarts or stops subscriptions so exactly the
// cameras with an ONVIF address and camera analytics on are watched.
func (r *Recorder) syncCameraAnalytics(cams []link.CameraConfig) {
	if r.Ctx == nil {
		return
	}
	st := &r.camEvents
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.subs == nil {
		st.subs = map[string]*cameraEventSub{}
	}
	keep := map[string]bool{}
	for i := range cams {
		c := &cams[i]
		if c.ONVIFURL == "" || !c.CameraAnalytics {
			continue
		}
		keep[c.ID] = true
		key := sha256.Sum256([]byte(c.ONVIFURL + "\x00" + c.Username + "\x00" + c.Password))
		if old := st.subs[c.ID]; old != nil {
			if old.key == key {
				continue
			}
			old.cancel()
		}
		ctx, cancel := context.WithCancel(r.Ctx)
		sub := &cameraEventSub{key: key, cancel: cancel, status: "Connecting"}
		st.subs[c.ID] = sub
		id, name := c.ID, c.Name
		go onvif.WatchEvents(ctx, c.ONVIFURL, c.Username, c.Password,
			func(e onvif.AnalyticsEvent) { r.OnCameraEvent(id, e) },
			func(msg string) { st.setStatus(sub, name, msg) })
	}
	for id, sub := range st.subs {
		if !keep[id] {
			sub.cancel()
			delete(st.subs, id)
		}
	}
}

func (st *cameraEventsState) setStatus(sub *cameraEventSub, cameraName, msg string) {
	st.mu.Lock()
	changed := sub.status != msg
	sub.status = msg
	st.mu.Unlock()
	if changed {
		slog.Info("camera analytics", "camera", cameraName, "status", msg)
	}
}

// AnalyticsStatus is a camera's analytics subscription state; "" when the
// camera isn't subscribed.
func (r *Recorder) AnalyticsStatus(cameraID string) string {
	st := &r.camEvents
	st.mu.Lock()
	defer st.mu.Unlock()
	if sub := st.subs[cameraID]; sub != nil {
		return sub.status
	}
	return ""
}

// OnCameraEvent records one event from a camera's own analytics.
func (r *Recorder) OnCameraEvent(cameraID string, e onvif.AnalyticsEvent) {
	// Events-only recording counts every camera event, motion and rule
	// endings included (an ending says the object was there until now),
	// before they are dropped below (recordctl.go).
	if cam, ok := r.camera(cameraID); ok && cam.CameraAnalytics {
		r.keepActivity(cam.ID, e.Kind, e.Label, e.At)
	}
	// A rule ending (IsInside=false) is not news. Camera motion is skipped:
	// our own motion detector covers it, and it is far noisier than the
	// camera's object analytics.
	if !e.Active || e.Kind == "Motion" {
		return
	}
	cam, ok := r.camera(cameraID)
	if !ok || !cam.CameraAnalytics {
		return
	}
	r.activity(cam.ID, e.Kind)
	// Same per-kind episode key as the built-in detector, so a person seen
	// by both the camera and Sentinel is one event, not two.
	record, mergeSince := r.detect.episode(cam.ID+"|"+e.Kind, e.At)
	if !record {
		return
	}
	label := e.Label
	if label == "" {
		label = e.Rule
	}
	score := e.Score
	if score == 0 {
		score = 1 // the camera asserted it without a confidence
	}
	ev, ok := r.recordDetection(cam, link.Detection{Source: link.SourceCamera, Kind: e.Kind, Label: label, Score: score,
		Rule: e.Rule, Topic: e.Topic, Summary: describeCameraEvent(e)}, e.At, mergeSince)
	if !ok {
		return
	}
	r.background(func() { r.finishDetection(cam, ev, nil) })
}

// describeCameraEvent: `a line crossing (person, rule "Gate")`.
func describeCameraEvent(e onvif.AnalyticsEvent) string {
	what := strings.ToLower(e.Kind)
	switch e.Kind {
	case "Person", "Vehicle":
		if e.Label != "" {
			what = e.Label
		}
	}
	article := "a "
	if strings.ContainsRune("aeiou", rune(what[0])) {
		article = "an "
	}
	var extra []string
	if e.Label != "" && e.Label != e.Rule && e.Kind != "Person" && e.Kind != "Vehicle" {
		extra = append(extra, e.Label)
	}
	if e.Rule != "" {
		extra = append(extra, fmt.Sprintf("rule %q", e.Rule))
	}
	if len(extra) > 0 {
		return article + what + " (" + strings.Join(extra, ", ") + ")"
	}
	return article + what
}

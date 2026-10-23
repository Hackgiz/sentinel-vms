package recorder

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
)

const (
	motionQuiet   = 30 * time.Second // quiet gap that ends a motion episode
	motionRepeat  = 60 * time.Second // during sustained motion, one event per minute
	alarmMergeWin = 60 * time.Second // Mac rule: detections within 60 s group into one alarm
	vehicleMemory = 15 * time.Minute
	// maxLoiterTracks bounds loitering bookkeeping per camera.
	maxLoiterTracks = 128
)

// detectState tracks motion episodes between callbacks.
type detectState struct {
	mu         sync.Mutex
	lastMotion map[string]time.Time
	lastEvent  map[string]time.Time
	vehicles   map[string][]seenBox            // recently seen vehicle boxes (parked-car filter)
	loiter     map[string]map[int]*loiterTrack // camera → person track (loitering)
}

// loiterTrack is one person track as loitering follows it.
type loiterTrack struct {
	first, last time.Time
	fired       bool
}

type seenBox struct {
	box  [4]float64
	last time.Time
}

// detectionSources builds the motion-reader list from the camera config.
// Each reader pulls the low-res live path from the local MediaMTX.
func (r *Recorder) detectionSources(cams []link.CameraConfig) []detect.Source {
	var out []detect.Source
	for _, c := range cams {
		if c.MotionLevel <= 0 {
			continue
		}
		out = append(out, detect.Source{
			CameraID: c.ID,
			URL:      r.liveRTSP(c.ID),
			Level:    c.MotionLevel,
			Objects:  r.Detect != nil && r.Detect.Objects != nil,
			Zones:    detectZones(c.Zones), // zones.go
			Lines:    detectLines(c.Zones), // lines.go
		})
	}
	return out
}

func (r *Recorder) liveRTSP(cameraID string) string {
	return fmt.Sprintf("rtsp://127.0.0.1:%d/%s", r.Media.Ports.RTSP, r.Media.LivePath(cameraID))
}

// episode applies the motion episode rule per camera+kind: a new event after
// 30 s without that kind, or once a minute while it continues. Returns
// whether to record an event and the alarm merge boundary (Mac rule:
// detections less than 60 s apart belong to one alarm; activity that never
// stopped always continues the alarm of its previous event; a new episode
// joins it only if it started within 60 s of the last sighting).
func (st *detectState) episode(key string, at time.Time) (bool, time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.lastMotion == nil {
		st.lastMotion, st.lastEvent = map[string]time.Time{}, map[string]time.Time{}
	}
	prevSeen, prevEvent := st.lastMotion[key], st.lastEvent[key]
	st.lastMotion[key] = at
	newEpisode := at.Sub(prevSeen) >= motionQuiet
	if !newEpisode && at.Sub(prevEvent) < motionRepeat {
		return false, time.Time{}
	}
	st.lastEvent[key] = at
	mergeSince := at.Add(time.Second) // matches nothing → new alarm
	if !prevEvent.IsZero() && (!newEpisode || at.Sub(prevSeen) < alarmMergeWin) {
		mergeSince = prevEvent
	}
	return true, mergeSince
}

// movingVehicles drops vehicles parked where one was already seen in the
// last 15 minutes, so a parked car doesn't fire every time someone walks by.
func (st *detectState) movingVehicles(cameraID string, objs []detect.Object, now time.Time) []detect.Object {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.vehicles == nil {
		st.vehicles = map[string][]seenBox{}
	}
	seen := st.vehicles[cameraID][:0]
	for _, b := range st.vehicles[cameraID] {
		if now.Sub(b.last) < vehicleMemory {
			seen = append(seen, b)
		}
	}
	var moving []detect.Object
	for _, o := range objs {
		matched := false
		for i := range seen {
			if detect.IoU(seen[i].box, o.Box) >= 0.6 {
				seen[i].last, seen[i].box, matched = now, o.Box, true
				break
			}
		}
		if !matched {
			seen = append(seen, seenBox{box: o.Box, last: now})
			moving = append(moving, o)
		}
	}
	st.vehicles[cameraID] = seen
	return moving
}

// OnMotion is the motion detector callback. Raw over-threshold frames become
// at most one event per episode (plus one per minute of sustained motion),
// each with a snapshot. Whether it raises an alarm is the management side's
// call (the camera's alert setting).
func (r *Recorder) OnMotion(m detect.Motion) {
	r.activity(m.CameraID, "motion")
	record, mergeSince := r.detect.episode(m.CameraID, m.At)
	if !record {
		return
	}
	cam, ok := r.camera(m.CameraID)
	if !ok {
		return
	}
	ev, ok := r.recordDetection(cam, link.Detection{Source: link.SourceMotion, Kind: "Motion", Score: m.Score}, m.At, mergeSince)
	if !ok {
		return
	}
	r.motionSnapshot(cam, ev)
}

// OnObjects handles one person/vehicle detection pass (frames with motion only).
func (r *Recorder) OnObjects(d detect.Detections) {
	cam, ok := r.camera(d.CameraID)
	if !ok {
		return
	}
	best := map[string]detect.Object{}
	var vehicles []detect.Object
	for _, o := range d.Objects {
		if o.Kind == "Vehicle" {
			vehicles = append(vehicles, o)
			continue
		}
		if b, ok := best[o.Kind]; !ok || o.Score > b.Score {
			best[o.Kind] = o
		}
	}
	for _, v := range r.detect.movingVehicles(cam.ID, vehicles, d.At) {
		if b, ok := best["Vehicle"]; !ok || v.Score > b.Score {
			best["Vehicle"] = v
		}
	}
	for kind, o := range best {
		r.activity(cam.ID, kind)
		record, mergeSince := r.detect.episode(cam.ID+"|"+kind, d.At)
		if !record {
			continue
		}
		box, _ := json.Marshal(o.Box)
		ev, ok := r.recordDetection(cam, link.Detection{Source: link.SourceObjects, Kind: kind, Label: o.Label, Score: o.Score, Box: string(box),
			TrackID: trackID(cam.ID, o.TrackID)}, d.At, mergeSince)
		if !ok {
			continue
		}
		var detected []byte
		if d.Frame != nil {
			detected, _ = detect.FrameJPEG(d.Frame, d.ContentW, d.ContentH, [][4]float64{o.Box}, kind)
		}
		r.background(func() { r.finishDetection(cam, ev, detected) })
	}
	r.onLoitering(cam, d)
}

// DetectRate is the configured detection passes per second (0 = auto) for
// detect.Manager.Rate: the server-wide setting, the same for every camera.
func (r *Recorder) DetectRate(string) int { return r.config().DetectRate }

// trackID is an event's track id: "<cameraId>:<n>", "" when untracked.
func trackID(cameraID string, n int) string {
	if n == 0 {
		return ""
	}
	return cameraID + ":" + strconv.Itoa(n)
}

// onLoitering raises one Loitering event per person track that has been
// followed for at least the camera's LoiterSeconds (WAVE16). The event
// carries the track's current box and id; server/sink.go makes it a Warning
// alarm. Detection only runs while there is motion, so a person standing
// perfectly still can drop out of tracking (the track ends after
// detect.TrackTTL unseen) and starts a new one when seen again.
func (r *Recorder) onLoitering(cam link.CameraConfig, d detect.Detections) {
	if cam.LoiterSeconds <= 0 {
		r.detect.forgetLoiter(cam.ID)
		return
	}
	for _, o := range r.detect.loitering(cam.ID, d.Objects, d.At, time.Duration(cam.LoiterSeconds)*time.Second) {
		_, mergeSince := r.detect.episode(cam.ID+"|Loitering", d.At)
		box, _ := json.Marshal(o.Box)
		ev, ok := r.recordDetection(cam, link.Detection{Source: link.SourceObjects, Kind: "Loitering", Label: "person", Score: o.Score, Box: string(box),
			TrackID: trackID(cam.ID, o.TrackID), Summary: fmt.Sprintf("a person stayed more than %d s", cam.LoiterSeconds)}, d.At, mergeSince)
		if !ok {
			continue
		}
		var detected []byte
		if d.Frame != nil {
			detected, _ = detect.FrameJPEG(d.Frame, d.ContentW, d.ContentH, [][4]float64{o.Box}, "Loitering")
		}
		r.background(func() { r.finishDetection(cam, ev, detected) })
	}
}

// loitering returns the tracked people first seen at least after ago that
// have not fired yet, marking them fired: one result per track, ever.
// Tracks unseen for longer than detect.TrackTTL are forgotten.
func (st *detectState) loitering(cameraID string, objs []detect.Object, at time.Time, after time.Duration) []detect.Object {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.loiter == nil {
		st.loiter = map[string]map[int]*loiterTrack{}
	}
	tracks := st.loiter[cameraID]
	if tracks == nil {
		tracks = map[int]*loiterTrack{}
		st.loiter[cameraID] = tracks
	}
	for id, t := range tracks {
		if at.Sub(t.last) > detect.TrackTTL {
			delete(tracks, id)
		}
	}
	var out []detect.Object
	for _, o := range objs {
		if o.Kind != "Person" || o.TrackID == 0 {
			continue
		}
		t := tracks[o.TrackID]
		if t == nil {
			if len(tracks) >= maxLoiterTracks {
				continue
			}
			t = &loiterTrack{first: at}
			tracks[o.TrackID] = t
		}
		t.last = at
		if !t.fired && at.Sub(t.first) >= after {
			t.fired = true
			out = append(out, o)
		}
	}
	return out
}

func (st *detectState) forgetLoiter(cameraID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.loiter, cameraID)
}

// activity tells the management side a detector saw something (MQTT binary
// sensors), whether or not an event is recorded. Ephemeral: not queued.
func (r *Recorder) activity(cameraID, kind string) {
	sink := r.currentSink()
	if sink == nil {
		return
	}
	_ = sink.Event(r.ctx(), link.Event{ID: link.NewID(), Type: link.EventActivity, ServerID: r.ServerID, CameraID: cameraID,
		Time: time.Now().UnixMilli(), Ephemeral: true, Activity: &link.Activity{Kind: kind}})
}

// recordDetection stores the event, queues it for the management side and
// delivers it right away (the alarm is raised before this returns when the
// management server is reachable, as before the split).
func (r *Recorder) recordDetection(cam link.CameraConfig, d link.Detection, at, mergeSince time.Time) (*event, bool) {
	ev := &event{ID: newEventID(), CameraID: cam.ID, Kind: d.Kind, Label: d.Label, Score: d.Score, Box: d.Box, CreatedAt: at.Unix()}
	d.EventID, d.CreatedAt, d.At, d.MergeSince = ev.ID, ev.CreatedAt, at.UnixMilli(), mergeSince.UnixMilli()
	msg := link.Event{ID: ev.ID, Type: link.EventDetection, ServerID: r.ServerID, CameraID: cam.ID, Time: time.Now().UnixMilli(), Detection: &d}
	if err := r.store.addEvent(r.ctx(), ev, msg); err != nil {
		slog.Error("save detection event", "kind", d.Kind, "err", err)
		return nil, false
	}
	r.deliver(msg)
	return ev, true
}

// motionSnapshot grabs the event thumbnail in the background (~1-2 s).
func (r *Recorder) motionSnapshot(cam link.CameraConfig, ev *event) {
	if !r.Detect.Available() {
		return
	}
	r.background(func() {
		if err := os.MkdirAll(r.eventsDir(), 0o750); err != nil {
			return
		}
		name := ev.ID + ".jpg"
		path := filepath.Join(r.eventsDir(), name)
		if err := r.Detect.Snapshot(r.ctx(), r.liveRTSP(cam.ID), path); err == nil && r.maskFile(cam.ID, path) {
			r.thumbnailSaved(cam.ID, ev.ID, name)
		}
	})
}

func (r *Recorder) thumbnailSaved(cameraID, eventID, file string) {
	msg := link.Event{ID: link.NewID(), Type: link.EventThumbnail, ServerID: r.ServerID, CameraID: cameraID,
		Time: time.Now().UnixMilli(), Thumbnail: &link.Thumbnail{EventID: eventID, File: file}}
	if err := r.store.setThumbnail(r.ctx(), eventID, file, msg); err != nil {
		slog.Error("save event thumbnail", "err", err)
		return
	}
	r.deliver(msg)
}

// finishDetection saves the snapshot and, when AI descriptions are on,
// sends follow-up frames to the management side (which holds the AI key).
//
// The thumbnail is the analyzed frame itself with the detection outlined —
// a snapshot grabbed afterwards often misses a person who's walking.
func (r *Recorder) finishDetection(cam link.CameraConfig, ev *event, detected []byte) {
	if err := os.MkdirAll(r.eventsDir(), 0o750); err != nil {
		return
	}
	thumb := ev.ID + ".jpg"
	var jpegs [][]byte
	if detected != nil {
		detected, _ = r.maskedJPEG(cam.ID, detected) // nil when it could not be masked: dropped
	}
	if detected != nil && os.WriteFile(filepath.Join(r.eventsDir(), thumb), detected, 0o640) == nil {
		r.thumbnailSaved(cam.ID, ev.ID, thumb)
		jpegs = append(jpegs, detected)
	}
	describe := (ev.Kind == "Person" || ev.Kind == "Vehicle") && r.config().AIDescriptions
	if !r.Detect.Available() || (!describe && jpegs != nil) {
		return
	}
	// Higher-resolution follow-up frames: for the AI (what happens next) and
	// as the thumbnail if the analyzed frame wasn't available.
	n := 1
	if describe {
		n = 2
	}
	frames, err := r.Detect.SnapshotFrames(r.ctx(), r.liveRTSP(cam.ID), r.eventsDir(), ev.ID, n)
	if err == nil {
		defer func() {
			for _, f := range frames {
				_ = os.Remove(f)
			}
		}()
		for _, f := range frames {
			if b, err := os.ReadFile(f); err == nil {
				b, ok := r.maskedJPEG(cam.ID, b) // zones.go: unmaskable frames are dropped
				if !ok {
					continue
				}
				if jpegs == nil {
					_ = os.WriteFile(filepath.Join(r.eventsDir(), thumb), b, 0o640)
					r.thumbnailSaved(cam.ID, ev.ID, thumb)
				}
				jpegs = append(jpegs, b)
			}
		}
	}
	if !describe || len(jpegs) == 0 {
		return
	}
	sink := r.currentSink()
	if sink == nil {
		return
	}
	// Ephemeral: a scene description is best effort, not worth queueing JPEGs.
	_ = sink.Event(r.ctx(), link.Event{ID: link.NewID(), Type: link.EventFrames, ServerID: r.ServerID, CameraID: cam.ID,
		Time: time.Now().UnixMilli(), Ephemeral: true, Frames: &link.Frames{EventID: ev.ID, Kind: ev.Kind, JPEGs: jpegs}})
}

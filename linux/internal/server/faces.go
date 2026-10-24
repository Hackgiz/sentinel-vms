package server

// Face recognition, the matching side (WAVE16-DETECTION.md, stream C). With
// both face models loaded (cmd/sentinel/faces.go) and faces.enabled on,
// every Person event whose thumbnail arrives is searched for faces; each
// face big enough to recognize is embedded, stored with a small crop, and
// compared with every enrolled face (brute force: a gallery is small). The
// best person at or above faces.MatchThreshold is the match: the event gets
// their id and name, and their alert setting acts on the event's alarm.
// Faces that match nobody are kept for enrollment and purged after
// faces.unassigned_days. Off by default; without the models the API says
// why.
//
// The thumbnail is small (the detector's frame, ~640 px wide), so a face
// there is often below faces.MinFacePx unless the person is about a meter
// from the camera. When the thumbnail has no usable face, the frame at the
// event's time is cut from the recording at full resolution and the area
// around the person's box is searched instead (recognizeRecorded).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"sentinel-linux/internal/faces"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/reid"
	"sentinel-linux/internal/store"
)

// Settings keys (Admin → People).
const (
	SettingFacesEnabled        = "faces.enabled"         // "1" = on; off by default
	SettingFacesUnassignedDays = "faces.unassigned_days" // days unassigned faces are kept (1–90, default 7)
)

const (
	facesQueueLen       = 256 // pending thumbnails; a full queue drops the oldest (and counts it)
	facesWorkers        = 3   // events recognized at once (each may wait on a recorder)
	facesDropLogEvery   = 10 * time.Second
	facesThumbMax       = 4 << 20 // biggest thumbnail fetched
	facesFetchWait      = 20 * time.Second
	facesMaxPerEvent    = 5 // faces stored per thumbnail, best first
	facesDefaultDays    = 7
	facesMaxDays        = 90
	facesPurgeEvery     = time.Hour
	facesWatchlistMerge = 60 * time.Second // repeat sightings fold into one Watchlist alarm

	// The full-resolution fallback: the recording is read this long after
	// the event (so its fragment is on disk), tried this many times.
	facesRecordedLag      = 3 * time.Second
	facesRecordedAttempts = 2
	facesRecordedWait     = 30 * time.Second // per snapshot command
)

type facesState struct {
	mu      sync.Mutex
	engine  faces.Engine
	reason  string // why faces are unavailable ("" with an engine)
	queue   chan string
	started bool

	// Events dropped from a full queue (drops, atomic) and how many of them
	// the log has reported (reported, lastDropLog under mu); inflight holds
	// the events a worker is on so a repeated thumbnail isn't run twice.
	drops       atomic.Int64
	reported    int64
	lastDropLog time.Time
	inflight    map[string]bool

	// thumb fetches an event's thumbnail JPEG; nil = s.eventThumbnail
	// (tests replace it).
	thumb func(ctx context.Context, e *store.Event) ([]byte, bool)
	// recorded fetches the full-resolution recorded frame at an event's
	// time; nil = s.recordedFrame (tests replace it).
	recorded func(ctx context.Context, e *store.Event) ([]byte, bool)
	// processed is called after each queued event is handled (tests).
	processed func(eventID string)
}

// SetFaceEngine installs the face models (nil = unavailable, with reason),
// registers the thumbnail hook and starts the worker and the hourly purge
// of unassigned faces. Call it once at startup; later calls only swap the
// engine.
func (s *Server) SetFaceEngine(e faces.Engine, reason string) {
	st := &s.faces
	st.mu.Lock()
	st.engine, st.reason = e, reason
	start := !st.started
	if start {
		st.started = true
		st.queue = make(chan string, facesQueueLen)
	}
	q := st.queue
	st.mu.Unlock()
	if start {
		s.OnThumbnail(s.facesOnThumbnail)
		ctx := s.ctxOrBackground()
		for i := 0; i < facesWorkers; i++ {
			go s.runFacesQueue(ctx, q)
		}
		go s.runFacesPurge(ctx)
	}
}

// faceEngine returns the engine, or nil and why it is unavailable.
func (s *Server) faceEngine() (faces.Engine, string) {
	st := &s.faces
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.engine == nil {
		if st.reason == "" {
			return nil, "face models not loaded"
		}
		return nil, st.reason
	}
	return st.engine, ""
}

func (s *Server) facesEnabled(ctx context.Context) bool {
	return s.Store.Setting(ctx, SettingFacesEnabled, "0") == "1"
}

func (s *Server) facesUnassignedDays(ctx context.Context) int {
	n, err := strconv.Atoi(s.Store.Setting(ctx, SettingFacesUnassignedDays, ""))
	if err != nil || n < 1 || n > facesMaxDays {
		return facesDefaultDays
	}
	return n
}

// facesOnThumbnail queues an event whose thumbnail just arrived (the
// OnThumbnail hook, on the sink goroutine). Never blocks: a full queue
// drops its oldest entry.
func (s *Server) facesOnThumbnail(eventID string) {
	st := &s.faces
	st.mu.Lock()
	q, ok := st.queue, st.engine != nil
	st.mu.Unlock()
	if !ok || q == nil {
		return
	}
	for {
		select {
		case q <- eventID:
			return
		default:
		}
		select {
		case <-q: // drop the oldest
			st.drops.Add(1)
			s.logFaceDrops(false)
		default:
		}
	}
}

// logFaceDrops reports the events dropped from a full queue since the last
// report, at most every facesDropLogEvery unless force.
func (s *Server) logFaceDrops(force bool) {
	st := &s.faces
	st.mu.Lock()
	defer st.mu.Unlock()
	total := st.drops.Load()
	n := total - st.reported
	if n <= 0 || (!force && time.Since(st.lastDropLog) < facesDropLogEvery) {
		return
	}
	st.reported, st.lastDropLog = total, time.Now()
	slog.Warn("faces: queue full, events skipped (recognition is behind)", "dropped", n, "total", total, "queue", facesQueueLen)
}

func (s *Server) runFacesQueue(ctx context.Context, q <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q:
			s.faces.mu.Lock()
			busy := s.faces.inflight[id]
			if s.faces.inflight == nil {
				s.faces.inflight = map[string]bool{}
			}
			s.faces.inflight[id] = true
			s.faces.mu.Unlock()
			if !busy {
				if err := s.recognizeEvent(ctx, id); err != nil {
					slog.Debug("faces: event not processed", "event", id, "err", err)
				}
			}
			s.faces.mu.Lock()
			if !busy {
				delete(s.faces.inflight, id)
			}
			done := s.faces.processed
			s.faces.mu.Unlock()
			s.logFaceDrops(false)
			if done != nil {
				done(id)
			}
		}
	}
}

// runFacesPurge deletes unassigned faces older than faces.unassigned_days,
// now and hourly.
func (s *Server) runFacesPurge(ctx context.Context) {
	t := time.NewTicker(facesPurgeEvery)
	defer t.Stop()
	for {
		s.purgeUnassignedFaces(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) purgeUnassignedFaces(ctx context.Context) {
	days := s.facesUnassignedDays(ctx)
	before := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	if n, err := s.Store.PurgeUnassignedFaces(ctx, before); err != nil {
		if ctx.Err() == nil {
			slog.Error("faces: purge unassigned", "err", err)
		}
	} else if n > 0 {
		slog.Info("faces: purged unassigned faces", "rows", n, "days", days)
	}
}

// recognizeEvent finds, stores and matches the faces on one event's
// thumbnail and applies a match to the event and its alarm.
func (s *Server) recognizeEvent(ctx context.Context, eventID string) error {
	eng, _ := s.faceEngine()
	if eng == nil || !s.facesEnabled(ctx) {
		return nil
	}
	e, err := s.Store.Event(ctx, eventID)
	if err != nil {
		return err
	}
	if e.Kind != "Person" || e.Thumbnail == "" {
		return nil
	}
	if have, err := s.Store.EventFaces(ctx, e.ID); err != nil || len(have) > 0 {
		return err // already done (a repeated thumbnail)
	}
	img, err := s.faceThumbnail(ctx, e)
	if err != nil {
		return err
	}
	keep, err := keptFaces(eng, img)
	if err != nil {
		return err
	}
	source := "snapshot"
	toFrame := func(b [4]float64) [4]float64 { return b }
	if len(keep) == 0 {
		crop, ok := s.recordedCrop(ctx, e)
		if !ok {
			slog.Info("faces: no face big enough to recognize", "event", e.ID, "camera", e.CameraID)
			return nil
		}
		if keep, err = keptFaces(eng, crop.img); err != nil {
			return err
		}
		if len(keep) == 0 {
			slog.Info("faces: no face big enough to recognize", "event", e.ID, "camera", e.CameraID, "searched", "recording")
			return nil
		}
		img, source, toFrame = crop.img, "recording", crop.toFrame
	}
	slog.Info("faces: found", "event", e.ID, "camera", e.CameraID, "faces", len(keep), "source", source)
	gallery, err := s.faceGallery(ctx, eng.EmbedModel())
	if err != nil {
		return err
	}
	var best struct {
		personID string
		sim      float64
	}
	for _, f := range keep {
		vec, err := eng.Embed(img, f)
		if err != nil {
			slog.Debug("faces: embed", "event", e.ID, "err", err)
			continue
		}
		crop, err := faces.CropJPEG(img, f)
		if err != nil {
			continue
		}
		fb := toFrame(f.Box) // stored in the full frame's coordinates, like the event's box
		box, _ := json.Marshal([]float64{round4(fb[0]), round4(fb[1]), round4(fb[2]), round4(fb[3])})
		row := &store.Face{EventID: e.ID, CameraID: e.CameraID, AtMs: secToMs(e.CreatedAt), Box: string(box), Score: round4(f.Score),
			Model: eng.EmbedModel(), Vec: reid.Encode(vec)}
		if pid, sim, ok := faces.Best(vec, gallery); ok {
			row.PersonID, row.Similarity = pid, round4(sim)
			if best.personID == "" || sim > best.sim {
				best.personID, best.sim = pid, sim
			}
		}
		if err := s.Store.AddFace(ctx, row, crop); err != nil {
			return err
		}
	}
	if best.personID == "" {
		return nil
	}
	p, err := s.Store.Person(ctx, best.personID)
	if err != nil {
		return err
	}
	if err := s.Store.SetEventPerson(ctx, e.ID, p.ID, p.Name); err != nil {
		return err
	}
	s.publishEventUpdated(e.ID)
	return s.applyPersonAlert(ctx, e, p)
}

func round4(v float64) float64 { return float64(int64(v*10000+0.5)) / 10000 }

// keptFaces finds the faces in img that are big enough to recognize, best
// first, at most facesMaxPerEvent.
func keptFaces(eng faces.Engine, img image.Image) ([]faces.Face, error) {
	found, err := eng.Find(img)
	if err != nil {
		return nil, fmt.Errorf("face detection: %w", err)
	}
	b := img.Bounds()
	var keep []faces.Face
	for _, f := range found {
		if f.MinSidePx(b.Dx(), b.Dy()) >= faces.MinFacePx {
			keep = append(keep, f)
		}
		if len(keep) == facesMaxPerEvent {
			break
		}
	}
	return keep, nil
}

// faceCrop is the part of a full-resolution frame searched for faces.
type faceCrop struct {
	img   image.Image     // the crop, origin 0,0
	frame image.Rectangle // the whole frame's bounds
	rect  image.Rectangle // the crop within the frame
}

// toFrame maps a box normalized to the crop into the whole frame.
func (c faceCrop) toFrame(b [4]float64) [4]float64 {
	fw, fh := float64(c.frame.Dx()), float64(c.frame.Dy())
	x0, y0 := float64(c.rect.Min.X-c.frame.Min.X), float64(c.rect.Min.Y-c.frame.Min.Y)
	cw, ch := float64(c.rect.Dx()), float64(c.rect.Dy())
	return [4]float64{(x0 + b[0]*cw) / fw, (y0 + b[1]*ch) / fh, (x0 + b[2]*cw) / fw, (y0 + b[3]*ch) / fh}
}

// faceRegion is the part of the frame (normalized) where the person's face
// can be: the event's box widened by half its width on each side (the
// event time is only known to the second) and its top three quarters, a
// little above the box. Without a box, the whole frame.
func faceRegion(boxJSON string) [4]float64 {
	var b []float64
	if json.Unmarshal([]byte(boxJSON), &b) != nil || len(b) != 4 || !(b[2] > b[0]) || !(b[3] > b[1]) {
		return [4]float64{0, 0, 1, 1}
	}
	bw, bh := b[2]-b[0], b[3]-b[1]
	clamp := func(v float64) float64 { return math.Max(0, math.Min(1, v)) }
	return [4]float64{clamp(b[0] - bw/2), clamp(b[1] - bh*0.15), clamp(b[2] + bw/2), clamp(b[1] + bh*0.75)}
}

// recordedCrop cuts the frame at the event's time from the recording at
// full resolution and returns the area around the person.
func (s *Server) recordedCrop(ctx context.Context, e *store.Event) (faceCrop, bool) {
	fetch := s.faces.recorded
	if fetch == nil {
		fetch = s.recordedFrame
	}
	jpg, ok := fetch(ctx, e)
	if !ok || len(jpg) == 0 {
		return faceCrop{}, false
	}
	src, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		slog.Debug("faces: recorded frame", "event", e.ID, "err", err)
		return faceCrop{}, false
	}
	fb := src.Bounds()
	r := faceRegion(e.Box)
	rect := image.Rect(
		fb.Min.X+int(r[0]*float64(fb.Dx())), fb.Min.Y+int(r[1]*float64(fb.Dy())),
		fb.Min.X+int(math.Ceil(r[2]*float64(fb.Dx()))), fb.Min.Y+int(math.Ceil(r[3]*float64(fb.Dy()))),
	).Intersect(fb)
	if rect.Dx() < 2 || rect.Dy() < 2 {
		return faceCrop{}, false
	}
	out := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(out, out.Bounds(), src, rect.Min, draw.Src)
	return faceCrop{img: out, frame: fb, rect: rect}, true
}

// recordedFrame asks the event's recorder for the recorded frame at the
// event's time (full resolution, privacy masks applied). The event time is
// whole seconds, so the middle of that second is used.
func (s *Server) recordedFrame(ctx context.Context, e *store.Event) ([]byte, bool) {
	rec := s.recorderAt(ctx, e.CameraID, e.CreatedAt)
	return recordedFrameVia(ctx, e, func(cctx context.Context, args link.SnapshotArgs, res *link.SnapshotResult) error {
		return s.command(cctx, rec, link.CmdSnapshot, args, res)
	})
}

// facesRetryWait is the pause before the second try (a var for tests).
var facesRetryWait = facesRecordedLag

// permanentFrameError reports a failure retrying cannot fix: the recorder
// answered that there is no such recording, the request is wrong, or it does
// not know the command (an older recorder). Timeouts, a recorder that is
// offline or busy, and transport errors are transient.
func permanentFrameError(err error) bool {
	var ce *link.CommandError
	if !errors.As(err, &ce) {
		return false
	}
	switch ce.Code {
	case link.CodeNotFound, link.CodeNoClip, link.CodeBadRequest, link.CodeUnknownCommand:
		return true
	}
	return false
}

// recordedFrameVia is recordedFrame with the snapshot command injected.
func recordedFrameVia(ctx context.Context, e *store.Event, snapshot func(context.Context, link.SnapshotArgs, *link.SnapshotResult) error) ([]byte, bool) {
	at := e.CreatedAt*1000 + 500
	for attempt := 0; attempt < facesRecordedAttempts; attempt++ {
		wait := time.Until(time.UnixMilli(at).Add(facesRecordedLag))
		if attempt > 0 {
			wait = facesRetryWait
		}
		if wait > 0 {
			select {
			case <-ctx.Done():
				return nil, false
			case <-time.After(wait):
			}
		}
		var res link.SnapshotResult
		cctx, cancel := context.WithTimeout(ctx, facesRecordedWait)
		err := snapshot(cctx, link.SnapshotArgs{CameraID: e.CameraID, At: at}, &res)
		cancel()
		if err == nil && len(res.JPEG) > 0 {
			return res.JPEG, true
		}
		slog.Debug("faces: recorded frame", "event", e.ID, "attempt", attempt+1, "err", err)
		if err != nil && permanentFrameError(err) {
			return nil, false
		}
	}
	return nil, false
}

// faceThumbnail fetches and decodes an event's thumbnail (the file the
// appearance index reads), as RGBA so the models read it cheaply.
func (s *Server) faceThumbnail(ctx context.Context, e *store.Event) (image.Image, error) {
	fetch := s.faces.thumb
	if fetch == nil {
		fetch = func(ctx context.Context, e *store.Event) ([]byte, bool) {
			return s.eventThumbnail(ctx, e, facesThumbMax)
		}
	}
	fctx, cancel := context.WithTimeout(ctx, facesFetchWait)
	jpg, ok := fetch(fctx, e)
	cancel()
	if !ok || len(jpg) == 0 || len(jpg) > facesThumbMax {
		return nil, errNoThumbnail
	}
	src, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return nil, fmt.Errorf("thumbnail: %w", err)
	}
	rgba := image.NewRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)
	return rgba, nil
}

// faceGallery loads every enrolled vector from model.
func (s *Server) faceGallery(ctx context.Context, model string) ([]faces.Enrolled, error) {
	rows, err := s.Store.Gallery(ctx, model)
	if err != nil {
		return nil, err
	}
	out := make([]faces.Enrolled, 0, len(rows))
	for _, r := range rows {
		if v, err := reid.Decode(r.Vec); err == nil {
			out = append(out, faces.Enrolled{PersonID: r.PersonID, Vec: v})
		}
	}
	return out, nil
}

// applyPersonAlert acts on the event's alarm as the person's alert setting
// says: default names them on it, never closes it (a known person), always
// raises a Critical Watchlist alarm.
func (s *Server) applyPersonAlert(ctx context.Context, e *store.Event, p *store.Person) error {
	camName := "camera " + e.CameraID
	if c, err := s.Store.Camera(ctx, e.CameraID); err == nil {
		camName = c.Name
	}
	switch p.Alert {
	case store.PersonAlertAlways:
		at := time.Unix(e.CreatedAt, 0)
		_, _, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: e.CameraID, CameraName: camName, Kind: "Watchlist",
			Title: "Watchlist: " + p.Name, Severity: "Critical", EventID: e.ID, At: at, MergeWindow: facesWatchlistMerge,
			Detail: fmt.Sprintf("%s at %s, %s.", p.Name, camName, at.Format("Jan 2 3:04:05 PM")),
			Origin: "Face recognition: " + p.Name + " is on the watchlist"})
		return err
	case store.PersonAlertNever:
		a, err := s.openEventAlarm(ctx, e)
		if a == nil {
			return err
		}
		note := "Known person: " + p.Name
		a, err = s.Store.SetAlarmState(ctx, a.ID, "Resolved", "", note)
		if err != nil {
			return err
		}
		s.auditSystem("System", "Alarms", "Set alarm resolved", a.Title+" ("+a.CameraName+"): "+note)
		s.publishAlarmState(a)
		return nil
	default:
		a, err := s.openEventAlarm(ctx, e)
		if a == nil {
			return err
		}
		a, err = s.Store.SetPersonAlarmText(ctx, a.ID, p.Name+" detected", p.Name+" at "+camName)
		if err != nil {
			return err
		}
		s.publishAlarmState(a)
		return nil
	}
}

// openEventAlarm is the event's alarm while it is open (nil otherwise: no
// alarm, or someone already handled it).
func (s *Server) openEventAlarm(ctx context.Context, e *store.Event) (*store.Alarm, error) {
	if e.AlarmID == "" {
		return nil, nil
	}
	a, err := s.Store.Alarm(ctx, e.AlarmID)
	if err != nil {
		return nil, err
	}
	if !store.IsOpenState(a.State) || a.State == "Snoozed" {
		return nil, nil
	}
	return a, nil
}

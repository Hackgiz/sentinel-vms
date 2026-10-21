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

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"sentinel-linux/internal/faces"
	"sentinel-linux/internal/reid"
	"sentinel-linux/internal/store"
)

// Settings keys (Admin → People).
const (
	SettingFacesEnabled        = "faces.enabled"         // "1" = on; off by default
	SettingFacesUnassignedDays = "faces.unassigned_days" // days unassigned faces are kept (1–90, default 7)
)

const (
	facesQueueLen       = 64      // pending thumbnails; a full queue drops the oldest
	facesThumbMax       = 4 << 20 // biggest thumbnail fetched
	facesFetchWait      = 20 * time.Second
	facesMaxPerEvent    = 5 // faces stored per thumbnail, best first
	facesDefaultDays    = 7
	facesMaxDays        = 90
	facesPurgeEvery     = time.Hour
	facesWatchlistMerge = 60 * time.Second // repeat sightings fold into one Watchlist alarm
)

type facesState struct {
	mu      sync.Mutex
	engine  faces.Engine
	reason  string // why faces are unavailable ("" with an engine)
	queue   chan string
	started bool

	// thumb fetches an event's thumbnail JPEG; nil = s.eventThumbnail
	// (tests replace it).
	thumb func(ctx context.Context, e *store.Event) ([]byte, bool)
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
		go s.runFacesQueue(ctx, q)
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
		default:
		}
	}
}

func (s *Server) runFacesQueue(ctx context.Context, q <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q:
			if err := s.recognizeEvent(ctx, id); err != nil {
				slog.Debug("faces: event not processed", "event", id, "err", err)
			}
			s.faces.mu.Lock()
			done := s.faces.processed
			s.faces.mu.Unlock()
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
	found, err := eng.Find(img)
	if err != nil {
		return fmt.Errorf("face detection: %w", err)
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
	if len(keep) == 0 {
		return nil
	}
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
		box, _ := json.Marshal([]float64{round4(f.Box[0]), round4(f.Box[1]), round4(f.Box[2]), round4(f.Box[3])})
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

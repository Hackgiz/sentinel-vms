package server

// API v2: alarms (paged list, state changes, evidence) and detection events.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"sentinel-linux/internal/store"
)

const (
	defaultSnoozeMinutes = 15
	maxSnoozeMinutes     = 24 * 60
	maxAlarmNote         = 500
)

// snoozeUnit is one "snoozeMinutes"; tests shorten it.
var snoozeUnit = time.Minute

func alarmPageKey(a *store.Alarm) store.PageKey {
	return store.PageKey{CreatedAt: a.CreatedAt, ID: a.ID}
}

func eventPageKey(e *store.Event) store.PageKey {
	return store.PageKey{CreatedAt: e.CreatedAt, ID: e.ID}
}

// v2AlarmView is an alarm with the instructions of the rules matching it.
func (s *Server) v2AlarmView(ctx context.Context, a *store.Alarm) v2Alarm {
	return toV2Alarm(a, s.alarmInstructions(ctx, a))
}

// handleV2Alarms lists alarms newest raised first (by createdAt, so pages
// stay put while alarms gain events). state=open (default) | all is the v1
// shorthand; states, severities, cameras, owner, from, to and q (Alarm
// Manager) narrow further, and limit with before (alias cursor) pages.
func (s *Server) handleV2Alarms(w http.ResponseWriter, r *http.Request) {
	q, ok := s.parseAlarmQuery(w, r, true)
	if !ok {
		return
	}
	page, ok := parseV2Page(w, r)
	if !ok {
		return
	}
	if raw := r.URL.Query().Get("before"); raw != "" {
		k, ok := decodeCursor(raw)
		if !ok {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "invalid cursor")
			return
		}
		page.after = k
	}
	ids, none := s.scopedCameraIDs(r.Context(), q.CameraIDs)
	if none {
		writeJSON(w, http.StatusOK, map[string]any{"items": []v2Alarm{}})
		return
	}
	// The camera list is already within scope, so no per-row scope check: a
	// scoped caller never sees camera-less (site-wide) alarms either.
	q.CameraIDs = ids
	alarms, err := s.Store.SearchAlarms(r.Context(), q, page.after, page.limit+1)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	v2PageOf(w, alarms, page.limit, alarmPageKey, func(a *store.Alarm) v2Alarm { return s.v2AlarmView(r.Context(), a) })
}

func (s *Server) handleV2Alarm(w http.ResponseWriter, r *http.Request) {
	a, err := s.scopedAlarm(r.Context(), r.PathValue("alarmId"))
	if err != nil {
		writeV2Fail(w, err, "alarm")
		return
	}
	writeJSON(w, http.StatusOK, s.v2AlarmView(r.Context(), a))
}

func (s *Server) handleV2AlarmState(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State         string `json:"state"`
		Note          string `json:"note"`
		SnoozeMinutes *int   `json:"snoozeMinutes"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	var snooze time.Duration
	switch {
	case !store.ValidAlarmState(in.State):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "unknown alarm state")
		return
	case len([]rune(in.Note)) > maxAlarmNote:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "note is too long (500 characters max)")
		return
	case in.SnoozeMinutes != nil && in.State != "Snoozed":
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "snoozeMinutes goes with state Snoozed")
		return
	case in.SnoozeMinutes != nil && (*in.SnoozeMinutes < 1 || *in.SnoozeMinutes > maxSnoozeMinutes):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "snoozeMinutes must be from 1 to 1440")
		return
	case in.State == "Snoozed":
		n := defaultSnoozeMinutes
		if in.SnoozeMinutes != nil {
			n = *in.SnoozeMinutes
		}
		snooze = time.Duration(n) * snoozeUnit
	}
	a, err := s.setAlarmState(r, r.PathValue("alarmId"), in.State, in.Note, snooze)
	if err != nil {
		writeV2Fail(w, err, "alarm")
		return
	}
	writeJSON(w, http.StatusOK, s.v2AlarmView(r.Context(), a))
}

func (s *Server) handleV2LockEvidence(w http.ResponseWriter, r *http.Request) {
	a, err := s.scopedAlarm(r.Context(), r.PathValue("alarmId"))
	if err != nil {
		writeV2Fail(w, err, "alarm")
		return
	}
	ev, err := s.lockEvidence(r.Context(), a, currentUser(r).Name)
	switch {
	case errors.Is(err, errNoClip), errors.Is(err, errClipWriting), errors.Is(err, errNotRecording):
		writeV2Error(w, http.StatusConflict, codeConflict, err.Error())
	case err != nil:
		writeV2Err(w, err)
	default:
		writeJSON(w, http.StatusOK, toV2Evidence(ev))
	}
}

// MARK: - Snooze

// snoozes holds the pending end of each snooze.
var snoozes struct {
	mu     sync.Mutex
	timers map[string]*time.Timer // alarm id →
}

// scheduleSnoozeEnd replaces any pending snooze end for the alarm; d > 0
// returns it to New after d if it is still Snoozed then. In memory only: a
// restart leaves the alarm snoozed until someone acts, like a v1 snooze.
func (s *Server) scheduleSnoozeEnd(id string, d time.Duration) {
	snoozes.mu.Lock()
	defer snoozes.mu.Unlock()
	if old := snoozes.timers[id]; old != nil {
		old.Stop()
		delete(snoozes.timers, id)
	}
	if d <= 0 {
		return
	}
	if snoozes.timers == nil {
		snoozes.timers = map[string]*time.Timer{}
	}
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		snoozes.mu.Lock()
		current := snoozes.timers[id] == t
		if current {
			delete(snoozes.timers, id)
		}
		snoozes.mu.Unlock()
		if current {
			s.endSnooze(id)
		}
	})
	snoozes.timers[id] = t
}

func (s *Server) endSnooze(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := s.Store.Alarm(ctx, id)
	if err != nil || a.State != "Snoozed" {
		return
	}
	if a, err = s.Store.SetAlarmState(ctx, id, "New", "", "Snooze ended"); err != nil {
		slog.Warn("ending alarm snooze failed", "alarm", id, "err", err)
		return
	}
	s.publishAlarmState(a)
}

// MARK: - Events

func (s *Server) handleV2Events(w http.ResponseWriter, r *http.Request) {
	page, ok := parseV2Page(w, r)
	if !ok {
		return
	}
	sc := s.cameraScope(r.Context())
	events, err := scopedRows(sc, page.after, page.limit+1, func(after store.PageKey, n int) ([]store.Event, error) {
		return s.Store.EventsPage(r.Context(), r.URL.Query().Get("cameraId"), after, n)
	}, eventPageKey, func(e *store.Event) bool { return sc.camera(e.CameraID) })
	if err != nil {
		writeV2Err(w, err)
		return
	}
	v2PageOf(w, events, page.limit, eventPageKey, toV2Event)
}

func (s *Server) handleV2Event(w http.ResponseWriter, r *http.Request) {
	e, err := s.scopedEvent(r.Context(), r.PathValue("eventId"))
	if err != nil {
		writeV2Fail(w, err, "event")
		return
	}
	writeJSON(w, http.StatusOK, toV2Event(e))
}

// handleV2EventThumbnail serves the snapshot like the v1 route
// (serveEventThumbnail) but with v2 errors.
func (s *Server) handleV2EventThumbnail(w http.ResponseWriter, r *http.Request) {
	e, err := s.scopedEvent(r.Context(), r.PathValue("eventId"))
	if err == nil && e.Thumbnail == "" {
		err = store.ErrNotFound
	}
	if err != nil {
		writeV2Fail(w, err, "thumbnail")
		return
	}
	s.serveVideoPath(w, r, s.eventRecorder(r.Context(), e), "/rs/events/"+filepath.Base(e.Thumbnail), func(h http.Header) {
		setContentType(h, "image/jpeg")
		h.Set("Cache-Control", "private, max-age=86400")
	})
}

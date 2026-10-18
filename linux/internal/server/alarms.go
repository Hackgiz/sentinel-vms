package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

const (
	alarmMergeWin = 60 * time.Second // Mac rule: detections within 60 s group into one alarm
	inboxKeep     = 30 * 24 * time.Hour
)

// RunMonitors does the management side's housekeeping until ctx ends:
// forgetting recorder message ids older than any replay.
func (s *Server) RunMonitors(ctx context.Context) {
	go s.RunExportSchedules(ctx) // scheduled exports (export_sched.go; WAVE7(C))
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		_ = s.Store.PruneLinkInbox(ctx, time.Now().Add(-inboxKeep))
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// hasClips reports, per alarm, whether a recording covers its start. One
// command per recording server (the one that had the camera then).
func (s *Server) hasClips(ctx context.Context, alarms []store.Alarm) []bool {
	out := make([]bool, len(alarms))
	type batch struct {
		q   link.CoversArgs
		idx []int
	}
	byRec := map[string]*batch{}
	for i, a := range alarms {
		if a.CameraID != "" && a.Kind != "Camera Offline" {
			id := s.recorderAt(ctx, a.CameraID, a.CreatedAt)
			b := byRec[id]
			if b == nil {
				b = &batch{}
				byRec[id] = b
			}
			b.q.Items = append(b.q.Items, link.CoverQuery{CameraID: a.CameraID, At: a.CreatedAt})
			b.idx = append(b.idx, i)
		}
	}
	for id, b := range byRec {
		var res link.CoversResult
		if err := s.command(ctx, id, link.CmdCovers, b.q, &res); err != nil || len(res.Covered) != len(b.idx) {
			continue
		}
		for j, i := range b.idx {
			out[i] = res.Covered[j]
		}
	}
	return out
}

var (
	errNoClip       = errors.New("No recording is linked to this alarm yet, or it has already been cleaned up.")
	errClipWriting  = errors.New("The recording for this alarm is still being written. Try again in a minute.")
	errNotRecording = errors.New("This alarm has no recording to lock.")
)

// lockEvidence has the recording server copy the alarm's recording into the
// evidence vault (outside the retention-managed recordings directory),
// hashed and read-only, and records it. Idempotent: a retry returns the clip
// already locked.
func (s *Server) lockEvidence(ctx context.Context, a *store.Alarm, who string) (*store.Evidence, error) {
	if ev, err := s.Store.EvidenceForAlarm(ctx, a.ID); err == nil {
		return ev, nil
	}
	if a.CameraID == "" || a.Kind == "Camera Offline" {
		return nil, errNotRecording
	}
	caseID := s.Store.NextCaseID(ctx, time.Now())
	rid := s.recorderAt(ctx, a.CameraID, a.CreatedAt) // the server with the alarm's footage
	var f link.EvidenceFile
	if err := s.command(ctx, rid, link.CmdEvidenceLock, link.EvidenceLockArgs{CameraID: a.CameraID, At: a.CreatedAt, CaseID: caseID}, &f); err != nil {
		switch commandCode(err) {
		case link.CodeNoClip:
			return nil, errNoClip
		case link.CodeClipWriting:
			return nil, errClipWriting
		}
		return nil, err
	}
	start, end := time.Unix(f.Start, 0), time.Unix(f.End, 0)
	rangeLabel := start.Format("Jan 2 15:04:05") + " – " + end.Format("15:04:05")
	if f.Writing {
		rangeLabel = start.Format("Jan 2 15:04:05") + " – " + time.Now().Format("15:04:05") + " (recording in progress)"
	}
	ev := &store.Evidence{
		ID: store.NewID(), CaseID: caseID, AlarmID: a.ID, CameraID: a.CameraID, CameraName: a.CameraName,
		Title: "Recorded Segment Export", Range: rangeLabel,
		File: f.File, Size: f.Size, SHA256: f.SHA256, LockedAt: time.Now().Unix(), LockedBy: who,
	}
	if err := s.Store.AddEvidenceOn(ctx, ev, rid); err != nil {
		_ = s.command(ctx, rid, link.CmdEvidenceDelete, link.FileArgs{File: f.File}, nil)
		return nil, err
	}
	_ = s.Store.AddAlarmNote(ctx, a.ID, "Evidence "+caseID+" locked by "+who)
	_ = s.Store.Audit(ctx, who, "Evidence", "Locked evidence", caseID+" for alarm "+a.Title+" ("+a.CameraName+"), sha256 "+f.SHA256[:16]+"…")
	return ev, nil
}

// MARK: - Phone payloads (Mac SentinelHTTPBridge shapes)

func (s *Server) phoneAlarm(ctx context.Context, a *store.Alarm) map[string]any {
	return s.phoneAlarmWithClip(ctx, a, s.hasClips(ctx, []store.Alarm{*a})[0])
}

func (s *Server) phoneAlarmWithClip(ctx context.Context, a *store.Alarm, hasClip bool) map[string]any {
	out := map[string]any{
		"id": a.ID, "title": a.Title, "detail": a.Detail, "severity": a.Severity, "state": a.State,
		"createdAt": a.CreatedAt, "lastEventAt": a.LastEventAt, "kind": a.Kind, "owner": a.Owner,
		"eventCount": a.EventCount, "source": a.CameraName, "hasClip": hasClip, "instructions": s.alarmInstructions(ctx, a),
	}
	if a.CameraID != "" {
		out["cameraID"] = a.CameraID
		out["cameraName"] = a.CameraName
	}
	if ev, err := s.Store.EvidenceForAlarm(ctx, a.ID); err == nil {
		out["evidence"] = phoneEvidence(ev)
	}
	return out
}

func phoneEvidence(e *store.Evidence) map[string]any {
	out := map[string]any{
		"id": e.ID, "caseID": e.CaseID, "title": e.Title, "camera": e.CameraName, "range": e.Range,
		"status": "Locked", "isLocked": true, "lockedAt": e.LockedAt, "sha256": e.SHA256, "lockedBy": e.LockedBy,
	}
	if e.AlarmID != "" {
		out["alertID"] = e.AlarmID
	}
	return out
}

// motionConfidence maps a motion score to the phone's "confidence" percent:
// 70% at the medium threshold, approaching 99% as more of the picture moves.
func motionConfidence(score float64) float64 {
	c := 0.6 + 0.4*math.Min(1, score/(4*detect.Thresholds[2]))
	return math.Min(0.99, math.Round(c*100)/100)
}

func (s *Server) handlePhoneAlerts(w http.ResponseWriter, r *http.Request, _ *store.PairedDevice) {
	alarms, err := s.Store.Alarms(r.Context(), true, 200)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "server busy, try again")
		return
	}
	alarms = s.visibleAlarms(r.Context(), alarms)
	clips := s.hasClips(r.Context(), alarms)
	out := make([]map[string]any, 0, len(alarms))
	for i := range alarms {
		out = append(out, s.phoneAlarmWithClip(r.Context(), &alarms[i], clips[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePhoneAlarmAction(w http.ResponseWriter, r *http.Request, dev *store.PairedDevice) {
	action := r.PathValue("action")
	if action != "acknowledge" && action != "lock-evidence" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	a, err := s.scopedAlarm(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Alarm not found — it may have been cleared on the server.")
		return
	}
	who := dev.Name + " (iPhone)"
	if action == "acknowledge" {
		if !store.IsOpenState(a.State) {
			writeError(w, http.StatusConflict, "This alarm was already "+lower(a.State)+" on the server.")
			return
		}
		// Only New/Snoozed move to Acknowledged; an alarm someone is
		// investigating isn't downgraded. Either way, return its state.
		if a.State == "New" || a.State == "Snoozed" {
			if a, err = s.Store.SetAlarmState(r.Context(), a.ID, "Acknowledged", who, "Acknowledged from "+who); err != nil {
				writeError(w, http.StatusServiceUnavailable, "server busy, try again")
				return
			}
			_ = s.Store.Audit(r.Context(), who, "Alarms", "Acknowledged alarm", a.Title+" ("+a.CameraName+")")
			s.publishAlarmState(a)
		}
		writeJSON(w, http.StatusOK, s.phoneAlarm(r.Context(), a))
		return
	}
	ev, err := s.lockEvidence(r.Context(), a, who)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, phoneEvidence(ev))
}

func (s *Server) handlePhoneEvents(w http.ResponseWriter, r *http.Request, _ *store.PairedDevice) {
	events, err := s.Store.Events(r.Context(), "", 200)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "server busy, try again")
		return
	}
	events = s.visibleEvents(r.Context(), events)
	names := s.cameraNames(r.Context())
	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		name := names[e.CameraID]
		if name == "" {
			name = "Camera"
		}
		conf := e.Score
		if e.Kind == "Motion" {
			conf = motionConfidence(e.Score)
		}
		item := map[string]any{
			"id": e.ID, "cameraID": e.CameraID, "cameraName": name, "kind": e.Kind,
			"confidence": conf, "createdAt": e.CreatedAt, "hasThumbnail": e.HasThumb,
		}
		if e.Description != "" {
			item["description"] = e.Description
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) cameraNames(ctx context.Context) map[string]string {
	names := map[string]string{}
	if cams, err := s.Store.Cameras(ctx); err == nil {
		for _, c := range cams {
			names[c.ID] = c.Name
		}
	}
	return names
}

// eventRecorder is the recording server that reported an event and keeps
// its snapshot: the one that had the camera when it happened (events
// carry no server column of their own).
func (s *Server) eventRecorder(ctx context.Context, e *store.Event) string {
	return s.recorderAt(ctx, e.CameraID, e.CreatedAt)
}

// eventThumbnail fetches an event's snapshot JPEG from its recording
// server, at most max bytes.
func (s *Server) eventThumbnail(ctx context.Context, e *store.Event, max int) ([]byte, bool) {
	res := s.fetchVideo(ctx, s.eventRecorder(ctx, e), "/rs/events/"+filepath.Base(e.Thumbnail), "", max)
	return res.Body, res.Status == http.StatusOK
}

func (s *Server) serveEventThumbnail(w http.ResponseWriter, r *http.Request, id string) {
	e, err := s.scopedEvent(r.Context(), id)
	if err != nil || e.Thumbnail == "" {
		writeError(w, http.StatusNotFound, "no thumbnail")
		return
	}
	s.serveVideoPath(w, r, s.eventRecorder(r.Context(), e), "/rs/events/"+filepath.Base(e.Thumbnail), func(h http.Header) {
		setContentType(h, "image/jpeg")
		h.Set("Cache-Control", "private, max-age=86400")
	})
}

// MARK: - Dashboard API

func (s *Server) handleListAlarms(w http.ResponseWriter, r *http.Request) {
	openOnly := r.URL.Query().Get("state") != "all"
	alarms, err := s.Store.Alarms(r.Context(), openOnly, 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	alarms = s.visibleAlarms(r.Context(), alarms)
	type view struct {
		store.Alarm
		HasClip      bool                `json:"hasClip"`
		Evidence     *store.Evidence     `json:"evidence,omitempty"`
		Instructions []rules.Instruction `json:"instructions"`
	}
	clips := s.hasClips(r.Context(), alarms)
	out := make([]view, 0, len(alarms))
	for i, a := range alarms {
		v := view{Alarm: a, Instructions: s.alarmInstructions(r.Context(), &a), HasClip: clips[i]}
		if ev, err := s.Store.EvidenceForAlarm(r.Context(), a.ID); err == nil {
			v.Evidence = ev
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetAlarmState(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State string `json:"state"`
		Note  string `json:"note"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !store.ValidAlarmState(in.State) {
		writeError(w, http.StatusBadRequest, "unknown alarm state")
		return
	}
	a, err := s.setAlarmState(r, r.PathValue("id"), in.State, in.Note, 0)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "alarm not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// setAlarmState moves an alarm to a (valid) state on behalf of the signed-in
// user, logs "<State> by <user>: <note>" in its response log, audits it and
// announces it on the bus (v1 and v2). snooze > 0 (state Snoozed, v2 only)
// returns the alarm to New after that long; v1 snoozes until someone acts.
func (s *Server) setAlarmState(r *http.Request, id, state, note string, snooze time.Duration) (*store.Alarm, error) {
	if _, err := s.scopedAlarm(r.Context(), id); err != nil {
		return nil, err
	}
	u := currentUser(r)
	line := state
	if snooze > 0 {
		line += " for " + strconv.Itoa(int(snooze/snoozeUnit)) + " min"
	}
	line += " by " + u.Name
	if note != "" {
		line += ": " + note
	}
	a, err := s.Store.SetAlarmState(r.Context(), id, state, u.Name, line)
	if err != nil {
		return nil, err
	}
	s.scheduleSnoozeEnd(a.ID, snooze)
	s.audit(r, "Alarms", "Set alarm "+lower(state), a.Title+" ("+a.CameraName+")")
	s.publishAlarmState(a)
	return a, nil
}

func (s *Server) handleLockEvidence(w http.ResponseWriter, r *http.Request) {
	a, err := s.scopedAlarm(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "alarm not found")
		return
	}
	ev, err := s.lockEvidence(r.Context(), a, currentUser(r).Name)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	events, err := s.Store.Events(r.Context(), r.URL.Query().Get("camera"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	events = s.visibleEvents(r.Context(), events)
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleListEvidence(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.EvidenceList(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sc := s.cameraScope(r.Context())
	list = keepVisible(list, func(e *store.Evidence) bool { return sc.camera(e.CameraID) })
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleEvidenceFile(w http.ResponseWriter, r *http.Request) {
	ev, err := s.Store.EvidenceByID(r.Context(), r.PathValue("id"))
	if err == nil && !s.cameraScope(r.Context()).camera(ev.CameraID) {
		err = store.ErrNotFound
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "evidence not found")
		return
	}
	rid, err := s.Store.EvidenceRecorderID(r.Context(), ev.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "evidence not found")
		return
	}
	s.serveVideoPath(w, r, rid, "/rs/evidence/"+filepath.Base(ev.File), func(h http.Header) {
		s.audit(r, "Evidence", "Downloaded evidence", ev.CaseID)
		setContentType(h, "video/mp4")
		h.Set("Content-Disposition", `attachment; filename="`+ev.CaseID+`.mp4"`)
		h.Set("X-Content-SHA256", ev.SHA256)
	})
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

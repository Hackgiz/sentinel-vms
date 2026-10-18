package server

// API v2: scheduled exports (Wave 7, stream C), and the scheduler that runs
// them. A schedule is "this camera, between 22:00 and 06:00 on these days":
// when a window ends, the scheduler enqueues a long export of it into the
// evidence vault. Times are the server's local zone (rulesLoc, like rules
// and time profiles); an overnight window belongs to the day it started.
// Windows that ended while the server was down are NOT backfilled.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/playback"
	"sentinel-linux/internal/store"
)

// schedTick is how often the scheduler looks for windows that have ended.
const schedTick = 30 * time.Second

func (s *Server) registerV2ExportSchedules(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/export-schedules", auth.ExportEvidence, s.handleV2ExportSchedules)
	s.v2Route(mux, "POST /api/v2/export-schedules", auth.ExportEvidence, s.handleV2CreateExportSchedule)
	s.v2Route(mux, "PATCH /api/v2/export-schedules/{scheduleId}", auth.ExportEvidence, s.handleV2UpdateExportSchedule)
	s.v2Route(mux, "DELETE /api/v2/export-schedules/{scheduleId}", auth.ExportEvidence, s.handleV2DeleteExportSchedule)
}

// MARK: - Window arithmetic

type schedWindow struct{ From, To time.Time }

// scheduleWindows lists the schedule's windows that end in (after, upTo], in
// loc, oldest first. A window starts on one of the schedule's days; when its
// end time is not after its start it ends the next day. Wall-clock times are
// kept across daylight-saving changes.
func scheduleWindows(e *store.ExportSchedule, after, upTo time.Time, loc *time.Location) []schedWindow {
	start, length, err := e.WindowMinutes()
	if err != nil {
		return nil
	}
	hasDay := map[int]bool{}
	for _, d := range e.Days {
		hasDay[d] = true
	}
	var out []schedWindow
	// A window is at most 12 h, so it started today or yesterday, and the
	// window that just ended started at most two calendar days back.
	for off := 0; off <= 2; off++ {
		day := upTo.In(loc).AddDate(0, 0, -off)
		if !hasDay[int(day.Weekday())] {
			continue
		}
		y, m, d := day.Date()
		from := time.Date(y, m, d, 0, start, 0, 0, loc)
		to := time.Date(y, m, d, 0, start+length, 0, 0, loc)
		if to.After(after) && !to.After(upTo) {
			out = append(out, schedWindow{From: from, To: to})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].To.Before(out[j].To) })
	return out
}

// MARK: - Scheduler

// RunExportSchedules fires scheduled exports until ctx ends. It starts
// from "now": windows that ended while the server was down are skipped.
func (s *Server) RunExportSchedules(ctx context.Context) {
	last := s.schedNow()
	every := s.exportJobs.schedEvery
	if every <= 0 {
		every = schedTick
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := s.schedNow()
		s.fireDueSchedules(ctx, last, now)
		last = now
	}
}

func (s *Server) schedNow() time.Time {
	if f := s.exportJobs.now; f != nil {
		return f()
	}
	return time.Now()
}

// fireDueSchedules starts an export for every enabled schedule with a window
// that ended in (after, upTo]. It returns the schedules it fired (or tried
// to). A window already handled (last_run_at) never fires twice.
func (s *Server) fireDueSchedules(ctx context.Context, after, upTo time.Time) []string {
	list, err := s.Store.ExportSchedules(ctx)
	if err != nil {
		return nil
	}
	var fired []string
	for i := range list {
		e := &list[i]
		if !e.Enabled {
			continue
		}
		ws := scheduleWindows(e, after, upTo, s.rulesLoc())
		if len(ws) == 0 {
			continue
		}
		w := ws[len(ws)-1] // after a long stall, only the latest window
		if w.To.Unix() <= e.LastRunAt {
			continue
		}
		fired = append(fired, e.ID)
		s.runScheduledWindow(ctx, e, w)
	}
	return fired
}

// runScheduledWindow enqueues the export of one window and notes how it
// went on the schedule.
func (s *Server) runScheduledWindow(ctx context.Context, e *store.ExportSchedule, w schedWindow) {
	end := w.To.Unix()
	from := w.From
	if w.To.Sub(from) > playback.MaxLongExport { // a daylight-saving change can stretch a window
		from = w.To.Add(-playback.MaxLongExport)
	}
	note := func(status string) { _ = s.Store.RecordExportScheduleRun(ctx, e.ID, end, status) }
	note("running")
	cam := s.cameraLabel(ctx, e.CameraID)
	job, err := s.startExportJob(exportJobParams{
		cameraID: e.CameraID, fromMs: from.UnixMilli(), toMs: w.To.UnixMilli(), dest: exportDestEvidence,
		title: e.Name + " " + longRangeLabel(from, w.To), note: "Scheduled export: " + e.Name,
		who: "Scheduler",
	})
	if err != nil {
		msg := err.Error()
		if commandCode(err) == link.CodeNotFound || errors.Is(err, playback.ErrNoRecording) {
			msg = "nothing was recorded in the window"
		}
		note("failed: " + msg)
		s.auditSystem("Scheduler", "Recordings", "Scheduled export failed", e.Name+" ("+cam+"): "+msg)
		return
	}
	s.auditSystem("Scheduler", "Recordings", "Started scheduled export", e.Name+" ("+cam+") "+longRangeLabel(from, w.To))
	go func() {
		<-job.done
		if v := job.view(); v.State == jobDone {
			note("ok")
		} else {
			note("failed: " + v.Error)
		}
	}()
}

// MARK: - API

type v2ExportSchedule struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CameraID   string `json:"cameraId"`
	Days       []int  `json:"days"`  // 0 = Sunday … 6: the day each window starts
	Start      string `json:"start"` // "HH:MM", server local time
	End        string `json:"end"`   // "HH:MM" or "24:00"; before start = overnight
	Dest       string `json:"dest"`
	Enabled    bool   `json:"enabled"`
	CreatedBy  string `json:"createdBy"`
	CreatedAt  int64  `json:"createdAt"`
	LastRunAt  int64  `json:"lastRunAt"` // end of the last window handled, Unix ms; 0 = never
	LastStatus string `json:"lastStatus"`
}

func toV2ExportSchedule(e *store.ExportSchedule) v2ExportSchedule {
	days := e.Days
	if days == nil {
		days = []int{}
	}
	out := v2ExportSchedule{ID: e.ID, Name: e.Name, CameraID: e.CameraID, Days: days, Start: e.StartHM, End: e.EndHM,
		Dest: e.Dest, Enabled: e.Enabled, CreatedBy: e.CreatedBy, CreatedAt: secToMs(e.CreatedAt), LastStatus: e.LastStatus}
	if e.LastRunAt > 0 {
		out.LastRunAt = secToMs(e.LastRunAt)
	}
	return out
}

// scopedSchedule reads a schedule whose camera the caller may see
// (store.ErrNotFound otherwise).
func (s *Server) scopedSchedule(r *http.Request) (*store.ExportSchedule, error) {
	e, err := s.Store.ExportSchedule(r.Context(), r.PathValue("scheduleId"))
	if err == nil && !s.cameraScope(r.Context()).camera(e.CameraID) {
		return nil, store.ErrNotFound
	}
	return e, err
}

func (s *Server) handleV2ExportSchedules(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ExportSchedules(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	sc := s.cameraScope(r.Context())
	out := make([]v2ExportSchedule, 0, len(list))
	for i := range list {
		if sc.camera(list[i].CameraID) {
			out = append(out, toV2ExportSchedule(&list[i]))
		}
	}
	v2Items(w, out)
}

// saveSchedule validates and stores e, answering the request on failure.
func (s *Server) saveSchedule(w http.ResponseWriter, r *http.Request, e *store.ExportSchedule) bool {
	ctx := r.Context()
	if !cameraIDPattern.MatchString(e.CameraID) || !s.cameraScope(ctx).camera(e.CameraID) {
		writeScopeNotFound(w, r)
		return false
	}
	if _, err := s.Store.Camera(ctx, e.CameraID); err != nil {
		writeV2Fail(w, err, "camera")
		return false
	}
	if err := e.Validate(); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return false
	}
	if err := s.Store.SaveExportSchedule(ctx, e); err != nil {
		writeV2Fail(w, err, "schedule")
		return false
	}
	return true
}

func (s *Server) handleV2CreateExportSchedule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		CameraID string `json:"cameraId"`
		Days     []int  `json:"days"`
		Start    string `json:"start"`
		End      string `json:"end"`
		Dest     string `json:"dest"`
		Enabled  *bool  `json:"enabled"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	e := &store.ExportSchedule{Name: in.Name, CameraID: in.CameraID, Days: in.Days, StartHM: in.Start, EndHM: in.End,
		Dest: in.Dest, Enabled: in.Enabled == nil || *in.Enabled, CreatedBy: currentUser(r).Name}
	if !s.saveSchedule(w, r, e) {
		return
	}
	s.audit(r, "Recordings", "Added export schedule", e.Name+" ("+s.cameraLabel(r.Context(), e.CameraID)+") "+e.StartHM+"–"+e.EndHM)
	writeJSON(w, http.StatusCreated, toV2ExportSchedule(e))
}

func (s *Server) handleV2UpdateExportSchedule(w http.ResponseWriter, r *http.Request) {
	e, err := s.scopedSchedule(r)
	if err != nil {
		writeV2Fail(w, err, "schedule")
		return
	}
	var in struct {
		Name     *string `json:"name"`
		CameraID *string `json:"cameraId"`
		Days     *[]int  `json:"days"`
		Start    *string `json:"start"`
		End      *string `json:"end"`
		Dest     *string `json:"dest"`
		Enabled  *bool   `json:"enabled"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	if in.Name != nil {
		e.Name = *in.Name
	}
	if in.CameraID != nil {
		e.CameraID = *in.CameraID
	}
	if in.Days != nil {
		e.Days = *in.Days
	}
	if in.Start != nil {
		e.StartHM = *in.Start
	}
	if in.End != nil {
		e.EndHM = *in.End
	}
	if in.Dest != nil {
		e.Dest = *in.Dest
	}
	if in.Enabled != nil {
		e.Enabled = *in.Enabled
	}
	if !s.saveSchedule(w, r, e) {
		return
	}
	s.audit(r, "Recordings", "Edited export schedule", e.Name+" ("+s.cameraLabel(r.Context(), e.CameraID)+") "+e.StartHM+"–"+e.EndHM)
	fresh, err := s.Store.ExportSchedule(r.Context(), e.ID)
	if err != nil {
		writeV2Fail(w, err, "schedule")
		return
	}
	writeJSON(w, http.StatusOK, toV2ExportSchedule(fresh))
}

func (s *Server) handleV2DeleteExportSchedule(w http.ResponseWriter, r *http.Request) {
	e, err := s.scopedSchedule(r)
	if err != nil {
		writeV2Fail(w, err, "schedule")
		return
	}
	if err := s.Store.DeleteExportSchedule(r.Context(), e.ID); err != nil {
		writeV2Fail(w, err, "schedule")
		return
	}
	s.audit(r, "Recordings", "Deleted export schedule", e.Name)
	w.WriteHeader(http.StatusNoContent)
}

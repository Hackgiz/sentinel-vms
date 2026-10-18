package server

// API v2: edge recording recovery (WAVE9-REACH.md, stream C). After an
// outage, pull the camera's own SD-card footage (ONVIF Profile G) into the
// gaps of its recordings. The recording server the camera is on does the
// work (link.CmdEdgeRecovery, recorder/edgefill.go); this side starts the
// job, polls it into a task (kind export: it is footage retrieval, behind
// the export permission) and audits it. Recovered segments then appear on
// the timeline like any footage, flagged edge: true.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/tasks"
)

func (s *Server) registerV2EdgeRecovery(mux *http.ServeMux) {
	s.v2Route(mux, "POST /api/v2/cameras/{cameraId}/edge-recovery", auth.ExportEvidence, s.inScope("cameraId", s.handleV2StartEdgeRecovery))
	s.v2Route(mux, "GET /api/v2/edge-recovery/{taskId}", auth.ExportEvidence, s.handleV2EdgeRecovery)
	s.v2Route(mux, "DELETE /api/v2/edge-recovery/{taskId}", auth.ExportEvidence, s.handleV2CancelEdgeRecovery)
}

type edgeJobsState struct {
	mu        sync.Mutex
	jobs      map[string]*edgeJob // task id → job
	pollEvery time.Duration       // 0 = 1 s (tests shorten it)
}

type edgeJob struct {
	taskID   string
	userID   string
	who      string
	cancel   context.CancelFunc
	done     chan struct{}
	cameraID string
	fromMs   int64
	toMs     int64

	mu         sync.Mutex
	st         link.EdgeRecoveryStatus
	state      string
	err        string
	finishedAt time.Time
}

// v2EdgeRecovery is a job as the API shows it.
type v2EdgeRecovery struct {
	TaskID    string  `json:"taskId"`
	State     string  `json:"state"`
	Progress  int     `json:"progress"` // 0-100
	Error     string  `json:"error,omitempty"`
	CameraID  string  `json:"cameraId"`
	FromMs    int64   `json:"fromMs"`
	ToMs      int64   `json:"toMs"`
	Gaps      int     `json:"gaps"`
	Pieces    int     `json:"pieces"`
	Recovered int     `json:"recovered"`
	Skipped   int     `json:"skipped"`
	Seconds   float64 `json:"seconds"`
}

func (j *edgeJob) view() v2EdgeRecovery {
	j.mu.Lock()
	defer j.mu.Unlock()
	pct := 0
	if j.st.Total > 0 {
		pct = j.st.Done * 100 / j.st.Total
	}
	if j.state == jobDone {
		pct = 100
	}
	return v2EdgeRecovery{TaskID: j.taskID, State: j.state, Progress: pct, Error: j.err, CameraID: j.cameraID,
		FromMs: j.fromMs, ToMs: j.toMs, Gaps: j.st.Gaps, Pieces: j.st.Total, Recovered: j.st.Recovered,
		Skipped: j.st.Skipped, Seconds: j.st.Seconds}
}

func (j *edgeJob) finished() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state != jobRunning
}

func (j *edgeJob) set(fn func()) {
	j.mu.Lock()
	fn()
	j.mu.Unlock()
}

func (s *Server) handleV2StartEdgeRecovery(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FromMs int64 `json:"fromMs"`
		ToMs   int64 `json:"toMs"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	cam, err := s.Store.Camera(ctx, r.PathValue("cameraId"))
	if err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	switch {
	case in.FromMs <= 0 || in.ToMs <= in.FromMs:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "fromMs must be before toMs (Unix ms)")
		return
	case in.ToMs-in.FromMs > link.MaxEdgeRecoveryMs:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "a recovery can cover at most 24 hours; choose a shorter range")
		return
	case cam.ONVIFURL == "":
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "this camera was added without ONVIF, so its recordings can't be searched")
		return
	}
	// The footage lands on the camera's recording server; the timeline only
	// asks the servers the camera was on at the time, so the range must lie
	// in its current placement.
	rid := cam.RecorderID
	if s.recorderAt(ctx, cam.ID, in.FromMs/1000) != rid || s.recorderAt(ctx, cam.ID, in.ToMs/1000) != rid {
		writeV2Error(w, http.StatusConflict, codeConflict, "the camera was on another recording server during part of that range; recover a range after it moved")
		return
	}
	sctx, cancelStart := context.WithTimeout(context.Background(), jobStartTimeout)
	defer cancelStart()
	var st link.EdgeRecoveryStatus
	if err := s.command(sctx, rid, link.CmdEdgeRecovery, link.EdgeRecoveryArgs{Op: link.EdgeOpStart, CameraID: cam.ID, From: in.FromMs, To: in.ToMs}, &st); err != nil {
		writeV2Err(w, err)
		return
	}
	who := currentUser(r)
	from, to := time.UnixMilli(in.FromMs), time.UnixMilli(in.ToMs)
	task := tasks.Default.Start(tasks.KindExport, "Edge recovery: "+cam.Name+" "+longRangeLabel(from, to))
	jctx, cancel := context.WithCancel(context.Background())
	job := &edgeJob{taskID: task.ID(), userID: who.ID, who: who.Name, cancel: cancel, done: make(chan struct{}),
		cameraID: cam.ID, fromMs: in.FromMs, toMs: in.ToMs, st: st, state: jobRunning}
	reg := &s.edgeJobs
	reg.mu.Lock()
	if reg.jobs == nil {
		reg.jobs = map[string]*edgeJob{}
	}
	for k, o := range reg.jobs {
		if o.finishedSince(jobKeep) {
			delete(reg.jobs, k)
		}
	}
	reg.jobs[job.taskID] = job
	every := reg.pollEvery
	reg.mu.Unlock()
	if every <= 0 {
		every = time.Second
	}
	s.audit(r, "Recordings", "Started edge recovery", cam.Name+" "+longRangeLabel(from, to)+fmt.Sprintf(" (%d gap(s))", st.Gaps))
	go func() {
		defer close(job.done)
		defer cancel()
		s.runEdgeJob(jctx, job, task, rid, st.JobID, cam.Name, every)
	}()
	writeJSON(w, http.StatusCreated, job.view())
}

// runEdgeJob polls the recording server's job into the task until it ends.
func (s *Server) runEdgeJob(ctx context.Context, job *edgeJob, task *tasks.Handle, rid, remoteID, camName string, every time.Duration) {
	end := func(state, msg string) {
		job.set(func() { job.state, job.err, job.finishedAt = state, msg, time.Now() })
	}
	canceled := func() {
		bg, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var st link.EdgeRecoveryStatus
		if s.command(bg, rid, link.CmdEdgeRecovery, link.EdgeRecoveryArgs{Op: link.EdgeOpCancel, JobID: remoteID}, &st) == nil {
			job.set(func() { job.st = st })
		}
		task.Fail(errors.New("canceled"))
		end(jobCanceled, "canceled")
	}
	label := camName + " " + longRangeLabel(time.UnixMilli(job.fromMs), time.UnixMilli(job.toMs))
	tick := time.NewTicker(every)
	defer tick.Stop()
	failures := 0
	// A job with nothing to recover is done when it starts.
	poll := job.remote().State == link.EdgeRunning
	for {
		if poll {
			select {
			case <-ctx.Done():
				canceled()
				return
			case <-tick.C:
			}
			var st link.EdgeRecoveryStatus
			pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := s.command(pctx, rid, link.CmdEdgeRecovery, link.EdgeRecoveryArgs{Op: link.EdgeOpStatus, JobID: remoteID}, &st)
			cancel()
			if ctx.Err() != nil {
				canceled()
				return
			}
			if err != nil {
				failures++
				if commandCode(err) == link.CodeNotFound || failures >= jobMaxPollFailure {
					err = fmt.Errorf("lost the recovery on its recording server: %w", err)
					task.Fail(err)
					end(jobFailed, err.Error())
					s.auditSystem(job.who, "Recordings", "Edge recovery failed", label+": "+err.Error())
					return
				}
				continue
			}
			failures = 0
			job.set(func() { job.st = st })
		}
		poll = true
		st := job.remote()
		switch st.State {
		case link.EdgeRunning:
			task.Progress(job.view().Progress)
		case link.EdgeCanceled:
			task.Fail(errors.New("canceled"))
			end(jobCanceled, "canceled")
			return
		case link.EdgeFailed:
			task.Fail(errors.New(st.Error))
			end(jobFailed, st.Error)
			s.auditSystem(job.who, "Recordings", "Edge recovery failed", label+": "+st.Error)
			return
		default: // done
			task.Finish()
			end(jobDone, st.Error) // a partial failure keeps its note
			s.auditSystem(job.who, "Recordings", "Recovered footage from camera",
				fmt.Sprintf("%s: %d segment(s), %s", label, st.Recovered, time.Duration(st.Seconds*float64(time.Second)).Round(time.Second)))
			return
		}
	}
}

func (j *edgeJob) remote() link.EdgeRecoveryStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st
}

func (j *edgeJob) finishedSince(d time.Duration) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state != jobRunning && time.Since(j.finishedAt) > d
}

func (s *Server) edgeJob(r *http.Request) *edgeJob {
	s.edgeJobs.mu.Lock()
	j := s.edgeJobs.jobs[r.PathValue("taskId")]
	s.edgeJobs.mu.Unlock()
	if j == nil || !s.cameraScope(r.Context()).camera(j.cameraID) || j.userID != currentUser(r).ID {
		return nil
	}
	return j
}

func (s *Server) handleV2EdgeRecovery(w http.ResponseWriter, r *http.Request) {
	j := s.edgeJob(r)
	if j == nil {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "recovery job not found")
		return
	}
	writeJSON(w, http.StatusOK, j.view())
}

// handleV2CancelEdgeRecovery stops a running job; segments already placed
// stay (they are complete footage). Cancelling a finished job changes nothing.
func (s *Server) handleV2CancelEdgeRecovery(w http.ResponseWriter, r *http.Request) {
	j := s.edgeJob(r)
	if j == nil {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "recovery job not found")
		return
	}
	if !j.finished() {
		j.cancel()
		select {
		case <-j.done:
		case <-time.After(20 * time.Second):
		}
		s.audit(r, "Recordings", "Canceled edge recovery", s.cameraLabel(r.Context(), j.cameraID)+" "+longRangeLabel(time.UnixMilli(j.fromMs), time.UnixMilli(j.toMs)))
	}
	writeJSON(w, http.StatusOK, j.view())
}

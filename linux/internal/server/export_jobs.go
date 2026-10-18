package server

// API v2: long (chunked) export jobs (Wave 7, stream C). A job covers up to
// 12 hours of one camera; the recording server holding the footage splits
// it into keyframe-aligned pieces, cuts and joins them (playback.LongExport,
// link.CmdExportLong) while this side polls its progress into a
// tasks.KindExport task. The result goes to the evidence vault or is kept
// for the browser to download, through the same paths as a short export.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/playback"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

func (s *Server) registerV2ExportJobs(mux *http.ServeMux) {
	s.v2Route(mux, "POST /api/v2/export-jobs", auth.ExportEvidence, s.handleV2StartExportJob)
	s.v2Route(mux, "GET /api/v2/export-jobs/{taskId}", auth.ExportEvidence, s.handleV2ExportJob)
	s.v2Route(mux, "DELETE /api/v2/export-jobs/{taskId}", auth.ExportEvidence, s.handleV2CancelExportJob)
	s.v2Route(mux, "GET /api/v2/export-jobs/{taskId}/download", auth.ExportEvidence, s.handleV2ExportJobDownload)
}

// Job states (v2 payload).
const (
	jobRunning  = "running"
	jobDone     = "done"
	jobFailed   = "failed"
	jobCanceled = "canceled"
)

const (
	exportDestDownload = "download"
	exportDestEvidence = "evidence"

	jobKeep           = time.Hour // a finished job stays readable this long
	jobStartTimeout   = 30 * time.Second
	jobMaxPollFailure = 10 // consecutive failed status polls before giving up
)

type exportJobsState struct {
	mu   sync.Mutex
	jobs map[string]*exportJob // task id → job

	pollEvery  time.Duration    // 0 = 1 s (tests shorten it)
	now        func() time.Time // the scheduler's clock; nil = time.Now (tests)
	schedEvery time.Duration    // scheduler tick; 0 = schedTick (tests)
}

type exportJob struct {
	taskID string
	userID string // "" for jobs the scheduler started
	who    string // name for audit rows and the evidence record
	cancel context.CancelFunc
	done   chan struct{} // closed when the job has finished

	cameraID string
	fromMs   int64
	toMs     int64
	dest     string

	mu         sync.Mutex
	state      string
	progress   int
	err        string
	download   string
	token      string // dest download: the prepared clip on its recording server
	recorderID string
	sha256     string
	preparedAt time.Time
	filename   string
	evidenceID string
	caseID     string
	finishedAt time.Time
}

type exportJobParams struct {
	cameraID     string
	fromMs, toMs int64
	dest         string
	title, note  string
	userID, who  string
	opts         exportOpts // WAVE8(D); zero for scheduler jobs
	caseID       string     // reserved case id (watermarked evidence jobs)
}

// v2ExportJob is a job as the API shows it.
type v2ExportJob struct {
	TaskID      string `json:"taskId"`
	State       string `json:"state"`
	Progress    int    `json:"progress"` // 0-100
	Error       string `json:"error,omitempty"`
	CameraID    string `json:"cameraId"`
	FromMs      int64  `json:"fromMs"`
	ToMs        int64  `json:"toMs"`
	Dest        string `json:"dest"`
	DownloadURL string `json:"downloadUrl,omitempty"` // dest download, once done
	Filename    string `json:"filename,omitempty"`
	EvidenceID  string `json:"evidenceId,omitempty"` // dest evidence, once done
	CaseID      string `json:"caseId,omitempty"`
}

func (j *exportJob) view() v2ExportJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return v2ExportJob{TaskID: j.taskID, State: j.state, Progress: j.progress, Error: j.err, CameraID: j.cameraID,
		FromMs: j.fromMs, ToMs: j.toMs, Dest: j.dest, DownloadURL: j.download, Filename: j.filename,
		EvidenceID: j.evidenceID, CaseID: j.caseID}
}

func (j *exportJob) set(fn func()) {
	j.mu.Lock()
	fn()
	j.mu.Unlock()
}

// longRangeLabel names a range that may cross midnight.
func longRangeLabel(from, to time.Time) string {
	return from.Format("Jan 2 15:04") + " – " + to.Format("Jan 2 15:04")
}

// startExportJob has the recording server holding the footage start a long
// export and returns the running job. Refusals the recording server makes up
// front (no recording, not enough disk, busy) come back as the error.
func (s *Server) startExportJob(p exportJobParams) (*exportJob, error) {
	from, to := time.UnixMilli(p.fromMs), time.UnixMilli(p.toMs)
	ctx, cancelStart := context.WithTimeout(context.Background(), jobStartTimeout)
	defer cancelStart()
	rid := s.recorderAt(ctx, p.cameraID, p.fromMs/1000)
	start := link.ExportLongArgs{Op: link.ExportLongOpStart, CameraID: p.cameraID, From: p.fromMs, To: p.toMs, Blur: p.opts.blur}
	releaseCase := func() {}
	if p.opts.watermark {
		if p.dest == exportDestEvidence { // the watermark carries the case id
			p.caseID, releaseCase = s.reserveCaseID(ctx)
		}
		start.Watermark = p.opts.watermarkText(p.who, time.Now(), p.caseID)
	}
	var st link.ExportLongStatus
	if err := s.command(ctx, rid, link.CmdExportLong, start, &st); err != nil {
		releaseCase()
		return nil, err
	}
	cam := s.cameraLabel(ctx, p.cameraID)
	task := tasks.Default.Start(tasks.KindExport, "Long export: "+cam+" "+longRangeLabel(from, to))
	jctx, cancel := context.WithCancel(context.Background())
	job := &exportJob{taskID: task.ID(), userID: p.userID, who: p.who, cancel: cancel, done: make(chan struct{}),
		cameraID: p.cameraID, fromMs: p.fromMs, toMs: p.toMs, dest: p.dest, state: jobRunning}

	reg := &s.exportJobs
	reg.mu.Lock()
	if reg.jobs == nil {
		reg.jobs = map[string]*exportJob{}
	}
	for k, o := range reg.jobs {
		if o.isFinished() && time.Since(o.finishedAtTime()) > jobKeep {
			delete(reg.jobs, k)
		}
	}
	reg.jobs[job.taskID] = job
	every := reg.pollEvery
	reg.mu.Unlock()
	if every <= 0 {
		every = time.Second
	}
	go func() {
		defer close(job.done)
		defer cancel()
		defer releaseCase()
		s.runExportJob(jctx, job, task, rid, st.JobID, p, every)
	}()
	return job, nil
}

func (j *exportJob) isFinished() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state != jobRunning
}

func (j *exportJob) finishedAtTime() time.Time {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.finishedAt
}

// runExportJob polls the recording server's job until it ends, mirrors its
// progress into the task, and files the result.
func (s *Server) runExportJob(ctx context.Context, job *exportJob, task *tasks.Handle, rid, remoteID string, p exportJobParams, every time.Duration) {
	fail := func(err error) {
		task.Fail(err)
		job.set(func() { job.state, job.err, job.finishedAt = jobFailed, err.Error(), time.Now() })
		s.auditSystem(p.who, "Recordings", "Long export failed", s.cameraLabel(context.Background(), p.cameraID)+": "+err.Error())
	}
	canceled := func() {
		bg, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = s.command(bg, rid, link.CmdExportLong, link.ExportLongArgs{Op: link.ExportLongOpCancel, JobID: remoteID}, nil)
		task.Fail(errors.New("canceled"))
		job.set(func() { job.state, job.err, job.finishedAt = jobCanceled, "canceled", time.Now() })
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			canceled()
			return
		case <-tick.C:
		}
		var st link.ExportLongStatus
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.command(pctx, rid, link.CmdExportLong, link.ExportLongArgs{Op: link.ExportLongOpStatus, JobID: remoteID}, &st)
		cancel()
		if ctx.Err() != nil {
			canceled()
			return
		}
		if err != nil {
			failures++
			if commandCode(err) == link.CodeNotFound || failures >= jobMaxPollFailure {
				fail(fmt.Errorf("lost the export on its recording server: %w", err))
				return
			}
			continue
		}
		failures = 0
		if st.Total > 0 {
			pct := st.Done * 100 / st.Total
			task.Progress(pct)
			job.set(func() { job.progress = pct })
		}
		switch st.State {
		case link.ExportLongRunning:
		case link.ExportLongCanceled:
			task.Fail(errors.New("canceled"))
			job.set(func() { job.state, job.err, job.finishedAt = jobCanceled, "canceled", time.Now() })
			return
		case link.ExportLongFailed:
			fail(errors.New(st.Error))
			return
		case link.ExportLongDone:
			if err := s.finishExportJob(ctx, job, rid, st, p); err != nil {
				fail(err)
				return
			}
			task.Finish()
			job.set(func() { job.state, job.progress, job.finishedAt = jobDone, 100, time.Now() })
			return
		}
	}
}

// finishExportJob files a finished long clip: into the evidence vault, or
// as a prepared download for the user who asked.
func (s *Server) finishExportJob(ctx context.Context, job *exportJob, rid string, st link.ExportLongStatus, p exportJobParams) error {
	from, to := time.UnixMilli(p.fromMs), time.UnixMilli(p.toMs)
	cam := s.cameraLabel(ctx, p.cameraID)
	if p.dest == exportDestDownload {
		filename := safeFilename(cam) + "-" + from.Format("20060102-150405") + "-long.mp4"
		job.set(func() {
			job.download, job.filename = "/api/v2/export-jobs/"+job.taskID+"/download", filename
			job.token, job.recorderID, job.sha256, job.preparedAt = st.Token, rid, st.SHA256, time.Now()
		})
		s.auditSystem(p.who, "Recordings", "Exported long clip", cam+" "+longRangeLabel(from, to)+", sha256 "+short(st.SHA256))
		return nil
	}
	caseID := p.caseID
	if caseID == "" {
		caseID = s.Store.NextCaseID(ctx, time.Now())
	}
	var f link.EvidenceFile
	if err := s.command(ctx, rid, link.CmdEvidenceAdopt, link.EvidenceAdoptArgs{Token: st.Token, CaseID: caseID}, &f); err != nil {
		return err
	}
	title := strings.TrimSpace(p.title)
	if title == "" {
		title = "Long Export"
	} else if len(title) > maxEvidenceTitle {
		title = title[:maxEvidenceTitle]
	}
	note := strings.TrimSpace(p.note)
	if len(note) > maxEvidenceNote {
		note = note[:maxEvidenceNote]
	}
	ev := &store.EvidenceCase{
		Evidence: store.Evidence{
			ID: store.NewID(), CaseID: caseID, CameraID: p.cameraID, CameraName: cam, Title: title,
			Range: longRangeLabel(from, to),
			File:  f.File, Size: f.Size, SHA256: f.SHA256, LockedAt: time.Now().Unix(), LockedBy: p.who,
		},
		Note: note, FromMs: p.fromMs, ToMs: p.toMs, RecorderID: rid,
	}
	if err := s.Store.AddEvidenceCase(ctx, ev); err != nil {
		_ = s.command(context.Background(), rid, link.CmdEvidenceDelete, link.FileArgs{File: f.File}, nil)
		return err
	}
	job.set(func() { job.evidenceID, job.caseID = ev.ID, caseID })
	s.auditSystem(p.who, "Evidence", "Locked evidence", caseID+" long clip "+cam+" "+ev.Range+", sha256 "+short(ev.SHA256))
	return nil
}

func short(sum string) string {
	if len(sum) > 16 {
		return sum[:16] + "…"
	}
	return sum
}

// auditSystem writes an audit row outside a request (job completion, the
// scheduler).
func (s *Server) auditSystem(user, area, action, detail string) {
	if user == "" {
		user = "System"
	}
	_ = s.Store.Audit(context.Background(), user, area, action, detail)
}

// MARK: - Handlers

func (s *Server) exportJob(r *http.Request) *exportJob {
	s.exportJobs.mu.Lock()
	j := s.exportJobs.jobs[r.PathValue("taskId")]
	s.exportJobs.mu.Unlock()
	if j == nil || !s.cameraScope(r.Context()).camera(j.cameraID) {
		return nil
	}
	if j.userID != "" && j.userID != currentUser(r).ID { // scheduler jobs are everyone's
		return nil
	}
	return j
}

func (s *Server) handleV2StartExportJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CameraID string `json:"cameraId"`
		FromMs   int64  `json:"fromMs"`
		ToMs     int64  `json:"toMs"`
		Dest     string `json:"dest"`
		Title    string `json:"title"`
		Note     string `json:"note"`
		exportOptsIn
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	if !cameraIDPattern.MatchString(in.CameraID) || !s.cameraScope(ctx).camera(in.CameraID) {
		writeScopeNotFound(w, r)
		return
	}
	if _, err := s.Store.Camera(ctx, in.CameraID); err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	opts, oerr := parseExportOptions(in.exportOptsIn)
	if oerr != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, oerr.Error())
		return
	}
	switch {
	case in.Dest != exportDestDownload && in.Dest != exportDestEvidence:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, `dest must be "download" or "evidence"`)
		return
	case in.FromMs <= 0 || in.ToMs <= in.FromMs:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "fromMs must be before toMs (Unix ms)")
		return
	case time.Duration(in.ToMs-in.FromMs)*time.Millisecond > playback.MaxLongExport:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "a long export can cover at most 12 hours; choose a shorter range")
		return
	}
	who := currentUser(r)
	job, err := s.startExportJob(exportJobParams{cameraID: in.CameraID, fromMs: in.FromMs, toMs: in.ToMs, dest: in.Dest,
		title: in.Title, note: in.Note, userID: who.ID, who: who.Name, opts: opts})
	if err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "Recordings", "Started long export", s.cameraLabel(ctx, in.CameraID)+" "+longRangeLabel(time.UnixMilli(in.FromMs), time.UnixMilli(in.ToMs))+" to "+in.Dest+opts.auditNote())
	writeJSON(w, http.StatusCreated, job.view())
}

func (s *Server) handleV2ExportJob(w http.ResponseWriter, r *http.Request) {
	j := s.exportJob(r)
	if j == nil {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "export job not found")
		return
	}
	writeJSON(w, http.StatusOK, j.view())
}

// handleV2CancelExportJob stops a running job (its temp pieces are removed
// by the recording server). Cancelling a finished job changes nothing.
func (s *Server) handleV2CancelExportJob(w http.ResponseWriter, r *http.Request) {
	j := s.exportJob(r)
	if j == nil {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "export job not found")
		return
	}
	if !j.isFinished() {
		j.cancel()
		select {
		case <-j.done:
		case <-time.After(20 * time.Second):
		}
		s.audit(r, "Recordings", "Canceled long export", s.cameraLabel(r.Context(), j.cameraID)+" "+longRangeLabel(time.UnixMilli(j.fromMs), time.UnixMilli(j.toMs)))
	}
	writeJSON(w, http.StatusOK, j.view())
}

// handleV2ExportJobDownload serves a finished download-destination job's
// MP4 (kept exportKeep after it was prepared) to the user who started it.
func (s *Server) handleV2ExportJobDownload(w http.ResponseWriter, r *http.Request) {
	j := s.exportJob(r)
	if j == nil {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "export job not found")
		return
	}
	j.mu.Lock()
	token, rid, sum, name, at := j.token, j.recorderID, j.sha256, j.filename, j.preparedAt
	j.mu.Unlock()
	if token == "" || time.Since(at) > exportKeep {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "the export isn't ready or has expired — export it again")
		return
	}
	s.serveVideoPath(w, r, rid, "/rs/exports/"+token, func(h http.Header) {
		if r.Header.Get("Range") == "" {
			s.audit(r, "Recordings", "Downloaded clip", name)
		}
		setContentType(h, "video/mp4")
		h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
		h.Set("X-Content-SHA256", sum)
	})
}

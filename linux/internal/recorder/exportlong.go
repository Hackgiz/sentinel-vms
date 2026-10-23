package recorder

// Long exports (Wave 7, stream C): link.CmdExportLong. The management side
// starts a job and polls it; the work (playback.LongExport) runs here,
// next to the footage. The result is a prepared clip in the exports
// directory, exactly like link.CmdExportClip's, so the existing download and
// evidence-adopt paths take it from there.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/playback"
)

// longKeep is how long a finished job's status is remembered.
const longKeep = time.Hour

type longJob struct {
	cancel context.CancelFunc
	work   string

	mu      sync.Mutex
	st      link.ExportLongStatus
	endedAt time.Time
}

type longExportState struct {
	mu   sync.Mutex
	jobs map[string]*longJob
}

// longWorkDir holds the pieces of running long exports. It is not the
// exports directory: sweepExports would delete a work directory that has
// been busy for longer than half an hour.
func (r *Recorder) longWorkDir() string { return filepath.Join(r.DataDir, "exports-long") }

func (j *longJob) status() link.ExportLongStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st
}

func (r *Recorder) exportLong(a link.ExportLongArgs) (link.ExportLongStatus, string, string) {
	switch a.Op {
	case link.ExportLongOpStart:
		return r.startLongExport(a)
	case link.ExportLongOpStatus, link.ExportLongOpCancel:
		r.longExp.mu.Lock()
		j := r.longExp.jobs[a.JobID]
		r.longExp.mu.Unlock()
		if j == nil {
			return link.ExportLongStatus{}, link.CodeNotFound, "no such export job (the server may have restarted)"
		}
		if a.Op == link.ExportLongOpCancel {
			j.cancel()
			// Give the job a moment to stop ffmpeg and clean up, so the caller
			// sees the final state.
			for i := 0; i < 50 && j.status().State == link.ExportLongRunning; i++ {
				time.Sleep(20 * time.Millisecond)
			}
		}
		return j.status(), "", ""
	}
	return link.ExportLongStatus{}, link.CodeBadRequest, "unknown export operation"
}

func (r *Recorder) startLongExport(a link.ExportLongArgs) (link.ExportLongStatus, string, string) {
	none := link.ExportLongStatus{}
	if !cameraIDPattern.MatchString(a.CameraID) {
		return none, link.CodeBadRequest, "bad camera id"
	}
	ffmpeg := r.FFmpegPath()
	if ffmpeg == "" {
		return none, link.CodeUnavailable, playback.ErrNoFFmpeg.Error()
	}
	from, to := time.UnixMilli(a.From), time.UnixMilli(a.To)
	lib := r.library()
	files, err := lib.Files(a.CameraID, from, to)
	if err != nil {
		return none, link.CodeInternal, err.Error()
	}
	files = playback.OneTier(files, from, to) // once, so every chunk is cut from the same tier
	if err := os.MkdirAll(r.exportsDir(), 0o750); err != nil {
		return none, link.CodeInternal, err.Error()
	}
	if err := os.MkdirAll(r.longWorkDir(), 0o750); err != nil {
		return none, link.CodeInternal, err.Error()
	}
	opts, oerr := r.exportOptions(a.CameraID, a.Watermark, a.Blur)
	if oerr != nil {
		return none, link.CodeBadRequest, oerr.Error()
	}
	// Refuse now, not an hour into the job: range, recordings, free disk.
	plan, err := playback.PreflightLong(files, from, to, r.longWorkDir(), nil)
	if err != nil {
		return none, longCode(err), err.Error()
	}
	select {
	case r.playback.slots <- struct{}{}:
	default:
		return none, link.CodeBusy, "other exports are running — try again in a moment"
	}
	// Sweep, create and register under one lock: a concurrent start must not
	// sweep this job's directory before it is registered.
	r.longExp.mu.Lock()
	r.sweepLongWorkLocked()
	work, err := os.MkdirTemp(r.longWorkDir(), "job-")
	if err != nil {
		r.longExp.mu.Unlock()
		<-r.playback.slots
		return none, link.CodeInternal, err.Error()
	}
	ctx, cancel := context.WithCancel(r.ctx())
	job := &longJob{cancel: cancel, work: work}
	id := randomHex(8)
	job.st = link.ExportLongStatus{JobID: id, State: link.ExportLongRunning, Total: len(plan.Pieces) + 1}
	if r.longExp.jobs == nil {
		r.longExp.jobs = map[string]*longJob{}
	}
	for k, o := range r.longExp.jobs { // forget old finished jobs
		if st := o.status(); st.State != link.ExportLongRunning && time.Since(o.endedAt) > longKeep {
			delete(r.longExp.jobs, k)
		}
	}
	r.longExp.jobs[id] = job
	r.longExp.mu.Unlock()

	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		defer func() { <-r.playback.slots }()
		defer cancel()
		defer os.RemoveAll(work)
		r.runLongExport(ctx, job, ffmpeg, files, a, opts)
	}()
	return job.status(), "", ""
}

func longCode(err error) string {
	switch {
	case errors.Is(err, playback.ErrNoRecording):
		return link.CodeNotFound
	case errors.Is(err, playback.ErrNoFFmpeg), errors.Is(err, playback.ErrDiskSpace):
		return link.CodeUnavailable
	}
	return link.CodeBadRequest
}

func (r *Recorder) runLongExport(ctx context.Context, job *longJob, ffmpeg string, files []playback.File, a link.ExportLongArgs, opts playback.ExportOptions) {
	from, to := time.UnixMilli(a.From), time.UnixMilli(a.To)
	finish := func(fn func(st *link.ExportLongStatus)) {
		job.mu.Lock()
		fn(&job.st)
		job.endedAt = time.Now()
		job.mu.Unlock()
	}
	out := filepath.Join(job.work, "long.mp4")
	err := playback.LongExport(ctx, playback.LongOptions{
		FFmpeg: ffmpeg, Lib: r.library(), CameraID: a.CameraID, Files: files, From: from, To: to,
		WorkDir: job.work, Out: out, Export: opts,
		Progress: func(done, total int) {
			job.mu.Lock()
			job.st.Done, job.st.Total = done, total
			job.mu.Unlock()
		},
	})
	var size int64
	var sum, token string
	if err == nil {
		if size, sum, err = hashFile(out); err == nil {
			token = randomHex(16)
			err = os.Rename(out, filepath.Join(r.exportsDir(), token+".mp4"))
		}
	}
	switch {
	case err == nil:
		r.auditCommand("Recordings", "Exported long clip", r.cameraName(a.CameraID)+" "+
			from.UTC().Format(time.RFC3339)+" – "+to.UTC().Format(time.RFC3339)+", sha256 "+sum)
		finish(func(st *link.ExportLongStatus) {
			st.State, st.Done, st.Token, st.Size, st.SHA256 = link.ExportLongDone, st.Total, token, size, sum
		})
	case errors.Is(err, context.Canceled):
		finish(func(st *link.ExportLongStatus) { st.State, st.Error = link.ExportLongCanceled, "canceled" })
	default:
		slog.Warn("long export failed", "camera", a.CameraID, "err", err)
		finish(func(st *link.ExportLongStatus) {
			st.State, st.Error, st.Code = link.ExportLongFailed, err.Error(), longCode(err)
		})
	}
}

// sweepLongWorkLocked removes work directories no running job owns (left
// behind by a crash or a restart). The caller holds longExp.mu.
func (r *Recorder) sweepLongWorkLocked() {
	entries, err := os.ReadDir(r.longWorkDir())
	if err != nil {
		return
	}
	active := map[string]bool{}
	for _, j := range r.longExp.jobs {
		if j.status().State == link.ExportLongRunning {
			active[j.work] = true
		}
	}
	for _, e := range entries {
		if p := filepath.Join(r.longWorkDir(), e.Name()); !active[p] {
			_ = os.RemoveAll(p)
		}
	}
}

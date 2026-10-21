package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sentinel-linux/internal/tasks"
)

// TaskKind is the tasks-list kind of a model download.
const TaskKind = "model-download"

// DownloadTimeout bounds one model's whole download.
const DownloadTimeout = 10 * time.Minute

const partSuffix = ".part"

// Job states (ModelDownload.state).
const (
	JobRunning  = "running"
	JobDone     = "done"
	JobFailed   = "failed"
	JobCanceled = "canceled"
)

// HTTPClient fetches models (tests replace it). No proxy support beyond
// what net/http gives (HTTPS_PROXY and friends).
var HTTPClient = &http.Client{}

// ErrNoSource is returned for a model that cannot be downloaded (bundled,
// custom, or no pinned URL).
var ErrNoSource = errors.New("this model has no download source")

// Job is one model download.
type Job struct {
	ModelID string
	TaskID  string

	cancel   context.CancelFunc
	finished chan struct{}

	mu         sync.Mutex
	state      string
	done       int64
	total      int64
	err        string
	startedAt  time.Time
	finishedAt time.Time
}

// JobView is a Job as the API shows it (ModelDownload).
type JobView struct {
	ModelID    string `json:"modelId"`
	TaskID     string `json:"taskId"`
	State      string `json:"state"`
	Done       int64  `json:"done"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
	StartedAt  int64  `json:"startedAt,omitempty"`
	FinishedAt int64  `json:"finishedAt,omitempty"`
}

// View is the job's current state (nil for a nil job).
func (j *Job) View() *JobView {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	v := &JobView{ModelID: j.ModelID, TaskID: j.TaskID, State: j.state, Done: j.done, Total: j.total,
		Error: j.err, StartedAt: j.startedAt.UnixMilli()}
	if !j.finishedAt.IsZero() {
		v.FinishedAt = j.finishedAt.UnixMilli()
	}
	return v
}

// Running reports whether the download is still going.
func (j *Job) Running() bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state == JobRunning
}

// Cancel stops the download (the partial file is deleted) and waits up to
// wait for it to stop.
func (j *Job) Cancel(wait time.Duration) {
	if j == nil {
		return
	}
	j.cancel()
	select {
	case <-j.finished:
	case <-time.After(wait):
	}
}

// Wait blocks until the download has finished.
func (j *Job) Wait() {
	if j != nil {
		<-j.finished
	}
}

func (j *Job) finish(state, err string) {
	j.mu.Lock()
	j.state, j.err, j.finishedAt = state, err, time.Now()
	j.mu.Unlock()
}

// Download starts fetching catalog model id into Dir(dataDir) and returns
// at once; progress shows in reg (kind TaskKind, title "Download <title>").
// Each file goes to <file>.part, is checked against its SHA-256 and then
// renamed into place. The caller keeps one download per model at a time.
func Download(ctx context.Context, dataDir, id string, reg *tasks.Registry) (*Job, error) {
	e, ok := Lookup(id)
	if !ok {
		return nil, fmt.Errorf("unknown model %q", id)
	}
	return DownloadEntry(ctx, dataDir, e, reg)
}

// DownloadEntry is Download for an explicit entry (tests).
func DownloadEntry(ctx context.Context, dataDir string, e Entry, reg *tasks.Registry) (*Job, error) {
	if !e.Downloadable() {
		return nil, ErrNoSource
	}
	dir := Dir(dataDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("models directory: %w", err)
	}
	if reg == nil {
		reg = tasks.Default
	}
	task := reg.Start(TaskKind, "Download "+e.Title)
	ctx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	j := &Job{ModelID: e.ID, TaskID: task.ID(), cancel: cancel, finished: make(chan struct{}),
		state: JobRunning, total: e.SizeBytes, startedAt: time.Now()}
	go func() {
		defer close(j.finished)
		defer cancel()
		err := j.run(ctx, dir, e, task)
		switch {
		case err == nil:
			task.Finish()
			j.finish(JobDone, "")
		case errors.Is(ctx.Err(), context.Canceled):
			task.Fail(errors.New("canceled"))
			j.finish(JobCanceled, "canceled")
		default:
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				err = fmt.Errorf("timed out after %s", DownloadTimeout)
			}
			task.Fail(err)
			j.finish(JobFailed, err.Error())
		}
	}()
	return j, nil
}

func (j *Job) run(ctx context.Context, dir string, e Entry, task *tasks.Handle) error {
	var base int64 // bytes of the files already done
	lastPct := -1
	progress := func(n int64) {
		j.mu.Lock()
		j.done = base + n
		done, total := j.done, j.total
		j.mu.Unlock()
		if total > 0 {
			if pct := int(done * 100 / total); pct != lastPct {
				task.Progress(pct)
				lastPct = pct
			}
		}
	}
	for _, f := range e.Files {
		dst := filepath.Join(dir, f.Name)
		if sum, err := fileSHA256(dst); err == nil && sum == f.SHA256 {
			base += f.Size // already there (e.g. one file of a pair)
			progress(0)
			continue
		}
		if err := fetch(ctx, f, dst, progress); err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		base += f.Size
		progress(0)
	}
	return nil
}

// fetch downloads one file to dst+".part", verifies it and renames it.
func fetch(ctx context.Context, f File, dst string, progress func(int64)) (err error) {
	part := dst + partSuffix
	defer func() {
		if err != nil {
			_ = os.Remove(part)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return err
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	h := sha256.New()
	var n int64
	buf := make([]byte, 256<<10)
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			if _, werr := out.Write(buf[:k]); werr != nil {
				out.Close()
				return werr
			}
			h.Write(buf[:k])
			n += int64(k)
			progress(n)
			if f.Size > 0 && n > 2*f.Size+(1<<20) {
				out.Close()
				return errors.New("download is larger than expected")
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if sum != f.SHA256 {
		return fmt.Errorf("SHA-256 mismatch (got %s…, want %s…)", sum[:12], f.SHA256[:12])
	}
	if err := os.Rename(part, dst); err != nil {
		return err
	}
	if fi, err := os.Stat(dst); err == nil {
		rememberHash(dst, hashKey{fi.Size(), fi.ModTime()}, sum)
	}
	return nil
}

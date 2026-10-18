// Package tasks is the management server's in-memory registry of
// long-running work (camera discovery scans, evidence export builds, the
// Mac import) for the System Dashboard's Current Tasks page. Nothing is
// persisted: a restart forgets finished and running tasks alike.
package tasks

import (
	"sort"
	"strconv"
	"sync"
	"time"
)

// States of a task.
const (
	Running = "running"
	Done    = "done"
	Failed  = "failed"
)

// Kinds of task the server registers.
const (
	KindDiscovery = "discovery"
	KindExport    = "export"
	KindImport    = "import"
)

// Retention is how long a finished task stays listed.
const Retention = 15 * time.Minute

// maxTasks bounds the registry; the oldest finished tasks go first.
const maxTasks = 500

// Indeterminate is the progress of a task that can't say how far along it is.
const Indeterminate = -1

// Task is one entry as listed.
type Task struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	State string `json:"state"`
	// Progress is 0-100, or Indeterminate (-1).
	Progress   int    `json:"progress"`
	StartedAt  int64  `json:"startedAt"`  // Unix ms
	FinishedAt int64  `json:"finishedAt"` // Unix ms, 0 while running
	Error      string `json:"error,omitempty"`
}

// Registry holds the tasks. Use New.
type Registry struct {
	mu    sync.Mutex
	now   func() time.Time
	seq   int64
	tasks map[string]*Task
}

// Default is the process-wide registry the server instruments.
var Default = New()

func New() *Registry {
	return &Registry{now: time.Now, tasks: map[string]*Task{}}
}

// SetClock replaces the time source (tests).
func (r *Registry) SetClock(now func() time.Time) {
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

// Handle updates one task. A nil Handle is a no-op, so instrumentation
// never needs a guard.
type Handle struct {
	r  *Registry
	id string
}

// Start registers a running task with indeterminate progress.
func (r *Registry) Start(kind, title string) *Handle {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	id := "t" + strconv.FormatInt(r.seq, 10)
	r.tasks[id] = &Task{ID: id, Kind: kind, Title: title, State: Running, Progress: Indeterminate, StartedAt: r.now().UnixMilli()}
	r.pruneLocked()
	return &Handle{r: r, id: id}
}

// ID is the task's id as listed ("" for a nil Handle).
func (h *Handle) ID() string {
	if h == nil {
		return ""
	}
	return h.id
}

// Progress sets 0-100 (clamped to 99: 100 means done) on a running task.
func (h *Handle) Progress(pct int) {
	if h == nil {
		return
	}
	h.r.update(h.id, func(t *Task, _ int64) {
		if t.State != Running {
			return
		}
		if pct < 0 {
			pct = 0
		} else if pct > 99 {
			pct = 99
		}
		t.Progress = pct
	})
}

// Finish marks the task done. Calling it after Fail keeps the failure, so
// `defer h.Finish()` is safe next to explicit Fail calls.
func (h *Handle) Finish() {
	if h == nil {
		return
	}
	h.r.update(h.id, func(t *Task, nowMs int64) {
		if t.State != Running {
			return
		}
		t.State, t.Progress, t.FinishedAt = Done, 100, nowMs
	})
}

// Fail marks the task failed with err's message.
func (h *Handle) Fail(err error) {
	if h == nil {
		return
	}
	h.r.update(h.id, func(t *Task, nowMs int64) {
		if t.State != Running {
			return
		}
		t.State, t.FinishedAt = Failed, nowMs
		if err != nil {
			t.Error = err.Error()
		}
	})
}

func (r *Registry) update(id string, fn func(t *Task, nowMs int64)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.tasks[id]; t != nil {
		fn(t, r.now().UnixMilli())
	}
}

// List returns running tasks and recently finished ones, newest first.
func (r *Registry) List() []Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	out := make([]Task, 0, len(r.tasks))
	for _, t := range r.tasks {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt > out[j].StartedAt
		}
		return len(out[i].ID) > len(out[j].ID) || (len(out[i].ID) == len(out[j].ID) && out[i].ID > out[j].ID)
	})
	return out
}

func (r *Registry) pruneLocked() {
	cutoff := r.now().Add(-Retention).UnixMilli()
	for id, t := range r.tasks {
		if t.State != Running && t.FinishedAt < cutoff {
			delete(r.tasks, id)
		}
	}
	for len(r.tasks) > maxTasks {
		var oldest *Task
		for _, t := range r.tasks {
			if t.State != Running && (oldest == nil || t.FinishedAt < oldest.FinishedAt) {
				oldest = t
			}
		}
		if oldest == nil {
			return
		}
		delete(r.tasks, oldest.ID)
	}
}

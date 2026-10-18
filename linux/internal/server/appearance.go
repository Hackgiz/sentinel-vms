package server

// Appearance search, the index side (WAVE9-REACH.md, stream A). With a
// re-identification model loaded (cmd/sentinel/appearance.go), every event
// whose thumbnail arrives with a detection box gets a vector: the box is
// cropped out of the thumbnail (never the whole frame), embedded, and
// stored in event_embeddings under the model's id. A tasks-tracked,
// cancellable backfill indexes the newest 50k events that have none yet.
// Without a model all of this is off and the API says why.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sentinel-linux/internal/reid"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

const (
	appearanceQueueLen  = 256     // pending on-event embeds; overflow is left to the backfill
	appearanceThumbMax  = 4 << 20 // biggest thumbnail fetched for a crop
	appearanceBackfill  = 50_000  // newest events a backfill considers
	appearanceTaskKind  = "appearance"
	appearanceFetchWait = 20 * time.Second
)

// Backfill states (v2 payload).
const (
	backfillRunning  = "running"
	backfillDone     = "done"
	backfillFailed   = "failed"
	backfillCanceled = "canceled"
)

type appearanceState struct {
	mu      sync.Mutex
	emb     reid.Embedder
	reason  string // why appearance search is unavailable ("" with a model)
	queue   chan string
	started bool
	job     *backfillJob // the latest backfill, running or finished

	// thumb fetches an event's thumbnail JPEG; nil = s.eventThumbnail
	// (tests replace it).
	thumb func(ctx context.Context, e *store.Event) ([]byte, bool)
}

// errNoThumbnail: the event's snapshot is not (yet) available.
var errNoThumbnail = errors.New("the event's snapshot isn't available")

// SetAppearanceEmbedder installs the appearance model (nil = unavailable,
// with reason). Vectors from any other model are purged, and on-event
// indexing starts.
func (s *Server) SetAppearanceEmbedder(e reid.Embedder, reason string) {
	st := &s.appearance
	st.mu.Lock()
	st.emb, st.reason = e, reason
	if e == nil && st.reason == "" {
		st.reason = "no appearance model installed"
	}
	start := e != nil && !st.started
	if start {
		st.started = true
		st.queue = make(chan string, appearanceQueueLen)
	}
	queue := st.queue
	st.mu.Unlock()
	if e == nil {
		return
	}
	if n, err := s.Store.PurgeEmbeddings(s.ctxOrBackground(), e.Model()); err != nil {
		slog.Error("appearance: purge stale vectors", "err", err)
	} else if n > 0 {
		slog.Info("appearance: model changed, dropped old vectors", "rows", n, "model", e.Model())
	}
	if start {
		go s.runAppearanceQueue(s.ctxOrBackground(), queue)
	}
}

// appearanceModel returns the embedder, or nil and the reason.
func (s *Server) appearanceModel() (reid.Embedder, string) {
	st := &s.appearance
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.emb == nil {
		reason := st.reason
		if reason == "" {
			reason = "no appearance model installed"
		}
		return nil, reason
	}
	return st.emb, ""
}

// appearanceOnThumbnail queues an event whose thumbnail just arrived (sink.go).
// Never blocks: a full queue drops it (a backfill picks it up later).
func (s *Server) appearanceOnThumbnail(eventID string) {
	st := &s.appearance
	st.mu.Lock()
	q := st.queue
	ok := st.emb != nil
	st.mu.Unlock()
	if !ok || q == nil {
		return
	}
	select {
	case q <- eventID:
	default:
	}
}

// runAppearanceQueue embeds queued events one at a time until ctx ends.
func (s *Server) runAppearanceQueue(ctx context.Context, q <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q:
			emb, _ := s.appearanceModel()
			if emb == nil {
				continue
			}
			e, err := s.Store.Event(ctx, id)
			if err != nil || e.Box == "" {
				continue // deleted, or a detection without a box (motion, audio)
			}
			if _, err := s.embedEvent(ctx, emb, e); err != nil && !errors.Is(err, reid.ErrNoBox) && !errors.Is(err, reid.ErrTooSmall) {
				slog.Debug("appearance: event not indexed", "event", id, "err", err)
			}
		}
	}
}

// embedEvent crops the event's detection box out of its thumbnail, embeds
// it and stores the vector. It returns the vector.
func (s *Server) embedEvent(ctx context.Context, emb reid.Embedder, e *store.Event) ([]float32, error) {
	box, err := reid.ParseBox(e.Box)
	if err != nil {
		return nil, err
	}
	if e.Thumbnail == "" {
		return nil, errNoThumbnail
	}
	fetch := s.appearance.thumb
	if fetch == nil {
		fetch = func(ctx context.Context, e *store.Event) ([]byte, bool) {
			return s.eventThumbnail(ctx, e, appearanceThumbMax)
		}
	}
	fctx, cancel := context.WithTimeout(ctx, appearanceFetchWait)
	jpg, ok := fetch(fctx, e)
	cancel()
	if !ok || len(jpg) == 0 || len(jpg) > appearanceThumbMax {
		return nil, errNoThumbnail
	}
	crop, err := reid.CropJPEG(jpg, box)
	if err != nil {
		return nil, err
	}
	vec, err := emb.Embed(crop)
	if err != nil {
		return nil, fmt.Errorf("appearance model: %w", err)
	}
	if err := s.Store.PutEmbedding(ctx, store.Embedding{EventID: e.ID, Model: emb.Model(), Dim: len(vec), Vec: reid.Encode(vec),
		CameraID: e.CameraID, AtMs: secToMs(e.CreatedAt)}); err != nil {
		return nil, err
	}
	return vec, nil
}

// MARK: - Backfill

type backfillJob struct {
	taskID string
	cancel context.CancelFunc
	done   chan struct{}

	mu         sync.Mutex
	state      string
	total      int
	processed  int
	indexed    int
	skipped    int
	err        string
	startedAt  time.Time
	finishedAt time.Time
}

// v2Backfill is a backfill as the API shows it.
type v2Backfill struct {
	TaskID     string `json:"taskId"`
	State      string `json:"state"`
	Total      int    `json:"total"`
	Processed  int    `json:"processed"`
	Indexed    int    `json:"indexed"`
	Skipped    int    `json:"skipped"`
	Error      string `json:"error,omitempty"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt,omitempty"`
}

func (j *backfillJob) view() *v2Backfill {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	v := &v2Backfill{TaskID: j.taskID, State: j.state, Total: j.total, Processed: j.processed, Indexed: j.indexed,
		Skipped: j.skipped, Error: j.err, StartedAt: j.startedAt.UnixMilli()}
	if !j.finishedAt.IsZero() {
		v.FinishedAt = j.finishedAt.UnixMilli()
	}
	return v
}

func (j *backfillJob) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state == backfillRunning
}

func (j *backfillJob) finish(state, err string) {
	j.mu.Lock()
	j.state, j.err, j.finishedAt = state, err, time.Now()
	j.mu.Unlock()
}

// startBackfill starts indexing the newest events without a vector, or
// returns the backfill already running (started=false).
func (s *Server) startBackfill(who string) (job *backfillJob, started bool, err error) {
	emb, reason := s.appearanceModel()
	if emb == nil {
		return nil, false, conflictError{"appearance search is unavailable: " + reason}
	}
	st := &s.appearance
	st.mu.Lock()
	if st.job != nil && st.job.running() {
		j := st.job
		st.mu.Unlock()
		return j, false, nil
	}
	task := tasks.Default.Start(appearanceTaskKind, "Appearance index: newest events")
	ctx, cancel := context.WithCancel(s.ctxOrBackground())
	job = &backfillJob{taskID: task.ID(), cancel: cancel, done: make(chan struct{}), state: backfillRunning, startedAt: time.Now()}
	st.job = job
	st.mu.Unlock()
	go func() {
		defer close(job.done)
		defer cancel()
		s.runBackfill(ctx, emb, job, task, who)
	}()
	return job, true, nil
}

func (s *Server) runBackfill(ctx context.Context, emb reid.Embedder, job *backfillJob, task *tasks.Handle, who string) {
	todo, err := s.Store.EventsToEmbed(ctx, emb.Model(), appearanceBackfill)
	if err != nil {
		if ctx.Err() != nil {
			task.Fail(errors.New("canceled"))
			job.finish(backfillCanceled, "canceled")
			return
		}
		task.Fail(err)
		job.finish(backfillFailed, err.Error())
		s.auditSystem(who, "Search", "Appearance index failed", err.Error())
		return
	}
	job.mu.Lock()
	job.total = len(todo)
	job.mu.Unlock()
	lastPct := -1
	for i := range todo {
		if ctx.Err() != nil {
			task.Fail(errors.New("canceled"))
			job.finish(backfillCanceled, "canceled")
			return
		}
		_, err := s.embedEvent(ctx, emb, &todo[i])
		if ctx.Err() != nil {
			continue // reported as canceled on the next turn
		}
		job.mu.Lock()
		job.processed++
		if err == nil {
			job.indexed++
		} else {
			job.skipped++
		}
		job.mu.Unlock()
		if pct := (i + 1) * 100 / len(todo); pct != lastPct {
			task.Progress(pct)
			lastPct = pct
		}
	}
	if ctx.Err() != nil {
		task.Fail(errors.New("canceled"))
		job.finish(backfillCanceled, "canceled")
		return
	}
	task.Finish()
	job.finish(backfillDone, "")
	v := job.view()
	s.auditSystem(who, "Search", "Appearance index built", fmt.Sprintf("%d of %d events indexed, %d skipped", v.Indexed, v.Total, v.Skipped))
}

// cancelBackfill stops the running backfill and waits for it (up to 20 s).
// It returns the job (nil when there never was one) and whether it was
// running.
func (s *Server) cancelBackfill() (*backfillJob, bool) {
	st := &s.appearance
	st.mu.Lock()
	j := st.job
	st.mu.Unlock()
	if j == nil || !j.running() {
		return j, false
	}
	j.cancel()
	select {
	case <-j.done:
	case <-time.After(20 * time.Second):
	}
	return j, true
}

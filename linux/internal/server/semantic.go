package server

// Semantic search, the index side (WAVE16-DETECTION.md, stream E). With the
// CLIP models loaded (cmd/sentinel/semantic.go), every event whose
// thumbnail arrives gets a vector of its whole snapshot (not the box: the
// scene is what people describe), stored in event_semantic under the model's
// id. A tasks-tracked, cancellable backfill indexes the newest 50k events
// with a thumbnail and no vector yet. Without the models all of this is off
// and the API says why. Mirrors appearance.go, which it does not touch: the
// backfill reuses its backfillJob type.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sentinel-linux/internal/reid"
	"sentinel-linux/internal/semantic"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

const (
	semanticQueueLen  = 256     // pending on-event embeds; overflow is left to the backfill
	semanticThumbMax  = 4 << 20 // biggest thumbnail fetched
	semanticBackfill  = 50_000  // newest events a backfill considers
	semanticTaskKind  = "semantic"
	semanticFetchWait = 20 * time.Second
	semanticNoModel   = "no semantic search model installed"
)

type semanticState struct {
	mu      sync.Mutex
	eng     semantic.Engine
	reason  string // why semantic search is unavailable ("" with a model)
	queue   chan string
	started bool
	hooked  bool
	job     *backfillJob // the latest backfill, running or finished

	// thumb fetches an event's thumbnail JPEG; nil = s.eventThumbnail
	// (tests replace it).
	thumb func(ctx context.Context, e *store.Event) ([]byte, bool)
}

// semanticStates holds each Server's semantic state, so this stream adds
// no field to Server (server.go stays untouched).
var semanticStates sync.Map // *Server → *semanticState

func (s *Server) semanticSt() *semanticState {
	if v, ok := semanticStates.Load(s); ok {
		return v.(*semanticState)
	}
	v, _ := semanticStates.LoadOrStore(s, &semanticState{})
	return v.(*semanticState)
}

// SetSemanticEngine installs the CLIP models (nil = unavailable, with
// reason). Vectors from any other model are purged, and on-event indexing
// starts.
func (s *Server) SetSemanticEngine(e semantic.Engine, reason string) {
	st := s.semanticSt()
	st.mu.Lock()
	st.eng, st.reason = e, reason
	if e == nil && st.reason == "" {
		st.reason = semanticNoModel
	}
	hook := !st.hooked
	st.hooked = true
	start := e != nil && !st.started
	if start {
		st.started = true
		st.queue = make(chan string, semanticQueueLen)
	}
	queue := st.queue
	st.mu.Unlock()
	if hook {
		s.OnThumbnail(s.semanticOnThumbnail) // wave16.go
	}
	if e == nil {
		return
	}
	if n, err := s.Store.PurgeSemantic(s.ctxOrBackground(), e.Model()); err != nil {
		slog.Error("semantic: purge stale vectors", "err", err)
	} else if n > 0 {
		slog.Info("semantic: model changed, dropped old vectors", "rows", n, "model", e.Model())
	}
	if start {
		go s.runSemanticQueue(s.ctxOrBackground(), queue)
	}
}

// semanticEngine returns the engine, or nil and the reason.
func (s *Server) semanticEngine() (semantic.Engine, string) {
	st := s.semanticSt()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.eng == nil {
		if st.reason == "" {
			return nil, semanticNoModel
		}
		return nil, st.reason
	}
	return st.eng, ""
}

// semanticOnThumbnail queues an event whose thumbnail just arrived (the
// OnThumbnail hook, on the sink goroutine). Never blocks: a full queue
// drops it (a backfill picks it up later).
func (s *Server) semanticOnThumbnail(eventID string) {
	st := s.semanticSt()
	st.mu.Lock()
	q := st.queue
	ok := st.eng != nil
	st.mu.Unlock()
	if !ok || q == nil {
		return
	}
	select {
	case q <- eventID:
	default:
	}
}

// runSemanticQueue embeds queued events one at a time until ctx ends.
func (s *Server) runSemanticQueue(ctx context.Context, q <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q:
			eng, _ := s.semanticEngine()
			if eng == nil {
				continue
			}
			e, err := s.Store.Event(ctx, id)
			if err != nil || e.Thumbnail == "" {
				continue // deleted, or no snapshot after all
			}
			if _, err := s.embedSemantic(ctx, eng, e); err != nil {
				slog.Debug("semantic: event not indexed", "event", id, "err", err)
			}
		}
	}
}

// embedSemantic embeds the event's whole thumbnail and stores the vector.
// It returns the vector.
func (s *Server) embedSemantic(ctx context.Context, eng semantic.Engine, e *store.Event) ([]float32, error) {
	if e.Thumbnail == "" {
		return nil, errNoThumbnail
	}
	fetch := s.semanticSt().thumb
	if fetch == nil {
		fetch = func(ctx context.Context, e *store.Event) ([]byte, bool) {
			return s.eventThumbnail(ctx, e, semanticThumbMax)
		}
	}
	fctx, cancel := context.WithTimeout(ctx, semanticFetchWait)
	jpg, ok := fetch(fctx, e)
	cancel()
	if !ok || len(jpg) == 0 || len(jpg) > semanticThumbMax {
		return nil, errNoThumbnail
	}
	img, err := semantic.DecodeJPEG(jpg)
	if err != nil {
		return nil, err
	}
	vec, err := eng.EmbedImage(img)
	if err != nil {
		return nil, fmt.Errorf("semantic model: %w", err)
	}
	if err := s.Store.PutSemantic(ctx, store.SemanticVector{EventID: e.ID, Model: eng.Model(), Dim: len(vec), Vec: reid.Encode(vec),
		CameraID: e.CameraID, AtMs: secToMs(e.CreatedAt)}); err != nil {
		return nil, err
	}
	return vec, nil
}

// MARK: - Backfill

// startSemanticBackfill starts indexing the newest events without a
// vector, or returns the backfill already running (started=false).
func (s *Server) startSemanticBackfill(who string) (job *backfillJob, started bool, err error) {
	eng, reason := s.semanticEngine()
	if eng == nil {
		return nil, false, conflictError{"semantic search is unavailable: " + reason}
	}
	st := s.semanticSt()
	st.mu.Lock()
	if st.job != nil && st.job.running() {
		j := st.job
		st.mu.Unlock()
		return j, false, nil
	}
	task := tasks.Default.Start(semanticTaskKind, "Semantic index: newest events")
	ctx, cancel := context.WithCancel(s.ctxOrBackground())
	job = &backfillJob{taskID: task.ID(), cancel: cancel, done: make(chan struct{}), state: backfillRunning, startedAt: time.Now()}
	st.job = job
	st.mu.Unlock()
	go func() {
		defer close(job.done)
		defer cancel()
		s.runSemanticBackfill(ctx, eng, job, task, who)
	}()
	return job, true, nil
}

func (s *Server) runSemanticBackfill(ctx context.Context, eng semantic.Engine, job *backfillJob, task *tasks.Handle, who string) {
	canceled := func() {
		task.Fail(errors.New("canceled"))
		job.finish(backfillCanceled, "canceled")
	}
	todo, err := s.Store.EventsToSemantic(ctx, eng.Model(), semanticBackfill)
	if err != nil {
		if ctx.Err() != nil {
			canceled()
			return
		}
		task.Fail(err)
		job.finish(backfillFailed, err.Error())
		s.auditSystem(who, "Search", "Semantic index failed", err.Error())
		return
	}
	job.mu.Lock()
	job.total = len(todo)
	job.mu.Unlock()
	lastPct := -1
	for i := range todo {
		if ctx.Err() != nil {
			canceled()
			return
		}
		_, err := s.embedSemantic(ctx, eng, &todo[i])
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
		canceled()
		return
	}
	task.Finish()
	job.finish(backfillDone, "")
	v := job.view()
	s.auditSystem(who, "Search", "Semantic index built", fmt.Sprintf("%d of %d events indexed, %d skipped", v.Indexed, v.Total, v.Skipped))
}

// cancelSemanticBackfill stops the running backfill and waits for it (up
// to 20 s). It returns the job (nil when there never was one) and whether
// it was running.
func (s *Server) cancelSemanticBackfill() (*backfillJob, bool) {
	st := s.semanticSt()
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

// MARK: - Search

const (
	semanticDefaultLimit    = 30
	semanticMaxLimit        = 100
	semanticDefaultMinScore = 0.2
	semanticMaxScan         = 200_000 // newest candidate vectors compared per search
)

type v2SemanticHit struct {
	Score float64 `json:"score"`
	Event v2Event `json:"event"`
}

type semanticQuery struct {
	Text     string
	Cameras  []string
	FromMs   int64
	ToMs     int64
	Limit    int
	MinScore float64
}

// semanticSearch embeds the text and ranks the stored snapshot vectors of
// the same model within the caller's camera scope (ctx), best first.
func (s *Server) semanticSearch(ctx context.Context, eng semantic.Engine, q semanticQuery) ([]v2SemanticHit, error) {
	items := []v2SemanticHit{}
	ids, none := s.scopedCameraIDs(ctx, q.Cameras)
	if none {
		return items, nil
	}
	query, err := eng.EmbedText(q.Text)
	if err != nil {
		return nil, fmt.Errorf("semantic model: %w", err)
	}
	rank := reid.NewRanker(query, q.Limit, q.MinScore)
	f := store.SemanticFilter{Model: eng.Model(), CameraIDs: ids, FromMs: q.FromMs, ToMs: q.ToMs, Max: semanticMaxScan}
	if err := s.Store.ScanSemantic(ctx, f, func(id string, raw []byte) {
		if v, err := reid.Decode(raw); err == nil {
			rank.Add(id, v)
		}
	}); err != nil {
		return nil, err
	}
	hits := rank.Hits()
	hitIDs := make([]string, len(hits))
	for i, h := range hits {
		hitIDs[i] = h.ID
	}
	events, err := s.Store.EventsByID(ctx, hitIDs)
	if err != nil {
		return nil, err
	}
	sc := s.cameraScope(ctx)
	for _, h := range hits {
		if ev := events[h.ID]; ev != nil && sc.camera(ev.CameraID) {
			items = append(items, v2SemanticHit{Score: roundScore(h.Score), Event: toV2Event(ev)})
		}
	}
	return items, nil
}

package push

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Queue delivers notifications in the background so raising an alarm never
// waits on Apple or the relay.
type Queue struct {
	// Sender returns the current sender (nil = push is off: items are dropped).
	// A func so settings changes apply to queued items too.
	Sender func() Sender
	// OnInvalidToken is told about tokens APNs says are dead.
	OnInvalidToken func(token string)
	// OnResult reports each final outcome (nil error = delivered).
	OnResult func(n Notification, err error)

	Size        int                             // capacity; 0 = 256
	Workers     int                             // 0 = 2
	MaxAttempts int                             // 0 = 4
	Backoff     func(attempt int) time.Duration // nil = 2s, 10s, 30s…
	SendTimeout time.Duration                   // per attempt; 0 = 30s

	once    sync.Once
	ch      chan item
	mu      sync.Mutex
	stats   Stats
	pending sync.WaitGroup
}

type item struct {
	n       Notification
	attempt int
}

// Stats are counters for the dashboard and tests.
type Stats struct {
	Queued, Sent, Failed, Dropped, Retried, InvalidTokens int
	LastError                                             string
	LastSentAt                                            time.Time
}

func (q *Queue) init() {
	q.once.Do(func() {
		size := q.Size
		if size <= 0 {
			size = 256
		}
		q.ch = make(chan item, size)
	})
}

// Run starts the workers and blocks until ctx ends.
func (q *Queue) Run(ctx context.Context) {
	q.init()
	n := q.Workers
	if n <= 0 {
		n = 2
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case it := <-q.ch:
					q.deliver(ctx, it)
				}
			}
		}()
	}
	wg.Wait()
}

// Enqueue adds a notification without blocking. It returns false (and counts
// a drop) when the queue is full.
func (q *Queue) Enqueue(n Notification) bool {
	q.init()
	return q.put(item{n: n})
}

func (q *Queue) put(it item) bool {
	q.pending.Add(1)
	select {
	case q.ch <- it:
		if it.attempt == 0 {
			q.count(func(s *Stats) { s.Queued++ })
		}
		return true
	default:
		q.pending.Done()
		q.count(func(s *Stats) { s.Dropped++; s.LastError = "push queue full — notification dropped" })
		return false
	}
}

func (q *Queue) count(f func(*Stats)) {
	q.mu.Lock()
	f(&q.stats)
	q.mu.Unlock()
}

func (q *Queue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stats
}

// Wait blocks until everything enqueued so far reached a final outcome
// (tests), or ctx ends.
func (q *Queue) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { q.pending.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *Queue) backoff(attempt int, err error) time.Duration {
	var re *RetryError
	if errors.As(err, &re) && re.After > 0 {
		d := time.Duration(re.After) * time.Second
		if d > 10*time.Minute {
			d = 10 * time.Minute
		}
		return d
	}
	if q.Backoff != nil {
		return q.Backoff(attempt)
	}
	switch attempt {
	case 1:
		return 2 * time.Second
	case 2:
		return 10 * time.Second
	}
	return 30 * time.Second
}

func (q *Queue) deliver(ctx context.Context, it item) {
	defer q.pending.Done()
	var s Sender
	if q.Sender != nil {
		s = q.Sender()
	}
	if s == nil {
		q.count(func(st *Stats) { st.Dropped++ })
		return
	}
	timeout := q.SendTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	sctx, cancel := context.WithTimeout(ctx, timeout)
	err := s.Send(sctx, it.n)
	cancel()
	it.attempt++
	max := q.MaxAttempts
	if max <= 0 {
		max = 4
	}
	switch {
	case err == nil:
		q.count(func(st *Stats) { st.Sent++; st.LastSentAt = time.Now() })
	case errors.Is(err, ErrInvalidToken):
		q.count(func(st *Stats) { st.InvalidTokens++; st.Failed++; st.LastError = err.Error() })
		if q.OnInvalidToken != nil {
			q.OnInvalidToken(it.n.Token)
		}
	case errors.Is(err, ErrRetry) && it.attempt < max && ctx.Err() == nil:
		q.count(func(st *Stats) { st.Retried++; st.LastError = err.Error() })
		q.pending.Add(1) // held by the timer until the retry is queued
		time.AfterFunc(q.backoff(it.attempt, err), func() {
			defer q.pending.Done()
			if ctx.Err() == nil {
				q.put(it)
			}
		})
		return
	default:
		q.count(func(st *Stats) { st.Failed++; st.LastError = err.Error() })
	}
	if q.OnResult != nil {
		q.OnResult(it.n, err)
	}
}

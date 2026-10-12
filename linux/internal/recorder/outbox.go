package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"sentinel-linux/internal/link"
)

// Events reach the management side at least once: each is written to the
// outbox together with its own row, delivered, and removed from the outbox
// only when the sink took it. Undelivered ones are retried every 30 s and on
// start, oldest first; the sink deduplicates by event id. While the
// management server is unreachable the outbox keeps up to 7 days / 100k
// messages.

// deliver hands one queued message to the sink now.
func (r *Recorder) deliver(msg link.Event) {
	sink := r.currentSink()
	if sink == nil {
		return // stays queued until a sink is attached
	}
	r.deliverMu.Lock()
	defer r.deliverMu.Unlock()
	if err := sink.Event(r.ctx(), msg); err != nil {
		slog.Warn("event not delivered to the management server; will retry", "type", msg.Type, "err", err)
		return
	}
	if err := r.store.delivered(r.ctx(), msg.ID); err != nil {
		slog.Error("outbox", "err", err)
	}
}

// Replay delivers everything still queued, oldest first, stopping at the
// first refusal (order matters: a thumbnail after its event).
func (r *Recorder) Replay(ctx context.Context) {
	sink := r.currentSink()
	if sink == nil {
		return
	}
	r.deliverMu.Lock()
	defer r.deliverMu.Unlock()
	if n, err := r.store.trimOutbox(ctx, outboxMaxAge, outboxMaxRows); err == nil && n > 0 {
		slog.Warn("outbox full or stale; dropped oldest undelivered events", "dropped", n)
		r.chain(auditBySystem, "Events", "Dropped undelivered events",
			fmt.Sprintf("%d events the management server didn't take within 7 days or 100,000 messages", n))
	}
	for ctx.Err() == nil {
		batch, err := r.store.pending(ctx, 100)
		if err != nil || len(batch) == 0 {
			return
		}
		for _, msg := range batch {
			if err := sink.Event(ctx, msg); err != nil {
				slog.Warn("event replay paused; management server refused", "type", msg.Type, "err", err)
				return
			}
			if err := r.store.delivered(ctx, msg.ID); err != nil {
				return
			}
		}
	}
}

// Pending is the number of events waiting for the management server.
func (r *Recorder) Pending() int { return r.store.pendingCount(r.ctx()) }

func (r *Recorder) pruneEvents(ctx context.Context) {
	n, files, err := r.store.pruneEvents(ctx, time.Now().Add(-eventsRetention))
	if err != nil {
		return
	}
	if n > 0 {
		r.audit("Events", "Deleted expired events", fmt.Sprintf("%d events older than 30 days, %d snapshots", n, len(files)))
	}
	for _, f := range files {
		_ = os.Remove(filepath.Join(r.eventsDir(), filepath.Base(f)))
	}
}

// Package inproc joins a recording server and a management server living in
// the same process (--role all).
//
// Every value crossing the link is copied through encoding/json, exactly as
// it will be serialized on the phase-2 WebSocket. That keeps the two sides
// from sharing memory (no aliased slices or maps) and makes every all-in-one
// test exercise the wire format. The cost is a few microseconds per call;
// video does not go through here (Recorder.Video is passed through as is).
package inproc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"sentinel-linux/internal/link"
)

// Connect returns the two ends of an in-process link: ms is the Recorder the
// management side holds, rs is the Sink the recorder reports to.
func Connect(rec link.Recorder, sink link.Sink) (ms link.Recorder, rs link.Sink) {
	return &recorderEnd{rec: rec}, &sinkEnd{sink: sink}
}

func clone[T any](in T) (T, error) {
	var out T
	raw, err := json.Marshal(in)
	if err != nil {
		return out, fmt.Errorf("link: encode %T: %w", in, err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("link: decode %T: %w", in, err)
	}
	return out, nil
}

type recorderEnd struct{ rec link.Recorder }

func (r *recorderEnd) ApplyConfig(ctx context.Context, s link.Snapshot) (link.Applied, error) {
	if err := ctx.Err(); err != nil {
		return link.Applied{}, err
	}
	in, err := clone(s)
	if err != nil {
		return link.Applied{}, err
	}
	a, err := r.rec.ApplyConfig(ctx, in)
	if err != nil {
		return link.Applied{}, err
	}
	return clone(a)
}

func (r *recorderEnd) Command(ctx context.Context, c link.Command) (link.Result, error) {
	if err := ctx.Err(); err != nil {
		return link.Result{}, err
	}
	in, err := clone(c)
	if err != nil {
		return link.Result{}, err
	}
	res, err := r.rec.Command(ctx, in)
	if err != nil {
		return link.Result{}, err
	}
	return clone(res)
}

func (r *recorderEnd) Status() link.ServerStatus {
	st, err := clone(r.rec.Status())
	if err != nil {
		slog.Error("in-process link: status not encodable", "err", err)
	}
	return st
}

func (r *recorderEnd) Video() http.Handler { return r.rec.Video() }

type sinkEnd struct{ sink link.Sink }

func (s *sinkEnd) Event(ctx context.Context, e link.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	in, err := clone(e)
	if err != nil {
		return err
	}
	return s.sink.Event(ctx, in)
}

func (s *sinkEnd) Status(st link.ServerStatus) {
	in, err := clone(st)
	if err != nil {
		slog.Error("in-process link: status not encodable", "err", err)
		return
	}
	s.sink.Status(in)
}

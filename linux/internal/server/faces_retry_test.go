package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
)

// recordedFrame retries only transient failures; a recorder that answers
// "no recording" / "unsupported" is asked once.
func TestRecordedFrameRetriesOnlyTransient(t *testing.T) {
	old := facesRetryWait
	facesRetryWait = time.Millisecond
	defer func() { facesRetryWait = old }()
	e := &store.Event{ID: "e", CameraID: "c", CreatedAt: time.Now().Add(-time.Hour).Unix()}

	run := func(errs ...error) (calls int, ok bool) {
		_, ok = recordedFrameVia(context.Background(), e, func(_ context.Context, _ link.SnapshotArgs, res *link.SnapshotResult) error {
			i := calls
			calls++
			if i < len(errs) {
				return errs[i]
			}
			res.JPEG = []byte{1}
			return nil
		})
		return
	}
	for _, code := range []string{link.CodeNotFound, link.CodeNoClip, link.CodeBadRequest, link.CodeUnknownCommand} {
		if calls, ok := run(&link.CommandError{Code: code, Message: "x"}, &link.CommandError{Code: code}); ok || calls != 1 {
			t.Fatalf("%s: calls=%d ok=%v, want one call and no frame", code, calls, ok)
		}
	}
	for name, err := range map[string]error{
		"timeout":  &link.CommandError{Code: link.CodeTimeout},
		"deadline": context.DeadlineExceeded,
		"offline":  link.ErrNoRecorder,
		"busy":     &link.CommandError{Code: link.CodeBusy},
		"wrapped":  fmt.Errorf("send: %w", errors.New("broken pipe")),
	} {
		if calls, ok := run(err); !ok || calls != 2 {
			t.Fatalf("%s: calls=%d ok=%v, want a retry that succeeds", name, calls, ok)
		}
	}
}

// A burst larger than one consumer can handle is spread over the workers,
// and events dropped from a full queue are counted and logged.
func TestFacesWorkersAndDropCount(t *testing.T) {
	s := &Server{}
	s.faces.engine = &fakeFaces{}
	s.faces.queue = make(chan string, 2)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		s.facesOnThumbnail(id)
	}
	if got := s.faces.drops.Load(); got != 3 {
		t.Fatalf("drops = %d, want 3", got)
	}
	s.logFaceDrops(true)
	if s.faces.reported != 3 {
		t.Fatalf("reported = %d", s.faces.reported)
	}

	if facesWorkers < 2 || facesWorkers > 3 {
		t.Fatalf("facesWorkers = %d", facesWorkers)
	}
	// Workers run concurrently: with a processed hook that blocks, all of
	// them pick up an event at the same time.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s2 := &Server{}
	var mu sync.Mutex
	active, peak := 0, 0
	release := make(chan struct{})
	s2.faces.processed = func(string) {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		<-release
	}
	q := make(chan string, 8)
	for i := 0; i < facesWorkers; i++ {
		go s2.runFacesQueue(ctx, q)
	}
	for i := 0; i < facesWorkers; i++ {
		q <- fmt.Sprint("ev", i)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		p := peak
		mu.Unlock()
		if p == facesWorkers {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	mu.Lock()
	defer mu.Unlock()
	if peak != facesWorkers {
		t.Fatalf("concurrent workers = %d, want %d", peak, facesWorkers)
	}
}

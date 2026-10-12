package inproc

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"sentinel-linux/internal/link"
)

type fakeRecorder struct {
	mu      sync.Mutex
	applied []link.Snapshot
	status  link.ServerStatus
}

func (f *fakeRecorder) ApplyConfig(_ context.Context, s link.Snapshot) (link.Applied, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, s)
	s.Cameras[0].Name = "mutated by recorder" // must not leak back to the caller
	return link.Applied{Rev: s.Rev, Errors: map[string]string{"B": "bad URL"}}, nil
}

func (f *fakeRecorder) Command(_ context.Context, c link.Command) (link.Result, error) {
	switch c.Type {
	case link.CmdRecordings:
		var a link.RecordingsArgs
		if err := c.Decode(&a); err != nil {
			return link.Fail(c, link.CodeBadRequest, err.Error()), nil
		}
		return link.OK(c, []link.Segment{{Name: a.CameraID + ".mp4", Size: 3}}), nil
	case "transport":
		return link.Result{}, errors.New("connection reset")
	}
	return link.Fail(c, link.CodeUnknownCommand, "unknown command "+c.Type), nil
}

func (f *fakeRecorder) Status() link.ServerStatus { return f.status }

func (f *fakeRecorder) Video() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("video " + r.URL.Path)) })
}

type fakeSink struct {
	mu     sync.Mutex
	events []link.Event
	status []link.ServerStatus
	fail   error
}

func (f *fakeSink) Event(_ context.Context, e link.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.events = append(f.events, e)
	if e.Frames != nil {
		e.Frames.JPEGs[0][0] = 0 // must not reach the recorder's buffer
	}
	return nil
}

func (f *fakeSink) Status(s link.ServerStatus) {
	f.mu.Lock()
	f.status = append(f.status, s)
	f.mu.Unlock()
}

func TestConfigAndCommandsCrossByValue(t *testing.T) {
	rec := &fakeRecorder{status: link.ServerStatus{ServerID: "local", Cameras: map[string]link.CameraStatus{"A": {Ready: true}}}}
	ms, _ := Connect(rec, &fakeSink{})
	ctx := context.Background()

	snap := link.Snapshot{Rev: 5, Cameras: []link.CameraConfig{{ID: "A", Name: "Door", Password: "pw"}}, WebRTCExtraHosts: []string{"h"}}
	a, err := ms.ApplyConfig(ctx, snap)
	if err != nil || a.Rev != 5 || a.Errors["B"] != "bad URL" {
		t.Fatalf("applied %+v %v", a, err)
	}
	if snap.Cameras[0].Name != "Door" {
		t.Fatal("recorder mutated the management side's snapshot")
	}
	if len(rec.applied) != 1 || rec.applied[0].Cameras[0].Password != "pw" {
		t.Fatalf("recorder got %+v", rec.applied)
	}

	c, _ := link.NewCommand(link.CmdRecordings, link.RecordingsArgs{CameraID: "A"})
	res, err := ms.Command(ctx, c)
	var segs []link.Segment
	if err != nil || res.Decode(&segs) != nil || len(segs) != 1 || segs[0].Name != "A.mp4" || res.Re != c.ID {
		t.Fatalf("command %+v %v %+v", res, err, segs)
	}
	c, _ = link.NewCommand("nope", nil)
	res, _ = ms.Command(ctx, c)
	if res.Err() == nil || res.Code != link.CodeUnknownCommand {
		t.Fatalf("unknown command %+v", res)
	}
	c, _ = link.NewCommand("transport", nil)
	if _, err := ms.Command(ctx, c); err == nil {
		t.Fatal("transport error swallowed")
	}

	st := ms.Status()
	st.Cameras["A"] = link.CameraStatus{}
	if !rec.status.Cameras["A"].Ready {
		t.Fatal("status map shared between the sides")
	}

	rr := httptest.NewRecorder()
	ms.Video().ServeHTTP(rr, httptest.NewRequest("GET", "/live/A/index.m3u8", nil))
	if rr.Body.String() != "video /live/A/index.m3u8" {
		t.Fatalf("video plane: %q", rr.Body)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ms.ApplyConfig(cancelled, snap); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled apply: %v", err)
	}
}

func TestEventsAndStatusReachTheSink(t *testing.T) {
	sink := &fakeSink{}
	_, rs := Connect(&fakeRecorder{}, sink)
	ctx := context.Background()
	frame := []byte{0xff, 0xd8}
	e := link.Event{ID: "e1", Type: link.EventFrames, Ephemeral: true, Frames: &link.Frames{EventID: "x", JPEGs: [][]byte{frame}}}
	if err := rs.Event(ctx, e); err != nil {
		t.Fatal(err)
	}
	if frame[0] != 0xff {
		t.Fatal("sink wrote into the recorder's frame buffer")
	}
	rs.Status(link.ServerStatus{ServerID: "local"})
	if len(sink.events) != 1 || sink.events[0].Frames.EventID != "x" || len(sink.status) != 1 {
		t.Fatalf("sink got %+v / %+v", sink.events, sink.status)
	}

	// A refusal is passed back so the recorder keeps the event in its outbox.
	sink.fail = errors.New("database is locked")
	if err := rs.Event(ctx, link.Event{ID: "e2", Type: link.EventDetection}); err == nil {
		t.Fatal("sink error swallowed")
	}
	// Values JSON can't carry fail loudly instead of arriving altered.
	sink.fail = nil
	bad := link.Event{ID: "e3", Type: link.EventDetection, Detection: &link.Detection{Score: math.NaN()}}
	if err := rs.Event(ctx, bad); err == nil {
		t.Fatal("NaN score crossed the link")
	}
}

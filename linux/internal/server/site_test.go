package server

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"sentinel-linux/internal/link/inproc"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/recorder"
	"sentinel-linux/internal/store"
)

// newSite joins a management side and a local recording server over the
// in-process link, the way --role all does, so these tests exercise the
// same messages a remote recording server would get. sup may be an empty
// supervisor (no MediaMTX).
func newSite(t *testing.T, st *store.Store, sup *media.Supervisor) (*Server, *recorder.Recorder) {
	t.Helper()
	rst, err := recorder.OpenStore(filepath.Join(st.DataDir(), "recorder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rst.DB.Close() })
	rec := recorder.New(st.DataDir(), sup, rst, "test", "")
	rec.Ctx = context.Background()
	t.Cleanup(rec.Wait) // background work ends before the stores close and the directory goes
	s := New(st, nil, "test")
	s.Ctx = context.Background()
	ms, rs := inproc.Connect(rec, s.Sink())
	s.AttachRecorder(ms)
	rec.Attach(rs)
	sites.Store(s, rec)
	t.Cleanup(func() { sites.Delete(s) })
	return s, rec
}

var sites sync.Map // *Server → its *recorder.Recorder

// recOf is the recording server joined to s by newSite.
func recOf(s *Server) *recorder.Recorder {
	r, _ := sites.Load(s)
	return r.(*recorder.Recorder)
}

// syncCams sends the camera table to the recording server, as the camera
// API does after every save; tests that write cameras straight to the
// store call it before feeding the recorder detections.
func syncCams(t *testing.T, s *Server) {
	t.Helper()
	if err := s.ApplyMedia(context.Background()); err != nil {
		t.Fatal(err)
	}
}

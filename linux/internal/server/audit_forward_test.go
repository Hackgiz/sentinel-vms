package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/link/inproc"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/recorder"
	"sentinel-linux/internal/store"
)

// gatedSink stands between a recording server and the management side:
// down refuses everything; lostAck passes events on but reports failure,
// as when the acknowledgement is lost on the way back.
type gatedSink struct {
	link.Sink
	mu            sync.Mutex
	down, lostAck bool
}

func (g *gatedSink) set(down, lostAck bool) {
	g.mu.Lock()
	g.down, g.lostAck = down, lostAck
	g.mu.Unlock()
}

func (g *gatedSink) Event(ctx context.Context, e link.Event) error {
	g.mu.Lock()
	down, lost := g.down, g.lostAck
	g.mu.Unlock()
	if down && !e.Ephemeral {
		return errors.New("management server unreachable")
	}
	err := g.Sink.Event(ctx, e)
	if lost && err == nil && !e.Ephemeral {
		return errors.New("acknowledgement lost")
	}
	return err
}

// addGatedRecorder is addRecorder with a gate on what it reports.
func addGatedRecorder(t *testing.T, s *Server, id string) (*recorder.Recorder, *gatedSink) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	rst, err := recorder.OpenStore(filepath.Join(dir, "recorder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rst.DB.Close() })
	sup := &media.Supervisor{ConfigPath: filepath.Join(dir, "mediamtx.yml"), RecordingsRoot: filepath.Join(dir, "rec"), Ports: media.DefaultPorts}
	rec := recorder.New(dir, sup, rst, "test-"+id, "")
	rec.ServerID = id
	rec.Ctx = ctx
	t.Cleanup(rec.Wait)
	if err := s.Store.AddRecordingServer(ctx, &store.RecordingServer{ID: id, Name: "Warehouse"}); err != nil {
		t.Fatal(err)
	}
	ms, sink := inproc.Connect(rec, s.Sink())
	gate := &gatedSink{Sink: sink}
	s.ConnectRecorder(id, ms)
	rec.Attach(gate)
	if err := s.ApplyRecorder(ctx, id); err != nil {
		t.Fatal(err)
	}
	rec.Wait()
	return rec, gate
}

func deleteEvidenceOn(t *testing.T, rec *recorder.Recorder, name string) {
	t.Helper()
	if err := os.MkdirAll(rec.EvidenceDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rec.EvidenceDir(), name), []byte("MP4"), 0o440); err != nil {
		t.Fatal(err)
	}
	c, _ := link.NewCommand(link.CmdEvidenceDelete, link.FileArgs{File: name})
	if res, err := rec.Command(context.Background(), c); err != nil || res.Err() != nil {
		t.Fatalf("delete %v %v", res, err)
	}
}

func serverAudit(t *testing.T, s *Server, id string) []store.SiteAuditEntry {
	t.Helper()
	got, err := s.Store.AuditPage(context.Background(), &id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestForwardedAuditSurvivesOutagesWithoutDuplicates(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	ctx := context.Background()
	rec, gate := addGatedRecorder(t, s, "rs2")

	// The first snapshot is audited on the recording server and forwarded.
	if got := serverAudit(t, s, "rs2"); len(got) != 1 || got[0].Action != "Applied configuration" || got[0].User != "Management server" {
		t.Fatalf("after connect %+v", got)
	}

	// Management server down: entries wait in the outbox.
	gate.set(true, false)
	deleteEvidenceOn(t, rec, "case-a.mp4")
	deleteEvidenceOn(t, rec, "case-b.mp4")
	rec.Wait()
	if n := rec.Pending(); n != 2 || len(serverAudit(t, s, "rs2")) != 1 {
		t.Fatalf("pending %d", n)
	}
	_ = s.Store.Audit(ctx, "boss", "Test", "Own entry while rs2 was away", "") // interleaved with the forwarded ones

	// Back, but the acknowledgement gets lost: stored, still queued (the
	// replay stops at the first failure to keep the order).
	gate.set(false, true)
	rec.Replay(ctx)
	if n := rec.Pending(); n != 2 || len(serverAudit(t, s, "rs2")) != 2 {
		t.Fatalf("lost acks: pending %d, stored %d", n, len(serverAudit(t, s, "rs2")))
	}
	// The replay after reconnecting sends them again; they're stored once.
	gate.set(false, false)
	rec.Replay(ctx)
	got := serverAudit(t, s, "rs2")
	if rec.Pending() != 0 || len(got) != 3 || got[0].Detail != "case-b.mp4" || got[1].Detail != "case-a.mp4" {
		t.Fatalf("after replay %+v, pending %d", got, rec.Pending())
	}

	// Both chains verify.
	code, v := v2Call(t, h, "GET", "/api/v2/audit/verify", admin, "")
	servers, _ := v["servers"].([]any)
	if code != 200 || v["ok"] != true || len(servers) != 1 {
		t.Fatalf("verify %d %v", code, v)
	}
	if rs := servers[0].(map[string]any); rs["serverId"] != "rs2" || rs["ok"] != true || rs["entries"] != 3.0 {
		t.Fatalf("rs2 chain %v", rs)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/audit?serverId=rs2", admin, ""); code != 200 || len(items(t, m)) != 3 || items(t, m)[0]["serverId"] != "rs2" {
		t.Fatalf("filtered %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/audit?serverId=", admin, ""); code != 200 {
		t.Fatalf("own %d", code)
	} else {
		for _, e := range items(t, m) {
			if e["serverId"] != "" {
				t.Fatalf("own entries include %v", e)
			}
		}
	}

	// Editing a forwarded entry breaks the site chain there and rs2's.
	tampered := got[1].ID
	if _, err := s.Store.DB.Exec(`UPDATE audit SET detail = 'case-z.mp4' WHERE id = ?`, tampered); err != nil {
		t.Fatal(err)
	}
	_, v = v2Call(t, h, "GET", "/api/v2/audit/verify", admin, "")
	rs := v["servers"].([]any)[0].(map[string]any)
	if v["ok"] != false || v["brokenAt"] != tampered || rs["ok"] != false || rs["entries"] != 1.0 {
		t.Fatalf("tampered %v", v)
	}
}

// The recording server's chain hash is the management side's.
func TestAuditHashMatchesTheStore(t *testing.T) {
	e := link.AuditEntry{ID: store.NewID(), Time: 1760000000, User: "System", Area: "Events", Action: "Deleted expired events", Detail: "4 events", PreviousHash: "ab"}
	if e.Hash() != store.AuditChainHash(store.AuditEntry{ID: e.ID, Time: e.Time, User: e.User, Area: e.Area, Action: e.Action, Detail: e.Detail, PreviousHash: e.PreviousHash}) {
		t.Fatal("link.AuditEntry.Hash differs from the store's chain hash")
	}
}

// --role all: the local recorder doesn't repeat what the management side
// audits (here: an evidence file it was asked to delete).
func TestLocalRecorderDoesNotDoubleAudit(t *testing.T) {
	s := newCompanionServer(t)
	syncCams(t, s)
	deleteEvidenceOn(t, recOf(s), "case-l.mp4")
	recOf(s).Wait()
	if got := serverAudit(t, s, store.LocalRecorderID); len(got) != 0 {
		t.Fatalf("local recorder audited %+v", got)
	}
}

func TestRecordingServerLog(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	super := v2Token(t, s, "sup", "Supervisor")
	op := v2Token(t, s, "op", "Operator")
	s.Logs = func(n int) string { return strings.TrimSuffix(strings.Repeat("local line\n", n), "\n") }

	get := func(path, tok string) *httptest.ResponseRecorder {
		t.Helper()
		return do(t, h, "GET", path, tok, "")
	}
	// Local: this process's log.
	w := get("/api/v2/recording-servers/local/log?lines=2", admin)
	if w.Code != 200 || w.Body.String() != "local line\nlocal line\n" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("local %d %q %v", w.Code, w.Body, w.Header())
	}
	if w := get("/api/v2/recording-servers/local/log", super); w.Code != 200 || strings.Count(w.Body.String(), "\n") != 300 {
		t.Fatalf("supervisor default %d, %d lines", w.Code, strings.Count(w.Body.String(), "\n"))
	}
	if w := get("/api/v2/recording-servers/local/log", op); w.Code != 403 {
		t.Fatalf("operator %d", w.Code)
	}
	for _, q := range []string{"0", "2001", "many"} {
		if w := get("/api/v2/recording-servers/local/log?lines="+q, admin); w.Code != 400 {
			t.Fatalf("lines=%s: %d", q, w.Code)
		}
	}
	if w := get("/api/v2/recording-servers/nope/log", admin); w.Code != 404 {
		t.Fatalf("unknown %d", w.Code)
	}

	// Another server: asked over its link; secrets are stripped again here.
	rec := addRecorder(t, s, "rs2", "Warehouse")
	if w := get("/api/v2/recording-servers/rs2/log", admin); w.Code != 503 {
		t.Fatalf("no log kept: %d", w.Code) // it answers "unavailable"
	}
	var asked int
	var mu sync.Mutex
	rec.Log = func(n int) string {
		mu.Lock()
		asked = n
		mu.Unlock()
		return "rs2 started\nsource=rtsp://admin:hunter2@10.0.0.5/s"
	}
	w = get("/api/v2/recording-servers/rs2/log?lines=50", admin)
	if w.Code != 200 || asked != 50 || strings.Contains(w.Body.String(), "hunter2") || !strings.HasPrefix(w.Body.String(), "rs2 started\n") {
		t.Fatalf("rs2 %d %q (asked %d)", w.Code, w.Body, asked)
	}

	// Offline: enrolled but not connected.
	if err := s.Store.AddRecordingServer(context.Background(), &store.RecordingServer{ID: "rs3", Name: "Yard"}); err != nil {
		t.Fatal(err)
	}
	w = get("/api/v2/recording-servers/rs3/log", admin)
	_, m := v2Call(t, h, "GET", "/api/v2/recording-servers/rs3/log", admin, "")
	if w.Code != 503 || errCode(m) != codeUnavailable {
		t.Fatalf("offline %d %v", w.Code, m)
	}

	// Each successful fetch is audited.
	n := 0
	entries, _ := s.Store.AuditLog(context.Background(), 100)
	for _, e := range entries {
		if e.Action == "Viewed recording server log" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("audited %d fetches", n)
	}
}

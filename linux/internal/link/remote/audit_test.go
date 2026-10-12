package remote

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/recorder"
)

// The recording server's audit chain crosses the control link like its
// events: acknowledged, queued while the management server is down,
// replayed once on reconnect. Its log is a command.
func TestAuditForwardingAndLogOverTheLink(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	dir := t.TempDir()
	id := enroll(t, s, dir)
	r := startRecWith(t, dir, func(lr LocalRecorder) LocalRecorder {
		lr.(*recorder.Recorder).Log = func(n int) string { return "control link connected\nlines=" + strconv.Itoa(n) }
		return lr
	})
	eventually(t, "connect", func() bool { return connected(s, id.ServerID) })
	forwarded := func() int {
		got, err := s.st.AuditPage(ctx, &id.ServerID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	eventually(t, "applied configuration forwarded", func() bool { return forwarded() == 1 && r.rec.Pending() == 0 })

	conn := s.l.Recorder(id.ServerID)
	c, _ := link.NewCommand(link.CmdLog, link.LogArgs{Lines: 20})
	res, err := conn.Command(ctx, c)
	var out link.LogResult
	if err != nil || res.Decode(&out) != nil || out.Text != "control link connected\nlines=20" {
		t.Fatalf("log %+v %v %q", res, err, out.Text)
	}

	// Management server down: the entry waits.
	addr := s.addr
	s.stop()
	eventually(t, "disconnect", func() bool { return !connected(s, id.ServerID) })
	if err := os.MkdirAll(r.rec.EvidenceDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.rec.EvidenceDir(), "case-9.mp4"), []byte("MP4"), 0o440); err != nil {
		t.Fatal(err)
	}
	c, _ = link.NewCommand(link.CmdEvidenceDelete, link.FileArgs{File: "case-9.mp4"})
	if res, err := r.rec.Command(ctx, c); err != nil || res.Err() != nil {
		t.Fatalf("delete %+v %v", res, err)
	}
	r.rec.Wait()
	if r.rec.Pending() != 1 || forwarded() != 1 {
		t.Fatalf("pending %d, forwarded %d", r.rec.Pending(), forwarded())
	}

	// Back: replayed once; the same snapshot again isn't a new entry.
	s.start(addr)
	eventually(t, "replay", func() bool { return connected(s, id.ServerID) && r.rec.Pending() == 0 })
	r.rec.Replay(ctx)
	if n := forwarded(); n != 2 {
		got, _ := s.st.AuditPage(ctx, &id.ServerID, 0, 100)
		t.Fatalf("forwarded %d: %+v", n, got)
	}
	site, servers, err := s.st.VerifyAuditChains(ctx)
	if err != nil || site.BrokenID != "" || len(servers) != 1 || servers[0].ServerID != id.ServerID || servers[0].Entries != 2 || servers[0].BrokenID != "" {
		t.Fatalf("verify %+v %+v %v", site, servers, err)
	}
}

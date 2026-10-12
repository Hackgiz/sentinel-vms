package recorder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
)

func command(t *testing.T, r *Recorder, typ string, args any) link.Result {
	t.Helper()
	c, err := link.NewCommand(typ, args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Command(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func writeEvidence(t *testing.T, r *Recorder, name string) {
	t.Helper()
	if err := os.MkdirAll(r.EvidenceDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.EvidenceDir(), name), []byte("MP4"), 0o440); err != nil {
		t.Fatal(err)
	}
}

func audits(events []link.Event) []link.AuditEntry {
	var out []link.AuditEntry
	for _, e := range events {
		if e.Type == link.EventAudit && e.Audit != nil {
			out = append(out, *e.Audit)
		}
	}
	return out
}

func TestAuditChainIsQueuedAndForwardedInOrder(t *testing.T) {
	r, sink := newTestRecorder(t)
	ctx := context.Background()
	r.ServerID = "rs-2" // a remote recording server: commands are audited here too
	sink.refuse = errors.New("management server unreachable")

	snap := link.Snapshot{Rev: 2, DefaultRetentionDays: 14, Cameras: []link.CameraConfig{{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", Recording: true}}}
	for i := 0; i < 2; i++ { // the second time as on a reconnect
		if _, err := r.ApplyConfig(ctx, snap); err != nil {
			t.Fatal(err)
		}
	}
	writeEvidence(t, r, "case-1.mp4")
	if res := command(t, r, link.CmdEvidenceDelete, link.FileArgs{File: "case-1.mp4"}); res.Err() != nil {
		t.Fatal(res.Err())
	}
	command(t, r, link.CmdEvidenceDelete, link.FileArgs{File: "missing.mp4"}) // nothing deleted, nothing audited
	r.OnMotion(detect.Motion{CameraID: camID, Score: 0.1, At: time.Now()})
	r.Wait()
	if n := r.Pending(); n != 3 || len(sink.durable()) != 0 {
		t.Fatalf("pending %d, delivered %d", n, len(sink.durable()))
	}

	// A management server away for too long: events are trimmed, audit
	// entries never are.
	if n, err := r.store.trimOutbox(ctx, -time.Hour, outboxMaxRows); err != nil || n != 1 {
		t.Fatalf("trimmed %d %v", n, err)
	}
	if r.Pending() != 2 {
		t.Fatalf("pending after trim %d", r.Pending())
	}

	sink.refuse = nil
	r.Replay(ctx)
	got := audits(sink.durable())
	if r.Pending() != 0 || len(got) != 2 {
		t.Fatalf("forwarded %+v, pending %d", got, r.Pending())
	}
	if got[0].Action != "Applied configuration" || !strings.Contains(got[0].Detail, "revision 2, 1 cameras (1 recording), default retention 14 days") ||
		got[0].User != auditByManagement || got[1].Action != "Deleted evidence file" || got[1].Detail != "case-1.mp4" {
		t.Fatalf("entries %+v", got)
	}
	prev := ""
	for _, e := range got {
		if e.PreviousHash != prev || e.Hash() != e.ChainHash {
			t.Fatalf("chain broken at %+v", e)
		}
		prev = e.ChainHash
	}
	for _, e := range sink.durable() {
		if e.Type == link.EventAudit && (e.ServerID != "rs-2" || e.ID != e.Audit.ID) {
			t.Fatalf("event %+v", e)
		}
	}

	// Restart on the same database: nothing is sent twice, and the chain
	// continues where it was.
	r2 := New(r.DataDir, r.Media, r.store, "test", "")
	r2.ServerID = "rs-2"
	s2 := &fakeSink{}
	r2.Attach(s2)
	t.Cleanup(r2.Wait)
	r2.Replay(ctx)
	if len(s2.events) != 0 {
		t.Fatalf("redelivered after restart: %+v", s2.events)
	}
	r2.audit("Events", "Deleted expired events", "3 events older than 30 days, 1 snapshots")
	r2.Wait()
	if more := audits(s2.durable()); len(more) != 1 || more[0].PreviousHash != prev || more[0].User != auditBySystem {
		t.Fatalf("continued chain %+v", more)
	}

	// A new server id (enrolled again) starts a new chain.
	r2.ServerID = "rs-3"
	r2.audit("Events", "Deleted expired events", "1 events older than 30 days, 0 snapshots")
	r2.Wait()
	if all := audits(s2.durable()); len(all) != 2 || all[1].PreviousHash != "" {
		t.Fatalf("new chain %+v", all)
	}
}

// In --role all the management side audits the user action behind each
// command; the local recorder doesn't repeat it.
func TestLocalRecorderAuditsOnlyItsOwnActions(t *testing.T) {
	r, sink := newTestRecorder(t)
	ctx := context.Background()
	if _, err := r.ApplyConfig(ctx, link.Snapshot{Rev: 7}); err != nil {
		t.Fatal(err)
	}
	writeEvidence(t, r, "case-2.mp4")
	command(t, r, link.CmdEvidenceDelete, link.FileArgs{File: "case-2.mp4"})
	r.Wait()
	if got, _ := r.store.auditLog(ctx, LocalServerID); len(got) != 0 {
		t.Fatalf("local recorder audited commands: %+v", got)
	}
	r.audit("Events", "Deleted expired events", "2 events older than 30 days, 0 snapshots")
	r.Wait()
	if got := audits(sink.durable()); len(got) != 1 || got[0].Action != "Deleted expired events" {
		t.Fatalf("own action %+v", got)
	}
}

func TestPruneEventsIsAudited(t *testing.T) {
	r, sink := newTestRecorder(t)
	ctx := context.Background()
	old := &event{ID: newEventID(), CameraID: camID, Kind: "Motion", Score: 0.1, CreatedAt: time.Now().Add(-40 * 24 * time.Hour).Unix()}
	if err := r.store.addEvent(ctx, old, link.Event{ID: old.ID, Type: link.EventDetection}); err != nil {
		t.Fatal(err)
	}
	r.Replay(ctx)
	r.pruneEvents(ctx)
	r.Wait()
	if got := audits(sink.durable()); len(got) != 1 || got[0].Detail != "1 events older than 30 days, 0 snapshots" {
		t.Fatalf("audited %+v", got)
	}
	r.pruneEvents(ctx) // nothing left: nothing audited
	r.Wait()
	if got := audits(sink.durable()); len(got) != 1 {
		t.Fatalf("audited again %+v", got)
	}
}

func TestLogCommand(t *testing.T) {
	r, _ := newTestRecorder(t)
	if res := command(t, r, link.CmdLog, link.LogArgs{Lines: 10}); res.Code != link.CodeUnavailable {
		t.Fatalf("no log kept: %+v", res)
	}
	r.Log = func(n int) string { return strings.Repeat("line\n", n-1) + "last" }
	var out link.LogResult
	if err := command(t, r, link.CmdLog, link.LogArgs{Lines: 3}).Decode(&out); err != nil || out.Text != "line\nline\nlast" {
		t.Fatalf("log %q %v", out.Text, err)
	}
	for _, n := range []int{0, link.LogMaxLines + 1} {
		if res := command(t, r, link.CmdLog, link.LogArgs{Lines: n}); res.Code != link.CodeBadRequest {
			t.Fatalf("lines %d: %+v", n, res)
		}
	}
}

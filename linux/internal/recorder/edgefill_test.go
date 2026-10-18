package recorder

import (
	"bytes"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/onvif/fakecam"
	"sentinel-linux/internal/playback"
)

const edgeCam = "7A1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F"

func writeSeg(t *testing.T, dir string, start time.Time, data []byte, end time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, start.Format("20060102-150405")+"-000000.mp4")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, end, end)
	return p
}

func TestEdgeGapsAgainstLocalSpans(t *testing.T) {
	root := t.TempDir()
	lib := &playback.Library{Root: root}
	dir := filepath.Join(root, edgeCam)
	t0 := time.Date(2026, 10, 9, 9, 0, 0, 0, time.Local)
	writeSeg(t, dir, t0, playback.SynthSegment(60, 2), t0.Add(time.Minute))
	writeSeg(t, dir, t0.Add(90*time.Second), playback.SynthSegment(60, 2), t0.Add(150*time.Second))
	writeSeg(t, dir, t0.Add(155*time.Second), playback.SynthSegment(60, 2), t0.Add(215*time.Second)) // 5 s hole: keyframe slack, not a gap
	writeSeg(t, dir, t0.Add(300*time.Second), []byte("not an mp4 yet"), t0.Add(330*time.Second))     // unindexable: still counts as recorded

	from, to := t0.Add(-time.Minute), t0.Add(400*time.Second)
	files, err := lib.Files(edgeCam, from, to)
	if err != nil {
		t.Fatal(err)
	}
	got := edgeGaps(files, from, to)
	want := []edgeSpan{
		{from, t0},
		{t0.Add(60 * time.Second), t0.Add(90 * time.Second)},
		{t0.Add(215 * time.Second), t0.Add(300 * time.Second)},
		{t0.Add(330 * time.Second), to},
	}
	if len(got) != len(want) {
		t.Fatalf("gaps %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Start.Equal(want[i].Start) || !got[i].End.Equal(want[i].End) {
			t.Errorf("gap %d: %v – %v, want %v – %v", i, got[i].Start, got[i].End, want[i].Start, want[i].End)
		}
	}
	// No recordings at all: the whole window is one gap; a window inside a
	// recording has none.
	if g := edgeGaps(nil, from, to); len(g) != 1 || !g[0].Start.Equal(from) || !g[0].End.Equal(to) {
		t.Errorf("empty: %v", g)
	}
	if g := edgeGaps(files, t0.Add(10*time.Second), t0.Add(50*time.Second)); len(g) != 0 {
		t.Errorf("inside a recording: %v", g)
	}
}

func TestPlanEdgePieces(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	rec := func(tok string, from, to time.Duration) onvif.EdgeRecording {
		return onvif.EdgeRecording{Token: tok, Earliest: t0.Add(from), Latest: t0.Add(to),
			Tracks: []onvif.EdgeTrack{{Token: "v", Type: "Video", From: t0.Add(from), To: t0.Add(to)}}}
	}
	recs := []onvif.EdgeRecording{rec("sub", 0, 20*time.Minute), rec("main", 0, 2*time.Hour)}
	gaps := []edgeSpan{
		{t0.Add(10 * time.Minute), t0.Add(50 * time.Minute)},   // 40 min: 15 + 15 + 10 from "main" (covers more than "sub")
		{t0.Add(115 * time.Minute), t0.Add(130 * time.Minute)}, // the card ends at 2 h: clipped to 5 min
		{t0.Add(3 * time.Hour), t0.Add(4 * time.Hour)},         // nothing on the card
		{t0.Add(-30 * time.Second), t0.Add(1 * time.Second)},   // only 1 s on the card: too short
	}
	got := planEdgePieces(gaps, recs)
	want := []edgePiece{
		{t0.Add(10 * time.Minute), t0.Add(25 * time.Minute), "main"},
		{t0.Add(25 * time.Minute), t0.Add(40 * time.Minute), "main"},
		{t0.Add(40 * time.Minute), t0.Add(50 * time.Minute), "main"},
		{t0.Add(115 * time.Minute), t0.Add(120 * time.Minute), "main"},
	}
	if len(got) != len(want) {
		t.Fatalf("pieces %+v", got)
	}
	for i := range want {
		if !got[i].Start.Equal(want[i].Start) || !got[i].End.Equal(want[i].End) || got[i].Recording != want[i].Recording {
			t.Errorf("piece %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestEdgeSegmentName(t *testing.T) {
	loc := time.FixedZone("X", 2*3600)
	at := time.Date(2026, 10, 16, 7, 4, 5, 123456789, time.UTC)
	name := edgeSegmentName(at, loc)
	if name != "20261016-090405-123456.mp4" {
		t.Fatalf("name %q", name)
	}
	if back, ok := playback.ParseName(name, loc); !ok || !back.Equal(at.Truncate(time.Microsecond)) {
		t.Fatalf("round trip %v %v", back, ok)
	}
}

// edgeFixture: a recorder whose camera has two local segments with a two
// minute hole between them, and a Profile G fakecam holding that time.
func edgeFixture(t *testing.T, profileG bool) (*Recorder, *fakecam.Camera, time.Time) {
	t.Helper()
	r := New(t.TempDir(), nil, nil, "test", "")
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	dir := filepath.Join(r.RecordingsRoot(), edgeCam)
	writeSeg(t, dir, start, playback.SynthSegment(60, 2), start.Add(time.Minute))
	writeSeg(t, dir, start.Add(3*time.Minute), playback.SynthSegment(60, 2), start.Add(4*time.Minute))

	cam := &fakecam.Camera{User: "admin", Pass: "pw", Manufacturer: "Acme", Model: "G1",
		Streams: []fakecam.Stream{{Token: "main", Name: "Main", Encoding: "H264", Width: 1920, Height: 1080, FPS: 25, RTSPURL: "rtsp://127.0.0.1:1/live"}}}
	if profileG {
		cam.EnableProfileG(start.Add(-time.Hour), start.Add(time.Hour))
		ln, err := net.Listen("tcp", "127.0.0.1:0") // OS-chosen free port
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		cam.ServeReplay(ln)
	}
	srv := httptest.NewServer(cam)
	t.Cleanup(srv.Close)
	r.cfgMu.Lock()
	r.cams = map[string]link.CameraConfig{edgeCam: {ID: edgeCam, Name: "Yard", ONVIFURL: srv.URL + "/onvif/device_service", Username: "admin", Password: "pw", Recording: true}}
	r.cfgMu.Unlock()
	t.Cleanup(r.Wait)
	return r, cam, start
}

// edgeFFmpeg installs a fake ffmpeg that logs its arguments and writes the
// synthetic recording as its output (the last argument).
func edgeFFmpeg(t *testing.T, seconds int) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "replay.mp4")
	if err := os.WriteFile(src, playback.SynthSegment(seconds, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "args.log")
	fakeFFmpegBin(t, "echo \"$*\" >> "+log+"\nfor last; do :; done\ncp "+src+" \"$last\"")
	return log
}

func waitEdge(t *testing.T, r *Recorder, id string) link.EdgeRecoveryStatus {
	t.Helper()
	for i := 0; i < 1000; i++ {
		st, code, msg := r.edgeRecovery(link.EdgeRecoveryArgs{Op: link.EdgeOpStatus, JobID: id})
		if code != "" {
			t.Fatal(code, msg)
		}
		if st.State != link.EdgeRunning {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("recovery did not finish")
	return link.EdgeRecoveryStatus{}
}

func TestEdgeRecoveryFillsGapWithMarkedSegment(t *testing.T) {
	log := edgeFFmpeg(t, 120)
	r, _, start := edgeFixture(t, true)
	dir := filepath.Join(r.RecordingsRoot(), edgeCam)
	before := map[string][]byte{}
	for _, n := range []string{start.Format("20060102-150405") + "-000000.mp4", start.Add(3*time.Minute).Format("20060102-150405") + "-000000.mp4"} {
		before[n], _ = os.ReadFile(filepath.Join(dir, n))
	}
	// An orphan marker (its segment expired) is swept by the job.
	orphan := filepath.Join(dir, "20200101-000000-000000.mp4.edge")
	_ = os.WriteFile(orphan, []byte("{}"), 0o644)

	from, to := start, start.Add(4*time.Minute)
	st, code, msg := r.edgeRecovery(link.EdgeRecoveryArgs{Op: link.EdgeOpStart, CameraID: edgeCam, From: from.UnixMilli(), To: to.UnixMilli()})
	if code != "" || st.JobID == "" || st.Gaps != 1 {
		t.Fatal(code, msg, st)
	}
	st = waitEdge(t, r, st.JobID)
	if st.State != link.EdgeDone || st.Total != 1 || st.Done != 1 || st.Recovered != 1 || st.Seconds != 120 || st.Error != "" {
		t.Fatalf("status %+v", st)
	}
	name := edgeSegmentName(start.Add(time.Minute), nil)
	seg := filepath.Join(dir, name)
	if b, err := os.ReadFile(seg); err != nil || !bytes.Equal(b, playback.SynthSegment(120, 2)) {
		t.Fatalf("recovered segment %s: %v", name, err)
	}
	if b, err := os.ReadFile(seg + ".edge"); err != nil || !strings.Contains(string(b), `"recording":"rec1"`) {
		t.Fatalf("marker %q %v", b, err)
	}
	if info, _ := os.Stat(seg); !info.ModTime().Equal(start.Add(3 * time.Minute)) {
		t.Errorf("segment mtime %v, want its end", info.ModTime())
	}
	for n, b := range before {
		if now, _ := os.ReadFile(filepath.Join(dir, n)); !bytes.Equal(now, b) {
			t.Errorf("local segment %s changed", n)
		}
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("orphan marker not swept")
	}
	// ffmpeg was pointed at the loopback relay, over TCP, for the piece's length.
	args, _ := os.ReadFile(log)
	for _, want := range []string{"-rtsp_transport tcp", "-i rtsp://127.0.0.1:", "/replay/rec1", "-t 120.000", "+frag_keyframe"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("ffmpeg args %q lack %q", args, want)
		}
	}
	if strings.Contains(string(args), "pw") {
		t.Error("credentials passed to ffmpeg")
	}

	// The timeline shows three spans; the middle one is marked as recovered.
	tl, err := r.timeline(link.RangeArgs{CameraID: edgeCam, From: from.UnixMilli(), To: to.UnixMilli()})
	if err != nil || len(tl.Spans) != 3 {
		t.Fatalf("timeline %+v %v", tl, err)
	}
	if tl.Spans[0].Edge || !tl.Spans[1].Edge || tl.Spans[2].Edge || tl.Spans[1].Start != start.Add(time.Minute).UnixMilli() {
		t.Fatalf("edge flags %+v", tl.Spans)
	}

	// Again: the hole is filled, nothing to do and nothing rewritten.
	st, code, msg = r.edgeRecovery(link.EdgeRecoveryArgs{Op: link.EdgeOpStart, CameraID: edgeCam, From: from.UnixMilli(), To: to.UnixMilli()})
	if code != "" {
		t.Fatal(code, msg)
	}
	if st = waitEdge(t, r, st.JobID); st.State != link.EdgeDone || st.Gaps != 0 || st.Recovered != 0 {
		t.Fatalf("second run %+v", st)
	}
	r.Wait()
	if left, _ := os.ReadDir(r.edgeWorkDir()); len(left) != 0 {
		t.Fatalf("work dir not cleaned: %v", left)
	}
}

func TestEdgeRecoveryNeverOverwrites(t *testing.T) {
	edgeFFmpeg(t, 120)
	r, _, start := edgeFixture(t, true)
	dir := filepath.Join(r.RecordingsRoot(), edgeCam)
	// A file already holds the name the recovered segment would get (empty,
	// so the index ignores it and the hole still looks like a hole).
	name := edgeSegmentName(start.Add(time.Minute), nil)
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	st, code, msg := r.edgeRecovery(link.EdgeRecoveryArgs{Op: link.EdgeOpStart, CameraID: edgeCam, From: start.UnixMilli(), To: start.Add(4 * time.Minute).UnixMilli()})
	if code != "" {
		t.Fatal(code, msg)
	}
	st = waitEdge(t, r, st.JobID)
	if st.State != link.EdgeDone || st.Recovered != 0 || st.Skipped != 1 {
		t.Fatalf("status %+v", st)
	}
	if info, _ := os.Stat(filepath.Join(dir, name)); info.Size() != 0 {
		t.Fatal("existing file was overwritten")
	}
	if _, err := os.Stat(filepath.Join(dir, name+".edge")); !os.IsNotExist(err) {
		t.Fatal("marker written for a segment we did not create")
	}

	// placeEdgeSegment also re-checks local footage: a piece over recorded
	// time is dropped.
	tmp := filepath.Join(t.TempDir(), "p.mp4")
	_ = os.WriteFile(tmp, playback.SynthSegment(30, 2), 0o644)
	got, err := r.placeEdgeSegment(edgeCam, tmp, edgePiece{Start: start.Add(10 * time.Second), End: start.Add(40 * time.Second), Recording: "rec1"}, 30*time.Second)
	if err != nil || got != 0 {
		t.Fatalf("piece over local footage: %v %v", got, err)
	}
}

func TestEdgeRecoveryCancel(t *testing.T) {
	fakeFFmpegBin(t, "exec sleep 30")
	r, _, start := edgeFixture(t, true)
	st, code, msg := r.edgeRecovery(link.EdgeRecoveryArgs{Op: link.EdgeOpStart, CameraID: edgeCam, From: start.UnixMilli(), To: start.Add(4 * time.Minute).UnixMilli()})
	if code != "" {
		t.Fatal(code, msg)
	}
	// Wait until ffmpeg runs (the plan is made), then cancel.
	for i := 0; i < 500; i++ {
		if s, _, _ := r.edgeRecovery(link.EdgeRecoveryArgs{Op: link.EdgeOpStatus, JobID: st.JobID}); s.Total > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, code, msg = r.edgeRecovery(link.EdgeRecoveryArgs{Op: link.EdgeOpCancel, JobID: st.JobID}); code != "" {
		t.Fatal(code, msg)
	}
	if st = waitEdge(t, r, st.JobID); st.State != link.EdgeCanceled || st.Recovered != 0 {
		t.Fatalf("status %+v", st)
	}
	r.Wait()
	if left, _ := os.ReadDir(r.edgeWorkDir()); len(left) != 0 {
		t.Fatalf("work dir not cleaned: %v", left)
	}
	entries, _ := os.ReadDir(filepath.Join(r.RecordingsRoot(), edgeCam))
	if len(entries) != 2 {
		t.Fatalf("recordings dir changed: %v", entries)
	}
}

func TestEdgeRecoveryRefusalsAndFailures(t *testing.T) {
	fakeFFmpegBin(t, "exec sleep 30")
	r, _, start := edgeFixture(t, false)
	from, to := start.UnixMilli(), start.Add(4*time.Minute).UnixMilli()
	r.cfgMu.Lock()
	r.cams["8B1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F"] = link.CameraConfig{ID: "8B1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F", Name: "No ONVIF"}
	r.cfgMu.Unlock()
	cases := map[string]link.EdgeRecoveryArgs{
		"bad camera":     {Op: link.EdgeOpStart, CameraID: "../x", From: from, To: to},
		"unknown camera": {Op: link.EdgeOpStart, CameraID: "9C1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F", From: from, To: to},
		"no onvif":       {Op: link.EdgeOpStart, CameraID: "8B1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F", From: from, To: to},
		"over 24 h":      {Op: link.EdgeOpStart, CameraID: edgeCam, From: from - 25*3600*1000, To: to},
		"backwards":      {Op: link.EdgeOpStart, CameraID: edgeCam, From: to, To: from},
		"future":         {Op: link.EdgeOpStart, CameraID: edgeCam, From: time.Now().Add(time.Hour).UnixMilli(), To: time.Now().Add(2 * time.Hour).UnixMilli()},
		"unknown op":     {Op: "bogus"},
		"unknown job":    {Op: link.EdgeOpStatus, JobID: "nope"},
	}
	for name, a := range cases {
		if _, code, _ := r.edgeRecovery(a); code == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	// A camera without Profile G: the job fails with a clear reason.
	st, code, msg := r.edgeRecovery(link.EdgeRecoveryArgs{Op: link.EdgeOpStart, CameraID: edgeCam, From: from, To: to})
	if code != "" {
		t.Fatal(code, msg)
	}
	if st = waitEdge(t, r, st.JobID); st.State != link.EdgeFailed || st.Code != link.CodeUnavailable || !strings.Contains(st.Error, "Profile G") {
		t.Fatalf("status %+v", st)
	}
}

func TestEdgeRecoveryOneJobPerCamera(t *testing.T) {
	fakeFFmpegBin(t, "exec sleep 30")
	r, _, start := edgeFixture(t, true)
	a := link.EdgeRecoveryArgs{Op: link.EdgeOpStart, CameraID: edgeCam, From: start.UnixMilli(), To: start.Add(4 * time.Minute).UnixMilli()}
	st, code, _ := r.edgeRecovery(a)
	if code != "" {
		t.Fatal(code)
	}
	if _, code, _ := r.edgeRecovery(a); code != link.CodeBusy {
		t.Fatalf("second job: %q", code)
	}
	_, _, _ = r.edgeRecovery(link.EdgeRecoveryArgs{Op: link.EdgeOpCancel, JobID: st.JobID})
	waitEdge(t, r, st.JobID)
}

func TestEdgeProbeStatus(t *testing.T) {
	r, _, _ := edgeFixture(t, true)
	cams := []link.CameraConfig{r.cams[edgeCam], {ID: "8B1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F", Name: "No ONVIF"}}
	r.syncEdgeProbes(cams)
	deadline := time.Now().Add(10 * time.Second)
	for !r.edgeRecordingStatus(edgeCam) {
		if time.Now().After(deadline) {
			t.Fatal("probe never reported Profile G")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st := r.Status(); !st.Cameras[edgeCam].EdgeRecording {
		t.Fatalf("status %+v", st.Cameras)
	}
	if r.edgeRecordingStatus("8B1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F") {
		t.Error("camera without ONVIF reported Profile G")
	}
	// A camera that leaves the configuration is forgotten.
	r.syncEdgeProbes(nil)
	if r.edgeRecordingStatus(edgeCam) {
		t.Error("removed camera still reported")
	}
}

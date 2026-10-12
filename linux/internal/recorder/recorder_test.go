package recorder

import (
	"bytes"
	"context"
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/onvif"
)

// fakeSink records what the recorder reports.
type fakeSink struct {
	mu     sync.Mutex
	events []link.Event
	status []link.ServerStatus
	refuse error
}

func (f *fakeSink) Event(_ context.Context, e link.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil && !e.Ephemeral {
		return f.refuse
	}
	f.events = append(f.events, e)
	return nil
}

func (f *fakeSink) Status(s link.ServerStatus) {
	f.mu.Lock()
	f.status = append(f.status, s)
	f.mu.Unlock()
}

func (f *fakeSink) types() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		out = append(out, e.Type)
	}
	return out
}

func (f *fakeSink) durable() []link.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []link.Event
	for _, e := range f.events {
		if !e.Ephemeral {
			out = append(out, e)
		}
	}
	return out
}

const camID = "07ACA52B-E211-4DD7-BCD0-1CC4B1BDD258"

func newTestRecorder(t *testing.T) (*Recorder, *fakeSink) {
	t.Helper()
	dir := t.TempDir()
	st, err := OpenStore(filepath.Join(dir, "recorder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	sup := &media.Supervisor{ConfigPath: filepath.Join(dir, "mediamtx.yml"), RecordingsRoot: filepath.Join(dir, "recordings"), Ports: media.DefaultPorts}
	r := New(dir, sup, st, "test", "")
	r.Ctx = context.Background()
	t.Cleanup(r.Wait) // background work ends before the store closes and the directory goes
	sink := &fakeSink{}
	r.Attach(sink)
	if _, err := r.ApplyConfig(context.Background(), link.Snapshot{Rev: 1, Cameras: []link.CameraConfig{
		{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s", Username: "admin", Password: "p@ss", Recording: true, MotionLevel: 2, CameraAnalytics: true},
	}}); err != nil {
		t.Fatal(err)
	}
	return r, sink
}

func TestApplyConfigWritesMediaConfigAndReportsStatus(t *testing.T) {
	r, _ := newTestRecorder(t)
	b, err := os.ReadFile(r.Media.ConfigPath)
	if err != nil || !strings.Contains(string(b), camID) || !strings.Contains(string(b), "p%40ss") {
		t.Fatalf("mediamtx config: %v\n%s", err, b)
	}
	a, err := r.ApplyConfig(context.Background(), link.Snapshot{Rev: 2, Cameras: []link.CameraConfig{
		{ID: camID, Name: "Door", RTSPURL: "rtsp://10.0.0.9/s"},
		{ID: "B", Name: "Broken", RTSPURL: "rtsp://[bad"},
	}})
	if err != nil || a.Rev != 2 || a.Errors["B"] == "" || a.Errors[camID] != "" {
		t.Fatalf("applied %+v %v", a, err)
	}
	st := r.Status()
	if st.ConfigRev != 2 || st.ServerID != LocalServerID || len(st.Cameras) != 2 || st.Capabilities.Detection {
		t.Fatalf("status %+v", st)
	}
}

func TestDetectionEventsAreDeliveredAndQueued(t *testing.T) {
	r, sink := newTestRecorder(t)
	t0 := time.Now()
	for i := 0; i <= 140; i++ { // 70 s of motion at 2 fps → episode start + one per minute
		r.OnMotion(detect.Motion{CameraID: camID, Score: 0.1, At: t0.Add(time.Duration(i) * 500 * time.Millisecond)})
	}
	r.OnMotion(detect.Motion{CameraID: "unknown", Score: 0.1, At: t0})
	got := sink.durable()
	if len(got) != 2 || got[0].Detection == nil || got[0].Detection.Kind != "Motion" || got[0].Detection.Source != link.SourceMotion {
		t.Fatalf("delivered %+v", got)
	}
	// The second event continues the first one's alarm (merge boundary =
	// the first event's time); the first one starts a new alarm.
	first, second := got[0].Detection, got[1].Detection
	if first.MergeSince <= first.At || second.MergeSince != first.At {
		t.Fatalf("merge boundaries %d/%d, %d/%d", first.At, first.MergeSince, second.At, second.MergeSince)
	}
	if got[0].ID != first.EventID || got[0].CameraID != camID {
		t.Fatalf("event id/camera: %+v", got[0])
	}
	if n := r.Pending(); n != 0 {
		t.Fatalf("outbox holds %d delivered events", n)
	}
	activity := 0
	for _, typ := range sink.types() {
		if typ == link.EventActivity {
			activity++
		}
	}
	if activity != 142 {
		t.Fatalf("activity events = %d", activity)
	}
}

func TestOutboxKeepsEventsUntilTheManagementServerTakesThem(t *testing.T) {
	r, sink := newTestRecorder(t)
	sink.refuse = errors.New("management server unreachable")
	t0 := time.Now()
	r.OnMotion(detect.Motion{CameraID: camID, Score: 0.1, At: t0})
	r.OnObjects(detect.Detections{CameraID: camID, At: t0, Objects: []detect.Object{{Kind: "Person", Label: "person", Score: 0.8, Box: [4]float64{0.1, 0.1, 0.2, 0.5}}}})
	if n := r.Pending(); n != 2 || len(sink.durable()) != 0 {
		t.Fatalf("pending %d, delivered %d", n, len(sink.durable()))
	}
	r.Replay(context.Background()) // still refused: nothing lost
	if r.Pending() != 2 {
		t.Fatal("refused replay dropped events")
	}
	sink.refuse = nil
	r.Replay(context.Background())
	got := sink.durable()
	if r.Pending() != 0 || len(got) != 2 || got[0].Detection.Kind != "Motion" || got[1].Detection.Kind != "Person" || got[1].Detection.Box == "" {
		t.Fatalf("replayed %+v, pending %d", got, r.Pending())
	}
	// A new recorder on the same database (restart) has nothing left to send.
	r2 := New(r.DataDir, r.Media, r.store, "test", "")
	s2 := &fakeSink{}
	r2.Attach(s2)
	r2.Replay(context.Background())
	if len(s2.events) != 0 {
		t.Fatalf("redelivered after restart: %+v", s2.events)
	}
}

func TestOutboxSurvivesRestart(t *testing.T) {
	r, sink := newTestRecorder(t)
	sink.refuse = errors.New("down")
	r.OnMotion(detect.Motion{CameraID: camID, Score: 0.1, At: time.Now()})
	r2 := New(r.DataDir, r.Media, r.store, "test", "")
	s2 := &fakeSink{}
	r2.Attach(s2)
	r2.Replay(context.Background())
	if d := s2.durable(); len(d) != 1 || d[0].Detection.Kind != "Motion" {
		t.Fatalf("after restart: %+v", d)
	}
}

func TestCameraAnalyticsEvents(t *testing.T) {
	r, sink := newTestRecorder(t)
	t0 := time.Now()
	r.OnCameraEvent(camID, onvif.AnalyticsEvent{Kind: "Line crossing", Label: "person", Rule: "Gate", Topic: "RuleEngine/LineDetector/Crossed", Active: true, At: t0})
	r.OnCameraEvent(camID, onvif.AnalyticsEvent{Kind: "Intrusion", Active: false, At: t0}) // rule ending
	r.OnCameraEvent(camID, onvif.AnalyticsEvent{Kind: "Motion", Active: true, At: t0})     // our detector covers motion
	got := sink.durable()
	if len(got) != 1 {
		t.Fatalf("events %+v", got)
	}
	d := got[0].Detection
	if d.Source != link.SourceCamera || d.Kind != "Line crossing" || d.Score != 1 || d.Rule != "Gate" || d.Summary != `a line crossing (person, rule "Gate")` {
		t.Fatalf("detection %+v", d)
	}
	// Our detector and the camera share the per-kind episode.
	r.OnObjects(detect.Detections{CameraID: camID, At: t0, Objects: []detect.Object{{Kind: "Person", Label: "person", Score: 0.8}}})
	r.OnCameraEvent(camID, onvif.AnalyticsEvent{Kind: "Person", Label: "person", Active: true, At: t0.Add(2 * time.Second)})
	if n := len(sink.durable()); n != 2 {
		t.Fatalf("person seen by both detectors gave %d events", n-1)
	}
}

func TestStorageHealthThresholds(t *testing.T) {
	r, sink := newTestRecorder(t)
	ctx := context.Background()
	var st storageState
	pct := 3.0
	stat := func(string) (float64, uint64, bool) { return pct, 2e9, true }
	r.checkStorage(ctx, &st, "/", stat)
	r.checkStorage(ctx, &st, "/", stat) // still low: reported once
	pct = 6                             // between thresholds: nothing
	r.checkStorage(ctx, &st, "/", stat)
	pct = 20
	r.checkStorage(ctx, &st, "/", stat)
	got := sink.durable()
	if len(got) != 2 || got[0].Type != link.EventStorageLow || got[0].Storage.FreePct != 3 || got[0].Storage.FreeBytes != 2e9 ||
		got[1].Type != link.EventStorageRecovered || got[1].Storage.FreePct != 20 {
		t.Fatalf("storage events %+v", got)
	}
}

func TestEvidenceLockAndAdopt(t *testing.T) {
	r, _ := newTestRecorder(t)
	ctx := context.Background()
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	dir := filepath.Join(r.RecordingsRoot(), camID)
	_ = os.MkdirAll(dir, 0o755)
	seg := filepath.Join(dir, start.Format("20060102-150405")+"-000001.mp4")
	if err := os.WriteFile(seg, []byte("fake mp4 bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(seg, start.Add(time.Minute), start.Add(time.Minute))

	cmd := func(typ string, args any) link.Result {
		c, _ := link.NewCommand(typ, args)
		res, err := r.Command(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	var cov link.CoversResult
	if err := cmd(link.CmdCovers, link.CoversArgs{Items: []link.CoverQuery{{CameraID: camID, At: start.Add(20 * time.Second).Unix()}, {CameraID: camID, At: start.Add(-time.Hour).Unix()}}}).Decode(&cov); err != nil ||
		len(cov.Covered) != 2 || !cov.Covered[0] || cov.Covered[1] {
		t.Fatalf("covers %+v %v", cov, err)
	}
	var f link.EvidenceFile
	if err := cmd(link.CmdEvidenceLock, link.EvidenceLockArgs{CameraID: camID, At: start.Add(20 * time.Second).Unix(), CaseID: "HG-20261011-00001"}).Decode(&f); err != nil ||
		f.File != "HG-20261011-00001.mp4" || f.Size != 14 || len(f.SHA256) != 64 || f.Start != start.Unix() {
		t.Fatalf("lock %+v %v", f, err)
	}
	// The same case id is never overwritten.
	res := cmd(link.CmdEvidenceLock, link.EvidenceLockArgs{CameraID: camID, At: start.Add(20 * time.Second).Unix(), CaseID: "HG-20261011-00001"})
	if res.Code != link.CodeInternal {
		t.Fatalf("relock %+v", res)
	}
	res = cmd(link.CmdEvidenceLock, link.EvidenceLockArgs{CameraID: camID, At: start.Add(-time.Hour).Unix(), CaseID: "HG-20261011-00002"})
	if res.Code != link.CodeNoClip {
		t.Fatalf("no clip %+v", res)
	}
	// Evidence files are served on the video plane.
	rec := httptest.NewRecorder()
	r.Video().ServeHTTP(rec, httptest.NewRequest("GET", "/rs/evidence/HG-20261011-00001.mp4", nil))
	if rec.Code != 200 || rec.Body.String() != "fake mp4 bytes" {
		t.Fatalf("evidence file %d %q", rec.Code, rec.Body)
	}
	for _, bad := range []string{"/rs/evidence/..%2Fsentinel.db", "/rs/evidence/missing.mp4"} {
		rec = httptest.NewRecorder()
		r.Video().ServeHTTP(rec, httptest.NewRequest("GET", bad, nil))
		if rec.Code != 404 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	cmd(link.CmdEvidenceDelete, link.FileArgs{File: "HG-20261011-00001.mp4"})
	if _, err := os.Stat(filepath.Join(r.EvidenceDir(), "HG-20261011-00001.mp4")); !os.IsNotExist(err) {
		t.Fatal("evidence not deleted")
	}

	// Adopt: a prepared download moves into the vault, never over a case file.
	_ = os.MkdirAll(r.ExportsDir(), 0o750)
	token := strings.Repeat("ab", 16)
	_ = os.WriteFile(filepath.Join(r.ExportsDir(), token+".mp4"), []byte("clip"), 0o640)
	if err := cmd(link.CmdEvidenceAdopt, link.EvidenceAdoptArgs{Token: token, CaseID: "HG-20261011-00003"}).Decode(&f); err != nil || f.Size != 4 {
		t.Fatalf("adopt %+v %v", f, err)
	}
	info, err := os.Stat(filepath.Join(r.EvidenceDir(), "HG-20261011-00003.mp4"))
	if err != nil || info.Mode().Perm() != 0o440 {
		t.Fatalf("vault file %v %v", info, err)
	}
	if res := cmd(link.CmdEvidenceAdopt, link.EvidenceAdoptArgs{Token: token, CaseID: "HG-20261011-00003"}); res.Code != link.CodeNotFound {
		t.Fatalf("second adopt %+v", res)
	}
	if res := cmd("no.such.command", nil); res.Code != link.CodeUnknownCommand {
		t.Fatalf("unknown %+v", res)
	}
}

func TestVideoPlaneRejectsOtherPaths(t *testing.T) {
	r, _ := newTestRecorder(t)
	for _, p := range []string{"/", "/api/v1/users", "/rs/events/..", "/live/not-a-camera/index.m3u8"} {
		rec := httptest.NewRecorder()
		r.Video().ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code == 200 {
			t.Errorf("%s served", p)
		}
	}
	rec := httptest.NewRecorder()
	r.Video().ServeHTTP(rec, httptest.NewRequest("POST", "/webrtc/"+camID+"/whep", strings.NewReader("v=0")))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("WHEP with WebRTC off: %d", rec.Code)
	}
}

// Moved from internal/server with the code they test.

func TestCompleteMP4Prefix(t *testing.T) {
	box := func(typ string, payload int) []byte {
		n := 8 + payload
		b := []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n), typ[0], typ[1], typ[2], typ[3]}
		return append(b, make([]byte, payload)...)
	}
	file := append(append(box("ftyp", 12), box("moov", 40)...), box("moof", 30)...)
	whole := int64(len(file))
	partial := append(file, box("mdat", 500)[:100]...) // fragment still being written
	n, err := completeMP4Prefix(bytes.NewReader(partial), int64(len(partial)))
	if err != nil || n != whole {
		t.Fatalf("prefix = %d, %v; want %d", n, err, whole)
	}
}

func TestDescribeCameraEvent(t *testing.T) {
	for _, c := range []struct {
		e    onvif.AnalyticsEvent
		want string
	}{
		{onvif.AnalyticsEvent{Kind: "Line crossing", Label: "person", Rule: "Gate"}, `a line crossing (person, rule "Gate")`},
		{onvif.AnalyticsEvent{Kind: "Intrusion", Label: "Fence", Rule: "Fence"}, `an intrusion (rule "Fence")`},
		{onvif.AnalyticsEvent{Kind: "Vehicle", Label: "truck"}, `a truck`},
	} {
		if got := describeCameraEvent(c.e); got != c.want {
			t.Errorf("got %s, want %s", got, c.want)
		}
	}
}

const (
	testSession = "0f3c9a52-7d4e-4b1a-9c2e-5a6b7c8d9e0f"
	testAnswer  = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\nc=IN IP4 0.0.0.0\r\na=mid:0\r\na=candidate:1 1 udp 2130706431 172.17.0.2 18189 typ host\r\na=end-of-candidates\r\n"
)

func TestRewriteWHEPLocation(t *testing.T) {
	for _, loc := range []string{
		"/CAM-sub/whep/" + testSession,
		"http://127.0.0.1:18889/CAM/whep/" + testSession,
		"whep/" + testSession,
	} {
		got, ok := rewriteWHEPLocation(loc, "CAMID")
		if !ok || got != "/webrtc/CAMID/whep/"+testSession {
			t.Errorf("%q → %q %v", loc, got, ok)
		}
	}
	for _, loc := range []string{"", "/CAM/whep/", "/CAM/whep/../../v3/config", "/CAM/whep/" + testSession + "x"} {
		if _, ok := rewriteWHEPLocation(loc, "CAMID"); ok {
			t.Errorf("%q should be rejected", loc)
		}
	}
}

func TestAddRequestHostCandidates(t *testing.T) {
	// Hostnames and already-offered IPs leave the answer untouched.
	for _, host := range []string{"nvr.local:8090", "172.17.0.2:8090", "0.0.0.0:8090"} {
		if got := addRequestHostCandidates(testAnswer, host, 18189); got != testAnswer {
			t.Errorf("%s changed the answer", host)
		}
	}
	got := addRequestHostCandidates(testAnswer, "[fd00::5]:8090", 18189)
	if !strings.Contains(got, "a=candidate:sentinel1 1 udp 2113937151 fd00::5 18189 typ host\r\na=candidate:sentinel2") {
		t.Errorf("IPv6 candidate missing:\n%s", got)
	}
	if strings.Index(got, "sentinel1") > strings.Index(got, "a=end-of-candidates") {
		t.Error("candidates must come before end-of-candidates")
	}
}

// The recorder never imports the management side.
func TestNoManagementImports(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, f := range files {
		parsed, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			switch strings.Trim(imp.Path.Value, `"`) {
			case "sentinel-linux/internal/server", "sentinel-linux/internal/store", "sentinel-linux/internal/bus":
				t.Errorf("%s imports %s", f, imp.Path.Value)
			}
		}
	}
}

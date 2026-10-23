package server

// WAVE22: the low-res tier through the management side (timeline quality,
// playback proxy, evidence).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/playback"
	"sentinel-linux/internal/store"
)

func TestTimelineQualityAndLowResPlayback(t *testing.T) {
	s, c, t0 := playbackFixture(t) // full-res 09:00:00–09:01:00 and 09:01:30–09:02:30
	dir := filepath.Join(recOf(s).RecordingsRoot(), playback.LowResDir, c.ID+playback.LowResSuffix)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, t0.Format("20060102-150405")+"-000000.mp4") // low-res 09:00:00–09:03:00
	if err := os.WriteFile(p, playback.SynthSegment(180, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, t0.Add(3*time.Minute), t0.Add(3*time.Minute))
	h := s.Handler()
	q := fmt.Sprintf("?from=%d&to=%d", t0.Add(-time.Hour).UnixMilli(), t0.Add(time.Hour).UnixMilli())
	want := []struct {
		start time.Duration
		q     string
	}{{0, "full"}, {time.Minute, "low"}, {90 * time.Second, "full"}, {150 * time.Second, "low"}}

	// v1.
	viewer := webAs(t, s, "viewer", "Viewer")
	rec := webDo(t, h, viewer, "GET", "/api/v1/cameras/"+c.ID+"/timeline"+q, "")
	var tl struct {
		Spans []link.Span `json:"spans"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tl); rec.Code != 200 || err != nil || len(tl.Spans) != len(want) {
		t.Fatalf("v1 timeline %d %s", rec.Code, rec.Body)
	}
	for i, w := range want {
		if tl.Spans[i].Start != t0.Add(w.start).UnixMilli() || tl.Spans[i].Quality != w.q {
			t.Fatalf("v1 span %d %+v, want %+v", i, tl.Spans[i], w)
		}
	}

	// v2.
	tok := v2Token(t, s, "viewer2", "Viewer")
	code, v2 := v2Call(t, h, "GET", "/api/v2/cameras/"+c.ID+"/timeline"+q, tok, "")
	spans, _ := v2["spans"].([]any)
	if code != 200 || len(spans) != len(want) {
		t.Fatalf("v2 timeline %d %v", code, v2)
	}
	for i, w := range want {
		sp := spans[i].(map[string]any)
		if sp["start"] != float64(t0.Add(w.start).UnixMilli()) || sp["quality"] != w.q || sp["recorderId"] == "" {
			t.Fatalf("v2 span %d %v, want %+v", i, sp, w)
		}
	}

	// The playlist's low-res pieces play through the management proxy.
	rec = webDo(t, h, viewer, "GET", "/api/v1/cameras/"+c.ID+"/playback.m3u8"+q, "")
	if rec.Code != 200 || strings.Count(rec.Body.String(), "#EXT-X-MAP") != 4 {
		t.Fatalf("playlist %d\n%s", rec.Code, rec.Body)
	}
	var lowPiece, lowInit string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if lowPiece == "" && strings.HasSuffix(line, "&tier=low") {
			lowPiece = line
		}
		if lowInit == "" && strings.Contains(line, "/init/") && strings.Contains(line, "tier=low") {
			lowInit = strings.TrimSuffix(strings.TrimPrefix(line, `#EXT-X-MAP:URI="`), `"`)
		}
	}
	if r := webDo(t, h, viewer, "GET", lowPiece, ""); lowPiece == "" || r.Code != 200 || string(r.Body.Bytes()[4:8]) != "moof" {
		t.Fatalf("low piece %q: %d", lowPiece, r.Code)
	}
	if r := webDo(t, h, viewer, "GET", lowInit, ""); lowInit == "" || r.Code != 200 || !strings.Contains(r.Body.String(), "moov") {
		t.Fatalf("low init %q: %d", lowInit, r.Code)
	}

	// Evidence for an alarm in the outage comes from the low-res tier and
	// says so (a Vehicle alarm: the fixture's Person alarm would absorb a Person one).
	ctx := context.Background()
	a, _, _ := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: c.ID, CameraName: c.Name, Kind: "Vehicle", Title: "Vehicle detected",
		Severity: "Critical", At: t0.Add(75 * time.Second), Origin: "test"})
	ev, err := s.lockEvidence(ctx, a, "tester")
	if err != nil || ev.Title != "Recorded Segment Export (low resolution)" {
		t.Fatalf("evidence %v %+v", err, ev)
	}
	b, _ := os.ReadFile(filepath.Join(recOf(s).EvidenceDir(), ev.CaseID+".mp4"))
	if len(b) != len(playback.SynthSegment(180, 2)) {
		t.Fatalf("evidence copied %d bytes", len(b))
	}
}

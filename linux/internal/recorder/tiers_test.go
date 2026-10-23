package recorder

// WAVE22: playback, timeline, covers and evidence over the full-res and
// low-res tiers; the recordings-folder move carries .buffer and .lowres.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/playback"
)

const tierCam = "6F1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F"

// tierFixture: low-res 24/7 for 3 min from start, full-res 60 s from
// start+60 s; both finished (mtime = end).
func tierFixture(t *testing.T) (r *Recorder, start time.Time, fullPath, lowPath string) {
	t.Helper()
	r = New(t.TempDir(), nil, nil, "test", "")
	start = time.Now().Add(-time.Hour).Truncate(time.Second)
	write := func(dir string, at time.Time, secs int) string {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, at.Format("20060102-150405")+"-000000.mp4")
		if err := os.WriteFile(p, playback.SynthSegment(secs, 2), 0o644); err != nil {
			t.Fatal(err)
		}
		end := at.Add(time.Duration(secs) * time.Second)
		_ = os.Chtimes(p, end, end)
		return p
	}
	root := r.RecordingsRoot()
	lowPath = write(filepath.Join(root, playback.LowResDir, tierCam+playback.LowResSuffix), start, 180)
	fullPath = write(filepath.Join(root, tierCam), start.Add(time.Minute), 60)
	// The events-mode buffer is never played.
	write(filepath.Join(root, playback.BufferDir, tierCam), start.Add(150*time.Second), 60)
	return r, start, fullPath, lowPath
}

func TestTimelineQualityAcrossTiers(t *testing.T) {
	r, start, _, _ := tierFixture(t)
	tl, err := r.timeline(link.RangeArgs{CameraID: tierCam, From: start.Add(-time.Hour).UnixMilli(), To: start.Add(time.Hour).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		from, to time.Duration
		q        string
	}{{0, time.Minute, "low"}, {time.Minute, 2 * time.Minute, "full"}, {2 * time.Minute, 3 * time.Minute, "low"}}
	if len(tl.Spans) != len(want) {
		t.Fatalf("spans %+v", tl.Spans)
	}
	for i, w := range want {
		sp := tl.Spans[i]
		if sp.Start != start.Add(w.from).UnixMilli() || sp.End != start.Add(w.to).UnixMilli() || sp.Quality != w.q || sp.Edge {
			t.Fatalf("span %d %+v, want %+v", i, sp, w)
		}
		if sp.Media != float64(i*60) {
			t.Fatalf("span %d media %v", i, sp.Media)
		}
	}
	if tl.Writing {
		t.Error("finished files reported as writing")
	}
}

func TestPlaybackServesLowResTier(t *testing.T) {
	r, start, _, _ := tierFixture(t)
	h := r.videoHandler()
	get := func(path string) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}
	code, pl := get(fmt.Sprintf("/api/v1/cameras/%s/playback.m3u8?from=%d&to=%d", tierCam, start.Add(-time.Minute).UnixMilli(), start.Add(time.Hour).UnixMilli()))
	if code != 200 {
		t.Fatalf("playlist %d %s", code, pl)
	}
	m3u8 := string(pl)
	maps := regexp.MustCompile(`#EXT-X-MAP:URI="([^"]+)"(,BYTERANGE="\d+@0")?`).FindAllStringSubmatch(m3u8, -1)
	if len(maps) != 3 || strings.Count(m3u8, "#EXT-X-DISCONTINUITY") != 2 {
		t.Fatalf("playlist:\n%s", m3u8)
	}
	// Low-res: the whole init section from /init/ with tier=low; full-res: a byte range of /recordings/.
	if !strings.HasSuffix(maps[0][1], "?tier=low") || maps[0][2] != "" || !strings.HasPrefix(maps[1][1], "/recordings/"+tierCam+"/") || maps[1][2] == "" {
		t.Fatalf("maps %q", maps)
	}
	if strings.Contains(m3u8, playback.BufferDir) || strings.Contains(m3u8, "-sub") {
		t.Fatalf("internal folder names leaked:\n%s", m3u8)
	}
	var lowPiece string
	for _, line := range strings.Split(m3u8, "\n") {
		if strings.HasPrefix(line, "/api/v1/cameras/") && strings.HasSuffix(line, "&tier=low") {
			lowPiece = line
			break
		}
	}
	if lowPiece == "" {
		t.Fatalf("no low-res piece:\n%s", m3u8)
	}
	if code, b := get(lowPiece); code != 200 || string(b[4:8]) != "moof" {
		t.Fatalf("low piece %d", code)
	}
	if code, b := get(maps[0][1]); code != 200 || !bytes.Contains(b, []byte("moov")) {
		t.Fatalf("low init %d", code)
	}
	// The low-res file is only there under tier=low; the full-res folder has no such file.
	if code, _ := get(strings.TrimSuffix(lowPiece, "&tier=low")); code != 404 {
		t.Fatalf("low piece without tier: %d", code)
	}
	// A "-sub" camera id is never accepted from a client.
	sub := strings.Replace(lowPiece, tierCam, tierCam+playback.LowResSuffix, 1)
	if code, _ := get(sub); code != 400 {
		t.Fatalf("-sub id: %d", code)
	}
}

func TestCoversAndEvidenceFallBackToLowRes(t *testing.T) {
	r, start, fullPath, lowPath := tierFixture(t)
	// Full-res covers 60–120 s: it wins; elsewhere the low-res file plays.
	for _, c := range []struct {
		at  time.Duration
		low bool
	}{{30 * time.Second, true}, {90 * time.Second, false}, {150 * time.Second, true}} {
		seg := r.segmentAt(tierCam, start.Add(c.at).Unix())
		if seg == nil || seg.Low != c.low {
			t.Fatalf("at %v: %+v", c.at, seg)
		}
	}
	if seg := r.segmentAt(tierCam, start.Add(10*time.Minute).Unix()); seg != nil {
		t.Fatalf("nothing recorded at +10 min, got %+v", seg)
	}
	cmd, _ := link.NewCommand(link.CmdCovers, link.CoversArgs{Items: []link.CoverQuery{
		{CameraID: tierCam, At: start.Add(30 * time.Second).Unix()},
		{CameraID: tierCam, At: start.Add(10 * time.Minute).Unix()},
		{CameraID: tierCam + playback.LowResSuffix, At: start.Add(30 * time.Second).Unix()},
	}})
	res, err := r.Command(t.Context(), cmd)
	var cov link.CoversResult
	if err != nil || res.Decode(&cov) != nil || len(cov.Covered) != 3 || !cov.Covered[0] || cov.Covered[1] || cov.Covered[2] {
		t.Fatalf("covers %v %+v %+v", err, res, cov)
	}

	// Evidence copies the segment that plays at that time.
	sum := func(p string) string {
		b, _ := os.ReadFile(p)
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	ev, code, msg := r.lockEvidence(link.EvidenceLockArgs{CameraID: tierCam, At: start.Add(30 * time.Second).Unix(), CaseID: "HG-LOW"})
	if code != "" || ev.Quality != "low" || ev.SHA256 != sum(lowPath) {
		t.Fatalf("low evidence %s %s %+v", code, msg, ev)
	}
	ev, code, msg = r.lockEvidence(link.EvidenceLockArgs{CameraID: tierCam, At: start.Add(90 * time.Second).Unix(), CaseID: "HG-FULL"})
	if code != "" || ev.Quality != "" || ev.SHA256 != sum(fullPath) {
		t.Fatalf("full evidence %s %s %+v", code, msg, ev)
	}
}

// moveTree walks the whole recordings tree, so the events-mode buffer and
// the low-res tier move with the full-res footage.
func TestMoveTreeCarriesBufferAndLowRes(t *testing.T) {
	from, to := t.TempDir(), filepath.Join(t.TempDir(), "new")
	files := []string{
		filepath.Join(tierCam, "20261023-090000-000000.mp4"),
		filepath.Join(playback.LowResDir, tierCam+playback.LowResSuffix, "20261023-090000-000000.mp4"),
		filepath.Join(playback.BufferDir, tierCam, "20261023-091400-000000.mp4"),
	}
	old := time.Now().Add(-time.Hour)
	for _, f := range files {
		p := filepath.Join(from, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
	}
	moved, skipped, err := moveTree(from, to, MoveRecentFile)
	if err != nil || moved != len(files) || skipped != 0 {
		t.Fatalf("moved %d skipped %d err %v", moved, skipped, err)
	}
	for _, f := range files {
		if b, err := os.ReadFile(filepath.Join(to, f)); err != nil || string(b) != f {
			t.Errorf("%s: %v %q", f, err, b)
		}
		if _, err := os.Stat(filepath.Join(from, f)); err == nil {
			t.Errorf("%s left behind", f)
		}
	}
}

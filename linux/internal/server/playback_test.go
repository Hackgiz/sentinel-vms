package server

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/playback"
	"sentinel-linux/internal/store"
)

// webAs signs a dashboard user of the given role in and returns their cookie.
func webAs(t *testing.T, s *Server, name, role string) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	u, err := s.Store.CreateUser(ctx, name, role, "x")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.Store.CreateSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookie, Value: tok}
}

func webDo(t *testing.T, h http.Handler, c *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// playbackFixture: a camera with two 60 s segments (09:00:00 and, after a
// 30 s outage, 09:01:30, server local time), an event and an alarm, and a
// fake ffmpeg that copies the clip's first cut part to the output.
func playbackFixture(t *testing.T) (*Server, *store.Camera, time.Time) {
	t.Helper()
	s := newCompanionServer(t)
	c := addCam(t, s)
	dir := filepath.Join(recOf(s).RecordingsRoot(), c.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 9, 9, 0, 0, 0, time.Local)
	for _, start := range []time.Time{t0, t0.Add(90 * time.Second)} {
		p := filepath.Join(dir, start.Format("20060102-150405")+"-000000.mp4")
		if err := os.WriteFile(p, playback.SynthSegment(60, 2), 0o644); err != nil {
			t.Fatal(err)
		}
		end := start.Add(time.Minute)
		_ = os.Chtimes(p, end, end)
	}
	ctx := context.Background()
	_ = s.Store.AddEvent(ctx, &store.Event{CameraID: c.ID, Kind: "Person", Score: 0.9, CreatedAt: t0.Add(20 * time.Second).Unix()})
	_, _, _ = s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: c.ID, CameraName: c.Name, Kind: "Person", Title: "Person detected",
		Severity: "Critical", At: t0.Add(100 * time.Second), Origin: "test"})

	bin := filepath.Join(s.Store.DataDir(), "bin")
	_ = os.MkdirAll(bin, 0o755)
	// Last argument is the output; a concat list or input sits after -i.
	script := "#!/bin/sh\nfor a; do out=\"$a\"; done\n[ \"$out\" = \"-encoders\" ] && exit 0 # the encoder probe (playback.MaskEncoderArgs)\nprintf 'clip:%s' \"$*\" > \"$out\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return s, c, t0
}

func TestTimelineSpansAndMarkers(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	h := s.Handler()
	viewer := webAs(t, s, "viewer", "Viewer")
	q := fmt.Sprintf("?from=%d&to=%d", t0.Add(-time.Hour).UnixMilli(), t0.Add(time.Hour).UnixMilli())

	rec := webDo(t, h, viewer, "GET", "/api/v1/cameras/"+c.ID+"/timeline"+q, "")
	var tl struct {
		Spans  []link.Span   `json:"spans"`
		Events []store.Event `json:"events"`
		Alarms []store.Alarm `json:"alarms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tl); rec.Code != 200 || err != nil {
		t.Fatalf("timeline %d %s", rec.Code, rec.Body)
	}
	if len(tl.Spans) != 2 || tl.Spans[0].Start != t0.UnixMilli() || tl.Spans[0].End != t0.Add(time.Minute).UnixMilli() ||
		tl.Spans[1].Start != t0.Add(90*time.Second).UnixMilli() || tl.Spans[1].Media != 60 {
		t.Fatalf("spans %+v", tl.Spans)
	}
	if len(tl.Events) != 1 || tl.Events[0].Kind != "Person" || len(tl.Alarms) != 1 || tl.Alarms[0].Severity != "Critical" {
		t.Fatalf("markers %+v %+v", tl.Events, tl.Alarms)
	}

	// The playlist for the same window, gzipped when the client accepts it.
	req := httptest.NewRequest("GET", "/api/v1/cameras/"+c.ID+"/playback.m3u8"+q, nil)
	req.AddCookie(viewer)
	req.Header.Set("Accept-Encoding", "gzip")
	prec := httptest.NewRecorder()
	h.ServeHTTP(prec, req)
	if prec.Code != 200 || prec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("playlist %d %v", prec.Code, prec.Header())
	}
	zr, err := gzip.NewReader(prec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	m3u8 := string(body)
	if !strings.HasPrefix(m3u8, "#EXTM3U") || strings.Count(m3u8, "#EXT-X-DISCONTINUITY") != 1 ||
		!strings.Contains(m3u8, "/recordings/"+c.ID+"/"+t0.Format("20060102-150405")+"-000000.mp4") {
		t.Fatalf("playlist:\n%s", m3u8)
	}
	// Media segments are keyframe-aligned pieces served by the API…
	var piece string
	for _, line := range strings.Split(m3u8, "\n") {
		if strings.HasPrefix(line, "/api/v1/cameras/"+c.ID+"/pieces/") {
			piece = line
			break
		}
	}
	prec2 := webDo(t, h, viewer, "GET", piece, "")
	if piece == "" || prec2.Code != 200 || prec2.Header().Get("Content-Type") != "video/mp4" || string(prec2.Body.Bytes()[4:8]) != "moof" {
		t.Fatalf("piece %q: %d %.40q", piece, prec2.Code, prec2.Body.Bytes())
	}
	if rec := webDo(t, h, nil, "GET", piece, ""); rec.Code != 401 {
		t.Errorf("piece signed out: %d", rec.Code)
	}
	if rec := webDo(t, h, viewer, "GET", strings.Replace(piece, "first=0", "first=999", 1), ""); rec.Code != 404 {
		t.Errorf("piece out of range: %d", rec.Code)
	}
	// …and the init section is a byte range of the file the /recordings/ handler serves.
	seg := "/recordings/" + c.ID + "/" + t0.Format("20060102-150405") + "-000000.mp4"
	req = httptest.NewRequest("GET", seg, nil)
	req.AddCookie(viewer)
	req.Header.Set("Range", "bytes=0-99")
	rrec := httptest.NewRecorder()
	h.ServeHTTP(rrec, req)
	if rrec.Code != http.StatusPartialContent || rrec.Body.Len() != 100 {
		t.Fatalf("range %d %d", rrec.Code, rrec.Body.Len())
	}

	// Bad windows.
	for _, bad := range []string{"", "?from=1&to=1", fmt.Sprintf("?from=0&to=%d", (48 * time.Hour).Milliseconds()), "?from=a&to=b"} {
		if rec := webDo(t, h, viewer, "GET", "/api/v1/cameras/"+c.ID+"/timeline"+bad, ""); rec.Code != 400 {
			t.Errorf("window %q: %d", bad, rec.Code)
		}
	}
	if rec := webDo(t, h, viewer, "GET", "/api/v1/cameras/not-a-camera/timeline"+q, ""); rec.Code != 400 {
		t.Errorf("bad id: %d", rec.Code)
	}
	empty := fmt.Sprintf("?from=%d&to=%d", t0.Add(-48*time.Hour).UnixMilli(), t0.Add(-47*time.Hour).UnixMilli())
	if rec := webDo(t, h, viewer, "GET", "/api/v1/cameras/"+c.ID+"/playback.m3u8"+empty, ""); rec.Code != 404 {
		t.Errorf("empty playlist: %d", rec.Code)
	}
}

func TestPlaybackPermissions(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	h := s.Handler()
	viewer := webAs(t, s, "viewer", "Viewer")
	q := fmt.Sprintf("?from=%d&to=%d", t0.UnixMilli(), t0.Add(time.Hour).UnixMilli())
	body := fmt.Sprintf(`{"from":%d,"to":%d}`, t0.Add(10*time.Second).UnixMilli(), t0.Add(20*time.Second).UnixMilli())

	// Signed out: nothing.
	for _, path := range []string{"/timeline" + q, "/playback.m3u8" + q, fmt.Sprintf("/snapshot.jpg?t=%d", t0.UnixMilli())} {
		if rec := webDo(t, h, nil, "GET", "/api/v1/cameras/"+c.ID+path, ""); rec.Code != 401 {
			t.Errorf("signed out %s: %d", path, rec.Code)
		}
	}
	// A paired phone's bearer token doesn't open dashboard endpoints.
	_, phone, _ := s.Store.CreatePairedDevice(context.Background(), "iPhone", "admin")
	if rec := do(t, h, "GET", "/api/v1/cameras/"+c.ID+"/timeline"+q, phone, ""); rec.Code != 401 {
		t.Errorf("phone token: %d", rec.Code)
	}
	// Viewers can watch but not export.
	if rec := webDo(t, h, viewer, "POST", "/api/v1/cameras/"+c.ID+"/export", body); rec.Code != 403 {
		t.Errorf("viewer export: %d %s", rec.Code, rec.Body)
	}
	if rec := webDo(t, h, viewer, "GET", fmt.Sprintf("/api/v1/cameras/%s/snapshot.jpg?t=%d", c.ID, t0.Add(5*time.Second).UnixMilli()), ""); rec.Code != 403 {
		t.Errorf("viewer snapshot: %d", rec.Code)
	}
}

func TestExportClipDownloadAndEvidence(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	h := s.Handler()
	op := webAs(t, s, "operator", "Operator")
	other := webAs(t, s, "other", "Supervisor")
	clip := func(from, to time.Duration, extra string) *httptest.ResponseRecorder {
		return webDo(t, h, op, "POST", "/api/v1/cameras/"+c.ID+"/export",
			fmt.Sprintf(`{"from":%d,"to":%d%s}`, t0.Add(from).UnixMilli(), t0.Add(to).UnixMilli(), extra))
	}

	// Download: prepared, then fetched once by the same user.
	rec := clip(50*time.Second, 100*time.Second, "") // spans the outage
	var res struct {
		URL, Filename, SHA256 string
		Size                  int64
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); rec.Code != 200 || err != nil || !strings.HasPrefix(res.URL, "/api/v1/exports/") {
		t.Fatalf("export %d %s", rec.Code, rec.Body)
	}
	if res.Filename != "Door-"+t0.Add(50*time.Second).Format("20060102-150405")+".mp4" {
		t.Errorf("filename %q", res.Filename)
	}
	if rec := webDo(t, h, other, "GET", res.URL, ""); rec.Code != 404 {
		t.Errorf("another user fetched the export: %d", rec.Code)
	}
	dl := webDo(t, h, op, "GET", res.URL, "")
	sum := sha256.Sum256(dl.Body.Bytes())
	if dl.Code != 200 || hex.EncodeToString(sum[:]) != res.SHA256 || !strings.Contains(dl.Header().Get("Content-Disposition"), res.Filename) {
		t.Fatalf("download %d %v", dl.Code, dl.Header())
	}
	// The fake ffmpeg echoes its arguments: -ss from the 09:00:50 keyframe,
	// -t = recorded time only (10 s before the outage + 10 s after).
	if args := dl.Body.String(); !strings.Contains(args, "-ss 0.000 ") || !strings.Contains(args, "-t 20.000") || !strings.Contains(args, "-c copy") {
		t.Fatalf("ffmpeg args: %s", args)
	}

	// Validation.
	if rec := clip(0, 31*time.Minute, ""); rec.Code != 400 {
		t.Errorf("31-minute clip: %d", rec.Code)
	}
	if rec := clip(65*time.Second, 85*time.Second, ""); rec.Code != 404 {
		t.Errorf("clip inside the outage: %d", rec.Code)
	}

	// Evidence: into the vault, hashed and read-only.
	rec = clip(10*time.Second, 20*time.Second, `,"evidence":true,"title":"Porch incident"`)
	var evRes struct{ Evidence store.Evidence }
	if err := json.Unmarshal(rec.Body.Bytes(), &evRes); rec.Code != 200 || err != nil {
		t.Fatalf("evidence %d %s", rec.Code, rec.Body)
	}
	ev := evRes.Evidence
	p := filepath.Join(recOf(s).EvidenceDir(), ev.CaseID+".mp4")
	b, err := os.ReadFile(p)
	sum = sha256.Sum256(b)
	info, _ := os.Stat(p)
	if err != nil || hex.EncodeToString(sum[:]) != ev.SHA256 || info.Mode().Perm() != 0o440 || ev.Title != "Porch incident" || ev.LockedBy != "operator" {
		t.Fatalf("evidence %+v (%v, mode %v)", ev, err, info.Mode())
	}
	if list, _ := s.Store.EvidenceList(context.Background()); len(list) != 1 {
		t.Fatalf("evidence list %+v", list)
	}

	// Snapshot.
	snap := webDo(t, h, op, "GET", fmt.Sprintf("/api/v1/cameras/%s/snapshot.jpg?t=%d", c.ID, t0.Add(33*time.Second).UnixMilli()), "")
	if snap.Code != 200 || snap.Header().Get("Content-Type") != "image/jpeg" || !strings.Contains(snap.Body.String(), "-ss 1.000") {
		t.Fatalf("snapshot %d %s", snap.Code, snap.Body)
	}

	// Everything is in the audit log.
	entries, _ := s.Store.AuditLog(context.Background(), 50)
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	all := strings.Join(actions, "|")
	for _, want := range []string{"Exported clip", "Downloaded clip", "Locked evidence", "Exported snapshot"} {
		if !strings.Contains(all, want) {
			t.Errorf("audit is missing %q: %s", want, all)
		}
	}
	// No scratch files left behind.
	if left, _ := filepath.Glob(filepath.Join(recOf(s).ExportsDir(), "work-*")); len(left) != 0 {
		t.Errorf("work dirs left: %v", left)
	}
}

func TestExportWithoutFFmpeg(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	_ = os.Remove(filepath.Join(s.Store.DataDir(), "bin", "ffmpeg"))
	if recOf(s).FFmpegPath() != "" {
		t.Skip("an ffmpeg is installed on this machine")
	}
	op := webAs(t, s, "operator", "Operator")
	rec := webDo(t, s.Handler(), op, "POST", "/api/v1/cameras/"+c.ID+"/export",
		fmt.Sprintf(`{"from":%d,"to":%d}`, t0.UnixMilli(), t0.Add(10*time.Second).UnixMilli()))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

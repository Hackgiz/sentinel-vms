package recorder

// Timeline playback and clip export (milestone M2).
//
// Playback is served by Sentinel itself rather than MediaMTX's playback
// server: a day's recording is described as an HLS VOD playlist (see package
// playback) whose init sections are byte ranges of the original fMP4 files
// (the /recordings/ handler) and whose media segments are keyframe-to-
// keyframe re-packs of their fragments (/pieces/). That needs no extra
// MediaMTX listener or config, keeps working while MediaMTX restarts, gives
// the browser exact seeking and a day-long scrubbable timeline, and still
// plays footage of cameras that were deleted (their recordings are kept,
// but MediaMTX no longer knows their path).

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/playback"
	"sentinel-linux/internal/videogw"
)

const (
	maxTimelineWindow = 26 * time.Hour // a calendar day, plus DST slack
	exportTimeout     = 5 * time.Minute
	exportKeep        = 30 * time.Minute // a prepared download is kept this long
)

var exportIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// playbackState holds the segment index cache and the export slots.
type playbackState struct {
	once  sync.Once
	lib   *playback.Library
	slots chan struct{} // concurrent exports (each runs an ffmpeg)

	warmMu     sync.Mutex
	warmCancel context.CancelFunc // the running index warm-up, if any
}

func (r *Recorder) library() *playback.Library {
	r.playback.once.Do(func() {
		r.playback.lib = &playback.Library{Root: r.recordingsRoot()}
		r.playback.slots = make(chan struct{}, 2)
	})
	return r.playback.lib
}

// Index warm-up (WAVE23 stream M): every client asks for a whole day per
// camera when Playback opens, and right after a restart indexing a day of
// 15-min segments from a slow disk took ~50 s, so the apps timed out. The
// sidecars make that a read of small files; the warm-up does it (and
// parses anything without a sidecar yet) before anyone asks.
var (
	playbackWarmWindow = 24 * time.Hour
	// No delay: apps reconnect and ask for a day per camera within seconds of
	// a restart. A recordings folder applied later restarts the warm-up for
	// the new folder (storagedir.go).
	playbackWarmDelay time.Duration = 0
	slowPlaylist                    = 5 * time.Second
)

// startPlaybackWarm (re)starts the background warm-up of the last day of
// every camera's recordings after delay, cancelling one already running.
func (r *Recorder) startPlaybackWarm(parent context.Context, delay time.Duration) {
	ctx, cancel := context.WithCancel(parent)
	r.playback.warmMu.Lock()
	if r.playback.warmCancel != nil {
		r.playback.warmCancel()
	}
	r.playback.warmCancel = cancel
	r.playback.warmMu.Unlock()
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		defer cancel()
		if delay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
		res := r.library().Warm(ctx, playbackWarmWindow, cameraIDPattern.MatchString)
		if ctx.Err() != nil {
			return // superseded or shutting down
		}
		slog.Info("playback index warm-up finished", "folder", r.recordingsRoot(), "files", res.Files, "fromSidecar", res.Loaded,
			"parsed", res.Parsed, "alreadyIndexed", res.Cached+res.Skipped, "took", res.Took.Round(time.Millisecond).String())
	}()
}

// logSlowIndex reports a timeline or playlist request whose indexing took
// long, so a slow disk or a cold index shows in the log.
func logSlowIndex(kind, cameraID string, from, to time.Time, files int, index, total time.Duration) {
	if total < slowPlaylist {
		return
	}
	slog.Warn("slow playback "+kind, "camera", cameraID, "from", from.Format(time.RFC3339), "to", to.Format(time.RFC3339),
		"windowHours", fmt.Sprintf("%.1f", to.Sub(from).Hours()), "files", files,
		"indexTime", index.Round(time.Millisecond).String(), "total", total.Round(time.Millisecond).String())
}

// parseWindow reads from/to (Unix milliseconds) from the query string.
func parseWindow(req *http.Request, max time.Duration) (from, to time.Time, err error) {
	f, err1 := strconv.ParseInt(req.URL.Query().Get("from"), 10, 64)
	t, err2 := strconv.ParseInt(req.URL.Query().Get("to"), 10, 64)
	if err1 != nil || err2 != nil {
		return from, to, errors.New("from and to must be Unix milliseconds")
	}
	from, to = time.UnixMilli(f), time.UnixMilli(t)
	if !to.After(from) || to.Sub(from) > max {
		return from, to, errors.New("pick a time range of up to one day")
	}
	return from, to, nil
}

// originalStream reports whether the request is from a native player that
// authenticated with a paired device's video token (the iPhone app's
// AVPlayer), which plays the recordings as they are — PCM audio included.
// Browsers play through MSE, which cannot (see playback.Index.WebInit), so
// session requests, and token requests from a browser (hls.js in the
// operator tile), get the video-only stream.
func originalStream(req *http.Request) bool {
	return req.Header.Get(videogw.TokenAuthHeader) == "1" && !strings.HasPrefix(req.Header.Get("User-Agent"), "Mozilla/")
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// tierLowQuery marks a playlist's init and piece URLs of a low-res file
// (WAVE22): the path keeps the camera's id and the file name, the recorder
// maps them to <root>/.lowres/<id>-sub/.
const tierLowQuery = "tier=low"

func lowTier(req *http.Request) bool { return req.URL.Query().Get("tier") == "low" }

// timeline returns the recorded spans of a window (the management side adds
// event and alarm markers).
func (r *Recorder) timeline(a link.RangeArgs) (link.Timeline, error) {
	from, to := time.UnixMilli(a.From), time.UnixMilli(a.To)
	began := time.Now()
	files, err := r.library().Files(a.CameraID, from, to)
	if err != nil {
		return link.Timeline{}, err
	}
	if d := time.Since(began); d >= slowPlaylist {
		logSlowIndex("timeline", a.CameraID, from, to, len(files), d, d)
	}
	plan := playback.BuildPlan(files, from, to)
	tl := link.Timeline{Spans: make([]link.Span, 0, len(plan.Spans)), Pending: []link.Span{}}
	for i, sp := range plan.Spans {
		f := plan.Runs[i].File
		tl.Spans = append(tl.Spans, link.Span{Start: ms(sp.Start), End: ms(sp.End), Media: sp.Media.Seconds(),
			Edge: r.isEdgeFile(a.CameraID, f), Quality: f.Quality()}) // WAVE9(C): edgefill.go
	}
	// Segments that hold no playable fragment yet (just started, or not fMP4)
	// still show as recorded so the bar has no false gaps.
	var newest time.Time
	for i := range files {
		f := &files[i]
		if !f.Index.Playable() && f.End().After(from) {
			tl.Pending = append(tl.Pending, link.Span{Start: ms(f.Start), End: ms(f.End()), Edge: r.isEdgeFile(a.CameraID, f), Quality: f.Quality()})
		}
		if f.ModTime.After(newest) {
			newest = f.ModTime
		}
	}
	tl.Writing = len(files) > 0 && time.Since(newest) < 30*time.Second
	return tl, nil
}

// isEdgeFile: footage recovered from the camera's card is only ever placed
// in the full-res folder.
func (r *Recorder) isEdgeFile(cameraID string, f *playback.File) bool {
	return !f.Low && r.isEdgeSegment(cameraID, f.Name)
}

func (r *Recorder) handlePlaybackPlaylist(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if !cameraIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "bad camera id")
		return
	}
	from, to, err := parseWindow(req, maxTimelineWindow)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	began := time.Now()
	files, err := r.library().Files(id, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	indexed := time.Since(began)
	defer func() { logSlowIndex("playlist", id, from, to, len(files), indexed, time.Since(began)) }()
	plan := playback.BuildPlan(files, from, to)
	if len(plan.Runs) == 0 {
		writeError(w, http.StatusNotFound, "nothing was recorded in that time range")
		return
	}
	raw := originalStream(req)
	body := plan.M3U8(
		func(f *playback.File) (string, bool) {
			// /recordings/ only serves the full-res folder, so a low-res
			// file's init section always comes whole from /init/.
			if f.Low {
				return "/api/v1/cameras/" + id + "/init/" + f.Name + "?" + tierLowQuery, false
			}
			if !raw && f.Index.WebFiltered() {
				return "/api/v1/cameras/" + id + "/init/" + f.Name, false
			}
			return "/recordings/" + id + "/" + f.Name, true
		},
		func(f *playback.File, pc playback.Piece) string {
			key := 0
			if pc.EndAtKey {
				key = 1
			}
			u := fmt.Sprintf("/api/v1/cameras/%s/pieces/%s?first=%d&last=%d&key=%d", id, f.Name, pc.First, pc.Last, key)
			if f.Low {
				u += "&" + tierLowQuery
			}
			return u
		})
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	// A day is tens of thousands of lines; it compresses ~10×.
	if strings.Contains(req.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(w)
		_, _ = io.WriteString(gz, body)
		_ = gz.Close()
		return
	}
	_, _ = io.WriteString(w, body)
}

// handlePlaybackPiece serves one keyframe-to-keyframe HLS segment, re-packed
// from the recording's fragments (playback.Index.Cut).
func (r *Recorder) handlePlaybackPiece(w http.ResponseWriter, req *http.Request) {
	id, name := req.PathValue("id"), req.PathValue("file")
	q := req.URL.Query()
	first, err1 := strconv.Atoi(q.Get("first"))
	last, err2 := strconv.Atoi(q.Get("last"))
	if !cameraIDPattern.MatchString(id) || !segmentPattern.MatchString(name) || err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "bad piece")
		return
	}
	low := lowTier(req)
	f, err := r.library().File(id, name, low)
	if err != nil || !f.Index.Playable() {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	src, err := os.Open(r.library().Path(id, name, low))
	if err != nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	defer src.Close()
	cut := f.Index.CutWeb
	if originalStream(req) {
		cut = f.Index.Cut
	}
	b, err := cut(src, first, last, q.Get("key") == "1")
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	// A piece never changes once its fragments are written.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

// handlePlaybackInit serves the init section of a recording whose tracks
// browsers cannot play (G.711 as PCM) with those tracks removed. Recordings
// without such tracks are mapped by byte range instead (/recordings/).
func (r *Recorder) handlePlaybackInit(w http.ResponseWriter, req *http.Request) {
	id, name := req.PathValue("id"), req.PathValue("file")
	if !cameraIDPattern.MatchString(id) || !segmentPattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "bad recording path")
		return
	}
	low := lowTier(req)
	f, err := r.library().File(id, name, low)
	if err != nil || !f.Index.Playable() {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	src, err := os.Open(r.library().Path(id, name, low))
	if err != nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	defer src.Close()
	var b []byte
	if originalStream(req) {
		b = make([]byte, f.Index.InitSize)
		_, err = src.ReadAt(b, 0)
	} else {
		b, err = f.Index.WebInit(src)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

// MARK: - Export

// exportCode maps an export/snapshot failure to a result code.
func exportCode(err error) string {
	switch {
	case errors.Is(err, playback.ErrNoRecording):
		return link.CodeNotFound
	case errors.Is(err, playback.ErrNoFFmpeg):
		return link.CodeUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return link.CodeTimeout
	}
	return link.CodeInternal
}

// exportClip cuts [from, to) into one MP4 (stream copy, no re-encode), kept
// for exportKeep as a prepared download (GET /rs/exports/{token}) unless
// adoptEvidence moves it into the vault.
func (r *Recorder) exportClip(ctx context.Context, a link.ExportArgs) (link.ExportResult, string, string) {
	if !cameraIDPattern.MatchString(a.CameraID) {
		return link.ExportResult{}, link.CodeBadRequest, "bad camera id"
	}
	from, to := time.UnixMilli(a.From), time.UnixMilli(a.To)
	files, err := r.library().Files(a.CameraID, from, to)
	if err != nil {
		return link.ExportResult{}, link.CodeInternal, err.Error()
	}
	if _, err := playback.ClipPlan(files, from, to); err != nil {
		code := link.CodeBadRequest
		if errors.Is(err, playback.ErrNoRecording) {
			code = link.CodeNotFound
		}
		return link.ExportResult{}, code, err.Error()
	}
	opts, oerr := r.exportOptions(a.CameraID, a.Watermark, a.Blur)
	if oerr != nil {
		return link.ExportResult{}, link.CodeBadRequest, oerr.Error()
	}
	opts.PreferSmall = a.Prefer == link.PreferSmall
	select {
	case r.playback.slots <- struct{}{}:
		defer func() { <-r.playback.slots }()
	default:
		return link.ExportResult{}, link.CodeBusy, "other exports are running — try again in a moment"
	}
	if err := os.MkdirAll(r.exportsDir(), 0o750); err != nil {
		return link.ExportResult{}, link.CodeInternal, err.Error()
	}
	r.sweepExports()
	work, err := os.MkdirTemp(r.exportsDir(), "work-")
	if err != nil {
		return link.ExportResult{}, link.CodeInternal, err.Error()
	}
	defer os.RemoveAll(work)

	ctx, cancel := context.WithTimeout(ctx, exportTimeout)
	defer cancel()
	out := filepath.Join(work, "clip.mp4")
	// With privacy masks, a watermark or pixelated regions the clip is
	// re-encoded (zones.go, exportopts.go).
	if err := playback.ExportWith(ctx, r.FFmpegPath(), r.library(), a.CameraID, files, from, to, work, out, opts); err != nil {
		slog.Warn("clip export failed", "camera", a.CameraID, "err", err)
		return link.ExportResult{}, exportCode(err), "export failed: " + err.Error()
	}
	size, sum, err := hashFile(out)
	if err != nil {
		return link.ExportResult{}, link.CodeInternal, err.Error()
	}
	token := randomHex(16)
	if err := os.Rename(out, filepath.Join(r.exportsDir(), token+".mp4")); err != nil {
		return link.ExportResult{}, link.CodeInternal, err.Error()
	}
	return link.ExportResult{Token: token, Size: size, SHA256: sum}, "", ""
}

// adoptEvidence moves a prepared clip into the evidence vault as
// <caseID>.mp4, read-only. An existing case file is never replaced.
func (r *Recorder) adoptEvidence(a link.EvidenceAdoptArgs) (link.EvidenceFile, string, string) {
	if !exportIDPattern.MatchString(a.Token) || !vaultFilePattern.MatchString(a.CaseID+".mp4") {
		return link.EvidenceFile{}, link.CodeBadRequest, "bad evidence request"
	}
	src := filepath.Join(r.exportsDir(), a.Token+".mp4")
	size, sum, err := hashFile(src)
	if err != nil {
		return link.EvidenceFile{}, link.CodeNotFound, "export not found or expired — export the clip again"
	}
	if err := os.MkdirAll(r.evidenceDir(), 0o750); err != nil {
		return link.EvidenceFile{}, link.CodeInternal, err.Error()
	}
	dst := filepath.Join(r.evidenceDir(), a.CaseID+".mp4")
	if err := os.Link(src, dst); err != nil { // fails if dst exists
		return link.EvidenceFile{}, link.CodeInternal, err.Error()
	}
	_ = os.Remove(src)
	_ = os.Chmod(dst, 0o440)
	return link.EvidenceFile{File: filepath.Base(dst), Size: size, SHA256: sum}, "", ""
}

// handleExportFile serves a prepared clip. The management side checks who
// may fetch it and for how long, and sets the download headers.
func (r *Recorder) handleExportFile(w http.ResponseWriter, req *http.Request) {
	token := req.PathValue("token")
	if !exportIDPattern.MatchString(token) {
		writeError(w, http.StatusNotFound, "export not found or expired — export the clip again")
		return
	}
	r.serveFile(w, req, filepath.Join(r.exportsDir(), token+".mp4"), "export not found or expired — export the clip again")
}

// handleEvidenceFile serves a file from the evidence vault.
func (r *Recorder) handleEvidenceFile(w http.ResponseWriter, req *http.Request) {
	file := req.PathValue("file")
	if !vaultFilePattern.MatchString(file) {
		writeError(w, http.StatusNotFound, "evidence file missing")
		return
	}
	r.serveFile(w, req, filepath.Join(r.evidenceDir(), file), "evidence file missing")
}

// handleEventSnapshot serves an event's snapshot JPEG.
func (r *Recorder) handleEventSnapshot(w http.ResponseWriter, req *http.Request) {
	file := filepath.Base(req.PathValue("file"))
	if file == "." || file == "/" || strings.HasPrefix(file, ".") {
		writeError(w, http.StatusNotFound, "no thumbnail")
		return
	}
	r.serveFile(w, req, filepath.Join(r.eventsDir(), file), "no thumbnail")
}

func (r *Recorder) serveFile(w http.ResponseWriter, req *http.Request, path, missing string) {
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, missing)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, missing)
		return
	}
	http.ServeContent(w, req, filepath.Base(path), info.ModTime(), f)
}

// sweepExports deletes prepared downloads past their keep time and any work
// directories left behind by a restart or a crash mid-export.
func (r *Recorder) sweepExports() {
	entries, err := os.ReadDir(r.exportsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		keep := exportKeep + exportTimeout
		if strings.HasSuffix(e.Name(), ".mp4") && exportIDPattern.MatchString(strings.TrimSuffix(e.Name(), ".mp4")) {
			keep = exportKeep
		}
		if time.Since(info.ModTime()) > keep {
			_ = os.RemoveAll(filepath.Join(r.exportsDir(), e.Name()))
		}
	}
}

// snapshot extracts the frame at t from the recordings as a JPEG.
func (r *Recorder) snapshot(ctx context.Context, a link.SnapshotArgs) ([]byte, string, string) {
	if !cameraIDPattern.MatchString(a.CameraID) {
		return nil, link.CodeBadRequest, "need a camera and a time (t, Unix ms)"
	}
	at := time.UnixMilli(a.At)
	files, err := r.library().Files(a.CameraID, at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		return nil, link.CodeInternal, err.Error()
	}
	if err := os.MkdirAll(r.exportsDir(), 0o750); err != nil {
		return nil, link.CodeInternal, err.Error()
	}
	work, err := os.MkdirTemp(r.exportsDir(), "snap-")
	if err != nil {
		return nil, link.CodeInternal, err.Error()
	}
	defer os.RemoveAll(work)
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	jpg, err := playback.Snapshot(ctx, r.FFmpegPath(), r.library(), a.CameraID, files, at, work)
	if err != nil {
		return nil, exportCode(err), "snapshot failed: " + err.Error()
	}
	jpg, ok := r.maskedJPEG(a.CameraID, jpg)
	if !ok {
		return nil, link.CodeInternal, "snapshot could not be masked"
	}
	return jpg, "", ""
}

func hashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

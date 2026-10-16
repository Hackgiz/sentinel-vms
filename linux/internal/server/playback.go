package server

// Timeline playback and clip export (milestone M2), management side.
//
// The recording server owns the footage: it builds the HLS VOD playlist and
// its keyframe pieces (internal/recorder/playback.go), cuts clips and grabs
// snapshots. This side checks permissions, adds event and alarm markers to
// the timeline, decides who may download a prepared clip, files evidence
// and writes the audit log.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/videogw"
)

const (
	maxTimelineWindow = 26 * time.Hour // a calendar day, plus DST slack
	exportTimeout     = 5 * time.Minute
	exportKeep        = 30 * time.Minute // a prepared download is kept this long
	maxMarkers        = 2000
)

var exportIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// exportsState remembers who prepared which clip download.
type exportsState struct {
	mu   sync.Mutex
	byID map[string]preparedExport // recorder export token → download
}

type preparedExport struct {
	userID     string
	recorderID string // where the clip was cut
	filename   string
	sha256     string
	created    time.Time
}

func (s *Server) registerPlayback(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/cameras/{id}/timeline", s.authed(auth.ViewLive, s.inScope("id", s.handleTimeline)))
	mux.Handle("GET /api/v1/cameras/{id}/playback.m3u8", s.authed(auth.ViewLive, s.inScope("id", s.handlePlaybackVideo)))
	mux.Handle("GET /api/v1/cameras/{id}/pieces/{file}", s.authed(auth.ViewLive, s.inScope("id", s.handlePlaybackVideo)))
	mux.Handle("GET /api/v1/cameras/{id}/init/{file}", s.authed(auth.ViewLive, s.inScope("id", s.handlePlaybackVideo)))
	mux.Handle("POST /api/v1/cameras/{id}/export", s.authed(auth.ExportEvidence, s.inScope("id", s.handleExportClip)))
	mux.Handle("GET /api/v1/exports/{token}", s.authed(auth.ExportEvidence, s.handleExportDownload))
	mux.Handle("GET /api/v1/cameras/{id}/snapshot.jpg", s.authed(auth.ExportEvidence, s.inScope("id", s.handleSnapshot)))
}

// parseWindow reads from/to (Unix milliseconds) from the query string.
func parseWindow(r *http.Request, max time.Duration) (from, to time.Time, err error) {
	f, err1 := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	t, err2 := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if err1 != nil || err2 != nil {
		return from, to, errors.New("from and to must be Unix milliseconds")
	}
	from, to = time.UnixMilli(f), time.UnixMilli(t)
	if !to.After(from) || to.Sub(from) > max {
		return from, to, errors.New("pick a time range of up to one day")
	}
	return from, to, nil
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !cameraIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "bad camera id")
		return
	}
	from, to, err := parseWindow(r, maxTimelineWindow)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tl, err := s.recordedTimeline(r.Context(), id, from, to)
	if err != nil {
		writeError(w, commandStatus(err), err.Error())
		return
	}
	events, alarms, err := s.timelineMarkers(r.Context(), id, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": ms(from), "to": ms(to), "now": ms(time.Now()),
		"spans": tl.Spans, "pending": tl.Pending, "writing": tl.Writing,
		"events": events, "alarms": alarms,
	})
}

// footageSpan is a recorded span and the recording server that has it.
// Media is relative to that server's playback playlist for the window.
type footageSpan struct {
	link.Span
	RecorderID string `json:"recorderId"`
}

// footageTimeline is a camera's footage across recording servers.
type footageTimeline struct {
	Spans   []footageSpan
	Pending []footageSpan
	Writing bool
}

// recordedTimeline asks every recording server the camera was on during
// [from, to) (and that is connected now) what it holds, and merges the
// answers by start time; the span lists are never nil (v1 and v2). It
// fails only when no server answered.
func (s *Server) recordedTimeline(ctx context.Context, id string, from, to time.Time) (footageTimeline, error) {
	out := footageTimeline{Spans: []footageSpan{}, Pending: []footageSpan{}}
	var firstErr error
	answered := 0
	for _, rid := range s.footageRecorders(ctx, id, from.Unix(), to.Unix()+1, false) {
		if !s.recorderConnected(rid) {
			continue
		}
		var tl link.Timeline
		if err := s.command(ctx, rid, link.CmdTimeline, link.RangeArgs{CameraID: id, From: ms(from), To: ms(to)}, &tl); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		answered++
		for _, sp := range tl.Spans {
			out.Spans = append(out.Spans, footageSpan{Span: sp, RecorderID: rid})
		}
		for _, sp := range tl.Pending {
			out.Pending = append(out.Pending, footageSpan{Span: sp, RecorderID: rid})
		}
		out.Writing = out.Writing || tl.Writing
	}
	if answered == 0 {
		if firstErr == nil {
			firstErr = link.ErrNoRecorder
		}
		return out, firstErr
	}
	sort.SliceStable(out.Spans, func(i, j int) bool { return out.Spans[i].Start < out.Spans[j].Start })
	sort.SliceStable(out.Pending, func(i, j int) bool { return out.Pending[i].Start < out.Pending[j].Start })
	return out, nil
}

// timelineMarkers lists a camera's events and alarms in [from, to), oldest
// first (v1 and v2).
func (s *Server) timelineMarkers(ctx context.Context, id string, from, to time.Time) ([]store.Event, []store.Alarm, error) {
	events, err := s.Store.CameraEventsBetween(ctx, id, from, to, maxMarkers)
	if err != nil {
		return nil, nil, err
	}
	alarms, err := s.Store.CameraAlarmsBetween(ctx, id, from, to, maxMarkers)
	return events, alarms, err
}

// handlePlaybackVideo serves the playback playlist and its pieces from the
// recording server (same paths there). /api/v1 names no server: a
// playlist comes from the one that had the camera at the window's start,
// a piece (or a rewritten init section) from the one that has its segment.
func (s *Server) handlePlaybackVideo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !cameraIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "bad camera id")
		return
	}
	r.Header.Del(videogw.TokenAuthHeader) // a session, not a device token: never trust the client's claim
	var rid string
	if file := r.PathValue("file"); file != "" {
		rid = s.segmentRecorder(r.Context(), id, file)
	} else {
		from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
		rid = s.recorderAt(r.Context(), id, from/1000)
	}
	s.serveVideo(w, r, rid)
}

// MARK: - Export

func (s *Server) cameraLabel(ctx context.Context, id string) string {
	if c, err := s.Store.Camera(ctx, id); err == nil {
		return c.Name
	}
	return "Camera " + id[:8]
}

func rangeLabel(from, to time.Time) string {
	return from.Format("Jan 2 15:04:05") + " – " + to.Format("15:04:05")
}

// handleExportClip has the recording server cut [from, to) into one MP4
// (stream copy, no re-encode). With evidence it goes into the evidence vault
// (hashed, read-only); otherwise it's kept briefly for the browser to
// download.
func (s *Server) handleExportClip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !cameraIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "bad camera id")
		return
	}
	var in struct {
		From     int64  `json:"from"` // Unix ms
		To       int64  `json:"to"`
		Evidence bool   `json:"evidence"`
		Title    string `json:"title"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	from, to := time.UnixMilli(in.From), time.UnixMilli(in.To)
	ctx, cancel := context.WithTimeout(r.Context(), exportTimeout+30*time.Second)
	defer cancel()
	who := currentUser(r)
	if in.Evidence { // the same path as the Exports workspace (v2_evidence.go)
		ev, err := s.cutEvidence(ctx, id, in.From, in.To, in.Title, "", who.Name)
		if err != nil {
			writeError(w, commandStatus(err), err.Error())
			return
		}
		s.audit(r, "Evidence", "Locked evidence", ev.CaseID+" clip "+ev.CameraName+" "+ev.Range+", sha256 "+ev.SHA256[:16]+"…")
		writeJSON(w, http.StatusOK, map[string]any{"evidence": &ev.Evidence})
		return
	}

	rid := s.recorderAt(r.Context(), id, in.From/1000) // the server with the footage
	var res link.ExportResult
	if err := s.command(ctx, rid, link.CmdExportClip, link.ExportArgs{CameraID: id, From: in.From, To: in.To}, &res); err != nil {
		writeError(w, commandStatus(err), err.Error())
		return
	}
	camName := s.cameraLabel(r.Context(), id)
	label := rangeLabel(from, to)
	filename := safeFilename(camName) + "-" + from.Format("20060102-150405") + ".mp4"
	s.rememberExport(res.Token, preparedExport{userID: who.ID, recorderID: rid, filename: filename, sha256: res.SHA256, created: time.Now()})
	s.audit(r, "Recordings", "Exported clip", camName+" "+label+", sha256 "+res.SHA256[:16]+"…")
	writeJSON(w, http.StatusOK, map[string]any{
		"url": "/api/v1/exports/" + res.Token, "filename": filename, "size": res.Size, "sha256": res.SHA256,
		"duration": to.Sub(from).Seconds(),
	})
}

// rememberExport records a prepared download and forgets expired ones (the
// recording server deletes their files).
func (s *Server) rememberExport(token string, ex preparedExport) {
	st := &s.exports
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.byID == nil {
		st.byID = map[string]preparedExport{}
	}
	for k, v := range st.byID {
		if time.Since(v.created) > exportKeep {
			delete(st.byID, k)
		}
	}
	st.byID[token] = ex
}

// handleExportDownload serves a prepared clip to the user who made it.
func (s *Server) handleExportDownload(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	s.exports.mu.Lock()
	ex, ok := s.exports.byID[token]
	s.exports.mu.Unlock()
	if !exportIDPattern.MatchString(token) || !ok || ex.userID != currentUser(r).ID || time.Since(ex.created) > exportKeep {
		writeError(w, http.StatusNotFound, "export not found or expired — export the clip again")
		return
	}
	s.serveVideoPath(w, r, ex.recorderID, "/rs/exports/"+token, func(h http.Header) {
		if r.Header.Get("Range") == "" {
			s.audit(r, "Recordings", "Downloaded clip", ex.filename)
		}
		setContentType(h, "video/mp4")
		h.Set("Content-Disposition", `attachment; filename="`+ex.filename+`"`)
		h.Set("X-Content-SHA256", ex.sha256)
	})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := strconv.ParseInt(r.URL.Query().Get("t"), 10, 64)
	if !cameraIDPattern.MatchString(id) || err != nil {
		writeError(w, http.StatusBadRequest, "need a camera and a time (t, Unix ms)")
		return
	}
	at := time.UnixMilli(t)
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute+10*time.Second)
	defer cancel()
	var res link.SnapshotResult
	if err := s.command(ctx, s.recorderAt(r.Context(), id, t/1000), link.CmdSnapshot, link.SnapshotArgs{CameraID: id, At: t}, &res); err != nil {
		writeError(w, commandStatus(err), err.Error())
		return
	}
	camName := s.cameraLabel(r.Context(), id)
	s.audit(r, "Recordings", "Exported snapshot", camName+" "+at.Format("Jan 2 15:04:05.000"))
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(camName)+"-"+at.Format("20060102-150405")+`.jpg"`)
	_, _ = w.Write(res.JPEG)
}

// MARK: - Helpers

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// safeFilename keeps a camera name usable in a Content-Disposition header
// and on any file system.
func safeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "camera"
	}
	return b.String()
}

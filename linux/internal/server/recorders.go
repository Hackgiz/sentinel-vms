package server

// The management side's view of its recording servers. Everything that
// needs a camera's stream, recordings or detector goes through here:
// link.Recorder for config, commands and status, and the recorder's video
// plane (an http.Handler) for video bytes.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
)

// Recording servers connect and disconnect at runtime: the local one in
// --role all (in-process link), remote ones over the control link
// (phase 2). Each camera belongs to one of them (store.Camera.RecorderID).

// recorderSet is the registry of connected recording servers.
type recorderSet struct {
	mu   sync.RWMutex
	byID map[string]link.Recorder
	last map[string]link.ServerStatus // latest status report per server (Sink.Status)
}

// AttachRecorder connects the local recording server (--role all).
func (s *Server) AttachRecorder(r link.Recorder) { s.ConnectRecorder(store.LocalRecorderID, r) }

// ConnectRecorder registers a connected recording server under its id,
// replacing an older connection (nil r disconnects it). The control link
// calls it after a server's hello, then ApplyRecorder.
func (s *Server) ConnectRecorder(id string, r link.Recorder) {
	if r == nil {
		s.recs.mu.Lock()
		_, had := s.recs.byID[id]
		delete(s.recs.byID, id)
		s.recs.mu.Unlock()
		if had {
			bus.Publish(s.Bus, TopicRecorderOnline, RecorderOnline{ID: id, Online: false})
		}
		return
	}
	s.recs.mu.Lock()
	if s.recs.byID == nil {
		s.recs.byID = map[string]link.Recorder{}
	}
	s.recs.byID[id] = r
	s.recs.mu.Unlock()
	bus.Publish(s.Bus, TopicRecorderOnline, RecorderOnline{ID: id, Online: true})
}

// DisconnectRecorder forgets r if it is still the connection registered
// for id (a reconnect may already have replaced it).
func (s *Server) DisconnectRecorder(id string, r link.Recorder) {
	s.recs.mu.Lock()
	gone := s.recs.byID[id] == r
	if gone {
		delete(s.recs.byID, id)
	}
	s.recs.mu.Unlock()
	if gone {
		bus.Publish(s.Bus, TopicRecorderOnline, RecorderOnline{ID: id, Online: false})
	}
}

// ConnectedRecorders lists the ids of connected recording servers.
func (s *Server) ConnectedRecorders() []string {
	s.recs.mu.RLock()
	defer s.recs.mu.RUnlock()
	ids := make([]string, 0, len(s.recs.byID))
	for id := range s.recs.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// HasRecorder reports whether any recording server is connected.
func (s *Server) HasRecorder() bool {
	s.recs.mu.RLock()
	defer s.recs.mu.RUnlock()
	return len(s.recs.byID) > 0
}

// recorderConnected reports whether recording server id is connected.
func (s *Server) recorderConnected(id string) bool {
	s.recs.mu.RLock()
	defer s.recs.mu.RUnlock()
	return s.recs.byID[id] != nil
}

// recByID returns a connected recording server, or a stand-in that
// answers ErrNoRecorder.
func (s *Server) recByID(id string) link.Recorder {
	s.recs.mu.RLock()
	defer s.recs.mu.RUnlock()
	if r := s.recs.byID[id]; r != nil {
		return r
	}
	return noRecorder{}
}

// recFor returns the recording server that has a camera now, and its id.
func (s *Server) recFor(ctx context.Context, cameraID string) (link.Recorder, string, error) {
	c, err := s.Store.Camera(ctx, cameraID)
	if err != nil {
		return noRecorder{}, "", err
	}
	return s.recByID(c.RecorderID), c.RecorderID, nil
}

// recorderAt is the recording server that had a camera at t (Unix s): its
// footage, events and evidence from then are there. Falls back to the
// camera's current server (or, for a deleted camera, its last one).
func (s *Server) recorderAt(ctx context.Context, cameraID string, t int64) string {
	ps, _ := s.Store.CameraPlacements(ctx, cameraID)
	at := ""
	for _, p := range ps { // oldest first: the latest that had started by t wins
		if p.From <= t && (p.To == 0 || t < p.To) {
			at = p.RecorderID
		}
	}
	if at != "" {
		return at
	}
	if c, err := s.Store.Camera(ctx, cameraID); err == nil {
		return c.RecorderID
	}
	if len(ps) > 0 {
		return ps[len(ps)-1].RecorderID
	}
	return store.LocalRecorderID
}

// footageRecorders lists the recording servers that had a camera during
// [from, to) (Unix s), oldest placement first, each once; with no
// placement in the window, the server that had it at to. all ignores the
// window.
func (s *Server) footageRecorders(ctx context.Context, cameraID string, from, to int64, all bool) []string {
	ps, _ := s.Store.CameraPlacements(ctx, cameraID)
	var ids []string
	seen := map[string]bool{}
	for _, p := range ps {
		if !all && !(p.From < to && (p.To == 0 || p.To > from)) {
			continue
		}
		if !seen[p.RecorderID] {
			seen[p.RecorderID] = true
			ids = append(ids, p.RecorderID)
		}
	}
	if len(ids) == 0 {
		ids = []string{s.recorderAt(ctx, cameraID, to)}
	}
	return ids
}

// defaultRecorderID is where a camera goes when no server is named, and
// the server /api/v1's single-server pages (System, hardware acceleration,
// live view settings) describe: the local one if it is connected or no
// other is enrolled, else the only active enrolled one. ok is false when
// several are enrolled and the local one isn't connected (id is then
// "local").
func (s *Server) defaultRecorderID(ctx context.Context) (id string, ok bool) {
	if s.recorderConnected(store.LocalRecorderID) {
		return store.LocalRecorderID, true
	}
	list, err := s.Store.RecordingServers(ctx)
	if err != nil {
		return store.LocalRecorderID, true
	}
	var others []string
	for _, r := range list {
		if r.ID != store.LocalRecorderID && !r.Revoked() {
			others = append(others, r.ID)
		}
	}
	switch len(others) {
	case 0:
		return store.LocalRecorderID, true
	case 1:
		return others[0], true
	}
	return store.LocalRecorderID, false
}

// anyObjectDetector reports whether a connected recording server has the
// person/vehicle detector.
func (s *Server) anyObjectDetector() bool {
	for _, id := range s.ConnectedRecorders() {
		if st, _ := s.statusOf(id); st.Capabilities.Objects {
			return true
		}
	}
	return false
}

// noteStatus keeps a server's latest status report.
func (s *Server) noteStatus(st link.ServerStatus) {
	s.recs.mu.Lock()
	if s.recs.last == nil {
		s.recs.last = map[string]link.ServerStatus{}
	}
	s.recs.last[st.ServerID] = st
	s.recs.mu.Unlock()
}

// statusOf asks a connected recording server for its status (live), or
// returns its last report if it isn't connected (Media.Running false).
func (s *Server) statusOf(id string) (link.ServerStatus, bool) {
	if s.recorderConnected(id) {
		st := s.recByID(id).Status()
		if st.Cameras == nil {
			st.Cameras = map[string]link.CameraStatus{}
		}
		return st, true
	}
	s.recs.mu.RLock()
	st, ok := s.recs.last[id]
	s.recs.mu.RUnlock()
	if !ok {
		st = link.ServerStatus{ServerID: id}
	}
	st.Media.Running = false
	if st.Cameras == nil {
		st.Cameras = map[string]link.CameraStatus{}
	}
	for cid, c := range st.Cameras {
		c.Ready = false
		st.Cameras[cid] = c
	}
	return st, false
}

type noRecorder struct{}

func (noRecorder) ApplyConfig(context.Context, link.Snapshot) (link.Applied, error) {
	return link.Applied{}, link.ErrNoRecorder
}

func (noRecorder) Command(context.Context, link.Command) (link.Result, error) {
	return link.Result{}, link.ErrNoRecorder
}

func (noRecorder) Status() link.ServerStatus {
	return link.ServerStatus{Media: link.MediaStatus{Error: link.ErrNoRecorder.Error()}, Cameras: map[string]link.CameraStatus{}}
}

func (noRecorder) Video() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusServiceUnavailable, "no recording server is connected")
	})
}

// statusCache asks each recording server for its status at most once
// (a camera list shows every camera's server).
type statusCache struct {
	s  *Server
	by map[string]cachedStatus
}

type cachedStatus struct {
	st        link.ServerStatus
	connected bool
}

func (s *Server) statuses() *statusCache {
	return &statusCache{s: s, by: map[string]cachedStatus{}}
}

// of is statusOf(id), remembered.
func (c *statusCache) of(id string) (link.ServerStatus, bool) {
	if v, ok := c.by[id]; ok {
		return v.st, v.connected
	}
	st, connected := c.s.statusOf(id)
	c.by[id] = cachedStatus{st, connected}
	return st, connected
}

// command runs a command on recording server id and decodes its result
// into out (nil = ignore the body). A failed operation is a
// *link.CommandError.
func (s *Server) command(ctx context.Context, id, typ string, args, out any) error {
	c, err := link.NewCommand(typ, args)
	if err != nil {
		return err
	}
	res, err := s.recByID(id).Command(ctx, c)
	if err != nil {
		return err
	}
	return res.Decode(out)
}

// commandStatus maps a command failure to an HTTP status.
func commandStatus(err error) int {
	var ce *link.CommandError
	if !errors.As(err, &ce) {
		if errors.Is(err, link.ErrNoRecorder) {
			return http.StatusServiceUnavailable
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return http.StatusGatewayTimeout
		}
		return http.StatusBadGateway
	}
	switch ce.Code {
	case link.CodeBadRequest:
		return http.StatusBadRequest
	case link.CodeNotFound:
		return http.StatusNotFound
	case link.CodeBusy:
		return http.StatusTooManyRequests
	case link.CodeUnavailable:
		return http.StatusServiceUnavailable
	case link.CodeTimeout:
		return http.StatusGatewayTimeout
	case link.CodeNoClip, link.CodeClipWriting:
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func commandCode(err error) string {
	var ce *link.CommandError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// MARK: - Video plane

// serveVideo hands an already authorized request to recording server id's
// video plane unchanged (same path, query, headers and body).
func (s *Server) serveVideo(w http.ResponseWriter, r *http.Request, id string) {
	s.recByID(id).Video().ServeHTTP(w, r)
}

// serveVideoPath serves path from recording server id's video plane in answer to r (which
// keeps its method, Range and conditional headers; the query is dropped so
// no client credential is forwarded). onOK runs just before a non-error
// response starts (not for the plane's JSON errors): it sets the download
// headers and writes the audit entry, exactly where the handler used to
// after opening the file.
func (s *Server) serveVideoPath(w http.ResponseWriter, r *http.Request, id, path string, onOK func(http.Header)) {
	r2 := r.Clone(r.Context())
	r2.URL = &url.URL{Path: path}
	r2.RequestURI = path
	s.recByID(id).Video().ServeHTTP(&okHookWriter{ResponseWriter: w, onOK: onOK}, r2)
}

// setContentType sets the download's type, except on a multi-range answer
// (http.ServeContent's multipart/byteranges must stay).
func setContentType(h http.Header, ct string) {
	if !strings.HasPrefix(h.Get("Content-Type"), "multipart/") {
		h.Set("Content-Type", ct)
	}
}

type okHookWriter struct {
	http.ResponseWriter
	onOK  func(http.Header)
	wrote bool
}

func (o *okHookWriter) WriteHeader(code int) {
	if !o.wrote {
		o.wrote = true
		h := o.Header()
		if o.onOK != nil && !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
			o.onOK(h)
		}
	}
	o.ResponseWriter.WriteHeader(code)
}

func (o *okHookWriter) Write(b []byte) (int, error) {
	if !o.wrote {
		o.WriteHeader(http.StatusOK)
	}
	return o.ResponseWriter.Write(b)
}

// videoResponse is a fetched video-plane response.
type videoResponse struct {
	Status   int
	Header   http.Header
	Body     []byte
	Overflow bool // the body was longer than the limit
}

// fetchVideo GETs path?query from recording server id's video plane into
// memory, keeping at most max bytes of the body.
func (s *Server) fetchVideo(ctx context.Context, id, path, rawQuery string, max int) videoResponse {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, (&url.URL{Path: path, RawQuery: rawQuery}).String(), nil)
	if err != nil {
		return videoResponse{Status: http.StatusBadRequest}
	}
	cw := &captureWriter{h: http.Header{}, max: max}
	s.recByID(id).Video().ServeHTTP(cw, req)
	if cw.code == 0 {
		cw.code = http.StatusOK
	}
	return videoResponse{Status: cw.code, Header: cw.h, Body: cw.buf.Bytes(), Overflow: cw.over}
}

type captureWriter struct {
	h    http.Header
	code int
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *captureWriter) Header() http.Header { return c.h }

func (c *captureWriter) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	if room := c.max - c.buf.Len(); room < len(b) {
		if room > 0 {
			c.buf.Write(b[:room])
		}
		c.over = true
		return len(b), nil
	}
	return c.buf.Write(b)
}

// logApplyErrors reports cameras a recorder couldn't configure.
func logApplyErrors(a link.Applied) {
	for id, msg := range a.Errors {
		slog.Warn("recording server could not configure camera", "camera", id, "err", msg)
	}
}

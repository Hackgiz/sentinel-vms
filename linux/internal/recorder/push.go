package recorder

// Phone cameras (WAVE23-PHONE-CAMERA.md section A): an iPhone pushes its
// camera as fMP4 segments to the video plane, through the same /v/{token}
// gateway as live video (a token with the push right), directly or via the
// management server and Sentinel Cloud:
//
//	POST   /push/{id}/sessions                       {"video","width","height","fps","audio","device"?}
//	PUT    /push/{id}/sessions/{session}/init        fMP4 init segment (ftyp+moov)
//	PUT    /push/{id}/sessions/{session}/segments/{seq}  one media segment (moof+mdat), seq 0,1,2…
//	DELETE /push/{id}/sessions/{session}
//
// Each session feeds the init segment and then the media segments, in
// order, into one ffmpeg (-f mp4 -i pipe:0 -c copy -f rtsp) that publishes
// to the camera's MediaMTX path, so recording, live view and detection see
// an ordinary stream. Segments may arrive out of order: up to pushAhead
// past the next one are held; a missing one is skipped after pushGapSkip;
// a session with no segment for pushIdleEnd ends. One session per camera:
// a new one ends the old one.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/childproc"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/videogw"
)

const (
	pushMaxSegment = videogw.MaxPushSegment // 8 MiB, init or media segment
	pushMaxStart   = videogw.MaxPushSession
	pushAhead      = 8                // segments past the next one that are held
	pushGapSkip    = 3 * time.Second  // a missing segment is given up on after this
	pushIdleEnd    = 15 * time.Second // no segment this long ends the session
	pushTick       = 250 * time.Millisecond
	pushWriteLimit = 10 * time.Second // a stuck ffmpeg ends the session
)

// pushSink takes a session's bytes in order (ffmpeg's stdin; tests fake it).
type pushSink interface {
	Write(p []byte) error
	Close() error
}

type pushState struct {
	mu       sync.Mutex
	sessions map[string]*pushSession // camera id → open session
	seen     map[string]link.PushStatus
	// newSink starts a session's ingest; nil = ffmpeg into MediaMTX.
	newSink func(cameraID string) (pushSink, error)
	now     func() time.Time // nil = time.Now
	tick    time.Duration    // watchdog period; 0 = pushTick
}

type pushSession struct {
	id, camera, device string

	mu           sync.Mutex
	sink         pushSink
	init         []byte
	next         int
	pending      map[int][]byte
	gapSince     time.Time // when the oldest held segment started waiting for next
	lastActivity time.Time
	ended        bool
	stop         chan struct{}
}

func (st *pushState) clock() time.Time {
	if st.now != nil {
		return st.now()
	}
	return time.Now()
}

// MARK: - Handlers

type pushStart struct {
	Video  string  `json:"video"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	FPS    float64 `json:"fps"`
	Audio  *string `json:"audio"`
	Device string  `json:"device"`
}

func (r *Recorder) handlePushStart(w http.ResponseWriter, req *http.Request) {
	cam, ok := r.pushCamera(w, req)
	if !ok {
		return
	}
	var in pushStart
	dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, pushMaxStart))
	if err := dec.Decode(&in); err != nil {
		pushError(w, http.StatusBadRequest, "bad_request", "send the stream description as JSON")
		return
	}
	switch {
	case in.Video != "h264" && in.Video != "hevc":
		pushError(w, http.StatusBadRequest, "bad_request", "video must be h264 or hevc")
		return
	case in.Width < 0 || in.Width > 8192 || in.Height < 0 || in.Height > 8192 || in.FPS < 0 || in.FPS > 240:
		pushError(w, http.StatusBadRequest, "bad_request", "width, height or fps out of range")
		return
	case in.Audio != nil && *in.Audio != "aac":
		pushError(w, http.StatusBadRequest, "bad_request", "audio must be aac or null")
		return
	}
	device := cleanDevice(in.Device)
	if device == "" {
		device = "iPhone"
	}
	sess := r.startPush(cam.ID, device)
	slog.Info("phone camera started streaming", "camera", cam.Name, "device", device, "video", in.Video, "width", in.Width, "height", in.Height)
	writeJSON(w, http.StatusOK, map[string]any{"sessionId": sess.id, "maxSegmentBytes": pushMaxSegment})
}

func (r *Recorder) handlePushInit(w http.ResponseWriter, req *http.Request) {
	sess, ok := r.pushSessionFor(w, req)
	if !ok {
		return
	}
	body, ok := readPushBody(w, req)
	if !ok {
		return
	}
	if boxType(body) != "ftyp" {
		pushError(w, http.StatusBadRequest, "bad_request", "the init segment must start with an ftyp box")
		return
	}
	code, msg := r.pushInit(sess, body)
	if code != 0 {
		pushError(w, code, codeName(code), msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Recorder) handlePushSegment(w http.ResponseWriter, req *http.Request) {
	sess, ok := r.pushSessionFor(w, req)
	if !ok {
		return
	}
	seq, err := strconv.Atoi(req.PathValue("seq"))
	if err != nil || seq < 0 {
		pushError(w, http.StatusBadRequest, "bad_request", "seq must be 0, 1, 2…")
		return
	}
	body, ok := readPushBody(w, req)
	if !ok {
		return
	}
	switch boxType(body) {
	case "moof", "styp", "sidx", "prft", "emsg":
	default:
		pushError(w, http.StatusBadRequest, "bad_request", "a media segment must start with a moof box")
		return
	}
	res := r.pushSegment(sess, seq, body)
	switch {
	case res.expected >= 0:
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    map[string]string{"code": "conflict", "message": res.msg},
			"expected": res.expected,
		})
	case res.code != 0:
		pushError(w, res.code, codeName(res.code), res.msg)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (r *Recorder) handlePushStop(w http.ResponseWriter, req *http.Request) {
	id, sid := req.PathValue("id"), req.PathValue("session")
	r.push.mu.Lock()
	sess := r.push.sessions[id]
	if sess == nil || sess.id != sid {
		sess = nil
	}
	r.push.mu.Unlock()
	if sess != nil {
		r.endPush(sess, "stopped by the phone")
	}
	w.WriteHeader(http.StatusNoContent)
}

// pushCamera answers 404 itself unless the path names a phone camera of
// this recorder.
func (r *Recorder) pushCamera(w http.ResponseWriter, req *http.Request) (link.CameraConfig, bool) {
	id := req.PathValue("id")
	c, ok := r.camera(id)
	if !cameraIDPattern.MatchString(id) || !ok || !c.IsPhone() {
		pushError(w, http.StatusNotFound, "not_found", "no phone camera "+strconv.Quote(id)+" on this recording server")
		return link.CameraConfig{}, false
	}
	return c, true
}

func (r *Recorder) pushSessionFor(w http.ResponseWriter, req *http.Request) (*pushSession, bool) {
	cam, ok := r.pushCamera(w, req)
	if !ok {
		return nil, false
	}
	sid := req.PathValue("session")
	r.push.mu.Lock()
	sess := r.push.sessions[cam.ID]
	r.push.mu.Unlock()
	if sess == nil || sess.id != sid {
		pushError(w, http.StatusNotFound, "not_found", "this push session ended; start a new one")
		return nil, false
	}
	return sess, true
}

func readPushBody(w http.ResponseWriter, req *http.Request) ([]byte, bool) {
	if req.ContentLength > pushMaxSegment {
		pushError(w, http.StatusRequestEntityTooLarge, "too_large", "a segment may be at most 8 MiB")
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, pushMaxSegment))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			pushError(w, http.StatusRequestEntityTooLarge, "too_large", "a segment may be at most 8 MiB")
		} else {
			pushError(w, http.StatusBadRequest, "bad_request", "the segment didn't arrive whole")
		}
		return nil, false
	}
	if len(body) < 8 {
		pushError(w, http.StatusBadRequest, "bad_request", "empty segment")
		return nil, false
	}
	return body, true
}

// boxType is the type of the first MP4 box ("" if there is none).
func boxType(b []byte) string {
	if len(b) < 8 {
		return ""
	}
	return string(b[4:8])
}

func pushError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func codeName(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "too_large"
	}
	return "unavailable"
}

// cleanDevice keeps a device name printable and short.
func cleanDevice(s string) string {
	s = strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return -1
		}
		return c
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	return s
}

// MARK: - Sessions

// startPush opens a new session for a camera, ending its previous one.
func (r *Recorder) startPush(cameraID, device string) *pushSession {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	st := &r.push
	sess := &pushSession{id: hex.EncodeToString(raw), camera: cameraID, device: device, pending: map[int][]byte{},
		lastActivity: st.clock(), stop: make(chan struct{})}
	st.mu.Lock()
	if st.sessions == nil {
		st.sessions, st.seen = map[string]*pushSession{}, map[string]link.PushStatus{}
	}
	old := st.sessions[cameraID]
	st.sessions[cameraID] = sess
	seen := st.seen[cameraID]
	seen.Device = device
	st.seen[cameraID] = seen
	st.mu.Unlock()
	if old != nil {
		r.endPush(old, "replaced by a new session")
	}
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		r.watchPush(sess)
	}()
	return sess
}

// pushInit starts the ingest with the init segment. A repeat of the same
// init is fine; a different one is a conflict (start a new session).
func (r *Recorder) pushInit(sess *pushSession, body []byte) (int, string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.ended {
		return http.StatusNotFound, "this push session ended; start a new one"
	}
	sess.lastActivity = r.push.clock()
	if sess.init != nil {
		if bytes.Equal(sess.init, body) {
			return 0, ""
		}
		return http.StatusConflict, "this session already has a different init segment; start a new session"
	}
	sink, err := r.newPushSink(sess.camera)
	if err != nil {
		slog.Warn("phone camera ingest did not start", "camera", r.cameraName(sess.camera), "err", err)
		return http.StatusServiceUnavailable, "the recording server can't take video right now: " + err.Error()
	}
	if err := sink.Write(body); err != nil {
		_ = sink.Close()
		return http.StatusServiceUnavailable, "the recording server can't take video right now"
	}
	sess.sink, sess.init = sink, body
	return 0, ""
}

type pushResult struct {
	code     int
	msg      string
	expected int // >= 0: 409 with the seq the server expects
}

// pushSegment takes one media segment: written now if it is the next one,
// held if it is at most pushAhead past it, acknowledged if already taken.
func (r *Recorder) pushSegment(sess *pushSession, seq int, body []byte) pushResult {
	ok := pushResult{expected: -1}
	sess.mu.Lock()
	if sess.ended {
		sess.mu.Unlock()
		return pushResult{code: http.StatusNotFound, msg: "this push session ended; start a new one", expected: -1}
	}
	if sess.init == nil {
		sess.mu.Unlock()
		return pushResult{msg: "send the init segment first", expected: 0}
	}
	now := r.push.clock()
	if seq < sess.next || sess.pending[seq] != nil {
		sess.lastActivity = now
		sess.mu.Unlock()
		return ok // a repeat
	}
	if seq > sess.next+pushAhead {
		exp := sess.next
		sess.mu.Unlock()
		return pushResult{msg: "segment " + strconv.Itoa(seq) + " is too far ahead", expected: exp}
	}
	sess.lastActivity = now
	sess.pending[seq] = body
	err := sess.flush(now)
	sess.mu.Unlock()
	if err != nil {
		r.endPush(sess, "the ingest stopped: "+err.Error())
		return pushResult{code: http.StatusServiceUnavailable, msg: "the recording server stopped taking video; start a new session", expected: -1}
	}
	r.push.mu.Lock()
	seen := r.push.seen[sess.camera]
	seen.LastSeenAt = now.UnixMilli()
	r.push.seen[sess.camera] = seen
	r.push.mu.Unlock()
	return ok
}

// flush writes the held segments that are next in order. Called with
// sess.mu held.
func (sess *pushSession) flush(now time.Time) error {
	for {
		b, ok := sess.pending[sess.next]
		if !ok {
			break
		}
		delete(sess.pending, sess.next)
		sess.next++
		if err := sess.sink.Write(b); err != nil {
			return err
		}
	}
	switch {
	case len(sess.pending) == 0:
		sess.gapSince = time.Time{}
	case sess.gapSince.IsZero():
		sess.gapSince = now
	}
	return nil
}

// skipGap gives up on the missing segment(s) before the oldest held one.
// Called with sess.mu held.
func (sess *pushSession) skipGap(now time.Time) error {
	keys := make([]int, 0, len(sess.pending))
	for k := range sess.pending {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Ints(keys)
	sess.next = keys[0]
	sess.gapSince = time.Time{}
	return sess.flush(now)
}

// watchPush skips gaps and ends an idle session.
func (r *Recorder) watchPush(sess *pushSession) {
	tick := r.push.tick
	if tick <= 0 {
		tick = pushTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-sess.stop:
			return
		case <-t.C:
		}
		now := r.push.clock()
		sess.mu.Lock()
		if sess.ended {
			sess.mu.Unlock()
			return
		}
		var err error
		if !sess.gapSince.IsZero() && now.Sub(sess.gapSince) >= pushGapSkip {
			slog.Info("phone camera segment missing; skipping ahead", "camera", r.cameraName(sess.camera), "missing", sess.next)
			err = sess.skipGap(now)
		}
		idle := now.Sub(sess.lastActivity) >= pushIdleEnd
		sess.mu.Unlock()
		switch {
		case err != nil:
			r.endPush(sess, "the ingest stopped: "+err.Error())
			return
		case idle:
			r.endPush(sess, "no video for 15 s")
			return
		}
	}
}

// endPush closes a session (once) and its ingest.
func (r *Recorder) endPush(sess *pushSession, why string) {
	r.push.mu.Lock()
	if r.push.sessions[sess.camera] == sess {
		delete(r.push.sessions, sess.camera)
	}
	r.push.mu.Unlock()
	sess.mu.Lock()
	if sess.ended {
		sess.mu.Unlock()
		return
	}
	sess.ended = true
	close(sess.stop)
	sink := sess.sink
	sess.sink, sess.pending = nil, nil
	sess.mu.Unlock()
	if sink != nil {
		_ = sink.Close()
	}
	slog.Info("phone camera stopped streaming", "camera", r.cameraName(sess.camera), "why", why)
}

// syncPush ends the sessions of cameras that are no longer phone cameras
// on this recorder (ApplyConfig).
func (r *Recorder) syncPush(cams []link.CameraConfig) {
	keep := map[string]bool{}
	for _, c := range cams {
		if c.IsPhone() {
			keep[c.ID] = true
		}
	}
	r.push.mu.Lock()
	var gone []*pushSession
	for id, sess := range r.push.sessions {
		if !keep[id] {
			gone = append(gone, sess)
		}
	}
	for id := range r.push.seen {
		if !keep[id] {
			delete(r.push.seen, id)
		}
	}
	r.push.mu.Unlock()
	for _, sess := range gone {
		r.endPush(sess, "the camera was removed or changed")
	}
}

// endAllPush ends every session (shutdown).
func (r *Recorder) endAllPush() {
	r.push.mu.Lock()
	all := make([]*pushSession, 0, len(r.push.sessions))
	for _, sess := range r.push.sessions {
		all = append(all, sess)
	}
	r.push.mu.Unlock()
	for _, sess := range all {
		r.endPush(sess, "the recording server is stopping")
	}
}

// pushStatus is a phone camera's push state for status reports.
func (r *Recorder) pushStatus(cameraID string) *link.PushStatus {
	r.push.mu.Lock()
	defer r.push.mu.Unlock()
	st := r.push.seen[cameraID]
	st.Streaming = r.push.sessions[cameraID] != nil
	return &st
}

// MARK: - ffmpeg ingest

func (r *Recorder) newPushSink(cameraID string) (pushSink, error) {
	if r.push.newSink != nil {
		return r.push.newSink(cameraID)
	}
	if r.Media == nil {
		return nil, errors.New("no media engine")
	}
	ff := r.FFmpegPath()
	if ff == "" {
		return nil, errors.New("ffmpeg isn't installed")
	}
	return startFFmpegSink(r.ctx(), ff, r.Media.PublishURL(cameraID))
}

// ffmpegSink publishes fMP4 from its stdin to MediaMTX over RTSP.
type ffmpegSink struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan struct{}
	stderr *tailBuffer
	once   sync.Once
}

func startFFmpegSink(ctx context.Context, ffmpeg, publishURL string) (*ffmpegSink, error) {
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "mp4", "-i", "pipe:0",
		"-map", "0", "-c", "copy", "-f", "rtsp", "-rtsp_transport", "tcp", publishURL)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	tail := &tailBuffer{max: 4096}
	cmd.Stdout, cmd.Stderr = io.Discard, tail
	childproc.Configure(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &ffmpegSink{cmd: cmd, stdin: stdin, done: make(chan struct{}), stderr: tail}
	go func() {
		err := cmd.Wait()
		if err != nil && ctx.Err() == nil {
			slog.Warn("phone camera ingest exited", "err", err, "ffmpeg", strings.TrimSpace(redactURL(tail.String(), publishURL)))
		}
		close(s.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.done:
		}
	}()
	return s, nil
}

func (s *ffmpegSink) Write(p []byte) error {
	select {
	case <-s.done:
		return errors.New("ffmpeg exited")
	default:
	}
	if f, ok := s.stdin.(*os.File); ok {
		_ = f.SetWriteDeadline(time.Now().Add(pushWriteLimit))
	}
	_, err := s.stdin.Write(p)
	return err
}

// Close ends the input (ffmpeg finishes and exits) and kills it if it
// doesn't within 5 s.
func (s *ffmpegSink) Close() error {
	s.once.Do(func() {
		_ = s.stdin.Close()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			_ = s.cmd.Process.Kill()
			<-s.done
		}
	})
	return nil
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// redactURL keeps the publish credential out of logs.
func redactURL(s, u string) string {
	return pushCredPattern.ReplaceAllString(strings.ReplaceAll(s, u, "rtsp://…"), "${1}…@")
}

var pushCredPattern = regexp.MustCompile(`(://[^:/@\s]+:)[^@\s]*@`)

package server

// API v2: two-way audio ("talk-down") through a camera speaker over the
// ONVIF audio backchannel (WAVE9-REACH.md, stream B).
//
//   - Capability: cameras with an ONVIF address are probed for a PCMU
//     backchannel (onvif.TalkbackSupported) when saved and, lazily, the
//     first time a camera list is served; the answer lives in memory only
//     and shows as `talkback` on the camera payload. A probe that changes
//     the answer publishes config.changed so clients re-read cameras.
//   - Session: GET /api/v2/cameras/{id}/talkback upgrades to a WebSocket.
//     The client sends binary frames of little-endian 16-bit mono PCM at
//     `rate` (8000, default, or 16000); the server encodes G.711 µ-law and
//     relays 20 ms RTP packets to the camera (internal/talkback). One
//     session per camera (409 / close 4409 for the second), 30 s without
//     audio or 5 min in all ends it, and the camera always gets TEARDOWN.
//   - Auth: as the event stream (bearer header, same-origin session
//     cookie, or {"type":"auth","accessToken"} as the first message).
//     Permission: auth.ControlPTZ (whoever may steer a camera may speak
//     through it); camera scope applies (404). Start and end are audited
//     with the duration.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/talkback"
)

// Talkback limits and timing; variables so tests can shorten them. A
// handler copies them when it is built (s.Handler()).
var (
	talkIdleTimeout = talkback.DefaultIdleTimeout
	talkMaxDuration = talkback.DefaultMaxDuration
	talkAuthTimeout = 10 * time.Second
	talkDialBudget  = 10 * time.Second
	talkProbeBudget = 10 * time.Second
	talkProbeTTL    = 10 * time.Minute
	talkFailTTL     = 30 * time.Second
)

// Talkback WebSocket close codes (api/openapi.yaml).
const (
	closeTalkBadRequest = 4400 // the camera has no talkback, or a bad rate
	closeTalkNotFound   = 4404
	closeTalkIdle       = 4408 // no audio for 30 s
	closeTalkBusy       = 4409 // someone else is talking through the camera
	closeTalkLimit      = 4410 // the 5 minute cap
	closeTalkCamera     = 4502 // the camera refused or dropped the backchannel
)

const talkRemoteMsg = "talkback is only available for cameras on the management server in this wave"

var talkUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 1024,
	// streamAuth refuses cross-site cookie upgrades; bearer and
	// first-message auth carry no ambient credentials.
	CheckOrigin: func(*http.Request) bool { return true },
}

// talkbackState is the server's talkback side: the per-camera session
// slots and the in-memory capability probes.
type talkbackState struct {
	mgr       *talkback.Manager
	authWait  time.Duration
	mu        sync.Mutex
	probes    map[string]*talkProbe
	dialHook  func(ctx context.Context, url, user, pass string) (talkback.Sink, error) // tests
	probeHook func(ctx context.Context, cam *store.Camera, pass string) (bool, string, error)
}

type talkProbe struct {
	supported bool
	url       string // the RTSP address that offered the backchannel
	failed    bool
	at        time.Time
	running   bool
}

func (p *talkProbe) fresh() bool {
	if p.at.IsZero() {
		return false
	}
	ttl := talkProbeTTL
	if p.failed {
		ttl = talkFailTTL
	}
	return time.Since(p.at) < ttl
}

func (s *Server) registerV2Talkback(mux *http.ServeMux) {
	s.talk.mgr = &talkback.Manager{IdleTimeout: talkIdleTimeout, MaxDuration: talkMaxDuration}
	s.talk.authWait = talkAuthTimeout
	s.v2Route(mux, "GET /api/v2/cameras/{cameraId}/talkback", publicRoute, s.handleV2Talkback)
}

// MARK: - Capability

// talkLocal reports whether the management server talks to this camera
// itself (control-link forwarding is a follow-up, as for PTZ).
func talkLocal(c *store.Camera) bool {
	return c.RecorderID == "" || c.RecorderID == store.LocalRecorderID
}

// talkProbable: only cameras with an ONVIF address on this server are
// probed, the way the other ONVIF capabilities are.
func talkProbable(c *store.Camera) bool { return c.ONVIFURL != "" && talkLocal(c) }

func (s *Server) runTalkProbe(ctx context.Context, cam *store.Camera, pass string) (bool, string, error) {
	if h := s.talk.probeHook; h != nil {
		return h(ctx, cam, pass)
	}
	ctx, cancel := context.WithTimeout(ctx, talkProbeBudget)
	defer cancel()
	return onvif.TalkbackSupported(ctx, cam.ONVIFURL, cam.RTSPURL, cam.Username, pass)
}

// talkbackProbe answers from the cache, else probes now (synchronously).
func (s *Server) talkbackProbe(ctx context.Context, cam *store.Camera, pass string) (bool, string, error) {
	if !talkProbable(cam) {
		return false, "", nil
	}
	s.talk.mu.Lock()
	if p := s.talk.probes[cam.ID]; p != nil && p.fresh() && !p.failed {
		s.talk.mu.Unlock()
		return p.supported, p.url, nil
	}
	s.talk.mu.Unlock()
	ok, url, err := s.runTalkProbe(ctx, cam, pass)
	s.storeTalkProbe(cam.ID, ok, url, err)
	return ok, url, err
}

// storeTalkProbe records a result and reports whether the visible answer
// changed.
func (s *Server) storeTalkProbe(id string, ok bool, url string, err error) bool {
	s.talk.mu.Lock()
	defer s.talk.mu.Unlock()
	if s.talk.probes == nil {
		s.talk.probes = map[string]*talkProbe{}
	}
	prev := s.talk.probes[id]
	was := prev != nil && prev.supported
	s.talk.probes[id] = &talkProbe{supported: ok, url: url, failed: err != nil, at: time.Now()}
	return was != ok
}

// talkbackRefresh probes a camera in the background unless a probe is
// already running or a fresh answer exists (force ignores the latter).
func (s *Server) talkbackRefresh(id string, force bool) {
	s.talk.mu.Lock()
	if s.talk.probes == nil {
		s.talk.probes = map[string]*talkProbe{}
	}
	p := s.talk.probes[id]
	if p != nil && (p.running || (!force && p.fresh())) {
		s.talk.mu.Unlock()
		return
	}
	if p == nil {
		p = &talkProbe{}
		s.talk.probes[id] = p
	}
	p.running = true
	s.talk.mu.Unlock()
	go func() {
		ctx := s.ctxOrBackground()
		done := func(ok bool, url string, err error) {
			if s.storeTalkProbe(id, ok, url, err) {
				if cams, err := s.Store.Cameras(ctx); err == nil {
					s.publishConfigChanged(cams) // clients re-read `talkback`
				}
			}
		}
		cam, err := s.Store.Camera(ctx, id)
		if err != nil || !talkProbable(cam) {
			done(false, "", nil)
			return
		}
		pass, err := s.Store.CameraPassword(cam)
		if err != nil {
			done(false, "", err)
			return
		}
		ok, url, err := s.runTalkProbe(ctx, cam, pass)
		if err != nil {
			slog.Debug("talkback probe failed", "camera", cam.Name, "err", err)
		}
		done(ok, url, err)
	}()
}

// talkbackCameraSaved drops a camera's probe (address or credentials may
// have changed) and probes it again in the background. saveCamera hook.
func (s *Server) talkbackCameraSaved(c *store.Camera) {
	s.talk.mu.Lock()
	running := false
	if p := s.talk.probes[c.ID]; p != nil {
		running = p.running
		if !running {
			delete(s.talk.probes, c.ID)
		}
	}
	s.talk.mu.Unlock()
	if talkProbable(c) && !running {
		s.talkbackRefresh(c.ID, true)
	}
}

// fillCameraTalkback sets `talkback` on camera views from the probe cache
// and starts background probes for cameras without a fresh answer.
func (s *Server) fillCameraTalkback(cams []v2Camera) {
	var stale []string
	s.talk.mu.Lock()
	for i := range cams {
		c := &cams[i]
		if c.ONVIFURL == "" || (c.RecorderID != "" && c.RecorderID != store.LocalRecorderID) {
			continue
		}
		p := s.talk.probes[c.ID]
		if p != nil {
			c.Talkback = p.supported
		}
		if p == nil || (!p.running && !p.fresh()) {
			stale = append(stale, c.ID)
		}
	}
	s.talk.mu.Unlock()
	for _, id := range stale {
		s.talkbackRefresh(id, false)
	}
}

// MARK: - Session

// talkTarget is a camera that passed the checks, with its backchannel.
type talkTarget struct {
	cam  *store.Camera
	pass string
	url  string
}

// talkCheck runs the permission, scope and capability checks for u. A
// non-zero status is the refusal (HTTP status; the close code is derived).
func (s *Server) talkCheck(ctx context.Context, u *store.User, id string) (*talkTarget, int, string) {
	if !s.can(u.Role, auth.ControlPTZ) {
		return nil, http.StatusForbidden, "your role (" + u.Role + ") can't do this"
	}
	if !s.scopeOf(ctx, u).camera(id) {
		return nil, http.StatusNotFound, "camera not found"
	}
	cam, err := s.Store.Camera(ctx, id)
	if err != nil {
		return nil, http.StatusNotFound, "camera not found"
	}
	// TODO(wave4): forward over control link
	if !talkLocal(cam) {
		return nil, http.StatusBadGateway, talkRemoteMsg
	}
	pass, err := s.Store.CameraPassword(cam)
	if err != nil {
		return nil, http.StatusInternalServerError, "the camera's password could not be read"
	}
	ok, url, err := s.talkbackProbe(ctx, cam, pass)
	switch {
	case errors.Is(err, onvif.ErrAuth):
		return nil, http.StatusBadGateway, "the camera rejected the stored username or password"
	case err != nil:
		return nil, http.StatusBadGateway, "the camera could not be asked about two-way audio: " + err.Error()
	case !ok:
		return nil, http.StatusBadRequest, "this camera does not report two-way audio"
	}
	return &talkTarget{cam: cam, pass: pass, url: url}, 0, ""
}

func talkCloseCode(status int) int {
	switch status {
	case http.StatusForbidden:
		return closeForbidden
	case http.StatusNotFound:
		return closeTalkNotFound
	case http.StatusConflict:
		return closeTalkBusy
	case http.StatusBadRequest:
		return closeTalkBadRequest
	default:
		return closeTalkCamera
	}
}

func v2CodeFor(status int) string {
	switch status {
	case http.StatusForbidden:
		return codeForbidden
	case http.StatusNotFound:
		return codeNotFound
	case http.StatusConflict:
		return codeConflict
	case http.StatusBadRequest:
		return codeBadRequest
	case http.StatusInternalServerError:
		return codeInternal
	default:
		return codeUnavailable
	}
}

func (s *Server) handleV2Talkback(w http.ResponseWriter, r *http.Request) {
	rate := 8000
	switch q := r.URL.Query().Get("rate"); q {
	case "", "8000":
	case "16000":
		rate = 16000
	default:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "rate must be 8000 or 16000")
		return
	}
	id := r.PathValue("cameraId")
	u, _, ok := s.streamAuth(w, r)
	if !ok {
		return
	}
	var target *talkTarget
	var talk *talkback.Talk
	if u != nil {
		t, status, msg := s.talkCheck(r.Context(), u, id)
		if status != 0 {
			writeV2Error(w, status, v2CodeFor(status), msg)
			return
		}
		tk, err := s.talk.mgr.Reserve(id, u.Name)
		if err != nil {
			writeV2Error(w, http.StatusConflict, codeConflict, talkBusyMsg(s.talk.mgr.Active(id)))
			return
		}
		target, talk = t, tk
	}
	ws, err := talkUpgrader.Upgrade(w, r, nil)
	if err != nil {
		if talk != nil {
			talk.End(talkback.EndClosed)
		}
		return // the upgrader already answered
	}
	defer ws.Close()
	ws.SetReadLimit(64 << 10)
	if u == nil {
		if u = s.talkFirstMessageAuth(ws); u == nil {
			return
		}
		t, status, msg := s.talkCheck(r.Context(), u, id)
		if status != 0 {
			talkClose(ws, talkCloseCode(status), msg)
			return
		}
		tk, err := s.talk.mgr.Reserve(id, u.Name)
		if err != nil {
			talkClose(ws, closeTalkBusy, talkBusyMsg(s.talk.mgr.Active(id)))
			return
		}
		target, talk = t, tk
	}
	s.runTalk(ws, u, target, talk, rate)
}

func talkBusyMsg(who string) string {
	if who == "" {
		return talkback.ErrBusy.Error()
	}
	return who + " is already talking through this camera"
}

func talkClose(ws *websocket.Conn, code int, reason string) {
	if len(reason) > 120 { // a close frame's reason is limited to 123 bytes
		reason = reason[:120]
	}
	_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(streamWriteWait))
}

// talkFirstMessageAuth waits for {"type":"auth","accessToken"}; it closes
// with 4401 and returns nil when that doesn't come or check out.
func (s *Server) talkFirstMessageAuth(ws *websocket.Conn) *store.User {
	_ = ws.SetReadDeadline(time.Now().Add(s.talk.authWait))
	defer ws.SetReadDeadline(time.Time{})
	var in struct {
		Type        string `json:"type"`
		AccessToken string `json:"accessToken"`
	}
	mt, data, err := ws.ReadMessage()
	if err == nil && mt == websocket.TextMessage {
		err = json.Unmarshal(data, &in)
	}
	if err != nil || mt != websocket.TextMessage || in.Type != "auth" || in.AccessToken == "" {
		talkClose(ws, closeSignInEnded, "sign in required")
		return nil
	}
	u, err := s.Store.AccessTokenUser(context.Background(), in.AccessToken)
	if err != nil {
		talkClose(ws, closeSignInEnded, "the token expired or was revoked")
		return nil
	}
	return u
}

type talkFrame struct {
	binary bool
	data   []byte
}

// runTalk opens the camera's backchannel and relays audio until the client
// hangs up or a limit ends the session.
func (s *Server) runTalk(ws *websocket.Conn, u *store.User, t *talkTarget, talk *talkback.Talk, rate int) {
	ctx, cancel := context.WithTimeout(s.ctxOrBackground(), talkDialBudget)
	sink, err := s.dialTalk(ctx, t.url, t.cam.Username, t.pass)
	cancel()
	if err != nil {
		talk.End(talkback.EndError)
		if errors.Is(err, onvif.ErrNoBackchannel) {
			s.talkbackCameraSaved(t.cam) // the camera changed: probe again
		}
		slog.Warn("talkback: camera refused the backchannel", "camera", t.cam.Name, "err", err)
		_ = ws.WriteJSON(map[string]any{"type": "error", "message": "the camera refused two-way audio: " + err.Error()})
		talkClose(ws, closeTalkCamera, "the camera refused two-way audio")
		return
	}
	talk.Attach(sink, rate)
	s.auditAs(u.Name, "Cameras", "Started talk-down", t.cam.Name)
	_ = ws.SetWriteDeadline(time.Now().Add(streamWriteWait))
	_ = ws.WriteJSON(map[string]any{
		"type": "ready", "cameraId": t.cam.ID, "codec": "PCMU/8000", "rate": rate,
		"idleTimeoutMs": s.talk.mgr.IdleTimeout.Milliseconds(), "maxDurationMs": s.talk.mgr.MaxDuration.Milliseconds(),
	})

	frames := make(chan talkFrame, 16)
	go func() {
		defer close(frames)
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			select {
			case frames <- talkFrame{binary: mt == websocket.BinaryMessage, data: data}:
			case <-talk.Done():
				return
			}
		}
	}()

	reason := talkback.EndClosed
	shutdown := s.ctxOrBackground().Done()
loop:
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				break loop // the client hung up
			}
			if f.binary {
				if err := talk.Feed(f.data); err != nil {
					slog.Warn("talkback: camera connection failed", "camera", t.cam.Name, "err", err)
				}
				continue
			}
			var m struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(f.data, &m) == nil && m.Type == "stop" {
				talkClose(ws, websocket.CloseNormalClosure, "stopped")
				break loop
			}
		case <-talk.Done():
			reason = talk.Reason()
			switch reason {
			case talkback.EndIdle:
				talkClose(ws, closeTalkIdle, "no audio for "+fmtSeconds(s.talk.mgr.IdleTimeout))
			case talkback.EndLimit:
				talkClose(ws, closeTalkLimit, "talk time limit reached ("+fmtSeconds(s.talk.mgr.MaxDuration)+")")
			default:
				talkClose(ws, closeTalkCamera, "the camera connection failed")
			}
			break loop
		case <-shutdown:
			talkClose(ws, websocket.CloseGoingAway, "server shutting down")
			break loop
		}
	}
	d, why := talk.End(reason)
	s.auditAs(u.Name, "Cameras", "Ended talk-down", fmt.Sprintf("%s: %s (%s)", t.cam.Name, fmtSeconds(d), talkReasonText(why)))
}

func (s *Server) dialTalk(ctx context.Context, url, user, pass string) (talkback.Sink, error) {
	if h := s.talk.dialHook; h != nil {
		return h(ctx, url, user, pass)
	}
	return talkback.Dial(ctx, url, user, pass)
}

func talkReasonText(r string) string {
	switch r {
	case talkback.EndIdle:
		return "idle timeout"
	case talkback.EndLimit:
		return "time limit"
	case talkback.EndError:
		return "camera connection failed"
	default:
		return "hung up"
	}
}

// fmtSeconds renders a duration as whole seconds ("12s"; at least "0s").
func fmtSeconds(d time.Duration) string {
	return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + "s"
}

// auditAs writes an audit entry for a user outside a request (a WebSocket
// session outlives its upgrade request's middleware).
func (s *Server) auditAs(user, area, action, detail string) {
	if err := s.Store.Audit(context.Background(), user, area, action, detail); err != nil {
		slog.Error("audit write failed", "err", err)
	}
}

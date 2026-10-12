// Package videogw is the /v/{token}/… video gateway shared by the
// management server (the gateway path: remote access and clients that
// can't reach a recording server) and each recording server's own video
// plane (PLATFORM.md, "Control link wire protocol", "Video plane").
//
// Clients put a video token (internal/videotoken) in the path,
// /v/<token>/live/<camera>/index.m3u8, so the relative URLs inside HLS
// playlists carry it and no player needs custom headers. The gateway
// verifies the token, checks that the path is one the token's camera and
// rights cover, and hands the request to a video plane (link.VideoPaths)
// with the prefix removed. Absolute URLs coming back (playback playlists,
// the WHEP Location) get the prefix put back so the client stays under it.
//
// It depends on the standard library and internal/videotoken only.
package videogw

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/videotoken"
)

const (
	// Prefix is where the gateway is mounted.
	Prefix = "/v/"

	MaxLivePlaylist     = 1 << 20  // a live HLS playlist is a few KB
	MaxPlaybackPlaylist = 32 << 20 // a day of playback is tens of thousands of lines
	MaxHLSSegment       = 32 << 20

	// InternalCamera is the camera claim of the tokens the management
	// server mints for itself to reach a recording server's internal /rs/
	// paths (exports, evidence, event snapshots). Client tokens always name
	// a real camera id, so they never match it.
	InternalCamera = "rs"
	// ManagementSubject is the subject of tokens the management server
	// mints for requests it already authorized without a signed-in user
	// (the gateway path). Its own per-user limits already applied.
	ManagementSubject = "management"
)

var (
	CameraIDPattern    = regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}$`)
	segmentPattern     = regexp.MustCompile(`^\d{8}-\d{6}-\d+\.mp4$`)
	hlsFilePattern     = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.(m3u8|ts|mp4|m4s)$`)
	whepSessionPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	internalFile       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,200}$`)
	uriAttrPattern     = regexp.MustCompile(`URI="([^"]+)"`)

	liveRights     = []videotoken.Right{videotoken.Live}
	recordedRights = []videotoken.Right{videotoken.Playback, videotoken.Export}
	internalRights = []videotoken.Right{videotoken.Export}
)

// Route is a video-plane path the gateway lets through.
type Route struct {
	Camera   string
	Rights   []videotoken.Right // any one of these suffices
	Playlist int                // > 0: an HLS playlist, buffered up to this many bytes and rewritten
	Segment  bool               // a live HLS segment: buffered so it goes out with a Content-Length
	WHEP     string             // "offer" or "session" for WebRTC signaling
	Internal bool               // an /rs/ path (Camera is InternalCamera)
}

// Match maps a method and a path below the token onto the video plane's
// public paths (link.VideoPaths, minus the internal /rs/ ones).
func Match(method, p string) (Route, bool) {
	get := method == http.MethodGet || method == http.MethodHead
	seg := strings.Split(strings.TrimPrefix(p, "/"), "/")
	var rt Route
	switch {
	case len(seg) == 3 && seg[0] == "live" && get && hlsFilePattern.MatchString(seg[2]):
		rt = Route{Camera: seg[1], Rights: liveRights}
		if strings.HasSuffix(seg[2], ".m3u8") {
			rt.Playlist = MaxLivePlaylist
		} else {
			rt.Segment = true
		}
	case len(seg) == 3 && seg[0] == "webrtc" && seg[2] == "whep" && method == http.MethodPost:
		rt = Route{Camera: seg[1], Rights: liveRights, WHEP: "offer"}
	case len(seg) == 4 && seg[0] == "webrtc" && seg[2] == "whep" && (method == http.MethodPatch || method == http.MethodDelete) &&
		whepSessionPattern.MatchString(seg[3]):
		rt = Route{Camera: seg[1], Rights: liveRights, WHEP: "session"}
	case len(seg) == 3 && seg[0] == "recordings" && get && segmentPattern.MatchString(seg[2]):
		rt = Route{Camera: seg[1], Rights: recordedRights}
	case len(seg) == 5 && p == "/api/v1/cameras/"+seg[3]+"/playback.m3u8" && get:
		rt = Route{Camera: seg[3], Rights: recordedRights, Playlist: MaxPlaybackPlaylist}
	case len(seg) == 6 && p == "/api/v1/cameras/"+seg[3]+"/pieces/"+seg[5] && get && segmentPattern.MatchString(seg[5]):
		rt = Route{Camera: seg[3], Rights: recordedRights}
	default:
		return Route{}, false
	}
	return rt, CameraIDPattern.MatchString(rt.Camera)
}

// MatchInternal maps the video plane's internal /rs/ paths, which only the
// management server reaches (with an InternalCamera token).
func MatchInternal(method, p string) (Route, bool) {
	seg := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if (method != http.MethodGet && method != http.MethodHead) || len(seg) != 3 || seg[0] != "rs" || !internalFile.MatchString(seg[2]) {
		return Route{}, false
	}
	switch seg[1] {
	case "exports", "evidence", "events":
		return Route{Camera: InternalCamera, Rights: internalRights, Internal: true}, true
	}
	return Route{}, false
}

// Allows reports whether claims cover the route.
func (rt Route) Allows(c videotoken.Claims) bool {
	if rt.Camera != c.Camera {
		return false
	}
	for _, r := range rt.Rights {
		if c.Has(r) {
			return true
		}
	}
	return false
}

// MARK: - Gateway

// Gateway serves /v/{token}/{video-plane path} for one video plane.
//
// No cookie or Origin (CSRF) checks: the gateway uses no ambient
// credential. The token is in the URL a request is made to, so a page on
// another site can only make a request here if it already holds the token,
// in which case it could use it directly. Cookies and Authorization are
// ignored and not forwarded. What remains of the dashboard's WHEP checks is
// the exact Content-Type and the per-user session rate limit.
type Gateway struct {
	// Verify checks a token. videotoken errors answer 401; any other error
	// means tokens can't be checked at all (500).
	Verify func(token string) (videotoken.Claims, error)
	// Plane is the video plane for a verified token.
	Plane func(c videotoken.Claims) http.Handler
	// AllowSession limits new WebRTC sessions per subject; nil = no limit.
	AllowSession func(subject string) bool
	// Internal lets InternalCamera tokens reach the /rs/ paths (recording
	// servers; the management server's gateway never serves them).
	Internal bool
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, Prefix), "/")
	rest = "/" + rest
	claims, err := g.Verify(token)
	if err != nil {
		switch {
		case errors.Is(err, videotoken.ErrExpired):
			WriteError(w, http.StatusUnauthorized, "unauthorized", "the video token expired — ask for a new one")
		case errors.Is(err, videotoken.ErrMalformed), errors.Is(err, videotoken.ErrBadSignature), errors.Is(err, videotoken.ErrWrongAudience):
			WriteError(w, http.StatusUnauthorized, "unauthorized", "the video token is invalid — ask for a new one")
		default:
			WriteError(w, http.StatusInternalServerError, "internal", "video tokens are unavailable")
		}
		return
	}
	rt, ok := Match(r.Method, rest)
	if !ok && g.Internal {
		rt, ok = MatchInternal(r.Method, rest)
	}
	if !ok {
		WriteError(w, http.StatusNotFound, "not_found", "no such video path")
		return
	}
	if rt.Camera != claims.Camera {
		WriteError(w, http.StatusForbidden, "forbidden", "this video token is for another camera")
		return
	}
	if !rt.Allows(claims) {
		WriteError(w, http.StatusForbidden, "forbidden", "this video token doesn't allow "+string(rt.Rights[0]))
		return
	}
	if !g.whepChecks(w, r, rt, claims) {
		return
	}
	Forward(w, r, g.Plane(claims), rest, Prefix+token, rt)
}

// whepChecks applies the WHEP Content-Type rules and, for a new session,
// the per-user rate limit.
func (g *Gateway) whepChecks(w http.ResponseWriter, r *http.Request, rt Route, c videotoken.Claims) bool {
	switch {
	case rt.WHEP == "offer" && !MediaTypeIs(r, "application/sdp"):
		WriteError(w, http.StatusUnsupportedMediaType, "bad_request", "send an SDP offer (application/sdp)")
		return false
	case rt.WHEP == "session" && r.Method == http.MethodPatch && !MediaTypeIs(r, "application/trickle-ice-sdpfrag"):
		WriteError(w, http.StatusUnsupportedMediaType, "bad_request", "send application/trickle-ice-sdpfrag")
		return false
	case rt.WHEP == "offer" && g.AllowSession != nil && !g.AllowSession(c.Subject):
		w.Header().Set("Retry-After", "30")
		WriteError(w, http.StatusTooManyRequests, "too_many_requests", "too many live sessions started — wait a moment")
		return false
	}
	return true
}

// MediaTypeIs reports whether the request's Content-Type is want.
func MediaTypeIs(r *http.Request, want string) bool {
	ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(ct), want)
}

// WriteError answers in API v2's error shape.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// Forward hands the request to plane at path rest (query kept: MediaMTX's
// ?session=, playback windows and piece ranges) and puts prefix back on
// absolute URLs in the answer.
func Forward(w http.ResponseWriter, r *http.Request, plane http.Handler, rest, prefix string, rt Route) {
	r2 := r.Clone(r.Context())
	r2.URL = &url.URL{Path: rest, RawQuery: r.URL.RawQuery}
	r2.RequestURI = r2.URL.RequestURI()
	r2.Header.Del("Cookie")
	r2.Header.Del("Authorization")
	if rt.Segment {
		// MediaMTX answers chunked; AVPlayer (Sentinel Operator on macOS,
		// the iPhone) stalls live playback when segments arrive without a
		// length, so send it whole, as the phone API does.
		cw := NewCapture(MaxHLSSegment)
		plane.ServeHTTP(cw, r2)
		if cw.Over {
			WriteError(w, http.StatusBadGateway, "unavailable", "segment too large")
			return
		}
		for k, v := range cw.H {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Length", strconv.Itoa(cw.Buf.Len()))
		w.WriteHeader(cw.Status())
		_, _ = w.Write(cw.Buf.Bytes())
		return
	}
	if rt.Playlist == 0 {
		plane.ServeHTTP(&LocationWriter{ResponseWriter: w, Fix: func(loc string) string { return AddPrefix(loc, prefix) }}, r2)
		return
	}
	// Playlists are rewritten, so ask for them uncompressed and compress
	// the result here.
	r2.Header.Del("Accept-Encoding")
	cw := NewCapture(rt.Playlist)
	plane.ServeHTTP(cw, r2)
	if cw.Over {
		WriteError(w, http.StatusBadGateway, "unavailable", "playlist too large")
		return
	}
	code := cw.Status()
	body := cw.Buf.Bytes()
	for k, v := range cw.H {
		if k != "Content-Length" && k != "Content-Encoding" {
			w.Header()[k] = v
		}
	}
	if code == http.StatusOK {
		body = PrefixPlaylist(body, prefix)
	}
	if len(body) > 1400 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.WriteHeader(code)
		gz := gzip.NewWriter(w)
		_, _ = gz.Write(body)
		_ = gz.Close()
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

// MARK: - Rewriting

// AddPrefix puts prefix in front of an absolute-path URL; relative URLs
// and full URLs are left alone.
func AddPrefix(ref, prefix string) string {
	if strings.HasPrefix(ref, "/") && !strings.HasPrefix(ref, "//") {
		return prefix + ref
	}
	return ref
}

// StripPrefix undoes AddPrefix for one gateway prefix (/v/<token>).
func StripPrefix(ref, prefix string) string {
	if rest, ok := strings.CutPrefix(ref, prefix); ok && strings.HasPrefix(rest, "/") {
		return rest
	}
	return ref
}

// PrefixPlaylist puts prefix in front of every absolute-path URI in an HLS
// playlist (segment lines and URI="…" attributes such as EXT-X-MAP).
// Relative URIs already resolve under the prefix; full URLs are left alone.
func PrefixPlaylist(data []byte, prefix string) []byte {
	return RewritePlaylist(data, func(ref string) string { return AddPrefix(ref, prefix) })
}

// RewritePlaylist applies fix to every URI in an HLS playlist.
func RewritePlaylist(data []byte, fix func(string) string) []byte {
	var out bytes.Buffer
	out.Grow(len(data) + len(data)/8)
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
		case strings.HasPrefix(trimmed, "#"):
			line = uriAttrPattern.ReplaceAllStringFunc(line, func(m string) string {
				return `URI="` + fix(uriAttrPattern.FindStringSubmatch(m)[1]) + `"`
			})
		default:
			line = fix(trimmed)
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// LocationWriter rewrites the Location header (the WHEP session URL)
// before the response starts.
type LocationWriter struct {
	http.ResponseWriter
	Fix   func(string) string
	wrote bool
}

func (l *LocationWriter) WriteHeader(code int) {
	if !l.wrote {
		l.wrote = true
		h := l.Header()
		if loc := h.Get("Location"); loc != "" {
			h.Set("Location", l.Fix(loc))
		}
	}
	l.ResponseWriter.WriteHeader(code)
}

func (l *LocationWriter) Write(b []byte) (int, error) {
	if !l.wrote {
		l.WriteHeader(http.StatusOK)
	}
	return l.ResponseWriter.Write(b)
}

func (l *LocationWriter) Unwrap() http.ResponseWriter { return l.ResponseWriter }

// Capture is a ResponseWriter that buffers up to a limit.
type Capture struct {
	H    http.Header
	Code int
	Buf  bytes.Buffer
	Max  int
	Over bool // the body was longer than Max
}

func NewCapture(max int) *Capture { return &Capture{H: http.Header{}, Max: max} }

func (c *Capture) Header() http.Header { return c.H }

func (c *Capture) WriteHeader(code int) {
	if c.Code == 0 {
		c.Code = code
	}
}

func (c *Capture) Write(b []byte) (int, error) {
	if c.Code == 0 {
		c.Code = http.StatusOK
	}
	if room := c.Max - c.Buf.Len(); room < len(b) {
		if room > 0 {
			c.Buf.Write(b[:room])
		}
		c.Over = true
		return len(b), nil
	}
	return c.Buf.Write(b)
}

// Status is the response code (200 if none was written).
func (c *Capture) Status() int {
	if c.Code == 0 {
		return http.StatusOK
	}
	return c.Code
}

// MARK: - Session limit

// SessionLimiter allows n new sessions per subject per window.
type SessionLimiter struct {
	N      int
	Window time.Duration
	// Exempt subjects aren't limited here (limited upstream).
	Exempt map[string]bool

	mu     sync.Mutex
	recent map[string][]time.Time
}

// Allow records a new session for subject if it's within the limit.
func (l *SessionLimiter) Allow(subject string) bool {
	if l.Exempt[subject] {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recent == nil {
		l.recent = map[string][]time.Time{}
	}
	now := time.Now()
	kept := l.recent[subject][:0]
	for _, t := range l.recent[subject] {
		if now.Sub(t) < l.Window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.N {
		l.recent[subject] = kept
		return false
	}
	l.recent[subject] = append(kept, now)
	return true
}

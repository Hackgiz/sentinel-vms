package remote

// The management server's view of a remote recording server's video plane:
// a reverse proxy to {videoUrl}/v/{token}/{path} with a token it mints for
// that one request. The recording server's gateway puts its own /v/<token>
// prefix on absolute URLs (playlists, the WHEP Location); the proxy takes
// it off again, so callers see exactly the paths an in-process video plane
// returns and the management gateway can put the client's prefix on.

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"sentinel-linux/internal/videogw"
	"sentinel-linux/internal/videotoken"
)

// proxyTokenTTL is how long a minted token lives: long enough for a
// request to start; streams already started don't need it again.
const proxyTokenTTL = 2 * time.Minute

var videoTransport = &http.Transport{
	Proxy:                 nil, // the recording server is on the site's network
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	MaxIdleConnsPerHost:   32,
	IdleConnTimeout:       90 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second,
}

// Video is the recording server's video plane, through its /v/ gateway.
func (c *Conn) Video() http.Handler { return http.HandlerFunc(c.serveVideo) }

func (c *Conn) serveVideo(w http.ResponseWriter, r *http.Request) {
	base, err := url.Parse(c.hello.VideoURL)
	if c.hello.VideoURL == "" || err != nil {
		videogw.WriteError(w, http.StatusServiceUnavailable, "unavailable", "this recording server has no video address")
		return
	}
	rt, ok := videogw.Match(r.Method, r.URL.Path)
	if !ok {
		rt, ok = videogw.MatchInternal(r.Method, r.URL.Path)
	}
	if !ok {
		videogw.WriteError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	rights := []videotoken.Right{videotoken.Live, videotoken.Playback}
	if rt.Internal {
		rights = []videotoken.Right{videotoken.Export}
	}
	if rt.Push { // WAVE23: the management gateway already checked a push token
		rights = []videotoken.Right{videotoken.Push}
	}
	tok, err := c.l.Server.MintRecorderVideoToken(r, c.id, rt.Camera, rights, proxyTokenTTL)
	if err != nil {
		videogw.WriteError(w, http.StatusInternalServerError, "internal", "video tokens are unavailable")
		return
	}
	prefix := videogw.Prefix + tok
	strip := func(ref string) string { return videogw.StripPrefix(ref, prefix) }
	proxy := &httputil.ReverseProxy{
		Transport: videoTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = &url.URL{Scheme: base.Scheme, Host: base.Host, Path: base.Path + prefix + r.URL.Path, RawQuery: r.URL.RawQuery}
			pr.Out.Host = base.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			if rt.Playlist > 0 {
				// Rewritten below: let the transport decompress it.
				pr.Out.Header.Del("Accept-Encoding")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", strip(loc))
			}
			if rt.Playlist == 0 || resp.StatusCode != http.StatusOK {
				return nil
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, int64(rt.Playlist)+1))
			resp.Body.Close()
			if err != nil {
				return err
			}
			if len(body) > rt.Playlist {
				return errPlaylistTooLarge
			}
			body = videogw.RewritePlaylist(body, strip)
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			resp.Header.Del("Content-Encoding")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			videogw.WriteError(w, http.StatusBadGateway, "unavailable", "the recording server's video is unreachable")
		},
	}
	proxy.ServeHTTP(w, r)
}

type proxyError string

func (e proxyError) Error() string { return string(e) }

const errPlaylistTooLarge = proxyError("playlist too large")

// MARK: - Recording server

// Recording servers limit new WebRTC sessions per user like the
// management server does (webrtc.go there); sessions the management server
// started for its gateway were limited there already.
const (
	whepRateWindow    = time.Minute
	whepRatePerWindow = 60
)

// VideoPlane is a recording server's /v/{token}/… gateway over its video
// plane, verifying tokens with tokens() (recorder.VideoTokens: the keys
// from the last snapshot, audience = this server). Mount it at
// videogw.Prefix on the --listen port.
func VideoPlane(plane http.Handler, tokens func() *videotoken.Verifier) http.Handler {
	limiter := &videogw.SessionLimiter{N: whepRatePerWindow, Window: whepRateWindow, Exempt: map[string]bool{videogw.ManagementSubject: true}}
	return &videogw.Gateway{
		Verify:       func(token string) (videotoken.Claims, error) { return tokens().Verify(token) },
		Plane:        func(videotoken.Claims) http.Handler { return plane },
		AllowSession: limiter.Allow,
		Internal:     true,
	}
}

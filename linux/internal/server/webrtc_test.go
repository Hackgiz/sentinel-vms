package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/store"
)

const (
	testSession = "0f3c9a52-7d4e-4b1a-9c2e-5a6b7c8d9e0f"
	testAnswer  = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\nc=IN IP4 0.0.0.0\r\na=mid:0\r\na=candidate:1 1 udp 2130706431 172.17.0.2 18189 typ host\r\na=end-of-candidates\r\n"
)

type upstreamHit struct {
	Method, Path, ContentType, IfMatch, Cookie, Body string
}

type whepFixture struct {
	s      *Server
	h      http.Handler
	cookie string
	camID  string
	mu     sync.Mutex
	hits   []upstreamHit
	status int // what the fake MediaMTX answers POSTs with
}

func newWHEPFixture(t *testing.T, role string) *whepFixture {
	t.Helper()
	f := &whepFixture{status: http.StatusCreated}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.hits = append(f.hits, upstreamHit{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("If-Match"), r.Header.Get("Cookie"), string(b)})
		status := f.status
		f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && status == http.StatusCreated:
			w.Header().Set("Content-Type", "application/sdp")
			w.Header().Set("ETag", `"abc"`)
			w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+testSession)
			w.Header().Set("Link", `<stun:stun.example.com>; rel="ice-server"`)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, testAnswer)
		case r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"status":"error","error":"the stream doesn't contain any supported codec, which are currently AV1, VP9, VP8, H264"}`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(upstream.Close)
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	port, _ := strconv.Atoi(portStr)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct horse battery")
	u, err := st.CreateUser(ctx, "op", role, hash)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := st.CreateSession(ctx, u.ID, sessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	cam, err := st.SaveCamera(ctx, "", store.CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.9/main", SubRTSPURL: "rtsp://10.0.0.9/sub"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sup := &media.Supervisor{
		ConfigPath: filepath.Join(dir, "mediamtx.yml"), RecordingsRoot: filepath.Join(dir, "rec"),
		WebRTC: media.WebRTCConfig{ICEPort: 18189, SignalPort: port},
	}
	s, _ := newSite(t, st, sup)
	if err := s.ApplyMedia(ctx); err != nil { // sub-stream → LivePath "<id>-sub"
		t.Fatal(err)
	}
	f.s, f.h, f.cookie, f.camID = s, s.Handler(), tok, cam.ID
	return f
}

func (f *whepFixture) req(method, path, ctype, body string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "192.168.1.50:8090"
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	r.Header.Set("Origin", "http://192.168.1.50:8090")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if f.cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.cookie})
	}
	for _, m := range mod {
		m(r)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *whepFixture) upstreamHits() []upstreamHit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upstreamHit(nil), f.hits...)
}

func TestWHEPOfferProxiesAndRewritesLocation(t *testing.T) {
	f := newWHEPFixture(t, "Viewer")
	w := f.req("POST", "/webrtc/"+f.camID+"/whep", "application/sdp", "v=0\r\noffer\r\n")
	if w.Code != http.StatusCreated {
		t.Fatalf("offer: %d %s", w.Code, w.Body)
	}
	if got, want := w.Header().Get("Location"), "/webrtc/"+f.camID+"/whep/"+testSession; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if w.Header().Get("ETag") != `"abc"` || w.Header().Get("Content-Type") != "application/sdp" {
		t.Errorf("headers %v", w.Header())
	}
	if w.Header().Get("Link") != "" {
		t.Error("upstream Link (ICE servers) must not be passed through")
	}
	body := w.Body.String()
	if !strings.Contains(body, "a=candidate:sentinel1 1 udp 2113937151 192.168.1.50 18189 typ host\r\n") ||
		!strings.Contains(body, "192.168.1.50 18189 typ host tcptype passive") ||
		!strings.Contains(body, "172.17.0.2 18189 typ host") {
		t.Errorf("answer missing request-host candidates:\n%s", body)
	}
	hits := f.upstreamHits()
	if len(hits) != 1 {
		t.Fatalf("upstream hits %v", hits)
	}
	h := hits[0]
	if h.Path != "/"+f.camID+"-sub/whep" || h.ContentType != "application/sdp" || h.Body != "v=0\r\noffer\r\n" {
		t.Errorf("upstream got %+v (want the sub-stream path)", h)
	}
	if h.Cookie != "" {
		t.Error("session cookie leaked to MediaMTX")
	}
}

func TestWHEPRequiresSignIn(t *testing.T) {
	f := newWHEPFixture(t, "Viewer")
	f.cookie = ""
	if w := f.req("POST", "/webrtc/"+f.camID+"/whep", "application/sdp", "v=0"); w.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: %d", w.Code)
	}
	f.cookie = "not-a-session"
	if w := f.req("DELETE", "/webrtc/"+f.camID+"/whep/"+testSession, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad cookie: %d", w.Code)
	}
	if len(f.upstreamHits()) != 0 {
		t.Fatal("unauthenticated request reached MediaMTX")
	}
}

func TestWHEPBadCameraAndSession(t *testing.T) {
	f := newWHEPFixture(t, "Viewer")
	for _, path := range []string{
		"/webrtc/00000000-0000-0000-0000-000000000000/whep", // valid form, no such camera
		"/webrtc/not-a-camera/whep",
		"/webrtc/" + strings.ToLower(f.camID) + "/whep",
	} {
		if w := f.req("POST", path, "application/sdp", "v=0"); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	for _, sess := range []string{"..%2F..%2Fv3%2Fpaths%2Flist", "abc", "whip"} {
		if w := f.req("DELETE", "/webrtc/"+f.camID+"/whep/"+sess, "", ""); w.Code != http.StatusNotFound {
			t.Errorf("session %q: %d", sess, w.Code)
		}
	}
	if len(f.upstreamHits()) != 0 {
		t.Fatalf("bad requests reached MediaMTX: %v", f.upstreamHits())
	}
}

func TestWHEPOriginAndContentType(t *testing.T) {
	f := newWHEPFixture(t, "Viewer")
	path := "/webrtc/" + f.camID + "/whep"
	if w := f.req("POST", path, "application/sdp", "v=0", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin: %d", w.Code)
	}
	if w := f.req("POST", path, "application/sdp", "v=0", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }); w.Code != http.StatusForbidden {
		t.Errorf("cross-site fetch: %d", w.Code)
	}
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/json"} {
		if w := f.req("POST", path, ct, "v=0"); w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("content-type %q: %d", ct, w.Code)
		}
	}
	if w := f.req("PATCH", path+"/"+testSession, "text/plain", "a=x"); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("patch text/plain: %d", w.Code)
	}
	if w := f.req("PATCH", path+"/"+testSession, "application/trickle-ice-sdpfrag", "a=x", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin patch: %d", w.Code)
	}
	if len(f.upstreamHits()) != 0 {
		t.Fatalf("refused requests reached MediaMTX: %v", f.upstreamHits())
	}
}

func TestWHEPMethodAllowlist(t *testing.T) {
	f := newWHEPFixture(t, "Viewer")
	id := f.camID
	for _, c := range []struct{ method, path string }{
		{"GET", "/webrtc/" + id + "/whep"},
		{"OPTIONS", "/webrtc/" + id + "/whep"},
		{"PUT", "/webrtc/" + id + "/whep"},
		{"POST", "/webrtc/" + id + "/whip"}, // publishing is never proxied
		{"GET", "/webrtc/" + id + "/"},      // MediaMTX's player page
		{"POST", "/webrtc/" + id + "/whep/" + testSession},
		{"PATCH", "/webrtc/" + id + "/whep"},
		{"GET", "/webrtc/v3/paths/list"},
	} {
		if w := f.req(c.method, c.path, "application/sdp", "v=0"); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
	if len(f.upstreamHits()) != 0 {
		t.Fatalf("reached MediaMTX: %v", f.upstreamHits())
	}
}

func TestWHEPPatchAndDelete(t *testing.T) {
	f := newWHEPFixture(t, "Viewer")
	base := "/webrtc/" + f.camID + "/whep/" + testSession
	if w := f.req("PATCH", base, "application/trickle-ice-sdpfrag", "a=ice-ufrag:x\r\n", func(r *http.Request) { r.Header.Set("If-Match", "*") }); w.Code != http.StatusNoContent {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	if w := f.req("DELETE", base, "", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	hits := f.upstreamHits()
	want := "/" + f.camID + "-sub/whep/" + testSession
	if len(hits) != 2 || hits[0].Method != "PATCH" || hits[0].Path != want || hits[0].ContentType != "application/trickle-ice-sdpfrag" ||
		hits[0].IfMatch != "*" || hits[0].Body != "a=ice-ufrag:x\r\n" || hits[1].Method != "DELETE" || hits[1].Path != want {
		t.Fatalf("upstream hits %+v", hits)
	}
}

func TestWHEPUpstreamErrorPassesThrough(t *testing.T) {
	f := newWHEPFixture(t, "Viewer")
	f.status = http.StatusBadRequest // e.g. H.265 to a browser without HEVC
	w := f.req("POST", "/webrtc/"+f.camID+"/whep", "application/sdp", "v=0")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "supported codec") || w.Header().Get("Location") != "" {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
}

func TestWHEPRateLimit(t *testing.T) {
	f := newWHEPFixture(t, "Viewer")
	path := "/webrtc/" + f.camID + "/whep"
	for i := 0; i < whepRatePerWindow; i++ {
		if w := f.req("POST", path, "application/sdp", "v=0"); w.Code != http.StatusCreated {
			t.Fatalf("offer %d: %d", i, w.Code)
		}
	}
	w := f.req("POST", path, "application/sdp", "v=0")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit: %d", w.Code)
	}
}

func TestWHEPDisabled(t *testing.T) {
	f := newWHEPFixture(t, "Viewer")
	recOf(f.s).Media.WebRTC.ICEPort = 0
	if w := f.req("POST", "/webrtc/"+f.camID+"/whep", "application/sdp", "v=0"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled: %d", w.Code)
	}
}

func TestWebRTCSettings(t *testing.T) {
	f := newWHEPFixture(t, "Admin")
	put := func(body string) *httptest.ResponseRecorder {
		return f.req("PUT", "/api/v1/webrtc", "application/json", body)
	}
	if w := put(`{"additionalHosts":["192.168.1.60"," nvr.lan ",""]}`); w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if w := put(`{"additionalHosts":["x'\ny"]}`); w.Code != 400 {
		t.Fatalf("bad host accepted: %d", w.Code)
	}
	w := f.req("GET", "/api/v1/webrtc", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"additionalHosts":["192.168.1.60","nvr.lan"]`) || !strings.Contains(w.Body.String(), `"port":18189`) {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	b, _ := os.ReadFile(recOf(f.s).Media.ConfigPath)
	cfg := string(b)
	if !strings.Contains(cfg, "webrtcAdditionalHosts: ['192.168.1.60', 'nvr.lan']") {
		t.Fatalf("hosts not applied to mediamtx config:\n%s", cfg)
	}
}

func TestWebRTCSettingsNeedsChangeSettings(t *testing.T) {
	f := newWHEPFixture(t, "Viewer")
	if w := f.req("PUT", "/api/v1/webrtc", "application/json", `{"additionalHosts":[]}`); w.Code != http.StatusForbidden {
		t.Fatalf("viewer changed settings: %d", w.Code)
	}
}

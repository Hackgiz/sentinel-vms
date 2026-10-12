package server

import (
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/media"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/videotoken"
)

// videoToken asks for a token over v2; returns the status and response.
func videoToken(t *testing.T, s *Server, bearer, camID, body string) (int, map[string]any) {
	t.Helper()
	return v2Call(t, s.Handler(), "POST", "/api/v2/cameras/"+camID+"/video-token", bearer, body)
}

// mustVideoToken asks for a token with rights and returns its baseUrl.
func mustVideoToken(t *testing.T, s *Server, bearer, camID string, rights ...string) string {
	t.Helper()
	body := ""
	if len(rights) > 0 {
		b, _ := json.Marshal(map[string]any{"rights": rights})
		body = string(b)
	}
	code, m := videoToken(t, s, bearer, camID, body)
	if code != 200 {
		t.Fatalf("video token %v: %d %v", rights, code, m)
	}
	return m["baseUrl"].(string)
}

// testSigner signs with the server's own key, on a clock of our choosing.
func testSigner(t *testing.T, s *Server, now time.Time) *videotoken.Signer {
	t.Helper()
	key, err := s.Store.VideoTokenKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sg := videotoken.NewSigner(key)
	sg.Now = func() time.Time { return now }
	return sg
}

func gatewayGet(h http.Handler, path string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	for _, m := range mod {
		m(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestVideoTokenIssue(t *testing.T) {
	s := newCompanionServer(t)
	c := addCam(t, s)
	tok := v2SignIn(t, s, "vera", "Viewer")
	start := time.Now()
	code, m := videoToken(t, s, tok["accessToken"].(string), c.ID, "")
	if code != 200 {
		t.Fatalf("issue: %d %v", code, m)
	}
	token := m["token"].(string)
	if m["baseUrl"] != "/v/"+token || m["cameraId"] != c.ID || m["recorderId"] != "local" {
		t.Errorf("response %v", m)
	}
	if r, _ := m["rights"].([]any); len(r) != 1 || r[0] != "live" {
		t.Errorf("default rights %v", m["rights"])
	}
	exp := time.UnixMilli(int64(m["expiresAt"].(float64)))
	if d := exp.Sub(start); d < 4*time.Minute || d > 6*time.Minute {
		t.Errorf("expires in %v", d)
	}
	signer, _ := s.videoSigner(context.Background())
	claims, err := videotoken.NewVerifier("local", signer.PublicKey()).Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := s.Store.UserByName(context.Background(), "vera")
	if claims.Camera != c.ID || claims.Subject != u.ID || claims.Audience != "local" || !claims.Has(videotoken.Live) || claims.Has(videotoken.Playback) {
		t.Errorf("claims %+v", claims)
	}
	// The dashboard's cookie works too.
	cookie := webAs(t, s, "web", "Viewer")
	if rec := webDo(t, s.Handler(), cookie, "POST", "/api/v2/cameras/"+c.ID+"/video-token", `{"rights":["playback"]}`); rec.Code != 200 {
		t.Errorf("cookie: %d %s", rec.Code, rec.Body)
	}
}

func TestVideoTokenRightsAndErrors(t *testing.T) {
	s := newCompanionServer(t)
	c := addCam(t, s)
	viewer := v2SignIn(t, s, "vera", "Viewer")["accessToken"].(string)
	operator := v2SignIn(t, s, "olga", "Operator")["accessToken"].(string)
	for _, tc := range []struct {
		bearer, cam, body string
		code              int
		errCode           string
	}{
		{viewer, c.ID, `{"rights":["live"]}`, 200, ""},
		{viewer, c.ID, `{"rights":["playback","live","playback"]}`, 200, ""},
		{viewer, c.ID, `{"rights":["export"]}`, 403, codeForbidden},
		{operator, c.ID, `{"rights":["export","playback"]}`, 200, ""},
		{viewer, c.ID, `{"rights":[]}`, 400, codeBadRequest},
		{viewer, c.ID, `{"rights":["admin"]}`, 400, codeBadRequest},
		{viewer, c.ID, `{"rights":["live"],"ttl":3600}`, 400, codeBadRequest},
		{viewer, "00000000-0000-0000-0000-000000000000", "", 404, codeNotFound},
		{viewer, "not-a-camera", "", 404, codeNotFound},
		{"", c.ID, "", 401, codeUnauthorized},
		{"sat_bogus", c.ID, "", 401, codeUnauthorized},
	} {
		code, m := videoToken(t, s, tc.bearer, tc.cam, tc.body)
		if code != tc.code || (tc.errCode != "" && errCode(m) != tc.errCode) {
			t.Errorf("%s %q: %d %v, want %d %s", tc.cam, tc.body, code, m, tc.code, tc.errCode)
		}
		if code == 200 && tc.body == `{"rights":["playback","live","playback"]}` {
			if r := m["rights"].([]any); len(r) != 2 || r[0] != "playback" || r[1] != "live" {
				t.Errorf("rights not deduplicated: %v", r)
			}
		}
	}
}

func TestVideoTokenAuditsFootageOnly(t *testing.T) {
	s := newCompanionServer(t)
	c := addCam(t, s)
	op := v2SignIn(t, s, "olga", "Operator")["accessToken"].(string)
	count := func() int {
		entries, _ := s.Store.AuditLog(context.Background(), 1000)
		n := 0
		for _, e := range entries {
			if e.Action == "Issued video token" {
				n++
			}
		}
		return n
	}
	mustVideoToken(t, s, op, c.ID)
	mustVideoToken(t, s, op, c.ID, "live")
	if n := count(); n != 0 {
		t.Errorf("live grants audited: %d", n)
	}
	mustVideoToken(t, s, op, c.ID, "playback")
	mustVideoToken(t, s, op, c.ID, "live", "export")
	if n := count(); n != 2 {
		t.Errorf("footage grants audited %d times, want 2", n)
	}
}

func TestVideoTokenNeedsARecordingServer(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, nil, "test") // --role management, nothing connected
	c := addCam(t, s)
	tok := v2SignIn(t, s, "vera", "Viewer")["accessToken"].(string)
	if code, m := videoToken(t, s, tok, c.ID, ""); code != 503 || errCode(m) != codeUnavailable {
		t.Errorf("issue: %d %v", code, m)
	}
	if w := gatewayGet(s.Handler(), "/v/v1.x.y/live/"+c.ID+"/index.m3u8"); w.Code != 503 {
		t.Errorf("gateway: %d", w.Code)
	}
}

// MARK: - Gateway

type gatewayFixture struct {
	s     *Server
	h     http.Handler
	camID string
	user  *store.User
	token string // the user's API access token
	mu    sync.Mutex
	hits  []upstreamHit
}

// newGatewayFixture: a camera and a fake MediaMTX answering HLS (playlist
// with a relative and an absolute segment URI, plus a segment) and WHEP.
func newGatewayFixture(t *testing.T) *gatewayFixture {
	t.Helper()
	f := &gatewayFixture{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.hits = append(f.hits, upstreamHit{r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), r.Header.Get("If-Match"), r.Header.Get("Cookie"), string(b)})
		f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, ".m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4?session=abc\"\n#EXTINF:1.0,\nseg1.mp4?session=abc\n#EXTINF:1.0,\n/odd/absolute.mp4\n")
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "video/mp4")
			w.(http.Flusher).Flush() // chunked, like MediaMTX
			_, _ = io.WriteString(w, "segment-bytes")
		case r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/sdp")
			w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/"+testSession)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, testAnswer)
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
	dir := t.TempDir()
	sup := &media.Supervisor{
		ConfigPath: filepath.Join(dir, "mediamtx.yml"), RecordingsRoot: filepath.Join(dir, "rec"),
		Ports: media.Ports{RTSP: 18554, HLS: port, API: 19997}, WebRTC: media.WebRTCConfig{ICEPort: 18189, SignalPort: port},
	}
	s, _ := newSite(t, st, sup)
	c := addCam(t, s)
	syncCams(t, s)
	f.s, f.h, f.camID = s, s.Handler(), c.ID
	f.token = v2SignIn(t, s, "vera", "Viewer")["accessToken"].(string)
	f.user, _ = st.UserByName(context.Background(), "vera")
	return f
}

func (f *gatewayFixture) upstreamHits() []upstreamHit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upstreamHit(nil), f.hits...)
}

func TestGatewayLiveHLS(t *testing.T) {
	f := newGatewayFixture(t)
	base := mustVideoToken(t, f.s, f.token, f.camID)
	w := gatewayGet(f.h, base+"/live/"+f.camID+"/index.m3u8?session=abc", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "ambient"})
	})
	if w.Code != 200 {
		t.Fatalf("playlist: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	// Relative URIs are untouched (they resolve under the prefix); absolute
	// ones get it.
	if !strings.Contains(body, "\nseg1.mp4?session=abc\n") || !strings.Contains(body, `URI="init.mp4?session=abc"`) ||
		!strings.Contains(body, "\n"+base+"/odd/absolute.mp4\n") {
		t.Errorf("playlist:\n%s", body)
	}
	if w.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Errorf("content type %q", w.Header().Get("Content-Type"))
	}
	seg := gatewayGet(f.h, base+"/live/"+f.camID+"/seg1.mp4?session=abc")
	if seg.Code != 200 || seg.Body.String() != "segment-bytes" || seg.Header().Get("Content-Length") != "13" {
		t.Errorf("segment: %d %q length %q", seg.Code, seg.Body, seg.Header().Get("Content-Length"))
	}
	hits := f.upstreamHits()
	if len(hits) != 2 || hits[0].Path != "/"+f.camID+"/index.m3u8?session=abc" || hits[1].Path != "/"+f.camID+"/seg1.mp4?session=abc" {
		t.Fatalf("upstream saw %+v", hits)
	}
	if hits[0].Cookie != "" {
		t.Error("cookie forwarded")
	}
}

func TestGatewayRefusals(t *testing.T) {
	f := newGatewayFixture(t)
	other := addCam(t, f.s)
	syncCams(t, f.s)
	live := strings.TrimPrefix(mustVideoToken(t, f.s, f.token, f.camID), "/v/")
	now := time.Now()
	signer := testSigner(t, f.s, now)
	sign := func(c videotoken.Claims, at time.Time, ttl time.Duration) string {
		signer.Now = func() time.Time { return at }
		tok, _, err := signer.Sign(c, ttl)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	claims := videotoken.Claims{Camera: f.camID, Subject: f.user.ID, Rights: []videotoken.Right{videotoken.Live}, Audience: "local"}
	expired := sign(claims, now.Add(-10*time.Minute), 5*time.Minute)
	wrongAud := claims
	wrongAud.Audience = "rs-2"
	parts := strings.Split(live, ".")
	tampered := parts[0] + "." + parts[1][:len(parts[1])-2] + "AA." + parts[2]
	_, foreignKey, _ := ed25519.GenerateKey(rand.Reader)
	foreign, _, _ := videotoken.NewSigner(foreignKey).Sign(claims, time.Minute)

	livePath := "/live/" + f.camID + "/index.m3u8"
	for name, tc := range map[string]struct {
		path    string
		code    int
		errCode string
	}{
		"expired":          {"/v/" + expired + livePath, 401, codeUnauthorized},
		"tampered":         {"/v/" + tampered + livePath, 401, codeUnauthorized},
		"foreign key":      {"/v/" + foreign + livePath, 401, codeUnauthorized},
		"wrong audience":   {"/v/" + sign(wrongAud, now, time.Minute) + livePath, 401, codeUnauthorized},
		"garbage":          {"/v/hello" + livePath, 401, codeUnauthorized},
		"no token":         {"/v/", 401, codeUnauthorized},
		"other camera":     {"/v/" + live + "/live/" + other.ID + "/index.m3u8", 403, codeForbidden},
		"live → recording": {"/v/" + live + "/recordings/" + f.camID + "/20261009-090000-000000.mp4", 403, codeForbidden},
		"live → playback":  {"/v/" + live + "/api/v1/cameras/" + f.camID + "/playback.m3u8?from=1&to=2", 403, codeForbidden},
		"internal path":    {"/v/" + live + "/rs/exports/0123456789abcdef0123456789abcdef", 404, codeNotFound},
		"api path":         {"/v/" + live + "/api/v1/cameras", 404, codeNotFound},
		"bad file":         {"/v/" + live + "/live/" + f.camID + "/x.exe", 404, codeNotFound},
		"bad camera id":    {"/v/" + live + "/live/nope/index.m3u8", 404, codeNotFound},
		"bare token":       {"/v/" + live, 404, codeNotFound},
	} {
		w := gatewayGet(f.h, tc.path)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		if w.Code != tc.code || errCode(m) != tc.errCode {
			t.Errorf("%s: %d %s, want %d %s", name, w.Code, w.Body, tc.code, tc.errCode)
		}
	}
	// Dot segments never reach the video plane (the mux redirects to the
	// cleaned path, which then fails the camera/right checks).
	for _, p := range []string{"/live/" + f.camID + "/..%2F..%2Fetc%2Fpasswd", "/live/" + f.camID + "/../" + other.ID + "/index.m3u8"} {
		if w := gatewayGet(f.h, "/v/"+live+p); w.Code == 200 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if hits := f.upstreamHits(); len(hits) != 0 {
		t.Errorf("refused requests reached MediaMTX: %+v", hits)
	}
	// A playback token doesn't open live video either.
	pb := mustVideoToken(t, f.s, f.token, f.camID, "playback")
	if w := gatewayGet(f.h, pb+livePath); w.Code != 403 {
		t.Errorf("playback → live: %d", w.Code)
	}
	// Methods other than the video plane's are refused.
	r := httptest.NewRequest("DELETE", "/v/"+live+livePath, nil)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Errorf("DELETE playlist: %d", w.Code)
	}
}

func TestGatewayWHEP(t *testing.T) {
	f := newGatewayFixture(t)
	base := mustVideoToken(t, f.s, f.token, f.camID)
	send := func(method, path, ctype, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "192.168.1.50:8090"
		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}
		// A cross-site page: fine here, the token is the credential.
		r.Header.Set("Origin", "https://elsewhere.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		return w
	}
	w := send("POST", base+"/webrtc/"+f.camID+"/whep", "application/sdp", "v=0\r\noffer\r\n")
	if w.Code != http.StatusCreated {
		t.Fatalf("offer: %d %s", w.Code, w.Body)
	}
	loc := w.Header().Get("Location")
	if want := base + "/webrtc/" + f.camID + "/whep/" + testSession; loc != want {
		t.Fatalf("Location = %q, want %q", loc, want)
	}
	if w.Header().Get("Content-Type") != "application/sdp" || !strings.Contains(w.Body.String(), "a=candidate:") {
		t.Errorf("answer %v %q", w.Header(), w.Body)
	}
	// The Location works for trickle ICE and teardown.
	if w := send("PATCH", loc, "application/trickle-ice-sdpfrag", "a=candidate:1 1 udp 1 10.0.0.1 9 typ host\r\n"); w.Code != http.StatusNoContent {
		t.Errorf("patch: %d %s", w.Code, w.Body)
	}
	if w := send("DELETE", loc, "", ""); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	hits := f.upstreamHits()
	if len(hits) != 3 || hits[0].Method != "POST" || hits[1].Method != "PATCH" || hits[2].Method != "DELETE" ||
		hits[2].Path != "/"+f.camID+"/whep/"+testSession {
		t.Fatalf("upstream saw %+v", hits)
	}
	// Content types are still enforced.
	if w := send("POST", base+"/webrtc/"+f.camID+"/whep", "text/plain", "v=0"); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain offer: %d", w.Code)
	}
	if w := send("PATCH", loc, "application/json", "{}"); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("json patch: %d", w.Code)
	}
	if w := send("PATCH", base+"/webrtc/"+f.camID+"/whep/not-a-session", "application/trickle-ice-sdpfrag", "x"); w.Code != 404 {
		t.Errorf("bad session: %d", w.Code)
	}
	// The per-user rate limit is shared with the dashboard's route.
	for i := 0; i < whepRatePerWindow; i++ {
		f.s.allowSession(f.user.ID)
	}
	if w := send("POST", base+"/webrtc/"+f.camID+"/whep", "application/sdp", "v=0\r\n"); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Errorf("rate limit: %d", w.Code)
	}
}

func TestGatewayPlayback(t *testing.T) {
	s, c, t0 := playbackFixture(t)
	h := s.Handler()
	tok := v2SignIn(t, s, "vera", "Viewer")["accessToken"].(string)
	base := mustVideoToken(t, s, tok, c.ID, "playback")
	q := fmt.Sprintf("?from=%d&to=%d", t0.UnixMilli(), t0.Add(time.Hour).UnixMilli())

	// Plain, then gzipped: the same rewritten playlist.
	plain := gatewayGet(h, base+"/api/v1/cameras/"+c.ID+"/playback.m3u8"+q)
	if plain.Code != 200 || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("playlist: %d %v %s", plain.Code, plain.Header(), plain.Body)
	}
	zipped := gatewayGet(h, base+"/api/v1/cameras/"+c.ID+"/playback.m3u8"+q, func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip") })
	if zipped.Code != 200 || zipped.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzipped playlist: %d %v", zipped.Code, zipped.Header())
	}
	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	unzipped, _ := io.ReadAll(zr)
	m3u8 := plain.Body.String()
	if string(unzipped) != m3u8 {
		t.Error("gzipped playlist differs")
	}
	seg := base + "/recordings/" + c.ID + "/" + t0.Format("20060102-150405") + "-000000.mp4"
	if !strings.HasPrefix(m3u8, "#EXTM3U") || !strings.Contains(m3u8, `URI="`+seg+`"`) {
		t.Fatalf("init sections not rewritten:\n%s", m3u8)
	}
	var piece string
	for _, line := range strings.Split(m3u8, "\n") {
		if strings.HasPrefix(line, "/api/") || strings.HasPrefix(line, "/recordings/") {
			t.Fatalf("absolute URI left without the prefix: %s", line)
		}
		if piece == "" && strings.HasPrefix(line, base+"/api/v1/cameras/"+c.ID+"/pieces/") {
			piece = line
		}
	}
	if w := gatewayGet(h, piece); piece == "" || w.Code != 200 || w.Header().Get("Content-Type") != "video/mp4" || string(w.Body.Bytes()[4:8]) != "moof" {
		t.Fatalf("piece %q: %d", piece, w.Code)
	}
	w := gatewayGet(h, seg, func(r *http.Request) { r.Header.Set("Range", "bytes=0-99") })
	if w.Code != http.StatusPartialContent || w.Body.Len() != 100 {
		t.Fatalf("segment range: %d %d", w.Code, w.Body.Len())
	}
	// Errors from the recording server pass through untouched.
	empty := fmt.Sprintf("?from=%d&to=%d", t0.Add(-48*time.Hour).UnixMilli(), t0.Add(-47*time.Hour).UnixMilli())
	if w := gatewayGet(h, base+"/api/v1/cameras/"+c.ID+"/playback.m3u8"+empty); w.Code != 404 {
		t.Errorf("empty window: %d", w.Code)
	}
	// An export token reads footage too; a live one doesn't.
	op := v2SignIn(t, s, "olga", "Operator")["accessToken"].(string)
	if w := gatewayGet(h, mustVideoToken(t, s, op, c.ID, "export")+"/api/v1/cameras/"+c.ID+"/playback.m3u8"+q); w.Code != 200 {
		t.Errorf("export token: %d", w.Code)
	}
	if w := gatewayGet(h, mustVideoToken(t, s, tok, c.ID)+"/api/v1/cameras/"+c.ID+"/playback.m3u8"+q); w.Code != 403 {
		t.Errorf("live token: %d", w.Code)
	}
}

// The snapshot carries the management server's public key, and the
// recording server accepts what the management server issues.
func TestSnapshotCarriesVideoTokenKey(t *testing.T) {
	s := newCompanionServer(t)
	c := addCam(t, s)
	cams, err := s.Store.Cameras(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snap := s.snapshot(context.Background(), store.LocalRecorderID, cams)
	signer, _ := s.videoSigner(context.Background())
	if len(snap.VideoTokenKeys) != 1 || snap.VideoTokenKeys[0] != videotoken.EncodePublicKey(signer.PublicKey()) {
		t.Fatalf("snapshot keys %v", snap.VideoTokenKeys)
	}
	syncCams(t, s)
	tok := v2SignIn(t, s, "vera", "Viewer")["accessToken"].(string)
	_, m := videoToken(t, s, tok, c.ID, `{"rights":["playback"]}`)
	claims, err := recOf(s).VideoTokens().Verify(m["token"].(string))
	if err != nil || claims.Camera != c.ID || !claims.Has(videotoken.Playback) {
		t.Fatalf("recorder verify: %+v %v", claims, err)
	}
	// The key survives a restart (same store, fresh cache).
	videoSigners.Delete(s.Store)
	again, _ := s.videoSigner(context.Background())
	if !again.PublicKey().Equal(signer.PublicKey()) {
		t.Error("key changed after reload")
	}
}

func TestPrefixPlaylist(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-MAP:URI=\"/recordings/C/a.mp4\",BYTERANGE=\"10@0\"\n#EXTINF:2,\n/api/v1/cameras/C/pieces/a.mp4?first=0\n#EXTINF:2,\nrel.mp4\nhttps://cdn.example/x.mp4\n//host/y.mp4\n"
	want := "#EXTM3U\n#EXT-X-MAP:URI=\"/v/T/recordings/C/a.mp4\",BYTERANGE=\"10@0\"\n#EXTINF:2,\n/v/T/api/v1/cameras/C/pieces/a.mp4?first=0\n#EXTINF:2,\nrel.mp4\nhttps://cdn.example/x.mp4\n//host/y.mp4\n"
	if got := string(prefixPlaylist([]byte(in), "/v/T")); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

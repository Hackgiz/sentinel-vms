package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/media"
	"sentinel-linux/internal/store"
)

func newCompanionServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, &media.Supervisor{}, nil, "test")
	s.Ctx = context.Background()
	s.ListenPort = "8090"
	return s
}

func do(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func activate(s *Server, code string) {
	s.pairing.active = &pairingCode{Code: code, ExpiresAt: time.Now().Add(time.Minute), URL: "http://10.0.0.2:8090", CreatedBy: "admin"}
}

func TestPhonePairingFlow(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	activate(s, "ABCDEFGH")

	if rec := do(t, h, "POST", "/pair", "", `{"code":"WRONG123","deviceName":"Test"}`); rec.Code != 401 {
		t.Fatalf("wrong code: %d", rec.Code)
	}
	rec := do(t, h, "POST", "/pair", "", `{"code":" abcdefgh ","deviceName":"Eric's iPhone"}`)
	if rec.Code != 200 {
		t.Fatalf("pair: %d %s", rec.Code, rec.Body)
	}
	// Exactly the fields SentinelPairResponse decodes.
	var pr struct {
		DeviceID   string `json:"deviceID"`
		Token      string `json:"token"`
		Name       string `json:"name"`
		ServerName string `json:"serverName"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pr); err != nil || pr.Token == "" || pr.DeviceID == "" || pr.Name != "Eric's iPhone" || pr.ServerName == "" {
		t.Fatalf("pair response %s", rec.Body)
	}
	if do(t, h, "POST", "/pair", "", `{"code":"ABCDEFGH"}`).Code != 401 {
		t.Fatal("code must be one-shot")
	}

	// Authenticated phone API, by header and by ?token=.
	if do(t, h, "GET", "/cameras", "", "").Code != 401 {
		t.Fatal("no token must be 401")
	}
	if rec := do(t, h, "GET", "/cameras", pr.Token, ""); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("cameras: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", "/alerts?token="+url.QueryEscape(pr.Token), "", ""); rec.Code != 200 {
		t.Fatalf("query token: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/devices/register", pr.Token, `{"apnsToken":"`+strings.Repeat("ab", 32)+`"}`); rec.Code != 200 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	if devs, _ := s.Store.PairedDevices(context.Background()); len(devs) != 1 || !devs[0].HasPush {
		t.Fatalf("devices %+v", devs)
	}

	// Revoke → the phone's next call is 401 (which makes the app unpair).
	if err := s.Store.RevokeDevice(context.Background(), pr.DeviceID); err != nil {
		t.Fatal(err)
	}
	if do(t, h, "GET", "/cameras", pr.Token, "").Code != 401 {
		t.Fatal("revoked token still works")
	}
}

func TestPairingCodeLockout(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	activate(s, "ABCDEFGH")
	for i := 0; i < pairingMaxFailures; i++ {
		do(t, h, "POST", "/pair", "", `{"code":"ZZZZZZZZ"}`)
	}
	if do(t, h, "POST", "/pair", "", `{"code":"ABCDEFGH"}`).Code != 401 {
		t.Fatal("code should be invalidated after repeated wrong guesses")
	}
}

func TestPhoneCameraShapeAndAlarmMessages(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	if _, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Door", RTSPURL: "rtsp://192.168.2.221:554/stream1", Recording: true, FPS: 15}); err != nil {
		t.Fatal(err)
	}
	_, token, _ := s.Store.CreatePairedDevice(ctx, "Phone", "admin")
	rec := do(t, h, "GET", "/cameras", token, "")
	var cams []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cams); err != nil || len(cams) != 1 {
		t.Fatalf("%s", rec.Body)
	}
	for _, k := range []string{"id", "name", "location", "ipAddress", "status", "isRecording", "resolution", "fps", "supportsPTZ"} {
		if _, ok := cams[0][k]; !ok {
			t.Errorf("camera summary missing %q (Sentinel Mobile requires it)", k)
		}
	}
	if cams[0]["ipAddress"] != "192.168.2.221" || cams[0]["fps"] != float64(15) || cams[0]["status"] != "offline" {
		t.Errorf("summary %v", cams[0])
	}
	// A bare "not found" would tell the phone to update the server.
	rec = do(t, h, "POST", "/alerts/"+store.NewID()+"/acknowledge", token, "")
	if rec.Code != 404 || strings.Contains(rec.Body.String(), `"not found"`) {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body)
	}
}

func TestTunnelOriginNeverServesDashboard(t *testing.T) {
	s := newCompanionServer(t)
	h := s.CompanionHandler()
	for _, p := range []string{"/", "/api/v1/status", "/api/v1/users", "/live/x/index.m3u8"} {
		if rec := do(t, h, "GET", p, "", ""); rec.Code != 404 {
			t.Errorf("%s via tunnel origin = %d, want 404", p, rec.Code)
		}
	}
	if rec := do(t, h, "GET", "/health", "", ""); rec.Code != 200 {
		t.Errorf("/health = %d", rec.Code)
	}
}

func TestRewritePlaylist(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1.0,\nseg1.ts?session=abc\n\nhttps://elsewhere/x.ts\n"
	out := string(rewritePlaylist([]byte(in), "CAM", "t0k"))
	for _, want := range []string{
		`#EXT-X-MAP:URI="/hls/CAM/init.mp4?token=t0k"`,
		"/hls/CAM/seg1.ts?session=abc&token=t0k",
		"https://elsewhere/x.ts",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPairingPayloadMatchesMacFormat(t *testing.T) {
	p := pairingPayload("http://192.168.4.20:8090", "ABCDEFGH", "https://x-y.trycloudflare.com")
	if !strings.HasPrefix(p, "sentinel-vms://pair?") {
		t.Fatal(p)
	}
	// Decode exactly as Sentinel Mobile does: strip prefix, percent-decode, JSON.
	raw, err := url.PathUnescape(strings.TrimPrefix(p, "sentinel-vms://pair?"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m["url"] != "http://192.168.4.20:8090" || m["code"] != "ABCDEFGH" || m["v"] != float64(1) || m["remoteURL"] != "https://x-y.trycloudflare.com" {
		t.Fatalf("%v", m)
	}
	if strings.Contains(pairingPayload("http://a:1", "C", "http://not-https"), "remoteURL") {
		t.Fatal("non-https remote URL must be left out (iOS ATS rejects it)")
	}
}

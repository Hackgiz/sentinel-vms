package videogw

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/videotoken"
)

const cam = "07ACA52B-E211-4DD7-BCD0-1CC4B1BDD258"

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		ok, internal bool
	}{
		{"GET", "/live/" + cam + "/index.m3u8", true, false},
		{"GET", "/recordings/" + cam + "/20260101-000000-0.mp4", true, false},
		{"POST", "/webrtc/" + cam + "/whep", true, false},
		{"GET", "/api/v1/cameras/" + cam + "/init/20260101-000000-0.mp4", true, false},
		{"GET", "/api/v1/cameras/" + cam + "/init/x.mp4", false, false},
		{"GET", "/live/lowercase-id/index.m3u8", false, false},
		{"GET", "/rs/evidence/case-1.mp4", false, true},
		{"GET", "/rs/exports/abc", false, true},
		{"GET", "/rs/events/x.jpg", false, true},
		{"POST", "/rs/events/x.jpg", false, false},
		{"GET", "/rs/evidence/../x", false, false},
		{"GET", "/rs/other/x", false, false},
	} {
		_, ok := Match(tc.method, tc.path)
		rt, internal := MatchInternal(tc.method, tc.path)
		if ok != tc.ok || internal != tc.internal || (internal && rt.Camera != InternalCamera) {
			t.Errorf("%s %s: public %v internal %v", tc.method, tc.path, ok, internal)
		}
	}
}

func TestRewrite(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-MAP:URI=\"/v/T/init.mp4\"\n/v/T/a.mp4\nrel.mp4\nhttps://x/y.mp4\n"
	want := "#EXTM3U\n#EXT-X-MAP:URI=\"/init.mp4\"\n/a.mp4\nrel.mp4\nhttps://x/y.mp4\n"
	if got := string(RewritePlaylist([]byte(in), func(s string) string { return StripPrefix(s, "/v/T") })); got != want {
		t.Fatalf("strip:\n%s", got)
	}
	if got := string(PrefixPlaylist([]byte(want), "/v/T")); got != in {
		t.Fatalf("prefix:\n%s", got)
	}
	if StripPrefix("/v/Tx/a", "/v/T") != "/v/Tx/a" {
		t.Fatal("strip must stop at a path boundary")
	}
}

func TestGatewayInternalPaths(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := videotoken.NewSigner(priv)
	plane := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("cookie forwarded")
		}
		w.Header().Set("Location", "/webrtc/x")
		_, _ = w.Write([]byte(r.URL.Path))
	})
	gw := func(internal bool) http.Handler {
		return &Gateway{
			Verify:   func(tok string) (videotoken.Claims, error) { return videotoken.NewVerifier("rs1", pub).Verify(tok) },
			Plane:    func(videotoken.Claims) http.Handler { return plane },
			Internal: internal,
		}
	}
	sign := func(camera string, r videotoken.Right) string {
		tok, _, err := signer.Sign(videotoken.Claims{Camera: camera, Subject: ManagementSubject, Rights: []videotoken.Right{r}, Audience: "rs1"}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	get := func(h http.Handler, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Cookie", "a=b")
		h.ServeHTTP(w, r)
		return w
	}
	itok := sign(InternalCamera, videotoken.Export)
	if w := get(gw(true), "/v/"+itok+"/rs/evidence/case.mp4"); w.Code != 200 || w.Body.String() != "/rs/evidence/case.mp4" || w.Header().Get("Location") != "/v/"+itok+"/webrtc/x" {
		t.Fatalf("internal: %d %s %v", w.Code, w.Body, w.Header())
	}
	if w := get(gw(false), "/v/"+itok+"/rs/evidence/case.mp4"); w.Code != http.StatusNotFound {
		t.Fatalf("management gateway served an internal path: %d", w.Code)
	}
	if w := get(gw(true), "/v/"+sign(cam, videotoken.Export)+"/rs/evidence/case.mp4"); w.Code != http.StatusForbidden {
		t.Fatalf("camera token on an internal path: %d", w.Code)
	}
	if w := get(gw(true), "/v/"+itok+"/recordings/"+cam+"/20260101-000000-0.mp4"); w.Code != http.StatusForbidden {
		t.Fatalf("internal token on a camera path: %d", w.Code)
	}
	if w := get(gw(true), "/v/junk/rs/evidence/case.mp4"); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"unauthorized"`) {
		t.Fatalf("bad token: %d %s", w.Code, w.Body)
	}
}

func TestSessionLimiter(t *testing.T) {
	l := &SessionLimiter{N: 2, Window: time.Minute, Exempt: map[string]bool{ManagementSubject: true}}
	if !l.Allow("u") || !l.Allow("u") || l.Allow("u") || !l.Allow("v") {
		t.Fatal("limit")
	}
	for i := 0; i < 5; i++ {
		if !l.Allow(ManagementSubject) {
			t.Fatal("exempt subject limited")
		}
	}
}

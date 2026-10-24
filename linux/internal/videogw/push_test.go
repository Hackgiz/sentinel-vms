package videogw

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/videotoken"
)

func TestPushRoutes(t *testing.T) {
	sid := strings.Repeat("ab", 16)
	for _, tc := range []struct {
		method, path string
		ok           bool
		max          int64
	}{
		{"POST", "/push/" + cam + "/sessions", true, MaxPushSession},
		{"PUT", "/push/" + cam + "/sessions/" + sid + "/init", true, MaxPushSegment},
		{"PUT", "/push/" + cam + "/sessions/" + sid + "/segments/0", true, MaxPushSegment},
		{"PUT", "/push/" + cam + "/sessions/" + sid + "/segments/123", true, MaxPushSegment},
		{"DELETE", "/push/" + cam + "/sessions/" + sid, true, MaxPushSession},
		{"PUT", "/push/" + cam + "/sessions/" + sid + "/segments/-1", false, 0},
		{"PUT", "/push/" + cam + "/sessions/" + sid + "/segments/01", false, 0},
		{"PUT", "/push/" + cam + "/sessions/XYZ/init", false, 0},
		{"GET", "/push/" + cam + "/sessions", false, 0},
		{"POST", "/push/lower-case/sessions", false, 0},
	} {
		rt, ok := Match(tc.method, tc.path)
		if ok != tc.ok || (ok && (!rt.Push || rt.MaxBody != tc.max || len(rt.Rights) != 1 || rt.Rights[0] != videotoken.Push)) {
			t.Errorf("%s %s: %v %+v", tc.method, tc.path, ok, rt)
		}
	}
}

func TestGatewayPushRightsAndBodyLimit(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := videotoken.NewSigner(priv)
	var got int
	plane := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		got = len(b)
		w.WriteHeader(http.StatusNoContent)
	})
	gw := &Gateway{
		Verify: func(tok string) (videotoken.Claims, error) { return videotoken.NewVerifier("rs1", pub).Verify(tok) },
		Plane:  func(videotoken.Claims) http.Handler { return plane },
	}
	sign := func(r videotoken.Right) string {
		tok, _, err := signer.Sign(videotoken.Claims{Camera: cam, Subject: "phone:" + cam, Rights: []videotoken.Right{r}, Audience: "rs1"}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	put := func(tok string, n int, chunked bool) int {
		r := httptest.NewRequest("PUT", "/v/"+tok+"/push/"+cam+"/sessions/"+strings.Repeat("ab", 16)+"/segments/0", bytes.NewReader(make([]byte, n)))
		if chunked {
			r.ContentLength = -1
		}
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		return w.Code
	}
	push := sign(videotoken.Push)
	if c := put(push, MaxPushSegment, false); c != 204 || got != MaxPushSegment {
		t.Fatalf("8 MiB segment: %d %d", c, got)
	}
	if c := put(push, MaxPushSegment+1, false); c != 413 {
		t.Fatalf("declared too big: %d", c)
	}
	if c := put(push, MaxPushSegment+1, true); c != 413 {
		t.Fatalf("chunked too big: %d", c)
	}
	if c := put(sign(videotoken.Live), 10, false); c != 403 {
		t.Fatalf("live token pushing: %d", c)
	}
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, httptest.NewRequest("GET", "/v/"+push+"/live/"+cam+"/index.m3u8", nil))
	if w.Code != 403 {
		t.Fatalf("push token watching: %d", w.Code)
	}
}

package recorder

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/playback"
	"sentinel-linux/internal/videogw"
)

// Browsers get PCM-audio recordings video-only; the iPhone app (a paired
// device's video token, no browser User-Agent) gets them untouched.
func TestPlaybackPCMFilteredOnlyForBrowsers(t *testing.T) {
	r := New(t.TempDir(), nil, nil, "test", "")
	cam := "6F1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F"
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	name := start.Format("20060102-150405") + "-000000.mp4"
	dir := filepath.Join(r.RecordingsRoot(), cam)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := playback.SynthSegmentPCM(20, 2)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	h := r.videoHandler()
	get := func(path, ua string, token bool) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if ua != "" {
			req.Header.Set("User-Agent", ua)
		}
		if token {
			req.Header.Set(videogw.TokenAuthHeader, "1")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}
	list := fmt.Sprintf("/api/v1/cameras/%s/playback.m3u8?from=%d&to=%d", cam, start.Add(-time.Minute).UnixMilli(), start.Add(time.Hour).UnixMilli())
	pieceRe := regexp.MustCompile(`(?m)^/api/v1/cameras/\S+/pieces/\S+$`)
	hasAudio := func(b []byte) bool { return bytes.Count(b, []byte("traf")) > 1 }

	cases := []struct {
		name, ua string
		token    bool
		original bool
	}{
		{"session in a browser", "Mozilla/5.0 Chrome/130", false, false},
		{"video token in a browser", "Mozilla/5.0 Chrome/130", true, false},
		{"video token from AVPlayer", "AppleCoreMedia/1.0.0.22A (iPhone; U; CPU OS 18_0)", true, true},
		{"no token, no browser (not trusted)", "AppleCoreMedia/1.0.0", false, false},
	}
	for _, tc := range cases {
		code, pl := get(list, tc.ua, tc.token)
		if code != 200 {
			t.Fatalf("%s: playlist %d %s", tc.name, code, pl)
		}
		m := regexp.MustCompile(`#EXT-X-MAP:URI="([^"]+)"(,BYTERANGE="(\d+)@0")?`).FindStringSubmatch(string(pl))
		if m == nil || (m[2] != "") != tc.original || strings.HasPrefix(m[1], "/recordings/") != tc.original {
			t.Fatalf("%s: map %q", tc.name, m)
		}
		piece := pieceRe.FindString(string(pl))
		if piece == "" {
			t.Fatalf("%s: no piece in\n%s", tc.name, pl)
		}
		_, init := get(m[1], tc.ua, tc.token)
		if !tc.original {
			// The filtered init has one trak.
			if bytes.Count(init, []byte("trak")) != 1 || bytes.Contains(init, []byte("ipcm")) {
				t.Fatalf("%s: init not filtered", tc.name)
			}
		} else if code, _ := get(m[1], tc.ua, tc.token); code != 200 {
			t.Fatalf("%s: recording %d", tc.name, code)
		}
		code, body := get(piece, tc.ua, tc.token)
		if code != 200 || hasAudio(body) != tc.original {
			t.Fatalf("%s: piece %d, audio=%v", tc.name, code, hasAudio(body))
		}
		// The init route follows the same rule.
		code, ini := get(strings.Replace(piece, "/pieces/", "/init/", 1)[:strings.Index(piece, "?")], tc.ua, tc.token)
		if code != 200 || bytes.Contains(ini, []byte("ipcm")) == !tc.original {
			t.Fatalf("%s: init route %d ipcm=%v", tc.name, code, bytes.Contains(ini, []byte("ipcm")))
		}
	}
}

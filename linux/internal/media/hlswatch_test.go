package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A camera whose clock jumped backwards (Hanwha XNV-8082R, every ~71.6 min)
// leaves its HLS muxer crash-looping; the supervisor must reconnect that
// camera, and only that camera, by changing its path in the config.
func TestTimestampCrashReconnectsOnlyThatCamera(t *testing.T) {
	dir := t.TempDir()
	s := &Supervisor{ConfigPath: filepath.Join(dir, "mediamtx.yml"), RecordingsRoot: filepath.Join(dir, "rec"), Ports: DefaultPorts}
	if err := s.Apply([]Source{
		{ID: "XNV", URL: "rtsp://10.0.0.1/s"},
		{ID: "TAPO", URL: "rtsp://10.0.0.2/s", SubURL: "rtsp://10.0.0.2/sub"},
	}); err != nil {
		t.Fatal(err)
	}
	read := func() string { b, _ := os.ReadFile(s.ConfigPath); return string(b) }
	before := read()

	w := &lineWriter{onLine: s.watchLogLine}
	log := "2026/10/23 11:51:54 INF [HLS] [muxer XNV] is converting into HLS, 1 track (H264)\n" +
		"2026/10/23 11:51:56 ERR [HLS] [muxer XNV] muxer instance crashed: muxer error: sample timestamp is impossible to handle\n"
	// Split mid-line, as a pipe may deliver it.
	_, _ = w.Write([]byte(log[:90]))
	_, _ = w.Write([]byte(log[90:]))

	after := read()
	if after == before {
		t.Fatal("timestamp crash didn't change the config")
	}
	xnv := after[strings.Index(after, "  XNV:\n"):strings.Index(after, "  TAPO:\n")]
	if !strings.Contains(xnv, "sourceOnDemandCloseAfter: 11s") {
		t.Fatalf("XNV path not changed:\n%s", xnv)
	}
	if strings.Count(after, "sourceOnDemandCloseAfter") != 1 {
		t.Fatal("other cameras were reconnected too")
	}

	// MediaMTX retries the muxer every few seconds: one reconnect per minute.
	_, _ = w.Write([]byte("ERR [HLS] [muxer XNV] muxer instance crashed: muxer error: unable to extract DTS: DTS is not monotonically increasing, was 1, now is -2\n"))
	if s.restartCounts()["XNV"] != 1 {
		t.Fatal("reconnected again inside the cooldown")
	}
	s.mu.Lock()
	s.restartedAt["XNV"] = time.Now().Add(-2 * restartCooldown)
	s.mu.Unlock()
	_, _ = w.Write([]byte("ERR [HLS] [muxer XNV] muxer instance crashed: muxer error: unable to extract DTS: DTS is not monotonically increasing, was 1, now is -2\n"))
	if !strings.Contains(read(), "sourceOnDemandCloseAfter: 10s") {
		t.Fatal("second reconnect must change the path again")
	}

	// Sub-streams are paths too; unknown paths and other errors are ignored.
	_, _ = w.Write([]byte("ERR [HLS] [muxer TAPO-sub] muxer instance crashed: muxer error: sample timestamp is impossible to handle\n" +
		"ERR [HLS] [muxer ghost] muxer instance crashed: muxer error: sample timestamp is impossible to handle\n" +
		"ERR [HLS] [muxer TAPO] muxer instance crashed: muxer error: codec not supported\n"))
	got := s.restartCounts()
	if got["TAPO-sub"] != 1 || got["ghost"] != 0 || got["TAPO"] != 0 {
		t.Fatalf("restarts = %v", got)
	}
}

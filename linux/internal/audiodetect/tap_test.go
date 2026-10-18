package audiodetect

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestManagerTapStatusAndFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	// A fake ffmpeg: the sub-stream has no audio (error, no output); the main
	// stream delivers 0.5 s of silence and then ends.
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n" +
		"case \"$*\" in *main*) head -c 32000 /dev/zero; exec sleep 30;; *) echo 'Output file does not contain any stream' >&2; exit 1;; esac\n"
	ff := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ff, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Manager{FFmpeg: ff, ClassifierNote: "no audio model", BackoffBase: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Sync(ctx, []Source{{CameraID: "c1", URL: "rtsp://h/sub", FallbackURL: "rtsp://h/main"}})
	waitFor(t, "no-audio status", func() bool { return strings.Contains(m.Status("c1"), "No audio") })
	if !strings.Contains(m.Status("c1"), "does not contain any stream") {
		t.Fatalf("status %q lacks the ffmpeg reason", m.Status("c1"))
	}
	waitFor(t, "listening on the fallback", func() bool { return strings.HasPrefix(m.Status("c1"), "Listening") })
	if st := m.Status("c1"); !strings.Contains(st, "loudness only (no audio model)") {
		t.Fatalf("status %q lacks the classifier reason", st)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "rtsp://h/sub") || !strings.Contains(string(b), "rtsp://h/main") {
		t.Fatalf("calls: %s", b)
	}
	m.Sync(ctx, nil)
	if m.Status("c1") != "" {
		t.Fatalf("status kept after removal: %q", m.Status("c1"))
	}
}

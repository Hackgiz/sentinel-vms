package media

// MediaMTX can't recover a path whose camera clock jumps backwards. Seen on
// a Hanwha XNV-8082R: its RTP timestamps run off a 32-bit microsecond
// counter, so every 2^32 µs (~71.6 min) they fall back ~4295 s. MediaMTX
// carries the jump into the path's timeline, which then sits below zero:
// recording carries on, but every HLS muxer for the path dies with "sample
// timestamp is impossible to handle" until the camera is reconnected, so
// live view shows an error indefinitely. The supervisor watches MediaMTX's
// log for those crashes and reconnects just that camera, which restarts its
// timeline at zero.

import (
	"bytes"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"time"
)

var hlsTimestampCrash = regexp.MustCompile(`\[HLS\] \[muxer ([^\]]+)\] muxer instance crashed: .*(sample timestamp is impossible to handle|DTS is not monotonically increasing)`)

// At most one reconnect per path per minute: MediaMTX retries a crashed
// muxer every few seconds, and a reconnect costs a short recording gap.
const restartCooldown = time.Minute

// lineWriter hands each complete line written to it to onLine.
type lineWriter struct {
	buf    []byte
	onLine func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	rest := w.buf
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			break
		}
		w.onLine(string(rest[:i]))
		rest = rest[i+1:]
	}
	w.buf = append(w.buf[:0], rest...)
	if len(w.buf) > 64<<10 { // no newline in 64 KiB: not a log line
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

func (s *Supervisor) watchLogLine(line string) {
	if m := hlsTimestampCrash.FindStringSubmatch(line); m != nil {
		s.restartPath(m[1], m[2])
	}
}

// restartPath reconnects one MediaMTX path by giving it a changed config.
func (s *Supervisor) restartPath(path, reason string) {
	s.mu.Lock()
	known := false
	for _, src := range s.sources {
		if path == src.ID || (src.SubURL != "" && path == src.ID+"-sub") {
			known = true
			break
		}
	}
	if !known || time.Since(s.restartedAt[path]) < restartCooldown {
		s.mu.Unlock()
		return
	}
	if s.restarts == nil {
		s.restarts, s.restartedAt = map[string]int{}, map[string]time.Time{}
	}
	s.restarts[path]++
	s.restartedAt[path] = time.Now()
	sources := s.sources
	s.mu.Unlock()
	slog.Warn("camera timestamps went backwards; reconnecting it so live view works again", "path", path, "err", reason)
	if err := s.Apply(sources); err != nil {
		slog.Warn("reconnecting camera failed", "path", path, "err", err)
	}
}

func (s *Supervisor) restartCounts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.restarts)
}

// restartYAML is the per-path line that makes MediaMTX reconnect a camera:
// any change outside the hot-reloadable record* keys recreates the path.
// sourceOnDemandCloseAfter does nothing for always-on paths, and toggling
// it between 10 s (the default) and 11 s is harmless for on-demand ones.
func restartYAML(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("    sourceOnDemandCloseAfter: %ds\n", 10+n%2)
}

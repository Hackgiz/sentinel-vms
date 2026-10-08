// Package detect watches camera streams for motion. Each enabled camera gets
// one ffmpeg reader on the LOCAL MediaMTX re-stream (no extra connection to
// the camera) that emits a tiny grayscale frame twice a second.
package detect

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

const (
	frameW, frameH = 64, 36
	cellsX, cellsY = 32, 18 // 2×2-pixel cells, same grid as the Mac
	lumaDelta      = 18     // a cell "changed" when its mean luma moves more than this
	sampleFPS      = "2"
)

// Thresholds per sensitivity level (fraction of cells changed). Medium = the
// Mac's default 0.04; a static scene scores ~0.
var Thresholds = map[int]float64{1: 0.08, 2: 0.04, 3: 0.02}

type Source struct {
	CameraID string
	URL      string // rtsp://127.0.0.1:<port>/<path>
	Level    int    // 1..3
	Objects  bool   // also run person/vehicle detection on frames with motion
}

// Detections is one object-detection pass on a frame with motion.
type Detections struct {
	CameraID string
	Objects  []Object
	At       time.Time
	// The analyzed frame (416×416 BGR, letterboxed) and its real-image size,
	// so the event snapshot shows exactly what was detected.
	Frame              []byte
	ContentW, ContentH int
}

// Motion is one frame pair over the threshold.
type Motion struct {
	CameraID string
	Score    float64
	At       time.Time
}

type Manager struct {
	FFmpeg   string
	OnMotion func(Motion)

	// Optional object detection. SourceSize reports a camera's stream
	// resolution (to undo the letterbox); 0,0 = assume 16:9.
	Objects    *ObjectDetector
	OnObjects  func(Detections)
	SourceSize func(cameraID string) (int, int)

	mu      sync.Mutex
	workers map[string]*worker
}

type worker struct {
	src    Source
	cancel context.CancelFunc
}

// Find looks next to the sentinel binary, in <data>/bin, then on PATH.
func Find(dataDir string) string {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "ffmpeg"))
	}
	candidates = append(candidates, filepath.Join(dataDir, "bin", "ffmpeg"))
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return c
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

func (m *Manager) Available() bool { return m != nil && m.FFmpeg != "" }

// Sync starts, restarts or stops readers so exactly sources are watched.
func (m *Manager) Sync(ctx context.Context, sources []Source) {
	if !m.Available() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workers == nil {
		m.workers = map[string]*worker{}
	}
	want := map[string]Source{}
	for _, s := range sources {
		want[s.CameraID] = s
	}
	for id, w := range m.workers {
		if s, ok := want[id]; !ok || s != w.src {
			w.cancel()
			delete(m.workers, id)
		}
	}
	for id, s := range want {
		if _, ok := m.workers[id]; ok {
			continue
		}
		wctx, cancel := context.WithCancel(ctx)
		m.workers[id] = &worker{src: s, cancel: cancel}
		go m.run(wctx, s)
	}
}

func (m *Manager) run(ctx context.Context, s Source) {
	backoff := 3 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := m.watch(ctx, s)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = 3 * time.Second
		}
		slog.Debug("motion reader stopped; retrying", "camera", s.CameraID, "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (m *Manager) watch(ctx context.Context, s Source) error {
	objects := s.Objects && m.Objects != nil && m.OnObjects != nil
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-rtsp_transport", "tcp", "-i", s.URL, "-an", "-sn", "-dn"}
	motionFilter := "scale=" + itoa(frameW) + ":" + itoa(frameH) + ":flags=area,format=gray"
	var detR, detW *os.File
	if objects {
		// One decode, two outputs: tiny gray frames for motion on stdout, and
		// YOLOX-ready letterboxed BGR frames (top-left, gray 114 padding) on fd 3.
		var err error
		if detR, detW, err = os.Pipe(); err != nil {
			return err
		}
		defer detR.Close()
		args = append(args, "-filter_complex",
			"[0:v]fps="+sampleFPS+",split=2[m][d];[m]"+motionFilter+"[mo];"+
				"[d]scale="+itoa(InputSize)+":"+itoa(InputSize)+":force_original_aspect_ratio=decrease:flags=bilinear,"+
				"pad="+itoa(InputSize)+":"+itoa(InputSize)+":0:0:color=0x727272,format=bgr24[do]",
			"-map", "[mo]", "-f", "rawvideo", "pipe:1",
			"-map", "[do]", "-f", "rawvideo", "pipe:3")
	} else {
		args = append(args, "-vf", "fps="+sampleFPS+","+motionFilter, "-f", "rawvideo", "pipe:1")
	}
	cmd := exec.CommandContext(ctx, m.FFmpeg, args...)
	cmd.WaitDelay = 3 * time.Second
	if objects {
		cmd.ExtraFiles = []*os.File{detW}
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		if detW != nil {
			detW.Close()
		}
		return err
	}

	// Keep only the newest detection frame; read continuously so ffmpeg
	// never blocks on a full pipe.
	var (
		latestMu sync.Mutex
		latest   []byte
		busy     bool
		lastRun  time.Time
	)
	if objects {
		detW.Close() // the child holds its own copy
		go func() {
			r := bufio.NewReaderSize(detR, 1<<20)
			for {
				buf := make([]byte, 3*InputSize*InputSize)
				if _, err := io.ReadFull(r, buf); err != nil {
					return
				}
				latestMu.Lock()
				latest = buf
				latestMu.Unlock()
			}
		}()
	}

	threshold := Thresholds[s.Level]
	if threshold == 0 {
		threshold = Thresholds[2]
	}
	r := bufio.NewReaderSize(out, frameW*frameH*2)
	prev := make([]byte, frameW*frameH)
	cur := make([]byte, frameW*frameH)
	have := false
	for {
		if _, err := io.ReadFull(r, cur); err != nil {
			_ = cmd.Wait()
			return err
		}
		if have {
			if score := Score(prev, cur); score >= threshold {
				now := time.Now()
				m.OnMotion(Motion{CameraID: s.CameraID, Score: score, At: now})
				if objects {
					latestMu.Lock()
					frame := latest
					run := frame != nil && !busy && now.Sub(lastRun) >= time.Second
					if run {
						busy, lastRun = true, now
					}
					latestMu.Unlock()
					if run {
						go func() {
							defer func() { latestMu.Lock(); busy = false; latestMu.Unlock() }()
							w, h := 0, 0
							if m.SourceSize != nil {
								w, h = m.SourceSize(s.CameraID)
							}
							cw, ch := ContentSize(w, h)
							objs, err := m.Objects.Detect(frame, cw, ch)
							if err != nil {
								slog.Warn("object detection failed", "camera", s.CameraID, "err", err)
								return
							}
							if len(objs) > 0 {
								m.OnObjects(Detections{CameraID: s.CameraID, Objects: objs, At: now, Frame: frame, ContentW: cw, ContentH: ch})
							}
						}()
					}
				}
			}
		}
		prev, cur = cur, prev
		have = true
	}
}

// Score is the fraction of 2×2 cells whose mean luma changed by more than
// lumaDelta between two 64×36 grayscale frames — the Mac's motion metric.
func Score(a, b []byte) float64 {
	changed := 0
	for cy := 0; cy < cellsY; cy++ {
		for cx := 0; cx < cellsX; cx++ {
			var sa, sb int
			for dy := 0; dy < 2; dy++ {
				row := (cy*2 + dy) * frameW
				for dx := 0; dx < 2; dx++ {
					i := row + cx*2 + dx
					sa += int(a[i])
					sb += int(b[i])
				}
			}
			d := (sa - sb) / 4
			if d < 0 {
				d = -d
			}
			if d > lumaDelta {
				changed++
			}
		}
	}
	return float64(changed) / float64(cellsX*cellsY)
}

// Snapshot grabs one JPEG frame (640 px wide) from url into path.
func (m *Manager) Snapshot(ctx context.Context, url, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tmp := path + ".tmp.jpg"
	cmd := exec.CommandContext(ctx, m.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-rtsp_transport", "tcp", "-i", url, "-an", "-frames:v", "1", "-vf", "scale=640:-2", "-q:v", "5", "-y", tmp)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// SnapshotFrames grabs n JPEG frames (640 px wide, ~0.5 s apart) from url into
// dir as <prefix>-1.jpg … and returns their paths. The first is the event
// thumbnail; the set feeds the AI scene description.
func (m *Manager) SnapshotFrames(ctx context.Context, url, dir, prefix string, n int) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	pattern := filepath.Join(dir, prefix+"-%d.jpg")
	cmd := exec.CommandContext(ctx, m.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-rtsp_transport", "tcp", "-i", url, "-an", "-frames:v", itoa(n), "-vf", "fps=2,scale=640:-2", "-q:v", "5", "-y", pattern)
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var out []string
	for i := 1; i <= n; i++ {
		p := filepath.Join(dir, prefix+"-"+itoa(i)+".jpg")
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, os.ErrNotExist
	}
	return out, nil
}

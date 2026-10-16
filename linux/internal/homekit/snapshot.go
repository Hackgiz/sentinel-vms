package homekit

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"sync"
	"time"
)

const (
	snapshotFresh = 3 * time.Second  // a cached frame this young is served as is
	snapshotStale = 60 * time.Second // a failed capture falls back to a frame this young
)

// snapshotCache serves HomeKit snapshot requests. Tiles poll hard and
// HomeView shows nine feeds, so a frame is reused for 3 s and concurrent
// requests for the same camera and size share one capture. When a capture
// fails, the last good frame is served for up to a minute, then a generated
// "offline" placeholder.
type snapshotCache struct {
	now     func() time.Time
	capture func(ctx context.Context, camID string, w, h int) ([]byte, error)

	mu      sync.Mutex
	entries map[snapKey]*snapEntry
	last    map[string]snapFrame // newest good frame per camera, any size
}

type snapKey struct {
	cam  string
	w, h int
}

type snapFrame struct {
	jpeg []byte
	at   time.Time
}

// snapEntry serializes captures for one key.
type snapEntry struct {
	mu    sync.Mutex
	frame snapFrame
}

func newSnapshotCache(now func() time.Time, capture func(ctx context.Context, camID string, w, h int) ([]byte, error)) *snapshotCache {
	if now == nil {
		now = time.Now
	}
	return &snapshotCache{now: now, capture: capture, entries: map[snapKey]*snapEntry{}, last: map[string]snapFrame{}}
}

// Get returns a JPEG for the camera at (w,h). It always returns an image.
func (c *snapshotCache) Get(ctx context.Context, camID string, w, h int) []byte {
	w, h = clampSize(w, h)
	key := snapKey{camID, w, h}
	c.mu.Lock()
	e := c.entries[key]
	if e == nil {
		e = &snapEntry{}
		c.entries[key] = e
	}
	c.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.frame.jpeg != nil && c.now().Sub(e.frame.at) < snapshotFresh {
		return e.frame.jpeg
	}
	b, err := c.capture(ctx, camID, w, h)
	if err == nil && len(b) > 0 {
		f := snapFrame{jpeg: b, at: c.now()}
		e.frame = f
		c.mu.Lock()
		c.last[camID] = f
		c.mu.Unlock()
		return b
	}
	if err != nil && err != ErrStreamingUnavailable {
		slog.Debug("homekit snapshot failed", "camera", camID, "err", err)
	}
	c.mu.Lock()
	f, ok := c.last[camID]
	c.mu.Unlock()
	if ok && c.now().Sub(f.at) < snapshotStale {
		return f.jpeg
	}
	return offlineJPEG(w, h)
}

// Forget drops everything cached for a camera that is no longer exposed.
func (c *snapshotCache) Forget(camID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if k.cam == camID {
			delete(c.entries, k)
		}
	}
	delete(c.last, camID)
}

// clampSize keeps requests sane: HomeKit asks for 320x180 up to 1920x1080.
func clampSize(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return 1280, 720
	}
	if w > 3840 {
		w = 3840
	}
	if h > 2160 {
		h = 2160
	}
	return w, h
}

var (
	placeholderMu    sync.Mutex
	placeholderCache = map[[2]int][]byte{}
)

// offlineJPEG is the picture shown when a camera cannot be captured: a dark
// frame with a slashed-out lens, so the tile reads as "no signal" rather
// than as a broken image. Generated once per size.
func offlineJPEG(w, h int) []byte {
	w, h = clampSize(w, h)
	placeholderMu.Lock()
	defer placeholderMu.Unlock()
	if b, ok := placeholderCache[[2]int{w, h}]; ok {
		return b
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{0x1c, 0x1c, 0x1e, 0xff}
	fg := color.RGBA{0x8e, 0x8e, 0x93, 0xff}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	// Lens ring centered, radius a fifth of the short side, with a slash.
	cx, cy := w/2, h/2
	r := min(w, h) / 5
	thick := max(2, r/12)
	for y := cy - r - thick; y <= cy+r+thick; y++ {
		for x := cx - r - thick; x <= cx+r+thick; x++ {
			if x < 0 || y < 0 || x >= w || y >= h {
				continue
			}
			dx, dy := x-cx, y-cy
			d2 := dx*dx + dy*dy
			ring := d2 <= (r+thick)*(r+thick) && d2 >= (r-thick)*(r-thick)
			// slash from top-right to bottom-left
			s := dx + dy
			slash := s >= -thick && s <= thick && d2 <= (r+thick)*(r+thick)
			if ring || slash {
				img.SetRGBA(x, y, fg)
			}
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
		return nil
	}
	placeholderCache[[2]int{w, h}] = buf.Bytes()
	return buf.Bytes()
}

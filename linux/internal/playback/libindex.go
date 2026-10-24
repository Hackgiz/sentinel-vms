package playback

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SettleAge is how long a segment must go unwritten before its index is
// stored in a sidecar: MediaMTX appends a part about every second, so a
// minute of silence means the segment is finished (or its camera stopped;
// if it resumes, the sidecar is extended, not discarded).
var SettleAge = time.Minute

// How an index was obtained.
type indexHow int

const (
	howCached   indexHow = iota // in memory already
	howLoaded                   // sidecar, as is
	howExtended                 // cached or sidecar index, parse resumed
	howParsed                   // parsed from the start
	howSkipped                  // warm-up: someone else is indexing it
)

// index returns the segment's index: from the cache, from its sidecar, or
// by parsing it (only the part not yet indexed when it grew). nil when it
// can't be read as fMP4.
func (l *Library) index(path string, size int64, mod time.Time) *Index {
	ix, _ := l.indexFile(path, size, mod, false)
	return ix
}

func (l *Library) indexFile(path string, size int64, mod time.Time, warm bool) (*Index, indexHow) {
	l.mu.Lock()
	var c *cachedIndex
	for {
		if l.cache == nil {
			l.cache = map[string]*cachedIndex{}
		}
		if l.inflight == nil {
			l.inflight = map[string]chan struct{}{}
		}
		l.tick++
		c = l.cache[path]
		if c != nil {
			c.used = l.tick
		}
		if c != nil && c.size == size && c.mod.Equal(mod) {
			save := l.shouldSave(c, path, size, mod)
			ix := c.ix
			l.mu.Unlock()
			if save {
				l.save(path, ix, size, mod)
			}
			return ix, howCached
		}
		ch := l.inflight[path]
		if ch == nil {
			break
		}
		l.mu.Unlock()
		if warm {
			return nil, howSkipped
		}
		<-ch // another request is indexing it: use its result
		l.mu.Lock()
	}
	ch := make(chan struct{})
	l.inflight[path] = ch
	if !warm {
		l.busy++
	}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.inflight, path)
		if !warm {
			l.busy--
		}
		l.mu.Unlock()
		close(ch)
	}()

	ix, how, saved := l.build(path, size, mod, c)

	l.mu.Lock()
	if l.cache == nil { // SetRoot meanwhile
		l.cache = map[string]*cachedIndex{}
	}
	if old := l.cache[path]; old != nil {
		l.frags -= fragCount(old.ix)
	}
	nc := &cachedIndex{size: size, mod: mod, ix: ix, used: l.tick, saved: saved}
	l.cache[path] = nc
	l.frags += fragCount(ix)
	for len(l.cache) > 1 && (len(l.cache) > maxCached || l.frags > maxCachedFragments) {
		var oldest string
		var min int64 = -1
		for k, v := range l.cache {
			if k != path && (min < 0 || v.used < min) {
				oldest, min = k, v.used
			}
		}
		l.frags -= fragCount(l.cache[oldest].ix)
		delete(l.cache, oldest)
	}
	save := l.shouldSave(nc, path, size, mod)
	l.mu.Unlock()
	if save {
		l.save(path, ix, size, mod)
	}
	return ix, how
}

// shouldSave reports (and marks, so it happens once) that c's index is due
// for a sidecar: the segment has settled and the sidecar on disk, if any,
// describes less of it. Called with l.mu held.
func (l *Library) shouldSave(c *cachedIndex, path string, size int64, mod time.Time) bool {
	if c.ix == nil || !c.ix.hasMoov || c.saved == size || time.Since(mod) < SettleAge || !sidecarAllowed(path) {
		return false
	}
	c.saved = size // a failed write is not retried until the segment changes
	return true
}

func (l *Library) save(path string, ix *Index, size int64, mod time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if err := writeSidecar(path, f, ix, size, mod); err != nil {
		slog.Debug("playback index not saved", "file", path, "err", err)
		return
	}
	l.stats.saved.Add(1)
}

// build indexes a segment that is not cached at its current size: it
// extends the cached index c when the segment grew, else loads the sidecar
// (extending it the same way), else parses the file. saved is the segment
// size the sidecar on disk describes.
func (l *Library) build(path string, size int64, mod time.Time, c *cachedIndex) (ix *Index, how indexHow, saved int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, howParsed, 0
	}
	defer f.Close()
	if c != nil && c.ix != nil && size > c.size && !mod.Before(c.mod) {
		// Grown: extend a copy (readers may still hold the cached one).
		if ix := extendCopy(c.ix, f, size); ix != nil {
			l.stats.extended.Add(1)
			return ix, howExtended, c.saved
		}
	}
	if sidecarAllowed(path) {
		if six, at, err := readSidecar(path, f, size); err == nil {
			if at == size {
				l.stats.loaded.Add(1)
				return six, howLoaded, at
			}
			if ix := extendCopy(six, f, size); ix != nil {
				l.stats.extended.Add(1)
				return ix, howExtended, at
			}
		}
	}
	l.stats.parsed.Add(1)
	ix, _ = ReadIndex(f, size)
	return ix, howParsed, 0
}

func extendCopy(from *Index, f *os.File, size int64) *Index {
	cp := *from
	cp.Fragments = append(make([]Fragment, 0, len(from.Fragments)+64), from.Fragments...)
	if cp.Extend(f, size) != nil {
		return nil
	}
	return &cp
}

// WarmResult is what one warm-up pass did.
type WarmResult struct {
	Files   int // segments looked at
	Cached  int // already in memory
	Loaded  int // read from their sidecar
	Parsed  int // parsed (wholly, or the part not yet indexed)
	Skipped int // a request was indexing them
	Took    time.Duration
}

// WarmPause is the yield between two segments of a warm-up.
var WarmPause = 50 * time.Millisecond

// Warm indexes every segment written within window (both tiers of every
// camera folder whose name valid accepts), newest first, one at a time,
// giving way to requests: before each segment it waits while a request is
// indexing, it skips a segment a request is already indexing, and it pauses
// WarmPause between segments. Each settled segment gets its sidecar, so the
// first day-long playlist after a restart is quick. It stops early when ctx
// ends or the recordings folder changes.
func (l *Library) Warm(ctx context.Context, window time.Duration, valid func(id string) bool) WarmResult {
	start := time.Now()
	type cand struct {
		path string
		size int64
		mod  time.Time
	}
	var cands []cand
	since := start.Add(-window)
	root := l.root()
	scan := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !namePattern.MatchString(e.Name()) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			info, err := os.Stat(p) // follows imported links, as Files does
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.ModTime().Before(since) {
				continue
			}
			cands = append(cands, cand{p, info.Size(), info.ModTime()})
		}
	}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() && valid(e.Name()) {
				scan(filepath.Join(root, e.Name()))
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(root, LowResDir)); err == nil {
		for _, e := range entries {
			if id, ok := strings.CutSuffix(e.Name(), LowResSuffix); e.IsDir() && ok && valid(id) {
				scan(filepath.Join(root, LowResDir, e.Name()))
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })

	var res WarmResult
	for _, c := range cands {
		if !l.waitIdle(ctx) || l.root() != root {
			break
		}
		res.Files++
		switch _, how := l.indexFile(c.path, c.size, c.mod, true); how {
		case howCached:
			res.Cached++
		case howLoaded:
			res.Loaded++
		case howSkipped:
			res.Skipped++
		default:
			res.Parsed++
		}
		select {
		case <-ctx.Done():
		case <-time.After(WarmPause):
		}
	}
	res.Took = time.Since(start)
	return res
}

// waitIdle waits until no request is indexing; false when ctx ended.
func (l *Library) waitIdle(ctx context.Context) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		l.mu.Lock()
		busy := l.busy
		l.mu.Unlock()
		if busy == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(WarmPause):
		}
	}
}

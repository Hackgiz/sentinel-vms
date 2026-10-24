package playback

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Library lists a camera's recording segments and keeps their indexes, so a
// timeline or playlist request only parses files that are new or still
// growing.
type Library struct {
	Root string         // <data>/recordings
	Loc  *time.Location // time zone of the file names; nil = time.Local

	mu       sync.Mutex
	cache    map[string]*cachedIndex
	frags    int // fragments held by cache
	tick     int64
	inflight map[string]chan struct{} // paths being indexed right now
	busy     int                      // of those, by requests (not the warm-up)

	stats libraryStats
}

// libraryStats counts how indexes were obtained (tests, logs).
type libraryStats struct {
	parsed   atomic.Int64 // whole segment parsed from the start
	extended atomic.Int64 // parse resumed (segment grew)
	loaded   atomic.Int64 // read from a sidecar, as is
	saved    atomic.Int64 // sidecars written
}

func fragCount(ix *Index) int {
	if ix == nil {
		return 0
	}
	return len(ix.Fragments)
}

type cachedIndex struct {
	size  int64
	mod   time.Time
	ix    *Index
	used  int64
	saved int64 // segment size the sidecar on disk describes (0: none)
}

// Recording tiers (WAVE22-RECORDING-MODES.md §1). Full-res footage lives in
// <root>/<cameraID>/; a dual-mode camera's low-res 24/7 sub-stream in
// <root>/.lowres/<cameraID>-sub/ (MediaMTX's %path for the sub-stream).
// <root>/.buffer/ holds the events-mode rolling buffer and is never played.
const (
	LowResDir    = ".lowres"
	LowResSuffix = "-sub"
	BufferDir    = ".buffer"
)

// Quality values of a timeline span (link.Span.Quality).
const (
	QualityFull = "full"
	QualityLow  = "low"
)

// The cache is bounded by the fragments it holds (~48 bytes each), not by
// files: a 15-min segment has ~900, an events-mode 60 s clip ~60, so a day of
// either tier, or both, fits. maxCached is a backstop on the entry count.
const (
	maxCachedFragments = 600 * 900
	maxCached          = 6000
)

// Files returns the segments of cameraID that may overlap [from, to), oldest
// first, with their indexes loaded: the full-res ones and the low-res ones
// (File.Low). At the same start a full-res file sorts first.
func (l *Library) Files(cameraID string, from, to time.Time) ([]File, error) {
	out, err := l.files(cameraID, false, from, to)
	if err != nil {
		return nil, err
	}
	low, err := l.files(cameraID, true, from, to)
	if err != nil {
		return nil, err
	}
	out = append(out, low...)
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		return !out[i].Low && out[j].Low
	})
	return out, nil
}

func (l *Library) files(cameraID string, low bool, from, to time.Time) ([]File, error) {
	dir := l.dir(cameraID, low)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []File{}, nil
		}
		return nil, err
	}
	loc := l.Loc
	if loc == nil {
		loc = time.Local
	}
	out := []File{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		start, ok := ParseName(e.Name(), loc)
		if !ok || !start.Before(to) {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, e.Name())) // follows links (recordings imported from a Mac)
		if err != nil || info.Size() == 0 || !info.Mode().IsRegular() {
			continue
		}
		// mtime is when the segment was last written, i.e. about its end.
		if info.ModTime().Add(time.Minute).Before(from) {
			continue
		}
		out = append(out, File{Name: e.Name(), Start: start, Size: info.Size(), ModTime: info.ModTime(), Low: low})
	}
	l.indexAll(dir, out)
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// indexParallel bounds how many of one listing's files are indexed at once.
// A day is ~100 files per camera; on a cold start each costs a few small
// reads (sidecar, segment head), which a spinning or USB disk serves much
// faster when it can reorder several at a time than one after another.
const indexParallel = 6

// indexAll fills in each file's index, several at a time.
func (l *Library) indexAll(dir string, files []File) {
	sem := make(chan struct{}, indexParallel)
	var wg sync.WaitGroup
	for i := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(f *File) {
			defer wg.Done()
			defer func() { <-sem }()
			f.Index = l.index(filepath.Join(dir, f.Name), f.Size, f.ModTime)
		}(&files[i])
	}
	wg.Wait()
}

// File returns one segment with its index (for serving pieces of it), from
// the low-res tier when low.
func (l *Library) File(cameraID, name string, low bool) (*File, error) {
	loc := l.Loc
	if loc == nil {
		loc = time.Local
	}
	start, ok := ParseName(name, loc)
	if !ok {
		return nil, os.ErrNotExist
	}
	path := l.Path(cameraID, name, low)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	f := &File{Name: name, Start: start, Size: info.Size(), ModTime: info.ModTime(), Low: low}
	f.Index = l.index(path, info.Size(), info.ModTime())
	return f, nil
}

// Path is where a camera's segment lives on disk (low: the low-res tier).
// cameraID is the camera's id, never a "-sub" path name: the folder is
// mapped here.
func (l *Library) Path(cameraID, name string, low bool) string {
	return filepath.Join(l.dir(cameraID, low), name)
}

// PathOf is where f (listed by Files for cameraID) lives on disk.
func (l *Library) PathOf(cameraID string, f *File) string { return l.Path(cameraID, f.Name, f.Low) }

func (l *Library) dir(cameraID string, low bool) string {
	return TierDir(l.root(), cameraID, low)
}

// TierDir is a camera's folder of one tier under a recordings root.
func TierDir(root, cameraID string, low bool) string {
	if low {
		return filepath.Join(root, LowResDir, cameraID+LowResSuffix)
	}
	return filepath.Join(root, cameraID)
}

func (l *Library) root() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Root
}

// SetRoot points the library at another recordings folder and forgets the
// cached indexes (they are keyed by path, so they would be stale anyway).
func (l *Library) SetRoot(dir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Root = dir
	l.cache, l.frags = nil, 0
}

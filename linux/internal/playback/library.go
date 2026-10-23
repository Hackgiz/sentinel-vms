package playback

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Library lists a camera's recording segments and keeps their indexes, so a
// timeline or playlist request only parses files that are new or still
// growing.
type Library struct {
	Root string         // <data>/recordings
	Loc  *time.Location // time zone of the file names; nil = time.Local

	mu    sync.Mutex
	cache map[string]*cachedIndex
	tick  int64
}

type cachedIndex struct {
	size int64
	mod  time.Time
	ix   *Index
	used int64
}

// maxCached bounds memory: ~900 fragments × 48 bytes per 15-min segment.
const maxCached = 600

// Files returns the segments of cameraID that may overlap [from, to), oldest
// first, with their indexes loaded.
func (l *Library) Files(cameraID string, from, to time.Time) ([]File, error) {
	dir := filepath.Join(l.root(), cameraID)
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
		f := File{Name: e.Name(), Start: start, Size: info.Size(), ModTime: info.ModTime()}
		f.Index = l.index(filepath.Join(dir, e.Name()), info.Size(), info.ModTime())
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// File returns one segment with its index (for serving pieces of it).
func (l *Library) File(cameraID, name string) (*File, error) {
	loc := l.Loc
	if loc == nil {
		loc = time.Local
	}
	start, ok := ParseName(name, loc)
	if !ok {
		return nil, os.ErrNotExist
	}
	info, err := os.Stat(l.Path(cameraID, name))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	f := &File{Name: name, Start: start, Size: info.Size(), ModTime: info.ModTime()}
	f.Index = l.index(l.Path(cameraID, name), info.Size(), info.ModTime())
	return f, nil
}

// Path is where a camera's segment lives on disk.
func (l *Library) Path(cameraID, name string) string { return filepath.Join(l.root(), cameraID, name) }

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
	l.cache = nil
}

// index returns the cached index, extends it if the file grew (a segment
// being recorded), or parses the file. nil when it can't be read as fMP4.
func (l *Library) index(path string, size int64, mod time.Time) *Index {
	l.mu.Lock()
	if l.cache == nil {
		l.cache = map[string]*cachedIndex{}
	}
	l.tick++
	c := l.cache[path]
	if c != nil {
		c.used = l.tick
	}
	l.mu.Unlock()
	if c != nil && c.size == size && c.mod.Equal(mod) {
		return c.ix
	}

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var ix *Index
	if c != nil && c.ix != nil && size > c.size && !mod.Before(c.mod) {
		// Grown: extend a copy (readers may still hold the cached one).
		cp := *c.ix
		cp.Fragments = append(make([]Fragment, 0, len(c.ix.Fragments)+64), c.ix.Fragments...)
		if cp.Extend(f, size) == nil {
			ix = &cp
		}
	}
	if ix == nil {
		ix, _ = ReadIndex(f, size)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.cache[path] = &cachedIndex{size: size, mod: mod, ix: ix, used: l.tick}
	if len(l.cache) > maxCached {
		var oldest string
		var min int64 = -1
		for k, v := range l.cache {
			if min < 0 || v.used < min {
				oldest, min = k, v.used
			}
		}
		delete(l.cache, oldest)
	}
	return ix
}

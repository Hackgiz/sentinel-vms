package playback

// WAVE23 stream M: index sidecars and the warm-up.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// settled writes a segment whose last write was an hour ago (so its index
// is due for a sidecar) and returns its path.
func settled(t *testing.T, dir string, start time.Time, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, writeSeg(t, dir, start, data))
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	return p
}

func fileIndex(t *testing.T, l *Library, p string) *Index {
	t.Helper()
	f, err := l.File("CAM", filepath.Base(p), false)
	if err != nil {
		t.Fatal(err)
	}
	return f.Index
}

type counts struct{ parsed, extended, loaded, saved int64 }

func statsOf(l *Library) counts {
	return counts{l.stats.parsed.Load(), l.stats.extended.Load(), l.stats.loaded.Load(), l.stats.saved.Load()}
}

func TestSidecarRoundTrip(t *testing.T) {
	l, dir := lib(t)
	data := SynthSegmentPCM(30, 2) // two tracks, one the web player drops
	p := settled(t, dir, t0, data)
	want := fileIndex(t, l, p)
	if got := statsOf(l); got != (counts{parsed: 1, saved: 1}) {
		t.Fatalf("first open %+v", got)
	}
	if _, err := os.Stat(SidecarPath(p)); err != nil {
		t.Fatal("no sidecar written:", err)
	}

	// Restart: a new library reads the sidecar instead of the segment.
	l2 := &Library{Root: l.Root, Loc: time.UTC}
	got := fileIndex(t, l2, p)
	if s := statsOf(l2); s != (counts{loaded: 1}) {
		t.Fatalf("after restart %+v", s)
	}
	if !reflect.DeepEqual(got.Fragments, want.Fragments) || got.InitSize != want.InitSize || got.Timescale != want.Timescale ||
		got.parsed != want.parsed || got.WebFiltered() != want.WebFiltered() || !reflect.DeepEqual(got.tracks, want.tracks) {
		t.Fatalf("loaded index differs:\n%+v\n%+v", got, want)
	}
	if !got.WebFiltered() {
		t.Fatal("the PCM track is no longer filtered for browsers")
	}
	r := bytes.NewReader(data)
	a, err1 := want.WebInit(r)
	b, err2 := got.WebInit(r)
	if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
		t.Fatalf("web init differs: %v %v", err1, err2)
	}
	// And it cuts the same pieces.
	plain := initSection()
	for i := 0; i < 12; i++ {
		key := -1
		if i%2 == 1 {
			key = 10
		}
		plain = append(plain, fragmentKeyAt(uint32(i+1), uint64(i)*90000, 25, key)...)
	}
	q := settled(t, dir, t0.Add(time.Hour), plain)
	want = fileIndex(t, l, q)
	got = fileIndex(t, &Library{Root: l.Root, Loc: time.UTC}, q)
	r = bytes.NewReader(plain)
	a, err1 = want.Cut(r, 1, 5, true)
	b, err2 = got.Cut(r, 1, 5, true)
	if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
		t.Fatalf("pieces differ: %v %v", err1, err2)
	}
	// Loaded as is: nothing to write back.
	fileIndex(t, l2, p)
	if s := statsOf(l2); s.saved != 0 || s.loaded != 1 {
		t.Fatalf("re-saved a sidecar that was current %+v", s)
	}
}

func TestSidecarMismatchRebuilds(t *testing.T) {
	cases := map[string]func(t *testing.T, seg string){
		"corrupt": func(t *testing.T, seg string) {
			b, _ := os.ReadFile(SidecarPath(seg))
			b[sidecarHeader+3] ^= 0xff
			_ = os.WriteFile(SidecarPath(seg), b, 0o640)
		},
		"version": func(t *testing.T, seg string) {
			b, _ := os.ReadFile(SidecarPath(seg))
			b[4] = 9
			_ = os.WriteFile(SidecarPath(seg), b, 0o640)
		},
		"truncated sidecar": func(t *testing.T, seg string) {
			b, _ := os.ReadFile(SidecarPath(seg))
			_ = os.WriteFile(SidecarPath(seg), b[:len(b)/2], 0o640)
		},
		"segment replaced": func(t *testing.T, seg string) {
			other := SynthSegment(40, 4) // longer, other keyframes: other head
			_ = os.WriteFile(seg, other, 0o644)
		},
		"segment shorter": func(t *testing.T, seg string) {
			_ = os.WriteFile(seg, SynthSegment(10, 2), 0o644)
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			l, dir := lib(t)
			p := settled(t, dir, t0, SynthSegment(20, 2))
			fileIndex(t, l, p)
			spoil(t, p)
			old := time.Now().Add(-time.Hour)
			_ = os.Chtimes(p, old, old)

			l2 := &Library{Root: l.Root, Loc: time.UTC}
			got := fileIndex(t, l2, p)
			if s := statsOf(l2); s.parsed != 1 || s.loaded != 0 || s.saved != 1 {
				t.Fatalf("stats %+v", s)
			}
			info, _ := os.Stat(p)
			f, _ := os.Open(p)
			defer f.Close()
			fresh, _ := ReadIndex(f, info.Size())
			if !reflect.DeepEqual(got.Fragments, fresh.Fragments) {
				t.Fatal("rebuilt index differs from a fresh parse")
			}
			// The rewritten sidecar is good again.
			l3 := &Library{Root: l.Root, Loc: time.UTC}
			fileIndex(t, l3, p)
			if s := statsOf(l3); s != (counts{loaded: 1}) {
				t.Fatalf("rewritten sidecar not used: %+v", s)
			}
		})
	}
}

// A segment still being written gets no sidecar; one that stopped growing
// (camera offline) does, and when it grows again after a restart the parse
// resumes from the sidecar's end instead of the start.
func TestSidecarIncrementalGrowth(t *testing.T) {
	l, dir := lib(t)
	full := SynthSegment(30, 2)
	half := full[:len(full)/2]
	name := writeSeg(t, dir, t0, half) // mtime now: still being written
	p := filepath.Join(dir, name)
	ix := fileIndex(t, l, p)
	n1 := len(ix.Fragments)
	if _, err := os.Stat(SidecarPath(p)); err == nil {
		t.Fatal("sidecar written for a segment still being recorded")
	}
	// It settles (no write for SettleAge): the next look saves the cached
	// index without reading the segment again.
	settle := SettleAge
	SettleAge = 0
	fileIndex(t, l, p)
	SettleAge = settle
	old := time.Now().Add(-time.Hour)
	if s := statsOf(l); s.parsed != 1 || s.saved != 1 {
		t.Fatalf("settled %+v", s)
	}

	// Restart; the camera came back and the segment grew.
	if err := os.WriteFile(p, full, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	l2 := &Library{Root: l.Root, Loc: time.UTC}
	ix = fileIndex(t, l2, p)
	if s := statsOf(l2); s != (counts{extended: 1}) {
		t.Fatalf("grown after restart %+v", s)
	}
	if len(ix.Fragments) != 30 || n1 >= 30 {
		t.Fatalf("fragments %d → %d", n1, len(ix.Fragments))
	}
	fresh, _ := ReadIndex(bytes.NewReader(full), int64(len(full)))
	if !reflect.DeepEqual(ix.Fragments, fresh.Fragments) {
		t.Fatal("extended index differs from a fresh parse")
	}
	// Finished: saved at its full size.
	_ = os.Chtimes(p, old, old)
	fileIndex(t, l2, p)
	l3 := &Library{Root: l.Root, Loc: time.UTC}
	if ix := fileIndex(t, l3, p); len(ix.Fragments) != 30 || statsOf(l3) != (counts{loaded: 1}) {
		t.Fatalf("final sidecar: %d fragments %+v", len(ix.Fragments), statsOf(l3))
	}
}

// The benchmark the 52 s cold playlist asked for: after a cache reset (a
// restart, or SetRoot) a day-long Files call reads sidecars and parses
// nothing.
func TestSecondOpenReadsSidecarsNotSegments(t *testing.T) {
	l, dir := lib(t)
	const n = 24
	for i := 0; i < n; i++ {
		settled(t, dir, t0.Add(time.Duration(i)*time.Minute), SynthSegment(60, 2))
	}
	// A stray sidecar-looking file and a temp file are not segments.
	_ = os.WriteFile(filepath.Join(dir, "20261010-090000-000000.mp4.idx.junk"), []byte("x"), 0o640)
	_ = os.WriteFile(filepath.Join(dir, ".20261010-090000-000000.mp4.idx-123"), []byte("x"), 0o640)
	from, to := t0.Add(-time.Hour), t0.Add(24*time.Hour)

	began := time.Now()
	files, err := l.Files("CAM", from, to)
	cold := time.Since(began)
	if err != nil || len(files) != n {
		t.Fatalf("files %d %v", len(files), err)
	}
	if s := statsOf(l); s.parsed != n || s.saved != n {
		t.Fatalf("first open %+v", s)
	}
	l.SetRoot(l.Root) // forget the cache
	began = time.Now()
	files2, _ := l.Files("CAM", from, to)
	warm := time.Since(began)
	if s := statsOf(l); s.parsed != n || s.loaded != n {
		t.Fatalf("second open parsed again: %+v", s)
	}
	for i := range files {
		if !reflect.DeepEqual(files[i].Index.Fragments, files2[i].Index.Fragments) {
			t.Fatalf("file %d differs", i)
		}
	}
	t.Logf("%d segments: parse %v, sidecars %v", n, cold, warm)
}

func TestWarmIndexesLastDay(t *testing.T) {
	const cam = "6F1C2D3E-4A5B-4C6D-8E9F-0A1B2C3D4E5F"
	root := t.TempDir()
	mk := func(parts ...string) string {
		d := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	full, low := mk(cam), mk(LowResDir, cam+LowResSuffix)
	buf, other := mk(BufferDir, cam), mk("not-a-camera")
	start := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	recent := []string{
		settled(t, full, start, SynthSegment(10, 2)),
		settled(t, full, start.Add(time.Minute), SynthSegment(10, 2)),
		settled(t, low, start, SynthSegment(10, 2)),
	}
	stale := settled(t, full, start.Add(-48*time.Hour), SynthSegment(10, 2))
	ancient := time.Now().Add(-30 * time.Hour)
	_ = os.Chtimes(stale, ancient, ancient)
	ignored := []string{stale, settled(t, buf, start, SynthSegment(10, 2)), settled(t, other, start, SynthSegment(10, 2))}

	old := WarmPause
	WarmPause = time.Millisecond
	defer func() { WarmPause = old }()
	valid := func(id string) bool { return id == cam }

	l := &Library{Root: root}
	res := l.Warm(context.Background(), 24*time.Hour, valid)
	if res.Files != 3 || res.Parsed != 3 {
		t.Fatalf("warm-up %+v", res)
	}
	for _, p := range recent {
		if _, err := os.Stat(SidecarPath(p)); err != nil {
			t.Errorf("%s: no sidecar", p)
		}
	}
	for _, p := range ignored {
		if _, err := os.Stat(SidecarPath(p)); err == nil {
			t.Errorf("%s: sidecar written", p)
		}
	}
	if res := l.Warm(context.Background(), 24*time.Hour, valid); res.Cached != 3 {
		t.Fatalf("second warm-up %+v", res)
	}
	// After a restart the warm-up only reads sidecars.
	l2 := &Library{Root: root}
	if res := l2.Warm(context.Background(), 24*time.Hour, valid); res.Loaded != 3 || res.Parsed != 0 {
		t.Fatalf("warm-up after restart %+v", res)
	}
	// A file a request is indexing is skipped, not indexed twice.
	l3 := &Library{Root: root}
	l3.inflight = map[string]chan struct{}{recent[0]: make(chan struct{})}
	if res := l3.Warm(context.Background(), 24*time.Hour, valid); res.Skipped != 1 || res.Loaded != 2 {
		t.Fatalf("warm-up with a request in flight %+v", res)
	}
	// Cancelled: stops at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := (&Library{Root: root}).Warm(ctx, 24*time.Hour, valid); res.Files != 0 {
		t.Fatalf("cancelled warm-up %+v", res)
	}
}

// Requests for a segment being indexed wait for that result instead of
// parsing it again; the warm-up gives way while a request indexes.
func TestRequestWaitsForInflightIndex(t *testing.T) {
	l, dir := lib(t)
	p := settled(t, dir, t0, SynthSegment(10, 2))
	ch := make(chan struct{})
	l.inflight = map[string]chan struct{}{p: ch}
	done := make(chan *Index)
	go func() { done <- fileIndex(t, l, p) }()
	select {
	case <-done:
		t.Fatal("did not wait for the indexing in flight")
	case <-time.After(50 * time.Millisecond):
	}
	l.mu.Lock()
	delete(l.inflight, p)
	l.mu.Unlock()
	close(ch)
	if ix := <-done; !ix.Playable() {
		t.Fatal("no index after waiting")
	}
}

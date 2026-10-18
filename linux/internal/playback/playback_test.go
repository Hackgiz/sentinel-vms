package playback

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)

func writeSeg(t *testing.T, dir string, start time.Time, data []byte) string {
	t.Helper()
	name := start.Format("20060102-150405") + "-" + start.Format(".000000")[1:] + ".mp4"
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

// MARK: - Tests

func TestParseName(t *testing.T) {
	got, ok := ParseName("20261010-214315-804164.mp4", time.UTC)
	want := time.Date(2026, 10, 10, 21, 43, 15, 804164000, time.UTC)
	if !ok || !got.Equal(want) {
		t.Fatalf("got %v %v", got, ok)
	}
	for _, bad := range []string{"x.mp4", "20261010-214315.mp4", "../20261010-214315-1.mp4", "20261010-214315-1.mp4.tmp"} {
		if _, ok := ParseName(bad, time.UTC); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestReadIndex(t *testing.T) {
	data := segmentFile(10, 2, 900000)
	ix, err := ReadIndex(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if !ix.Playable() || ix.Timescale != 90000 || len(ix.Fragments) != 10 || ix.InitSize != int64(len(initSection())) {
		t.Fatalf("index %+v", ix)
	}
	if ix.Duration() != 10*time.Second || ix.Fragments[0].Time != 10*time.Second {
		t.Fatalf("duration %v first %v", ix.Duration(), ix.Fragments[0].Time)
	}
	for i, f := range ix.Fragments {
		if f.Key != (i%2 == 0) || f.KeyOff != 0 || f.Dur != time.Second {
			t.Fatalf("fragment %d: %+v", i, f)
		}
	}
	last := ix.Fragments[9]
	if last.Offset+last.Size != int64(len(data)) {
		t.Fatalf("last fragment ends at %d, file is %d", last.Offset+last.Size, len(data))
	}
}

func TestReadIndexGrowingFile(t *testing.T) {
	data := segmentFile(6, 1, 0)
	cut := len(data) - 50 // last mdat half written
	ix, err := ReadIndex(bytes.NewReader(data[:cut]), int64(cut))
	if err != nil || len(ix.Fragments) != 5 {
		t.Fatalf("partial: %v %d fragments", err, len(ix.Fragments))
	}
	if err := ix.Extend(bytes.NewReader(data), int64(len(data))); err != nil || len(ix.Fragments) != 6 || ix.Duration() != 6*time.Second {
		t.Fatalf("extended: %v %d fragments", err, len(ix.Fragments))
	}
	// Only the header so far: not playable, not an error.
	init := initSection()
	if ix, err := ReadIndex(bytes.NewReader(init[:20]), 20); err != nil || ix.Playable() {
		t.Fatalf("header only: %v %+v", err, ix)
	}
	if ix, _ := ReadIndex(strings.NewReader("fake mp4 bytes"), 14); ix.Playable() {
		t.Fatal("garbage is playable")
	}
}

func lib(t *testing.T) (*Library, string) {
	root := t.TempDir()
	dir := filepath.Join(root, "CAM")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Library{Root: root, Loc: time.UTC}, dir
}

func TestPlanSpansGapsAndPlaylist(t *testing.T) {
	l, dir := lib(t)
	// 09:00:00–09:01:00, then a 30 s outage, then 09:01:30–09:02:30.
	writeSeg(t, dir, t0, segmentFile(60, 2, 0))
	writeSeg(t, dir, t0.Add(90*time.Second), segmentFile(60, 2, 0))
	from, to := t0.Add(-time.Hour), t0.Add(time.Hour)
	files, err := l.Files("CAM", from, to)
	if err != nil || len(files) != 2 {
		t.Fatalf("files %v %d", err, len(files))
	}
	p := BuildPlan(files, from, to)
	if len(p.Spans) != 2 || !p.Spans[0].Start.Equal(t0) || !p.Spans[0].End.Equal(t0.Add(time.Minute)) ||
		!p.Spans[1].Start.Equal(t0.Add(90*time.Second)) || p.Spans[1].Media != time.Minute || p.Duration() != 2*time.Minute {
		t.Fatalf("spans %+v", p.Spans)
	}
	// Wall ↔ media: the outage has no media time.
	if m, ok := p.MediaAt(t0.Add(100 * time.Second)); !ok || m != 70*time.Second {
		t.Fatalf("media at 09:01:40 = %v %v", m, ok)
	}
	if _, ok := p.MediaAt(t0.Add(75 * time.Second)); ok {
		t.Fatal("gap has media")
	}
	m3u8 := p.M3U8(func(f *File) (string, bool) { return "/recordings/CAM/" + f.Name, true },
		func(f *File, pc Piece) string {
			return fmt.Sprintf("/p/%s?%d-%d-%v", f.Name, pc.First, pc.Last, pc.EndAtKey)
		})
	var sum time.Duration
	for _, line := range strings.Split(m3u8, "\n") {
		if v, ok := strings.CutPrefix(line, "#EXTINF:"); ok {
			d, _ := time.ParseDuration(strings.TrimSuffix(v, ",") + "s")
			if d != minPiece { // keyframes every 2 s → 4 s pieces
				t.Errorf("piece of %v", d)
			}
			sum += d
		}
	}
	if !strings.Contains(m3u8, "/p/"+files[0].Name+"?0-4-true\n") || !strings.Contains(m3u8, "?56-59-false\n") {
		t.Errorf("piece URLs:\n%s", m3u8)
	}
	if sum != 2*time.Minute || strings.Count(m3u8, "#EXT-X-DISCONTINUITY") != 1 || strings.Count(m3u8, "#EXT-X-MAP") != 2 ||
		!strings.Contains(m3u8, "#EXT-X-ENDLIST") || !strings.Contains(m3u8, "#EXT-X-PROGRAM-DATE-TIME:2026-10-10T09:01:30.000Z") {
		t.Fatalf("playlist (sum %v):\n%s", sum, m3u8)
	}
}

func TestPlanStartsOnKeyframeAndSplitsStalls(t *testing.T) {
	l, dir := lib(t)
	data := initSection()
	for i := 0; i < 20; i++ { // keyframe every 5 s; decode time jumps 10 s after fragment 9
		base := uint64(i) * 90000
		if i >= 10 {
			base += 10 * 90000
		}
		data = append(data, fragment(uint32(i+1), base, 25, i%5 == 0)...)
	}
	writeSeg(t, dir, t0, data)
	files, _ := l.Files("CAM", t0, t0.Add(time.Hour))
	// From 09:00:07: back up to the keyframe at 09:00:05.
	p := BuildPlan(files, t0.Add(7*time.Second), t0.Add(time.Hour))
	if len(p.Runs) != 2 || !p.Spans[0].Start.Equal(t0.Add(5*time.Second)) || !p.Spans[0].End.Equal(t0.Add(10*time.Second)) ||
		!p.Spans[1].Start.Equal(t0.Add(20*time.Second)) || !p.Spans[1].End.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("spans %+v", p.Spans)
	}
	if c := p.Covered(t0.Add(7*time.Second), t0.Add(25*time.Second)); c != 8*time.Second {
		t.Fatalf("covered %v", c)
	}
	// Byte range of a run covers exactly its fragments.
	off, size := p.Runs[0].ByteRange()
	ix := files[0].Index
	if off != ix.Fragments[5].Offset || off+size != ix.Fragments[9].Offset+ix.Fragments[9].Size {
		t.Fatalf("byte range %d+%d", off, size)
	}
}

// MediaMTX parts rarely start on a keyframe: pieces must still begin on one.
func TestPiecesCutOnMidFragmentKeyframes(t *testing.T) {
	l, dir := lib(t)
	data := initSection()
	for i := 0; i < 12; i++ { // keyframe 10 frames (0.4 s) into every other fragment
		key := -1
		if i%2 == 1 {
			key = 10
		}
		data = append(data, fragmentKeyAt(uint32(i+1), uint64(i)*90000, 25, key)...)
	}
	writeSeg(t, dir, t0, data)
	files, _ := l.Files("CAM", t0, t0.Add(time.Hour))
	ix := files[0].Index
	if !ix.Fragments[1].Key || ix.Fragments[1].KeyOff != 400*time.Millisecond || ix.Fragments[0].Key {
		t.Fatalf("fragments %+v", ix.Fragments[:2])
	}
	// Nothing decodable before 09:00:01.4, so playback starts there.
	p := BuildPlan(files, t0, t0.Add(time.Hour))
	if len(p.Runs) != 1 || !p.Spans[0].Start.Equal(t0.Add(1400*time.Millisecond)) || !p.Spans[0].End.Equal(t0.Add(12*time.Second)) {
		t.Fatalf("spans %+v", p.Spans)
	}
	pc := p.Runs[0].Pieces[0] // keyframe 1.4 s → keyframe 5.4 s
	if pc.First != 1 || pc.Last != 5 || !pc.EndAtKey || pc.Dur != 4*time.Second {
		t.Fatalf("piece %+v", pc)
	}
	f, _ := os.Open(filepath.Join(dir, files[0].Name))
	defer f.Close()
	cut, err := ix.Cut(f, pc.First, pc.Last, pc.EndAtKey)
	if err != nil {
		t.Fatal(err)
	}
	// The cut is a valid fragment: starts on the keyframe, 4 s of video, the
	// frames' own bytes, and the audio of the same 4 s.
	whole := append(initSection(), cut...)
	cix, err := ReadIndex(bytes.NewReader(whole), int64(len(whole)))
	if err != nil || len(cix.Fragments) != 1 {
		t.Fatalf("cut index %v %+v", err, cix)
	}
	cf := cix.Fragments[0]
	if !cf.Key || cf.KeyOff != 0 || cf.Time != 1400*time.Millisecond || cf.Dur != 4*time.Second {
		t.Fatalf("cut fragment %+v", cf)
	}
	tracks, _ := cix.readSamples(cut, 0, 0)
	v, a := tracks[2], tracks[1]
	if len(v.samples) != 100 || len(a.samples) != 8 {
		t.Fatalf("%d video, %d audio samples", len(v.samples), len(a.samples))
	}
	for i, s := range v.samples {
		if got := binary.BigEndian.Uint64(cut[s.off:]); got != uint64(s.dts) || s.dts != int64(126000+i*3600) {
			t.Fatalf("frame %d: dts %d, payload says %d", i, s.dts, got)
		}
	}
	if cut[a.samples[0].off] != 'a' {
		t.Fatal("audio payload")
	}
	// The last piece runs to the end of the recording.
	lp := p.Runs[0].Pieces[len(p.Runs[0].Pieces)-1]
	if lp.EndAtKey || lp.Last != 11 {
		t.Fatalf("last piece %+v", lp)
	}
	if _, err := ix.Cut(f, lp.First, lp.Last, lp.EndAtKey); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Cut(f, 0, 2, true); err == nil { // fragment 0 has no keyframe
		t.Fatal("cut without a keyframe")
	}
}

func TestPlanOutsideWindowAndUnreadableFiles(t *testing.T) {
	l, dir := lib(t)
	writeSeg(t, dir, t0, segmentFile(30, 1, 0))
	writeSeg(t, dir, t0.Add(time.Minute), []byte("not an mp4"))
	files, _ := l.Files("CAM", t0, t0.Add(time.Hour))
	if len(files) != 2 || files[1].Index.Playable() {
		t.Fatalf("files %+v", files)
	}
	if p := BuildPlan(files, t0.Add(31*time.Second), t0.Add(time.Hour)); len(p.Runs) != 0 {
		t.Fatalf("expected nothing after the recording, got %+v", p.Spans)
	}
	if _, err := ClipPlan(files, t0.Add(40*time.Second), t0.Add(50*time.Second)); err != ErrNoRecording {
		t.Fatalf("err %v", err)
	}
	if _, err := ClipPlan(files, t0, t0.Add(31*time.Minute)); err == nil {
		t.Fatal("over-long clip accepted")
	}
	if _, err := ClipPlan(files, t0.Add(time.Second), t0); err == nil {
		t.Fatal("backwards clip accepted")
	}
}

func TestLibraryExtendsGrowingSegment(t *testing.T) {
	l, dir := lib(t)
	full := segmentFile(10, 1, 0)
	name := writeSeg(t, dir, t0, full[:len(full)/2])
	files, _ := l.Files("CAM", t0, t0.Add(time.Hour))
	n1 := len(files[0].Index.Fragments)
	held := files[0].Index
	if err := os.WriteFile(filepath.Join(dir, name), full, 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Second)
	_ = os.Chtimes(filepath.Join(dir, name), later, later)
	files, _ = l.Files("CAM", t0, t0.Add(time.Hour))
	if n1 >= 10 || len(files[0].Index.Fragments) != 10 || len(held.Fragments) != n1 {
		t.Fatalf("fragments %d → %d (held copy %d)", n1, len(files[0].Index.Fragments), len(held.Fragments))
	}
}

func TestWriteRunsAndArgs(t *testing.T) {
	l, dir := lib(t)
	writeSeg(t, dir, t0, segmentFile(30, 5, 0))
	files, _ := l.Files("CAM", t0, t0.Add(time.Hour))
	p, err := ClipPlan(files, t0.Add(12*time.Second), t0.Add(22*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	names, err := writeRuns(l, "CAM", p, work)
	if err != nil || len(names) != 1 {
		t.Fatal(err, names)
	}
	// The cut file is a valid fMP4 that starts on the 09:00:10 keyframe.
	b, _ := os.ReadFile(filepath.Join(work, names[0]))
	ix, err := ReadIndex(bytes.NewReader(b), int64(len(b)))
	if err != nil || !ix.Playable() || !ix.Fragments[0].Key || ix.Fragments[0].Time != 10*time.Second || ix.Duration() != p.Runs[0].Dur() {
		t.Fatalf("cut file: %v %+v", err, ix)
	}
	if !strings.Contains(concatList(names, p), "file 'part-000.mp4'\nduration 12.000\n") {
		t.Fatalf("list:\n%s", concatList(names, p))
	}
	args := strings.Join(ClipArgs("clip.ffconcat", 2*time.Second, 10*time.Second, "out.mp4", true), " ")
	if !strings.Contains(args, "-ss 2.000 -i clip.ffconcat -t 10.000") || !strings.Contains(args, "-c copy") {
		t.Fatalf("args %s", args)
	}
	if args := strings.Join(ClipArgs("l", 0, time.Second, "o", false), " "); !strings.Contains(args, "-an") || strings.Contains(args, "0:a?") {
		t.Fatalf("video-only args %s", args)
	}
}

func TestClipArgsMasked(t *testing.T) {
	// No masks: byte-for-byte the stream-copy command.
	if got, want := strings.Join(ClipArgsMasked("l", time.Second, 5*time.Second, "o.mp4", true, nil), " "), strings.Join(ClipArgs("l", time.Second, 5*time.Second, "o.mp4", true), " "); got != want {
		t.Fatalf("unmasked args differ:\n%s\n%s", got, want)
	}
	masks := [][4]float64{{0.25, 0.5, 0.75, 1}, {0, 0, 0.1, 0.2}}
	args := strings.Join(ClipArgsMasked("l", 0, time.Second, "o.mp4", true, masks), " ")
	for _, want := range []string{
		"drawbox=x=iw*0.25000:y=ih*0.50000:w=iw*0.50000:h=ih*0.50000:color=black:t=fill,drawbox=x=iw*0.00000:y=ih*0.00000:w=iw*0.10000:h=ih*0.20000:color=black:t=fill",
		"-c:v libx264", "-c:a copy", "-map 0:a?",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("masked args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "-c copy") {
		t.Fatalf("masked export must re-encode:\n%s", args)
	}
	if args := strings.Join(ClipArgsMasked("l", 0, time.Second, "o.mp4", false, masks), " "); !strings.Contains(args, "-an") || strings.Contains(args, "0:a?") {
		t.Fatalf("masked video-only args %s", args)
	}
}

// Optional check against real MediaMTX output:
// SENTINEL_REAL_RECORDINGS=/path/to/recordings/<camera> go test ./internal/playback -run Real -v
func TestRealRecordings(t *testing.T) {
	dir := os.Getenv("SENTINEL_REAL_RECORDINGS")
	if dir == "" {
		t.Skip("set SENTINEL_REAL_RECORDINGS to a camera's recordings directory")
	}
	l := &Library{Root: filepath.Dir(dir)}
	files, err := l.Files(filepath.Base(dir), time.Unix(0, 0), time.Now().Add(time.Hour))
	if err != nil || len(files) == 0 {
		t.Fatal(err, len(files))
	}
	for _, f := range files {
		keys := 0
		for _, fr := range f.Index.Fragments {
			if fr.Key {
				keys++
			}
		}
		t.Logf("%s: playable=%v init=%d timescale=%d fragments=%d keyframes=%d duration=%v (mtime says %v)",
			f.Name, f.Index.Playable(), f.Index.InitSize, f.Index.Timescale, len(f.Index.Fragments), keys, f.Index.Duration(), f.ModTime.Sub(f.Start).Round(time.Millisecond))
	}
	p := BuildPlan(files, files[0].Start, time.Now().Add(time.Hour))
	for _, s := range p.Spans {
		t.Logf("span %s – %s media %v", s.Start.Format("15:04:05.000"), s.End.Format("15:04:05.000"), s.Media)
	}
	ffmpeg := os.Getenv("SENTINEL_TEST_FFMPEG")
	if ffmpeg == "" || len(files) < 2 {
		return
	}
	// A clip across the first segment boundary, and a snapshot.
	from := files[1].Start.Add(-5500 * time.Millisecond)
	out := filepath.Join(t.TempDir(), "clip.mp4")
	if err := Export(t.Context(), ffmpeg, l, filepath.Base(dir), files, from, from.Add(10*time.Second), t.TempDir(), out); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(out)
	t.Logf("clip %s – %s: %d bytes at %s", from.Format("15:04:05.000"), from.Add(10*time.Second).Format("15:04:05.000"), info.Size(), out)
	jpg, err := Snapshot(t.Context(), ffmpeg, l, filepath.Base(dir), files, from.Add(3*time.Second), t.TempDir())
	if err != nil || len(jpg) < 1000 || jpg[0] != 0xFF || jpg[1] != 0xD8 {
		t.Fatalf("snapshot: %v (%d bytes)", err, len(jpg))
	}
	if keep := os.Getenv("SENTINEL_TEST_KEEP"); keep != "" {
		b, _ := os.ReadFile(out)
		_ = os.WriteFile(filepath.Join(keep, "clip.mp4"), b, 0o644)
		_ = os.WriteFile(filepath.Join(keep, "snapshot.jpg"), jpg, 0o644)
	}
}

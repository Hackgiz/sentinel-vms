package playback

// WAVE24: exports whose range holds both recording tiers.

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initSized is initSection with a w×h video sample entry; audioHandler is
// "soun" for a track VideoInfo counts as audio, anything else for none.
func initSized(w, h int, audioHandler string) []byte {
	visual := make([]byte, 0, 78)
	visual = append(visual, make([]byte, 6)...)               // reserved
	visual = binary.BigEndian.AppendUint16(visual, 1)         // data reference index
	visual = append(visual, make([]byte, 16)...)              // pre-defined, reserved
	visual = binary.BigEndian.AppendUint16(visual, uint16(w)) // width
	visual = binary.BigEndian.AppendUint16(visual, uint16(h)) // height
	visual = append(visual, make([]byte, 78-len(visual))...)  // resolution, frame count, compressor, depth
	trak := func(id uint32, handler string, timescale uint32, entry []byte) []byte {
		stsd := box("stsd", u32(0, 1), entry)
		minf := box("minf", box("stbl", stsd))
		tkhd := box("tkhd", u32(0, 0, 0, id, 0))
		mdhd := box("mdhd", u32(0, 0, 0, timescale, 0))
		hdlr := box("hdlr", u32(0, 0), []byte(handler), make([]byte, 13))
		return box("trak", tkhd, box("mdia", mdhd, hdlr, minf))
	}
	mvex := box("mvex", box("trex", u32(0, 1, 1, 1024, 0, 0)), box("trex", u32(0, 2, 1, 0, 0, 0x10000)))
	return append(box("ftyp", []byte("iso5"), u32(512), []byte("iso5iso6mp41")),
		box("moov", box("mvhd", make([]byte, 100)),
			trak(1, audioHandler, 48000, box("mp4a", make([]byte, 8))),
			trak(2, "vide", 90000, box("avc1", visual)), mvex)...)
}

func sizedSegment(w, h int, audioHandler string, n, gop int) []byte {
	b := initSized(w, h, audioHandler)
	for i := 0; i < n; i++ {
		b = append(b, fragment(uint32(i+1), uint64(i)*90000, 25, i%gop == 0)...)
	}
	return b
}

func TestVideoInfo(t *testing.T) {
	w, h, audio := VideoInfo(initSized(1920, 1080, "soun"))
	if w != 1920 || h != 1080 || !audio {
		t.Fatalf("got %dx%d audio=%v", w, h, audio)
	}
	if _, _, audio := VideoInfo(initSized(640, 360, "meta")); audio {
		t.Fatal("no audio track expected")
	}
	if w, h, _ := VideoInfo(initSection()); w != 0 || h != 0 {
		t.Fatalf("an init without a visual size should give 0x0, got %dx%d", w, h)
	}
	if w, h, a := VideoInfo([]byte("junk")); w != 0 || h != 0 || a {
		t.Fatal("junk")
	}
}

func TestMixedSize(t *testing.T) {
	sizes := [][2]int{{640, 360}, {1920, 1080}, {640, 360}}
	if w, h := MixedSize(sizes, false); w != 1920 || h != 1080 {
		t.Fatalf("default %dx%d", w, h)
	}
	if w, h := MixedSize(sizes, true); w != 640 || h != 360 {
		t.Fatalf("small %dx%d", w, h)
	}
	if w, h := MixedSize([][2]int{{641, 361}}, false); w != 640 || h != 360 {
		t.Fatalf("even %dx%d", w, h)
	}
	if w, h := MixedSize([][2]int{{640, 360}, {0, 0}}, false); w != 0 || h != 0 {
		t.Fatalf("unknown size %dx%d", w, h)
	}
}

func TestPlanMixed(t *testing.T) {
	l, full, low, _ := tierLib(t)
	writeSeg(t, low, t0, sizedSegment(640, 360, "soun", 60, 2))
	writeSeg(t, full, t0.Add(20*time.Second), sizedSegment(1920, 1080, "soun", 20, 2))
	files, err := l.Files("CAM", t0.Add(-time.Hour), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ClipPlan(files, t0, t0.Add(time.Minute))
	if err != nil || !p.Mixed() || len(p.Runs) != 3 {
		t.Fatalf("mixed range: %v mixed=%v runs=%d", err, p.Mixed(), len(p.Runs))
	}
	for name, rng := range map[string][2]time.Duration{"full only": {22 * time.Second, 38 * time.Second}, "low only": {45 * time.Second, 58 * time.Second}} {
		p, err := ClipPlan(files, t0.Add(rng[0]), t0.Add(rng[1]))
		if err != nil || p.Mixed() {
			t.Fatalf("%s: err=%v mixed=%v", name, err, p.Mixed())
		}
	}
}

func TestMixedRunArgs(t *testing.T) {
	enc := []string{"-c:v", "libx264"}
	s := strings.Join(MixedRunArgs("in.mp4", 2*time.Second, 10*time.Second, 1920, 1080, true, enc, "out.mp4"), " ")
	for _, want := range []string{"-ss 2.000 -i in.mp4 -t 10.000", "-c:a aac", "scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2", "-c:v libx264", "-pix_fmt yuv420p", "-video_track_timescale 90000"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
	if s := strings.Join(MixedRunArgs("in.mp4", 0, time.Second, 640, 360, false, enc, "o.mp4"), " "); !strings.Contains(s, "-an") || strings.Contains(s, "aac") {
		t.Fatalf("no audio expected: %s", s)
	}
}

// With both tiers in the range the export re-encodes every run to one size;
// with one tier it stays a stream copy. ffmpeg is a script that logs its calls.
func TestExportWithMixedTiers(t *testing.T) {
	l, full, low, _ := tierLib(t)
	writeSeg(t, low, t0, sizedSegment(640, 360, "soun", 60, 2))
	writeSeg(t, full, t0.Add(20*time.Second), sizedSegment(1920, 1080, "soun", 20, 2))
	files, _ := l.Files("CAM", t0.Add(-time.Hour), t0.Add(time.Hour))
	from, to := t0.Add(5*time.Second), t0.Add(55*time.Second)

	export := func(from, to time.Time, o ExportOptions, files []File) string {
		t.Helper()
		bin, log := fakeFFmpeg(t, t.TempDir(), fakeWithEncoders)
		out := filepath.Join(t.TempDir(), "o.mp4")
		if err := ExportWith(context.Background(), bin, l, "CAM", files, from, to, t.TempDir(), out, o); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(out); err != nil {
			t.Fatal("no output file")
		}
		b, _ := os.ReadFile(log)
		return string(b)
	}

	calls := export(from, to, ExportOptions{}, files)
	if n := strings.Count(calls, "scale=1920:1080:"); n != 3 {
		t.Fatalf("want 3 runs scaled to 1920x1080, got %d:\n%s", n, calls)
	}
	if !strings.Contains(calls, "-c:a aac") || !strings.Contains(calls, "-c copy") {
		t.Fatalf("want audio kept and a stream-copy join:\n%s", calls)
	}
	// The first run is cut from the keyframe at 4 s, so 1 s is skipped; the last ends at the range end (15 s).
	if !strings.Contains(calls, "-ss 1.000 -i") {
		t.Fatalf("first run not trimmed to the range start:\n%s", calls)
	}

	calls = export(from, to, ExportOptions{PreferSmall: true}, files)
	if n := strings.Count(calls, "scale=640:360:"); n != 3 || strings.Contains(calls, "scale=1920") {
		t.Fatalf("prefer small: want 3 runs scaled to 640x360:\n%s", calls)
	}

	// Masks etc. still apply, on the joined clip.
	calls = export(from, to, ExportOptions{Masks: [][4]float64{maskA}}, files)
	if !strings.Contains(calls, "-vf "+DrawboxFilter([][4]float64{maskA})) {
		t.Fatalf("masks lost on a mixed export:\n%s", calls)
	}

	// A range inside one tier is a stream copy of the camera's own video.
	for _, rng := range [][2]time.Duration{{22 * time.Second, 38 * time.Second}, {45 * time.Second, 58 * time.Second}} {
		calls = export(t0.Add(rng[0]), t0.Add(rng[1]), ExportOptions{}, files)
		if strings.Contains(calls, "scale=") || !strings.Contains(calls, "-c copy") {
			t.Fatalf("single tier should stream copy:\n%s", calls)
		}
	}

	// One part without audio: the whole clip is video only.
	l2, full2, low2, _ := tierLib(t)
	writeSeg(t, low2, t0, sizedSegment(640, 360, "meta", 60, 2))
	writeSeg(t, full2, t0.Add(20*time.Second), sizedSegment(1920, 1080, "soun", 20, 2))
	files2, _ := l2.Files("CAM", t0.Add(-time.Hour), t0.Add(time.Hour))
	bin, log := fakeFFmpeg(t, t.TempDir(), fakeWithEncoders)
	if err := ExportWith(context.Background(), bin, l2, "CAM", files2, from, to, t.TempDir(), filepath.Join(t.TempDir(), "o.mp4"), ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "aac") || !strings.Contains(string(b), "-an") {
		t.Fatalf("audio should be dropped when a part has none:\n%s", b)
	}

	// Unknown sizes (an init without a visual size): fall back to one tier.
	l3, full3, low3, _ := tierLib(t)
	writeSeg(t, low3, t0, segmentFile(60, 2, 0))
	writeSeg(t, full3, t0.Add(20*time.Second), segmentFile(20, 2, 0))
	files3, _ := l3.Files("CAM", t0.Add(-time.Hour), t0.Add(time.Hour))
	bin, log = fakeFFmpeg(t, t.TempDir(), fakeWithEncoders)
	if err := ExportWith(context.Background(), bin, l3, "CAM", files3, from, to, t.TempDir(), filepath.Join(t.TempDir(), "o.mp4"), ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(log)
	if strings.Contains(string(b), "scale=") || !strings.Contains(string(b), "-c copy") {
		t.Fatalf("unknown sizes should fall back to the one-tier copy:\n%s", b)
	}
}

// With a real ffmpeg (on PATH, or SENTINEL_TEST_FFMPEG) a low-res and a
// full-res recording become one MP4 of the full-res size.
func TestExportMixedTiersRealFFmpeg(t *testing.T) {
	ff := os.Getenv("SENTINEL_TEST_FFMPEG")
	if ff == "" {
		p, err := exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg not found")
		}
		ff = p
	}
	l, full, low, _ := tierLib(t)
	src := func(dir string, start time.Time, size string, secs string) {
		t.Helper()
		tmp := filepath.Join(t.TempDir(), "src.mp4")
		if err := runFFmpeg(context.Background(), ff, []string{"-nostdin", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc=size=" + size + ":rate=10", "-t", secs, "-pix_fmt", "yuv420p",
			"-c:v", MaskEncoderArgs(context.Background(), ff)[1], "-g", "10", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-y", tmp}); err != nil {
			t.Skipf("cannot make a source clip with this ffmpeg: %v", err)
		}
		b, err := os.ReadFile(tmp)
		if err != nil {
			t.Fatal(err)
		}
		writeSeg(t, dir, start, b)
	}
	src(low, t0, "320x180", "12")
	src(full, t0.Add(4*time.Second), "640x360", "4")
	files, err := l.Files("CAM", t0.Add(-time.Minute), t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := ClipPlan(files, t0, t0.Add(12*time.Second)); err != nil || !p.Mixed() {
		t.Skipf("fixture does not mix tiers: %v", err)
	}
	out := filepath.Join(t.TempDir(), "o.mp4")
	if err := ExportWith(context.Background(), ff, l, "CAM", files, t0, t0.Add(12*time.Second), t.TempDir(), out, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd := exec.Command(ff, "-hide_banner", "-i", out)
	cmd.Stderr = &stderr
	_ = cmd.Run() // no output file given: ffmpeg exits non-zero after printing the streams
	if !strings.Contains(stderr.String(), "640x360") || strings.Contains(stderr.String(), "320x180") {
		t.Fatalf("export is not one 640x360 video:\n%s", stderr.String())
	}
}

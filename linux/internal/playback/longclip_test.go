package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// keyframeStarts returns the wall times where the plan's pieces begin.
func keyframeStarts(files []File, from, to time.Time) map[time.Time]bool {
	out := map[time.Time]bool{}
	p := BuildPlan(files, from, to)
	for _, r := range p.Runs {
		for _, pc := range r.Pieces {
			out[pc.Wall] = true
		}
	}
	return out
}

func TestSplitRangeKeyframeAligned(t *testing.T) {
	l, dir := lib(t)
	writeSeg(t, dir, t0, segmentFile(120, 2, 0)) // 09:00–09:02, keyframe every 2 s → 4 s pieces
	files, _ := l.Files("CAM", t0, t0.Add(time.Hour))
	from, to := t0.Add(3*time.Second), t0.Add(110*time.Second)

	plan, err := SplitRange(files, from, to, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	keys := keyframeStarts(files, from, to)
	if len(plan.Pieces) < 5 {
		t.Fatalf("expected several chunks, got %+v", plan.Pieces)
	}
	if !plan.Pieces[0].From.Equal(from) || !plan.Pieces[len(plan.Pieces)-1].To.Equal(to) {
		t.Fatalf("outer bounds %v .. %v", plan.Pieces[0].From, plan.Pieces[len(plan.Pieces)-1].To)
	}
	for i, pc := range plan.Pieces {
		if pc.To.Sub(pc.From) > 20*time.Second || !pc.To.After(pc.From) {
			t.Errorf("chunk %d is %v long", i, pc.To.Sub(pc.From))
		}
		if i > 0 {
			prev := plan.Pieces[i-1]
			if !prev.To.Equal(pc.From) {
				t.Errorf("chunk %d starts %v, previous ended %v: frames lost or repeated", i, pc.From, prev.To)
			}
			if !keys[pc.From] {
				t.Errorf("boundary %v is not a keyframe", pc.From)
			}
		}
	}
	if plan.EstBytes <= 0 {
		t.Error("no size estimate")
	}
}

func TestSplitRangeGapsAndEmpty(t *testing.T) {
	l, dir := lib(t)
	// Two minutes recorded, a 10-minute outage, two more minutes.
	writeSeg(t, dir, t0, segmentFile(120, 2, 0))
	writeSeg(t, dir, t0.Add(12*time.Minute), segmentFile(120, 2, 0))
	files, _ := l.Files("CAM", t0, t0.Add(time.Hour))
	from, to := t0, t0.Add(14*time.Minute)

	// A budget shorter than the outage ends a chunk where the recording stops.
	plan, err := SplitRange(files, from, to, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pieces) != 2 || !plan.Pieces[0].To.Equal(t0.Add(2*time.Minute)) || !plan.Pieces[1].From.Equal(t0.Add(12*time.Minute)) {
		t.Fatalf("chunks %+v", plan.Pieces)
	}
	// A budget over the outage keeps one chunk (the gap is closed up).
	if plan, err = SplitRange(files, from, to, 20*time.Minute); err != nil || len(plan.Pieces) != 1 {
		t.Fatalf("%v %+v", err, plan.Pieces)
	}
	if _, err := SplitRange(files, t0.Add(3*time.Minute), t0.Add(8*time.Minute), time.Minute); !errors.Is(err, ErrNoRecording) {
		t.Fatalf("nothing recorded: %v", err)
	}
	if _, err := SplitRange(files, to, from, time.Minute); err == nil {
		t.Fatal("reversed range accepted")
	}
}

func TestCheckDiskRefusal(t *testing.T) {
	if err := CheckDisk(100, 199); !errors.Is(err, ErrDiskSpace) {
		t.Fatalf("199 free for 100 est: %v", err)
	}
	if err := CheckDisk(100, 200); err != nil {
		t.Fatalf("exactly 2x: %v", err)
	}
	l, dir := lib(t)
	writeSeg(t, dir, t0, segmentFile(60, 2, 0))
	files, _ := l.Files("CAM", t0, t0.Add(time.Hour))
	_, err := PreflightLong(files, t0, t0.Add(time.Minute), t.TempDir(), func(string) (uint64, error) { return 10, nil })
	if !errors.Is(err, ErrDiskSpace) {
		t.Fatalf("preflight with 10 bytes free: %v", err)
	}
	if _, err := PreflightLong(files, t0, t0.Add(time.Minute), t.TempDir(), func(string) (uint64, error) { return 1 << 40, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := PreflightLong(files, t0, t0.Add(MaxLongExport+time.Second), t.TempDir(), nil); !errors.Is(err, ErrTooLong) {
		t.Fatalf("over 12 h: %v", err)
	}
}

func TestConcatArgsAndList(t *testing.T) {
	args := strings.Join(ConcatArgs("join.ffconcat", "out.mp4", true), " ")
	for _, want := range []string{"-f concat -safe 0 -i join.ffconcat", "-map 0:a?", "-c copy", "-y out.mp4"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if a := strings.Join(ConcatArgs("l", "o", false), " "); !strings.Contains(a, "-an") || strings.Contains(a, "0:a?") {
		t.Errorf("video-only args %s", a)
	}
	if got := ConcatList([]string{"piece-000.mp4", "piece-001.mp4"}); got != "ffconcat version 1.0\nfile 'piece-000.mp4'\nfile 'piece-001.mp4'\n" {
		t.Errorf("list %q", got)
	}
}

// fakeFFmpeg is a shell script standing in for ffmpeg: it appends its
// arguments to <dir>/calls.log and writes bytes to the output (last
// argument). SLOW=1 makes it sleep instead, for cancellation tests.
func fakeFFmpeg(t *testing.T, dir, body string) (bin, log string) {
	t.Helper()
	bin, log = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$@\" >> '" + log + "'\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

const writeOutput = `for last; do :; done
echo fake-mp4 > "$last"`

func longSetup(t *testing.T) (*Library, []File, time.Time, time.Time) {
	l, dir := lib(t)
	// 70 minutes of footage in two contiguous 35-minute segments.
	writeSeg(t, dir, t0, segmentFile(35*60, 60, 0))
	writeSeg(t, dir, t0.Add(35*time.Minute), segmentFile(35*60, 60, 0))
	from, to := t0, t0.Add(70*time.Minute)
	files, err := l.Files("CAM", from, to)
	if err != nil || len(files) != 2 {
		t.Fatal(err, len(files))
	}
	return l, files, from, to
}

func TestLongExportConcatsPiecesAndCleans(t *testing.T) {
	l, files, from, to := longSetup(t)
	bin, log := fakeFFmpeg(t, t.TempDir(), writeOutput)
	work, out := t.TempDir(), filepath.Join(t.TempDir(), "long.mp4")
	var steps [][2]int
	err := LongExport(context.Background(), LongOptions{
		FFmpeg: bin, Lib: l, CameraID: "CAM", Files: files, From: from, To: to, WorkDir: work, Out: out,
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
		Progress:  func(d, n int) { steps = append(steps, [2]int{d, n}) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "fake-mp4\n" {
		t.Fatalf("output %q", b)
	}
	calls, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	// 70 min at a 30-min cap: 3 chunks (30, 30, 10), then one join.
	if len(lines) != 4 {
		t.Fatalf("%d ffmpeg calls:\n%s", len(lines), calls)
	}
	join := lines[3]
	if !strings.Contains(join, "-f concat") || !strings.Contains(join, "join.ffconcat") || !strings.Contains(join, "-c copy") || !strings.HasSuffix(join, out) {
		t.Fatalf("join call: %s", join)
	}
	// Progress ends at total/total and counts pieces done, then the join.
	if len(steps) == 0 || steps[0] != [2]int{0, 4} || steps[len(steps)-1] != [2]int{4, 4} || len(steps) != 5 {
		t.Fatalf("progress %v", steps)
	}
	if left, _ := os.ReadDir(work); len(left) != 0 {
		t.Fatalf("temp pieces left behind: %v", left)
	}
}

func TestLongExportRefusesOnDisk(t *testing.T) {
	l, files, from, to := longSetup(t)
	bin, log := fakeFFmpeg(t, t.TempDir(), writeOutput)
	out := filepath.Join(t.TempDir(), "long.mp4")
	err := LongExport(context.Background(), LongOptions{
		FFmpeg: bin, Lib: l, CameraID: "CAM", Files: files, From: from, To: to, WorkDir: t.TempDir(), Out: out,
		FreeBytes: func(string) (uint64, error) { return 1024, nil },
	})
	if !errors.Is(err, ErrDiskSpace) {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("ffmpeg ran despite the disk refusal")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("output exists")
	}
}

func TestLongExportCancelCleansUp(t *testing.T) {
	l, files, from, to := longSetup(t)
	bin, _ := fakeFFmpeg(t, t.TempDir(), "exec sleep 30")
	work, out := t.TempDir(), filepath.Join(t.TempDir(), "long.mp4")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	err := LongExport(ctx, LongOptions{
		FFmpeg: bin, Lib: l, CameraID: "CAM", Files: files, From: from, To: to, WorkDir: work, Out: out,
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("cancel did not stop ffmpeg")
	}
	if left, _ := os.ReadDir(work); len(left) != 0 {
		t.Fatalf("temp pieces left behind: %v", left)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("output left behind")
	}
}

func TestLongExportFailureCleansUp(t *testing.T) {
	l, files, from, to := longSetup(t)
	bin, _ := fakeFFmpeg(t, t.TempDir(), "echo boom >&2; exit 1")
	work, out := t.TempDir(), filepath.Join(t.TempDir(), "long.mp4")
	err := LongExport(context.Background(), LongOptions{
		FFmpeg: bin, Lib: l, CameraID: "CAM", Files: files, From: from, To: to, WorkDir: work, Out: out,
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "piece 1 of 3") {
		t.Fatalf("err %v", err)
	}
	if left, _ := os.ReadDir(work); len(left) != 0 {
		t.Fatalf("temp pieces left behind: %v", left)
	}
}

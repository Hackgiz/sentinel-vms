package playback

// Long exports (Wave 7, stream C): "give me the whole night". A job splits
// [from, to) into pieces of at most MaxClip that start and end on keyframes,
// cuts each with Export (stream copy, so the evidence is still the camera's
// own encoded video), then joins them with ffmpeg's concat demuxer (stream
// copy again). Temp pieces are always removed; the job is cancelled through
// its context.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// MaxLongExport is the longest range one long export may cover.
const MaxLongExport = 12 * time.Hour

const (
	// pieceTimeout bounds one piece's ffmpeg run, concatTimeout the join.
	pieceTimeout = 5 * time.Minute
	// pieceTimeoutReencode applies when the options re-encode the piece.
	pieceTimeoutReencode = 25 * time.Minute
	concatTimeout        = 30 * time.Minute
	// diskFactor: the pieces and the joined file coexist, so a long export
	// needs this many times its estimated size free.
	diskFactor = 2
	// contiguousSlack: a piece starting this close to the previous piece's
	// end continues it (no recording gap in between).
	contiguousSlack = time.Second
)

var (
	ErrTooLong   = errors.New("a long export can cover at most 12 hours")
	ErrDiskSpace = errors.New("not enough free disk space for this export")
)

// PieceRange is one chunk of a long export: a range Export can cut in one
// go (at most MaxClip). Every boundary between two chunks sits on a
// keyframe, so the chunks join without a repeated or a lost frame.
type PieceRange struct {
	From, To time.Time
}

// LongPlan is a long export laid out.
type LongPlan struct {
	Pieces []PieceRange
	// EstBytes estimates the joined file's size (the recorded bytes inside
	// the range, summed over its keyframe pieces).
	EstBytes int64
}

// SplitRange lays [from, to) out as chunks of at most max, with every
// internal boundary on a keyframe (the start of one of the plan's HLS
// pieces). A recording gap longer than the budget ends a chunk early. It
// fails with ErrNoRecording when nothing was recorded in the range.
func SplitRange(files []File, from, to time.Time, max time.Duration) (LongPlan, error) {
	if !to.After(from) {
		return LongPlan{}, errors.New("the export must end after it starts")
	}
	if max <= 0 {
		max = MaxClip
	}
	p := BuildPlan(files, from, to)
	type pc struct {
		start, end time.Time
	}
	var pcs []pc
	var est int64
	for _, r := range p.Runs {
		for _, x := range r.Pieces {
			pcs = append(pcs, pc{x.Wall, x.Wall.Add(x.Dur)})
			est += x.Size
		}
	}
	if len(pcs) == 0 || p.Covered(from, to) <= 0 {
		return LongPlan{}, ErrNoRecording
	}
	// The end a chunk gets when it stops after piece j: the next piece's
	// start when the recording continues (a keyframe), else the piece's end.
	endAfter := func(j int) time.Time {
		e := pcs[j].end
		if j+1 < len(pcs) {
			if next := pcs[j+1].start; next.Sub(e).Abs() < contiguousSlack {
				e = next
			}
		}
		if e.After(to) {
			e = to
		}
		return e
	}
	var out []PieceRange
	for i := 0; i < len(pcs); {
		start := pcs[i].start
		if start.Before(from) {
			start = from // the first chunk starts exactly at from (Export seeks to it)
		}
		j := i
		for j+1 < len(pcs) && !pcs[j+1].start.After(to) && endAfter(j+1).Sub(start) <= max {
			j++
		}
		end := endAfter(j)
		if end.Sub(start) > max { // one piece longer than the budget: cut it there
			end = start.Add(max)
		}
		if end.After(start) {
			out = append(out, PieceRange{From: start, To: end})
		}
		i = j + 1
	}
	if len(out) == 0 {
		return LongPlan{}, ErrNoRecording
	}
	return LongPlan{Pieces: out, EstBytes: est}, nil
}

// FreeBytes reports the free space of the file system holding dir.
func FreeBytes(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// CheckDisk refuses an export needing more than free allows.
func CheckDisk(estBytes int64, free uint64) error {
	need := uint64(estBytes) * diskFactor
	if free < need {
		return fmt.Errorf("%w: it needs about %s free and only %s is available",
			ErrDiskSpace, humanBytes(need), humanBytes(free))
	}
	return nil
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// PreflightLong checks the range, lays it out and checks the disk at dir
// (where the pieces will be written). LongExport runs it first; callers can
// run it earlier to refuse a job synchronously.
func PreflightLong(files []File, from, to time.Time, dir string, free func(string) (uint64, error)) (LongPlan, error) {
	if !to.After(from) {
		return LongPlan{}, errors.New("the export must end after it starts")
	}
	if to.Sub(from) > MaxLongExport {
		return LongPlan{}, ErrTooLong
	}
	plan, err := SplitRange(files, from, to, MaxClip)
	if err != nil {
		return LongPlan{}, err
	}
	if free == nil {
		free = FreeBytes
	}
	avail, err := free(dir)
	if err != nil {
		return LongPlan{}, fmt.Errorf("checking free disk space: %w", err)
	}
	return plan, CheckDisk(plan.EstBytes, avail)
}

// LongOptions describes one long export.
type LongOptions struct {
	FFmpeg   string
	Lib      *Library
	CameraID string
	Files    []File
	From, To time.Time
	// WorkDir is an empty scratch directory for the pieces; LongExport
	// removes everything it puts there. Out must not be inside it.
	WorkDir string
	Out     string
	// Export carries the Stream D options (masks, watermark, pixelated
	// regions) into every piece; the zero value keeps the stream copy. The
	// pieces are then re-encoded with the same encoder, so the join (stream
	// copy) stays valid.
	Export ExportOptions
	// FreeBytes overrides the free-space probe (tests); nil = FreeBytes.
	FreeBytes func(dir string) (uint64, error)
	// Progress is called after each step with steps done of total (the
	// pieces, then the join).
	Progress func(done, total int)
}

// ConcatArgs is the ffmpeg command joining already-cut pieces: the concat
// demuxer with stream copy (no re-encode).
func ConcatArgs(list, out string, withAudio bool) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-i", list, "-map", "0:v:0"}
	if withAudio {
		args = append(args, "-map", "0:a?")
	} else {
		args = append(args, "-an")
	}
	return append(args, "-dn", "-sn", "-c", "copy", "-movflags", "+faststart", "-f", "mp4", "-y", out)
}

// ConcatList is the ffconcat script joining the piece files (names relative
// to the script's directory).
func ConcatList(names []string) string {
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	for _, n := range names {
		fmt.Fprintf(&b, "file '%s'\n", n)
	}
	return b.String()
}

// LongExport writes [From, To) to Out as one MP4. It refuses ranges over
// MaxLongExport and disks with less than twice the estimated size free,
// cuts each chunk with ExportWith, and joins them. Cancelling ctx stops ffmpeg;
// on any failure Out is removed, and the pieces always are.
func LongExport(ctx context.Context, o LongOptions) (err error) {
	if o.FFmpeg == "" {
		return ErrNoFFmpeg
	}
	plan, err := PreflightLong(o.Files, o.From, o.To, o.WorkDir, o.FreeBytes)
	if err != nil {
		return err
	}
	total := len(plan.Pieces) + 1
	progress := func(done int) {
		if o.Progress != nil {
			o.Progress(done, total)
		}
	}
	var made []string // everything created under WorkDir
	defer func() {
		for _, p := range made {
			_ = os.RemoveAll(p)
		}
		if err != nil {
			_ = os.Remove(o.Out)
		}
	}()
	progress(0)
	var names []string
	for i, pr := range plan.Pieces {
		if err = ctx.Err(); err != nil {
			return err
		}
		scratch := filepath.Join(o.WorkDir, fmt.Sprintf("scratch-%03d", i))
		made = append(made, scratch)
		if err = os.Mkdir(scratch, 0o700); err != nil {
			return err
		}
		name := fmt.Sprintf("piece-%03d.mp4", i)
		piece := filepath.Join(o.WorkDir, name)
		made = append(made, piece)
		limit := pieceTimeout
		if o.Export.Reencodes() {
			limit = pieceTimeoutReencode
		}
		pctx, cancel := context.WithTimeout(ctx, limit)
		err = ExportWith(pctx, o.FFmpeg, o.Lib, o.CameraID, o.Files, pr.From, pr.To, scratch, piece, o.Export)
		cancel()
		_ = os.RemoveAll(scratch) // the cut runs are only needed while ffmpeg reads them
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = fmt.Errorf("piece %d of %d: %w", i+1, len(plan.Pieces), err)
			}
			return err
		}
		names = append(names, name)
		progress(i + 1)
	}
	if len(names) == 1 { // nothing to join
		if err = os.Rename(filepath.Join(o.WorkDir, names[0]), o.Out); err != nil {
			return err
		}
		progress(total)
		return nil
	}
	list := filepath.Join(o.WorkDir, "join.ffconcat")
	made = append(made, list)
	if err = os.WriteFile(list, []byte(ConcatList(names)), 0o600); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, concatTimeout)
	defer cancel()
	err = runFFmpeg(cctx, o.FFmpeg, ConcatArgs(list, o.Out, true))
	if err != nil && ctx.Err() == nil {
		// Pieces whose audio MP4 can't hold were cut video-only: join the video.
		err = runFFmpeg(cctx, o.FFmpeg, ConcatArgs(list, o.Out, false))
	}
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return err
	}
	if info, serr := os.Stat(o.Out); serr != nil || info.Size() == 0 {
		err = errors.New("ffmpeg produced no output")
		return err
	}
	progress(total)
	return nil
}

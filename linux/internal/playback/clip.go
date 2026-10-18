package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MaxClip is the longest clip one export may cover.
const MaxClip = 30 * time.Minute

var (
	ErrNoRecording = errors.New("nothing was recorded in that time range")
	ErrNoFFmpeg    = errors.New("exports need ffmpeg, which wasn't found (place it next to the sentinel binary or in <data>/bin)")
)

// writeRuns writes each run of the plan into dir as a standalone fMP4 (the
// file's init section + the run's keyframe-aligned pieces). ffmpeg then only
// ever reads a small file that starts on a keyframe, so it never has to seek
// inside a 15-minute fragmented file without an index, and a segment that is
// still being recorded is cut at its last complete fragment.
func writeRuns(lib *Library, cameraID string, p Plan, dir string) ([]string, error) {
	var names []string
	for i, r := range p.Runs {
		src, err := os.Open(lib.Path(cameraID, r.File.Name))
		if err != nil {
			return nil, err
		}
		name := fmt.Sprintf("part-%03d.mp4", i)
		dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			src.Close()
			return nil, err
		}
		_, err = io.Copy(dst, io.NewSectionReader(src, 0, r.File.Index.InitSize))
		for _, pc := range r.Pieces {
			if err != nil {
				break
			}
			var b []byte
			if b, err = r.File.Index.Cut(src, pc.First, pc.Last, pc.EndAtKey); err == nil {
				_, err = dst.Write(b)
			}
		}
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		src.Close()
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

// concatList is an ffconcat script joining the runs; the durations let
// ffmpeg's -ss land in the right run without probing each file.
func concatList(names []string, p Plan) string {
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	for i, n := range names {
		fmt.Fprintf(&b, "file '%s'\nduration %.3f\n", n, p.Runs[i].Dur().Seconds())
	}
	return b.String()
}

func secs(d time.Duration) string { return fmt.Sprintf("%.3f", d.Seconds()) }

// ClipArgs is the ffmpeg command for an export: stream copy (no re-encode,
// so the evidence is the camera's own encoded video), starting at the
// keyframe before offset with an edit list that makes players begin exactly
// at offset, ending after dur. withAudio=false drops audio for codecs MP4
// can't carry.
func ClipArgs(list string, offset, dur time.Duration, out string, withAudio bool) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-ss", secs(offset), "-i", list,
		"-t", secs(dur), "-map", "0:v:0"}
	if withAudio {
		args = append(args, "-map", "0:a?")
	} else {
		args = append(args, "-an")
	}
	return append(args, "-dn", "-sn", "-c", "copy", "-movflags", "+faststart", "-f", "mp4", "-y", out)
}

// SnapshotArgs grabs the frame at offset as a JPEG.
func SnapshotArgs(in string, offset time.Duration, out string) []string {
	return []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-ss", secs(offset), "-i", in, "-frames:v", "1", "-an", "-q:v", "3", "-f", "image2", "-c:v", "mjpeg", "-y", out}
}

// ClipPlan checks a requested export range and lays it out.
func ClipPlan(files []File, from, to time.Time) (Plan, error) {
	if !to.After(from) {
		return Plan{}, errors.New("the clip must end after it starts")
	}
	if to.Sub(from) > MaxClip {
		return Plan{}, fmt.Errorf("clips can be at most %d minutes", int(MaxClip.Minutes()))
	}
	p := BuildPlan(files, from, to)
	if len(p.Runs) == 0 || p.Covered(from, to) <= 0 {
		return Plan{}, ErrNoRecording
	}
	return p, nil
}

// ExportMasked is Export with privacy masks (masked.go): with masks the
// video is re-encoded with drawbox filters, without it stream-copied. It is
// a thin wrapper over ExportWith (exportwith.go), which also takes a
// watermark and pixelated regions.
func ExportMasked(ctx context.Context, ffmpeg string, lib *Library, cameraID string, files []File, from, to time.Time, workDir, out string, masks [][4]float64) error {
	return ExportWith(ctx, ffmpeg, lib, cameraID, files, from, to, workDir, out, ExportOptions{Masks: masks})
}

// Snapshot returns the frame recorded at t as a JPEG.
func Snapshot(ctx context.Context, ffmpeg string, lib *Library, cameraID string, files []File, at time.Time, workDir string) ([]byte, error) {
	if ffmpeg == "" {
		return nil, ErrNoFFmpeg
	}
	p := BuildPlan(files, at, at.Add(time.Millisecond))
	if _, ok := p.MediaAt(at); !ok || len(p.Runs) == 0 {
		return nil, ErrNoRecording
	}
	p.Runs, p.Spans = p.Runs[:1], p.Spans[:1]
	names, err := writeRuns(lib, cameraID, p, workDir)
	if err != nil {
		return nil, err
	}
	out := filepath.Join(workDir, "snapshot.jpg")
	if err := runFFmpeg(ctx, ffmpeg, SnapshotArgs(filepath.Join(workDir, names[0]), decodableFrom(p.Runs[0], at), out)); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(out)
	if err == nil && len(b) == 0 {
		err = errors.New("ffmpeg produced no image")
	}
	return b, err
}

// decodableFrom is t as an offset into the run's cut file (which starts on
// a keyframe), never before its start.
func decodableFrom(r Run, t time.Time) time.Duration {
	if d := t.Sub(r.Start()); d > 0 {
		return d
	}
	return 0
}

func runFFmpeg(ctx context.Context, ffmpeg string, args []string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		if msg != "" {
			return fmt.Errorf("ffmpeg: %s", msg)
		}
		return fmt.Errorf("ffmpeg: %w", err)
	}
	return nil
}

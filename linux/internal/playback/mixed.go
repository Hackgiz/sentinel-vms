package playback

// Exports whose range holds both recording tiers (WAVE24). The stream-copy
// export joins runs with ffmpeg's concat demuxer, which cannot change
// resolution mid-file, so a range with full-res and low-res footage is first
// normalized: each run is re-encoded to one size (and one codec) into its own
// MP4, then the normalized parts go through the usual export (stream copy, or
// the mask/blur/watermark re-encode). A range with one tier stays a stream
// copy of the camera's own bitstream.

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// VideoInfo reads a recording's init section (ftyp + moov): the coded size of
// its first video track (0×0 when unknown) and whether it also has an audio
// track.
func VideoInfo(initSection []byte) (width, height int, audio bool) {
	children(initSection, func(typ string, moov []byte) {
		if typ != "moov" {
			return
		}
		children(moov, func(typ string, trak []byte) {
			if typ != "trak" {
				return
			}
			var handler string
			var stsd []byte
			children(trak, func(typ string, mdia []byte) {
				if typ != "mdia" {
					return
				}
				children(mdia, func(typ string, body []byte) {
					switch typ {
					case "hdlr":
						if len(body) >= 12 {
							handler = string(body[8:12])
						}
					case "minf":
						children(body, func(typ string, stbl []byte) {
							if typ == "stbl" {
								children(stbl, func(typ string, body []byte) {
									if typ == "stsd" {
										stsd = body
									}
								})
							}
						})
					}
				})
			})
			switch handler {
			case "soun":
				audio = true
			case "vide":
				// stsd: version/flags, entry count, then the first sample
				// entry: size, type, 6 reserved bytes, data reference index,
				// 16 bytes of pre-defined/reserved, width, height.
				if width == 0 && len(stsd) >= 8+8+24+4 {
					width = int(binary.BigEndian.Uint16(stsd[8+8+24:]))
					height = int(binary.BigEndian.Uint16(stsd[8+8+24+2:]))
				}
			}
		})
	})
	return width, height, audio
}

// Mixed reports whether the plan plays both the full-res and the low-res tier.
func (p Plan) Mixed() bool {
	var full, low bool
	for _, r := range p.Runs {
		if r.File.Low {
			low = true
		} else {
			full = true
		}
	}
	return full && low
}

// MixedSize is the size every run of a mixed export is brought to: the
// largest run's size, or with preferSmall the smallest's. Dimensions are made
// even (yuv420p). 0×0 when any size is unknown.
func MixedSize(sizes [][2]int, preferSmall bool) (w, h int) {
	for _, s := range sizes {
		if s[0] <= 0 || s[1] <= 0 {
			return 0, 0
		}
		better := w == 0 || (!preferSmall && s[0]*s[1] > w*h) || (preferSmall && s[0]*s[1] < w*h)
		if better {
			w, h = s[0], s[1]
		}
	}
	return w &^ 1, h &^ 1
}

// MixedScaleFilter fits the video into w×h, keeping its aspect ratio
// (letterboxed in black when it differs).
func MixedScaleFilter(w, h int) string {
	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1", w, h, w, h)
}

// MixedRunArgs re-encodes one run's cut file to w×h: skip ss, keep t. Every
// part gets the same encoder and track timescale so the parts can be joined
// by stream copy. With audio, it is AAC at 48 kHz stereo; without, dropped.
func MixedRunArgs(in string, ss, t time.Duration, w, h int, withAudio bool, enc []string, out string) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-ss", secs(ss), "-i", in, "-t", secs(t), "-map", "0:v:0"}
	if withAudio {
		args = append(args, "-map", "0:a:0", "-c:a", "aac", "-b:a", "96k", "-ar", "48000", "-ac", "2")
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-vf", MixedScaleFilter(w, h))
	args = append(args, enc...)
	return append(args, "-pix_fmt", "yuv420p", "-video_track_timescale", "90000",
		"-dn", "-sn", "-movflags", "+faststart", "-f", "mp4", "-y", out)
}

// runInfo reads the init section of a run's file.
func runInfo(lib *Library, cameraID string, r Run) (w, h int, audio bool, err error) {
	f, err := os.Open(lib.PathOf(cameraID, r.File))
	if err != nil {
		return 0, 0, false, err
	}
	defer f.Close()
	b := make([]byte, r.File.Index.InitSize)
	if _, err := io.ReadFull(io.NewSectionReader(f, 0, r.File.Index.InitSize), b); err != nil {
		return 0, 0, false, err
	}
	w, h, audio = VideoInfo(b)
	return w, h, audio, nil
}

// normalizeMixed writes the plan's runs as parts of one size into workDir
// and returns the part names, in order. ok is false when a run's size can't
// be read (the caller then falls back to a single tier).
func normalizeMixed(ctx context.Context, ffmpeg string, lib *Library, cameraID string, p Plan, from, to time.Time, workDir string, preferSmall bool) (names []string, ok bool, err error) {
	sizes := make([][2]int, len(p.Runs))
	allAudio := true
	for i, r := range p.Runs {
		w, h, audio, err := runInfo(lib, cameraID, r)
		if err != nil {
			return nil, false, err
		}
		sizes[i] = [2]int{w, h}
		allAudio = allAudio && audio
	}
	w, h := MixedSize(sizes, preferSmall)
	if w == 0 || h == 0 {
		return nil, false, nil
	}
	parts, err := writeRuns(lib, cameraID, p, workDir)
	if err != nil {
		return nil, false, err
	}
	enc := MaskEncoderArgs(ctx, ffmpeg)
	build := func(withAudio bool) ([]string, error) {
		var out []string
		for i, r := range p.Runs {
			start := r.Start()
			var ss time.Duration
			if i == 0 && from.After(start) {
				ss, start = from.Sub(start), from
			}
			end := r.Start().Add(r.Dur())
			if end.After(to) {
				end = to
			}
			t := end.Sub(start)
			if t < 100*time.Millisecond {
				t = 100 * time.Millisecond
			}
			name := fmt.Sprintf("norm-%03d.mp4", i)
			if err := runFFmpeg(ctx, ffmpeg, MixedRunArgs(filepath.Join(workDir, parts[i]), ss, t, w, h, withAudio, enc, filepath.Join(workDir, name))); err != nil {
				return nil, err
			}
			out = append(out, name)
		}
		return out, nil
	}
	// Audio only when every part has it; a codec MP4 can't hold (G.711)
	// falls back to video only.
	if allAudio {
		if names, err = build(true); err == nil || ctx.Err() != nil {
			return names, err == nil, err
		}
	}
	names, err = build(false)
	return names, err == nil, err
}

// exportMixed writes a mixed-tier plan as one MP4. handled is false when it
// could not (sizes unknown), so the caller exports one tier instead.
func exportMixed(ctx context.Context, ffmpeg string, lib *Library, cameraID string, p Plan, from, to time.Time, workDir, out string, o ExportOptions) (handled bool, err error) {
	names, ok, err := normalizeMixed(ctx, ffmpeg, lib, cameraID, p, from, to, workDir, o.PreferSmall)
	if err != nil || !ok {
		return false, err
	}
	// The parts are already trimmed to [from, to): the second stage is the
	// ordinary export (stream copy or masks/blur/watermark) from offset 0.
	list := filepath.Join(workDir, "clip.ffconcat")
	var b bytes.Buffer
	b.WriteString("ffconcat version 1.0\n")
	for _, n := range names {
		fmt.Fprintf(&b, "file '%s'\n", n)
	}
	if err := os.WriteFile(list, b.Bytes(), 0o600); err != nil {
		return true, err
	}
	return true, encodeClip(ctx, ffmpeg, list, 0, p.Covered(from, to), workDir, out, o)
}

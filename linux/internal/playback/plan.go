package playback

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// File is one recording segment on disk.
type File struct {
	Name    string
	Start   time.Time // wall time of the first sample, from the file name
	Size    int64
	ModTime time.Time
	Index   *Index // nil or not Playable(): shown on the timeline, not playable
}

// End is when the segment's last sample was recorded. Without an index the
// file's modification time stands in (that's what the Recordings list uses).
func (f *File) End() time.Time {
	if f.Index.Playable() {
		return f.Start.Add(f.Index.Duration())
	}
	return f.ModTime
}

var namePattern = regexp.MustCompile(`^(\d{8}-\d{6})-(\d+)\.mp4$`)

// ParseName reads a segment's start from MediaMTX's recordPath name
// %Y%m%d-%H%M%S-%f (%f = microseconds), written in the server's local time.
func ParseName(name string, loc *time.Location) (time.Time, bool) {
	m := namePattern.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102-150405", m[1], loc)
	if err != nil {
		return time.Time{}, false
	}
	frac := m[2]
	if len(frac) > 9 {
		frac = frac[:9]
	}
	if n, err := strconv.Atoi(frac); err == nil {
		for i := len(frac); i < 9; i++ {
			n *= 10
		}
		t = t.Add(time.Duration(n))
	}
	return t, true
}

// Span is a continuous stretch of recording inside a window and where it sits
// in that window's playlist: media time Media plays wall time Start.
type Span struct {
	Start time.Time
	End   time.Time
	Media time.Duration
}

// Piece is one HLS media segment: the samples of a recording file from the
// keyframe in fragment First up to the keyframe in fragment Last (or the end
// of Last when !EndAtKey), re-packed by Index.Cut so it starts on a keyframe.
type Piece struct {
	First, Last  int
	EndAtKey     bool
	Offset, Size int64 // bytes of fragments First..Last
	Wall         time.Time
	Dur          time.Duration
}

// Run is a gap-free stretch of one file: one discontinuity in the playlist.
type Run struct {
	File   *File
	Pieces []Piece
}

func (r *Run) Start() time.Time { return r.Pieces[0].Wall }
func (r *Run) Dur() time.Duration {
	var d time.Duration
	for _, p := range r.Pieces {
		d += p.Dur
	}
	return d
}

// ByteRange is the part of the file the run covers (after the init section).
func (r *Run) ByteRange() (off, size int64) {
	first, last := r.Pieces[0], r.Pieces[len(r.Pieces)-1]
	return first.Offset, last.Offset + last.Size - first.Offset
}

// Plan is a time window laid out for playback or export.
type Plan struct {
	Runs  []Run
	Spans []Span
}

const (
	// HLS segments run from keyframe to keyframe and last at least this long
	// (longer when the camera's keyframes are further apart).
	minPiece = 4 * time.Second
	// A jump in decode time larger than this inside one file is a gap (the
	// camera stalled without the connection dropping).
	gapTolerance = time.Second
	// How far before the window start to look for a keyframe to begin on.
	maxPreroll = 30 * time.Second
)

// BuildPlan lays out [from, to) over files (sorted by Start, oldest first).
// Every run and piece starts on a keyframe — the first one at or before from
// when there is one — so playback and exports can decode from the requested
// time. Durations are rounded to the millisecond so the playlist's EXTINF
// values add up to exactly the Span media offsets.
func BuildPlan(files []File, from, to time.Time) Plan {
	var p Plan
	var media time.Duration
	for fi := range files {
		f := &files[fi]
		if !f.Index.Playable() || !f.Start.Before(to) || !f.End().After(from) {
			continue
		}
		frs := f.Index.Fragments
		base := frs[0].Time
		wall := func(i int) time.Time { return f.Start.Add(frs[i].Time - base) }

		// First fragment that ends after from, then back up to a keyframe.
		first := -1
		for i := range frs {
			if wall(i).Add(frs[i].Dur).After(from) {
				first = i
				break
			}
		}
		if first < 0 || !wall(first).Before(to) {
			continue
		}
		for i := first; i >= 0 && from.Sub(wall(i)) <= maxPreroll; i-- {
			if frs[i].Key && !wall(i).Add(frs[i].KeyOff).After(from) {
				first = i
				break
			}
		}

		key := func(i int) time.Time { return wall(i).Add(frs[i].KeyOff) }
		var run *Run
		var cur *Piece
		last := -1
		closeRun := func() {
			if cur != nil {
				cur.Last, cur.EndAtKey = last, false
				cur.Size = frs[last].Offset + frs[last].Size - cur.Offset
				cur.Dur = wall(last).Add(frs[last].Dur).Sub(cur.Wall)
				if cur.Dur > 0 {
					run.Pieces = append(run.Pieces, *cur)
				}
			}
			if run != nil && len(run.Pieces) > 0 {
				p.Runs = append(p.Runs, *run)
				p.Spans = append(p.Spans, Span{Start: run.Start(), End: run.Start().Add(run.Dur()), Media: media})
				media += run.Dur()
			}
			run, cur = nil, nil
		}
		var prevEnd time.Duration
		for i := first; i < len(frs) && wall(i).Before(to); i++ {
			fr := frs[i]
			if run != nil && fr.Time-prevEnd > gapTolerance {
				closeRun()
			}
			prevEnd = fr.Time + fr.Dur
			switch {
			case run == nil:
				if !fr.Key {
					continue // a run can only start on a keyframe
				}
				run = &Run{File: f}
				cur = &Piece{First: i, Offset: fr.Offset, Wall: key(i)}
			case fr.Key && key(i).Sub(cur.Wall) >= minPiece:
				cur.Last, cur.EndAtKey = i, true
				cur.Size = fr.Offset + fr.Size - cur.Offset
				cur.Dur = key(i).Sub(cur.Wall)
				run.Pieces = append(run.Pieces, *cur)
				cur = &Piece{First: i, Offset: fr.Offset, Wall: key(i)}
			}
			last = i
		}
		closeRun()
	}
	// Round piece durations; keep spans consistent with the rounded sums.
	media = 0
	for ri := range p.Runs {
		var d time.Duration
		for pi := range p.Runs[ri].Pieces {
			pc := &p.Runs[ri].Pieces[pi]
			pc.Dur = pc.Dur.Round(time.Millisecond)
			d += pc.Dur
		}
		p.Spans[ri].Media = media
		p.Spans[ri].End = p.Spans[ri].Start.Add(d)
		media += d
	}
	return p
}

// Duration is the playlist's total media time.
func (p *Plan) Duration() time.Duration {
	if n := len(p.Spans); n > 0 {
		last := p.Spans[n-1]
		return last.Media + last.End.Sub(last.Start)
	}
	return 0
}

// Covered is how much recording lies inside [from, to).
func (p *Plan) Covered(from, to time.Time) time.Duration {
	var d time.Duration
	for _, s := range p.Spans {
		a, b := s.Start, s.End
		if a.Before(from) {
			a = from
		}
		if b.After(to) {
			b = to
		}
		if b.After(a) {
			d += b.Sub(a)
		}
	}
	return d
}

// M3U8 renders the plan as an HLS VOD playlist (fMP4). The init section is a
// byte range of the recording file (initURL); each piece is fetched from
// pieceURL, which serves Index.Cut.
func (p *Plan) M3U8(initURL func(f *File) string, pieceURL func(f *File, pc Piece) string) string {
	target := 1
	for _, r := range p.Runs {
		for _, pc := range r.Pieces {
			if s := int(pc.Dur.Seconds() + 0.999); s > target {
				target = s
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%d\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-INDEPENDENT-SEGMENTS\n", target)
	for i, r := range p.Runs {
		if i > 0 {
			b.WriteString("#EXT-X-DISCONTINUITY\n") // decode times restart in every file
		}
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"%s\",BYTERANGE=\"%d@0\"\n", initURL(r.File), r.File.Index.InitSize)
		for j, pc := range r.Pieces {
			if j == 0 {
				fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", pc.Wall.UTC().Format("2006-01-02T15:04:05.000Z"))
			}
			fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", pc.Dur.Seconds(), pieceURL(r.File, pc))
		}
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// MediaAt maps a wall time to the playlist's media time; ok is false in a gap.
func (p *Plan) MediaAt(t time.Time) (time.Duration, bool) {
	for _, s := range p.Spans {
		if !t.Before(s.Start) && t.Before(s.End) {
			return s.Media + t.Sub(s.Start), true
		}
	}
	return 0, false
}

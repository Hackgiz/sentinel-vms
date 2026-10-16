// Package playback turns MediaMTX's fragmented-MP4 recording segments into a
// seekable timeline: it indexes each segment's fragments (byte range + decode
// time, keyframes), lays a time window out as an HLS VOD playlist of
// keyframe-to-keyframe pieces, and cuts clips for export.
//
// Nothing is re-encoded or stored for playback: the init section is a byte
// range of the same file the iPhone app and the Recordings page use, and each
// piece re-packs that file's encoded frames into one moof+mdat on request
// (remux.go).
package playback

import (
	"encoding/binary"
	"errors"
	"io"
	"time"
)

// Index is the byte/time layout of one fragmented-MP4 recording segment.
type Index struct {
	InitSize  int64 // ftyp + moov: the playlist's EXT-X-MAP byte range
	Timescale uint32
	Fragments []Fragment

	// Parser state, so a segment that is still being written can be extended
	// instead of re-read from the start.
	parsed  int64 // bytes covered by whole boxes so far
	track   uint32
	trexDur uint32
	trexFlg uint32
	pending int64 // offset of a moof still waiting for its mdat; -1 = none
	pendFr  Fragment
	hasMoov bool
	tracks  map[uint32]trackDef // every track, for re-cutting fragments (remux.go)
	dropped map[uint32]bool     // tracks browsers' MSE cannot play (web.go); nil when none
}

// trackDef is what a fragment's sample table needs from the moov.
type trackDef struct {
	timescale uint32
	dur, size uint32 // trex defaults
	flags     uint32
}

// Fragment is one moof+mdat pair (MediaMTX writes one per ~1 s "part").
type Fragment struct {
	Offset int64         // moof start
	Size   int64         // moof + mdat
	Time   time.Duration // decode time of the first sample (tfdt) on the timing track
	Dur    time.Duration // sum of sample durations on the timing track
	Key    bool          // holds a sync sample (keyframe)…
	KeyOff time.Duration // …this far after Time

	// MediaMTX cuts ~1 s parts independently of the camera's GOP, so most
	// fragments start mid-GOP; what matters is whether one contains a keyframe.
}

var errNotFMP4 = errors.New("not a fragmented MP4 recording")

const (
	maxMoov = 16 << 20
	maxMoof = 8 << 20
)

// ReadIndex parses a recording segment. A trailing box that is only partly
// written (the segment is still recording) is left out.
func ReadIndex(r io.ReaderAt, size int64) (*Index, error) {
	ix := &Index{pending: -1}
	if err := ix.Extend(r, size); err != nil {
		return nil, err
	}
	return ix, nil
}

// Extend continues parsing a segment that has grown to size bytes.
func (ix *Index) Extend(r io.ReaderAt, size int64) error {
	hdr := make([]byte, 16)
	off := ix.parsed
	ix.pending = -1 // a moof left waiting last time is parsed again
	for off+8 <= size {
		if _, err := r.ReadAt(hdr[:8], off); err != nil {
			break
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		hdrLen := int64(8)
		switch boxSize {
		case 0: // runs to end of file — still open
			boxSize = -1
		case 1:
			if _, err := r.ReadAt(hdr[8:16], off+8); err != nil {
				boxSize = -1
				break
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
			hdrLen = 16
		}
		if boxSize < hdrLen || off+boxSize > size {
			break // partly written
		}
		switch typ {
		case "moov":
			if boxSize > maxMoov {
				return errNotFMP4
			}
			body := make([]byte, boxSize-hdrLen)
			if _, err := r.ReadAt(body, off+hdrLen); err != nil {
				return err
			}
			if err := ix.parseMoov(body); err != nil {
				return err
			}
			ix.InitSize = off + boxSize
			ix.hasMoov = true
		case "moof":
			if !ix.hasMoov || boxSize > maxMoof {
				return errNotFMP4
			}
			body := make([]byte, boxSize-hdrLen)
			if _, err := r.ReadAt(body, off+hdrLen); err != nil {
				return err
			}
			fr, ok := ix.parseMoof(body)
			if !ok {
				// No sample of the timing track (e.g. an audio-only part):
				// it plays as part of the previous fragment's time.
				fr = Fragment{Time: ix.end(), Dur: 0}
			}
			fr.Offset = off
			ix.pending, ix.pendFr = off, fr
		case "mdat":
			if ix.pending >= 0 {
				fr := ix.pendFr
				fr.Size = off + boxSize - fr.Offset
				ix.Fragments = append(ix.Fragments, fr)
				ix.pending = -1
			}
		}
		off += boxSize
		// Only advance past whole moof+mdat pairs, so a resumed parse never
		// starts between a moof and its mdat.
		if ix.pending < 0 {
			ix.parsed = off
		}
	}
	return nil
}

// Playable reports whether the segment has an init section and at least one
// whole fragment.
func (ix *Index) Playable() bool { return ix != nil && ix.hasMoov && len(ix.Fragments) > 0 }

// end is where the timing track stands after the last fragment.
func (ix *Index) end() time.Duration {
	if n := len(ix.Fragments); n > 0 {
		return ix.Fragments[n-1].Time + ix.Fragments[n-1].Dur
	}
	return 0
}

// Duration is the recorded length (first sample to end of last sample).
func (ix *Index) Duration() time.Duration {
	if len(ix.Fragments) == 0 {
		return 0
	}
	return ix.end() - ix.Fragments[0].Time
}

// children calls fn for each box inside b.
func children(b []byte, fn func(typ string, body []byte)) {
	for len(b) >= 8 {
		size := uint64(binary.BigEndian.Uint32(b))
		typ := string(b[4:8])
		hdr := uint64(8)
		if size == 1 {
			if len(b) < 16 {
				return
			}
			size, hdr = binary.BigEndian.Uint64(b[8:]), 16
		} else if size == 0 {
			size = uint64(len(b))
		}
		if size < hdr || size > uint64(len(b)) {
			return
		}
		fn(typ, b[hdr:size])
		b = b[size:]
	}
}

type trackInfo struct {
	id        uint32
	timescale uint32
	handler   string
	codec     string // fourcc of the first sample entry (avc1, mp4a, ipcm, …)
}

// parseMoov picks the timing track (the first video track, else the first
// track) and its trex defaults.
func (ix *Index) parseMoov(moov []byte) error {
	var tracks []trackInfo
	trex := map[uint32][3]uint32{}
	children(moov, func(typ string, body []byte) {
		switch typ {
		case "trak":
			var t trackInfo
			children(body, func(typ string, body []byte) {
				switch typ {
				case "tkhd":
					if len(body) >= 1 {
						if body[0] == 1 && len(body) >= 24 {
							t.id = binary.BigEndian.Uint32(body[20:])
						} else if len(body) >= 16 {
							t.id = binary.BigEndian.Uint32(body[12:])
						}
					}
				case "mdia":
					children(body, func(typ string, body []byte) {
						switch typ {
						case "mdhd":
							if len(body) >= 1 {
								if body[0] == 1 && len(body) >= 24 {
									t.timescale = binary.BigEndian.Uint32(body[20:])
								} else if len(body) >= 16 {
									t.timescale = binary.BigEndian.Uint32(body[12:])
								}
							}
						case "hdlr":
							if len(body) >= 12 {
								t.handler = string(body[8:12])
							}
						case "minf":
							t.codec = sampleEntryType(body)
						}
					})
				}
			})
			tracks = append(tracks, t)
		case "mvex":
			children(body, func(typ string, body []byte) {
				if typ == "trex" && len(body) >= 24 {
					trex[binary.BigEndian.Uint32(body[4:])] = [3]uint32{binary.BigEndian.Uint32(body[12:]), binary.BigEndian.Uint32(body[16:]), binary.BigEndian.Uint32(body[20:])}
				}
			})
		}
	})
	var pick *trackInfo
	for i := range tracks {
		if tracks[i].handler == "vide" && tracks[i].timescale > 0 {
			pick = &tracks[i]
			break
		}
	}
	if pick == nil {
		for i := range tracks {
			if tracks[i].timescale > 0 {
				pick = &tracks[i]
				break
			}
		}
	}
	if pick == nil {
		return errNotFMP4
	}
	ix.track, ix.Timescale = pick.id, pick.timescale
	ix.trexDur, ix.trexFlg = trex[pick.id][0], trex[pick.id][2]
	ix.tracks = map[uint32]trackDef{}
	ix.dropped = nil
	for _, t := range tracks {
		if !webSafe(t) {
			if ix.dropped == nil {
				ix.dropped = map[uint32]bool{}
			}
			ix.dropped[t.id] = true
		}
		if t.timescale > 0 {
			d := trex[t.id]
			ix.tracks[t.id] = trackDef{timescale: t.timescale, dur: d[0], size: d[1], flags: d[2]}
		}
	}
	return nil
}

const sampleIsNonSync = 0x10000

// parseMoof reads the timing track's traf: tfdt and the trun sample table.
func (ix *Index) parseMoof(moof []byte) (Fragment, bool) {
	var fr Fragment
	found := false
	children(moof, func(typ string, body []byte) {
		if typ != "traf" || found {
			return
		}
		var (
			mine     bool
			defDur   = ix.trexDur
			defFlags = ix.trexFlg
			base     uint64
			hasBase  bool
			ticks    uint64
			first    = true
			key      bool
			keyAt    uint64
		)
		children(body, func(typ string, b []byte) {
			switch typ {
			case "tfhd":
				if len(b) < 8 {
					return
				}
				flags := binary.BigEndian.Uint32(b) & 0xffffff
				if binary.BigEndian.Uint32(b[4:]) != ix.track {
					return
				}
				mine = true
				p := 8
				if flags&0x1 != 0 {
					p += 8
				}
				if flags&0x2 != 0 {
					p += 4
				}
				if flags&0x8 != 0 && len(b) >= p+4 {
					defDur = binary.BigEndian.Uint32(b[p:])
					p += 4
				}
				if flags&0x10 != 0 {
					p += 4
				}
				if flags&0x20 != 0 && len(b) >= p+4 {
					defFlags = binary.BigEndian.Uint32(b[p:])
				}
			case "tfdt":
				if !mine || len(b) < 8 {
					return
				}
				if b[0] == 1 && len(b) >= 12 {
					base = binary.BigEndian.Uint64(b[4:])
				} else {
					base = uint64(binary.BigEndian.Uint32(b[4:]))
				}
				hasBase = true
			case "trun":
				if !mine || len(b) < 8 {
					return
				}
				flags := binary.BigEndian.Uint32(b) & 0xffffff
				n := int(binary.BigEndian.Uint32(b[4:]))
				p := 8
				if flags&0x1 != 0 {
					p += 4
				}
				firstFlags, hasFirst := uint32(0), false
				if flags&0x4 != 0 && len(b) >= p+4 {
					firstFlags, hasFirst = binary.BigEndian.Uint32(b[p:]), true
					p += 4
				}
				per := 0
				for _, bit := range []uint32{0x100, 0x200, 0x400, 0x800} {
					if flags&bit != 0 {
						per += 4
					}
				}
				for i := 0; i < n; i++ {
					if len(b) < p+per {
						break
					}
					q := p
					dur := defDur
					if flags&0x100 != 0 {
						dur = binary.BigEndian.Uint32(b[q:])
						q += 4
					}
					if flags&0x200 != 0 {
						q += 4
					}
					sf := defFlags
					if flags&0x400 != 0 {
						sf = binary.BigEndian.Uint32(b[q:])
					}
					if i == 0 && hasFirst {
						sf = firstFlags
					}
					if !key && sf&sampleIsNonSync == 0 {
						key, keyAt = true, ticks
					}
					first = false
					ticks += uint64(dur)
					p += per
				}
			}
		})
		if !mine || first { // no samples of the timing track here
			return
		}
		found = true
		fr.Key, fr.KeyOff = key, ix.toDuration(keyAt)
		fr.Dur = ix.toDuration(ticks)
		if hasBase {
			fr.Time = ix.toDuration(base)
		} else {
			fr.Time = ix.end()
		}
	})
	return fr, found
}

func (ix *Index) toDuration(ticks uint64) time.Duration {
	ts := uint64(ix.Timescale)
	return time.Duration(ticks/ts)*time.Second + time.Duration((ticks%ts)*uint64(time.Second)/ts)
}

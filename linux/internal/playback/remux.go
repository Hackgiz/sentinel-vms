package playback

import (
	"encoding/binary"
	"errors"
	"io"
)

// MediaMTX writes a moof+mdat about every second regardless of where the
// camera's keyframes fall, so a byte range of whole fragments almost never
// starts on a keyframe. Players that load such a range after a seek drop the
// frames before the first keyframe and are left with a hole they keep trying
// to fill (hls.js reloads neighbouring segments in a loop). Cut therefore
// re-packs the samples between two keyframes into one fresh moof+mdat: the
// encoded frames are copied untouched, only the sample tables are rewritten.

type sample struct {
	frag  int   // which of the cut's fragments it came from
	dts   int64 // track timescale
	dur   uint32
	size  uint32
	flags uint32
	cto   int32
	off   int64 // offset into the buffer holding the fragments
}

type trackRun struct {
	id      uint32
	samples []sample
	signed  bool // composition offsets need a version 1 trun
}

// readSamples parses the sample tables of fragments buf holds (which starts
// at fragment first's moof).
func (ix *Index) readSamples(buf []byte, first, last int) (map[uint32]*trackRun, []uint32) {
	tracks := map[uint32]*trackRun{}
	var order []uint32
	next := map[uint32]int64{} // decode time where each track left off
	base := ix.Fragments[first].Offset
	for fi := first; fi <= last; fi++ {
		fr := ix.Fragments[fi]
		moofAt := fr.Offset - base
		if moofAt+8 > int64(len(buf)) {
			break
		}
		size := int64(binary.BigEndian.Uint32(buf[moofAt:]))
		if size < 8 || moofAt+size > int64(len(buf)) || string(buf[moofAt+4:moofAt+8]) != "moof" {
			break
		}
		children(buf[moofAt+8:moofAt+size], func(typ string, traf []byte) {
			if typ != "traf" {
				return
			}
			var (
				id                   uint32
				def                  trackDef
				known                bool
				dataBase             = moofAt
				dts                  int64
				hasTfdt              bool
				defDur, defSz, defFl uint32
			)
			children(traf, func(typ string, b []byte) {
				switch typ {
				case "tfhd":
					if len(b) < 8 {
						return
					}
					flags := binary.BigEndian.Uint32(b) & 0xffffff
					id = binary.BigEndian.Uint32(b[4:])
					def, known = ix.tracks[id]
					defDur, defSz, defFl = def.dur, def.size, def.flags
					p := 8
					if flags&0x1 != 0 && len(b) >= p+8 {
						dataBase = int64(binary.BigEndian.Uint64(b[p:])) - base
						p += 8
					}
					if flags&0x2 != 0 {
						p += 4
					}
					if flags&0x8 != 0 && len(b) >= p+4 {
						defDur = binary.BigEndian.Uint32(b[p:])
						p += 4
					}
					if flags&0x10 != 0 && len(b) >= p+4 {
						defSz = binary.BigEndian.Uint32(b[p:])
						p += 4
					}
					if flags&0x20 != 0 && len(b) >= p+4 {
						defFl = binary.BigEndian.Uint32(b[p:])
					}
				case "tfdt":
					if len(b) >= 12 && b[0] == 1 {
						dts, hasTfdt = int64(binary.BigEndian.Uint64(b[4:])), true
					} else if len(b) >= 8 {
						dts, hasTfdt = int64(binary.BigEndian.Uint32(b[4:])), true
					}
				case "trun":
					if !known || len(b) < 8 {
						return
					}
					if !hasTfdt {
						dts, hasTfdt = next[id], true
					}
					version := b[0]
					flags := binary.BigEndian.Uint32(b) & 0xffffff
					n := int(binary.BigEndian.Uint32(b[4:]))
					p := 8
					off := dataBase
					if flags&0x1 != 0 && len(b) >= p+4 {
						off += int64(int32(binary.BigEndian.Uint32(b[p:])))
						p += 4
					}
					firstFlags, hasFirst := uint32(0), false
					if flags&0x4 != 0 && len(b) >= p+4 {
						firstFlags, hasFirst = binary.BigEndian.Uint32(b[p:]), true
						p += 4
					}
					t := tracks[id]
					if t == nil {
						t = &trackRun{id: id}
						tracks[id] = t
						order = append(order, id)
					}
					for i := 0; i < n; i++ {
						s := sample{frag: fi, dts: dts, dur: defDur, size: defSz, flags: defFl, off: off}
						for _, f := range []uint32{0x100, 0x200, 0x400, 0x800} {
							if flags&f == 0 {
								continue
							}
							if len(b) < p+4 {
								return
							}
							v := binary.BigEndian.Uint32(b[p:])
							p += 4
							switch f {
							case 0x100:
								s.dur = v
							case 0x200:
								s.size = v
							case 0x400:
								s.flags = v
							case 0x800:
								s.cto = int32(v)
								if version == 0 && int32(v) < 0 {
									s.cto = 0 // v0 offsets are unsigned; never seen in practice
								}
							}
						}
						if i == 0 && hasFirst {
							s.flags = firstFlags
						}
						if s.cto < 0 {
							t.signed = true
						}
						t.samples = append(t.samples, s)
						dts += int64(s.dur)
						off += int64(s.size)
					}
					next[id] = dts
				}
			})
		})
	}
	return tracks, order
}

var errNoKeyframe = errors.New("no keyframe in that range")

// Cut returns one moof+mdat with every track's samples from the keyframe in
// fragment first up to (not including) the keyframe in fragment last, or to
// the end of fragment last when endAtKey is false.
func (ix *Index) Cut(r io.ReaderAt, first, last int, endAtKey bool) ([]byte, error) {
	return ix.cut(r, first, last, endAtKey, false)
}

// CutWeb is Cut for browsers: tracks MSE cannot decode (see WebInit) are
// left out, so the piece matches the init section served for them.
func (ix *Index) CutWeb(r io.ReaderAt, first, last int, endAtKey bool) ([]byte, error) {
	return ix.cut(r, first, last, endAtKey, true)
}

func (ix *Index) cut(r io.ReaderAt, first, last int, endAtKey, web bool) ([]byte, error) {
	if !ix.Playable() || first < 0 || last < first || last >= len(ix.Fragments) {
		return nil, errors.New("fragment range out of bounds")
	}
	start := ix.Fragments[first].Offset
	end := ix.Fragments[last].Offset + ix.Fragments[last].Size
	if end-start > 256<<20 {
		return nil, errors.New("range too large")
	}
	buf := make([]byte, end-start)
	if _, err := r.ReadAt(buf, start); err != nil {
		return nil, err
	}
	tracks, order := ix.readSamples(buf, first, last)
	video := tracks[ix.track]
	if video == nil {
		return nil, errNoKeyframe
	}
	// Cut points on the timing track, as seconds so audio can be cut too.
	// Video is cut by decode time; other tracks by the keyframes'
	// presentation time, so with B-frames the audio lines up with what's
	// shown and consecutive pieces leave no hole in the buffer.
	from, to := -1.0, -1.0
	fromPTS, toPTS := -1.0, -1.0
	vts := float64(ix.tracks[ix.track].timescale)
	for _, s := range video.samples {
		if s.flags&sampleIsNonSync != 0 {
			continue
		}
		t, pts := float64(s.dts)/vts, float64(s.dts+int64(s.cto))/vts
		if from < 0 && s.frag == first {
			from, fromPTS = t, pts
		}
		if endAtKey && to < 0 && s.frag == last && t > from {
			to, toPTS = t, pts
		}
	}
	if from < 0 || (endAtKey && to < 0) {
		return nil, errNoKeyframe
	}
	type outTrack struct {
		run     *trackRun
		samples []sample
	}
	var outs []outTrack
	for _, id := range order {
		if web && ix.dropped[id] {
			continue
		}
		t := tracks[id]
		ts := float64(ix.tracks[id].timescale)
		lo, hi := fromPTS, toPTS
		if id == ix.track {
			lo, hi = from, to
		}
		var keep []sample
		for _, s := range t.samples {
			at := float64(s.dts) / ts
			// Half a millisecond of slack so rounding never drops the keyframe itself.
			if at >= lo-0.0005 && (hi < 0 || at < hi-0.0005) {
				keep = append(keep, s)
			}
		}
		if len(keep) > 0 {
			outs = append(outs, outTrack{t, keep})
		}
	}

	// moof size first, so trun data offsets can point into the mdat.
	moofSize := 8 + 16 // moof header + mfhd
	for _, o := range outs {
		moofSize += 8 + 16 + 20 + 20 + 16*len(o.samples) // traf + tfhd + tfdt v1 + trun (header, count, data offset) + entries
	}
	var dataSize int64
	for _, o := range outs {
		for _, s := range o.samples {
			dataSize += int64(s.size)
		}
	}
	out := make([]byte, 0, int64(moofSize)+8+dataSize)
	u32 := func(v uint32) { out = binary.BigEndian.AppendUint32(out, v) }
	u32(uint32(moofSize))
	out = append(out, "moof"...)
	u32(16)
	out = append(out, "mfhd"...)
	u32(0)
	u32(1)
	dataOff := int64(moofSize) + 8
	for _, o := range outs {
		u32(uint32(8 + 16 + 20 + 20 + 16*len(o.samples)))
		out = append(out, "traf"...)
		u32(16)
		out = append(out, "tfhd"...)
		u32(0x020000) // default-base-is-moof
		u32(o.run.id)
		u32(20)
		out = append(out, "tfdt"...)
		u32(1 << 24)
		out = binary.BigEndian.AppendUint64(out, uint64(o.samples[0].dts))
		u32(uint32(20 + 16*len(o.samples)))
		out = append(out, "trun"...)
		vf := uint32(0x000f01)
		if o.run.signed {
			vf |= 1 << 24
		}
		u32(vf)
		u32(uint32(len(o.samples)))
		u32(uint32(dataOff))
		for _, s := range o.samples {
			u32(s.dur)
			u32(s.size)
			u32(s.flags)
			u32(uint32(s.cto))
			dataOff += int64(s.size)
		}
	}
	u32(uint32(8 + dataSize))
	out = append(out, "mdat"...)
	for _, o := range outs {
		for _, s := range o.samples {
			if s.off < 0 || s.off+int64(s.size) > int64(len(buf)) {
				return nil, errors.New("sample data outside the fragments")
			}
			out = append(out, buf[s.off:s.off+int64(s.size)]...)
		}
	}
	return out, nil
}

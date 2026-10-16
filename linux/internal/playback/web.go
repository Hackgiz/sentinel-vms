package playback

import (
	"encoding/binary"
	"errors"
	"io"
)

// Browsers play HLS through Media Source Extensions, which decode H.264/AAC
// in fMP4 but not what MediaMTX writes for a camera with G.711 audio: raw PCM
// (sample entry ipcm, pcm_s16be). One such track makes the whole stream fail
// (hls.js stalls, the tile stays black), while native players (the iPhone
// app) cope. Recordings stay as they are — PCM audio is evidence, and clip
// export copies every track — so the browser playlist serves those files
// video-only instead: WebInit hands out the init section without the
// unsupported tracks and CutWeb leaves their samples out of the pieces.
// Recordings that are already MSE-safe keep the zero-copy byte-range init.

// sampleEntryType is the fourcc of the first sample description in a trak's
// minf (minf → stbl → stsd → first entry).
func sampleEntryType(minf []byte) string {
	var typ string
	children(minf, func(t string, stbl []byte) {
		if t != "stbl" {
			return
		}
		children(stbl, func(t string, stsd []byte) {
			// version/flags, entry count, then the first entry's size and type.
			if t == "stsd" && len(stsd) >= 16 {
				typ = string(stsd[12:16])
			}
		})
	})
	return typ
}

// webSafe reports whether MSE can play the track: video as is (which codec
// is the camera's business), audio only as AAC (mp4a). Anything else — PCM,
// data or timecode tracks — is left out of the browser stream.
func webSafe(t trackInfo) bool {
	switch t.handler {
	case "vide":
		return true
	case "soun":
		return t.codec == "mp4a"
	}
	return false
}

// WebFiltered reports whether the segment has tracks the browser stream
// leaves out, so its init section must come from WebInit rather than a byte
// range of the file.
func (ix *Index) WebFiltered() bool { return ix != nil && len(ix.dropped) > 0 }

// WebInit returns the segment's ftyp + moov without the tracks browsers
// cannot play (their trak, and their trex in mvex).
func (ix *Index) WebInit(r io.ReaderAt) ([]byte, error) {
	if !ix.Playable() || ix.InitSize <= 0 || ix.InitSize > maxMoov+4096 {
		return nil, errors.New("no init section")
	}
	buf := make([]byte, ix.InitSize)
	if _, err := r.ReadAt(buf, 0); err != nil {
		return nil, err
	}
	var out []byte
	for rest := buf; len(rest) >= 8; {
		size := int(binary.BigEndian.Uint32(rest))
		if size < 8 || size > len(rest) {
			return nil, errNotFMP4
		}
		if string(rest[4:8]) == "moov" {
			out = appendBox(out, "moov", ix.filterMoov(rest[8:size]))
		} else {
			out = append(out, rest[:size]...)
		}
		rest = rest[size:]
	}
	return out, nil
}

func appendBox(dst []byte, typ string, body []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(8+len(body)))
	dst = append(dst, typ...)
	return append(dst, body...)
}

// boxes calls fn with each box inside b, whole (header included) and body.
func boxes(b []byte, fn func(typ string, whole, body []byte)) {
	for len(b) >= 8 {
		size := int(binary.BigEndian.Uint32(b))
		if size < 8 || size > len(b) {
			return
		}
		fn(string(b[4:8]), b[:size], b[8:size])
		b = b[size:]
	}
}

func (ix *Index) filterMoov(moov []byte) []byte {
	var out []byte
	boxes(moov, func(typ string, whole, body []byte) {
		switch typ {
		case "trak":
			if ix.dropped[trackID(body)] {
				return
			}
		case "mvex":
			var inner []byte
			boxes(body, func(typ string, whole, body []byte) {
				if typ == "trex" && len(body) >= 8 && ix.dropped[binary.BigEndian.Uint32(body[4:])] {
					return
				}
				inner = append(inner, whole...)
			})
			out = appendBox(out, "mvex", inner)
			return
		}
		out = append(out, whole...)
	})
	return out
}

// trackID reads a trak's tkhd track id.
func trackID(trak []byte) uint32 {
	var id uint32
	children(trak, func(typ string, body []byte) {
		if typ != "tkhd" || len(body) < 1 {
			return
		}
		if body[0] == 1 && len(body) >= 24 {
			id = binary.BigEndian.Uint32(body[20:])
		} else if len(body) >= 16 {
			id = binary.BigEndian.Uint32(body[12:])
		}
	})
	return id
}

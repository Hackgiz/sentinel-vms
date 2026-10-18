// Package talkback pushes an operator's voice to a camera speaker over the
// ONVIF audio backchannel: 16-bit PCM in, G.711 µ-law RTP (PCMU/8000, 20 ms
// packets) out over an RTSP interleaved connection. One session per
// camera; idle and duration limits are enforced here (talk.go).
package talkback

// G.711 µ-law, built from the ITU-T G.191 reference ("ulaw_compress"):
// the 16-bit sample is reduced to 14 bits, biased by 33, its segment found
// and the sign/segment/mantissa byte inverted. Encoding is one lookup in a
// table indexed by the 14-bit value (sample >> 2), filled at init from the
// reference so the table and the algorithm can't drift apart.

var ulawTable [1 << 14]byte // index: uint16(sample) >> 2

func init() {
	for i := range ulawTable {
		ulawTable[i] = ulawReference(int16(uint16(i << 2)))
	}
}

// ulawReference is ITU-T G.191 ulaw_compress for one sample.
func ulawReference(x int16) byte {
	var absno int
	if x < 0 {
		absno = int(^x)>>2 + 33
	} else {
		absno = int(x)>>2 + 33
	}
	if absno > 0x1FFF {
		absno = 0x1FFF
	}
	segno := 1
	for i := absno >> 6; i != 0; i >>= 1 {
		segno++
	}
	high := 8 - segno
	low := 0x0F - (absno>>segno)&0x0F
	out := byte(high<<4 | low)
	if x >= 0 {
		out |= 0x80
	}
	return out
}

// MuLaw encodes one 16-bit linear sample.
func MuLaw(x int16) byte { return ulawTable[uint16(x)>>2] }

// EncodeMuLaw encodes samples into dst (grown as needed) and returns it.
func EncodeMuLaw(dst []byte, samples []int16) []byte {
	for _, s := range samples {
		dst = append(dst, ulawTable[uint16(s)>>2])
	}
	return dst
}

// MuLawDecode expands one µ-law byte (ITU-T G.191 ulaw_expand), for tests
// and diagnostics.
func MuLawDecode(b byte) int16 {
	sign := b < 0x80
	b = ^b
	exp := int(b>>4) & 0x07
	mant := int(b & 0x0F)
	v := ((mant<<3)+0x84)<<exp - 0x84
	if sign {
		return int16(-v)
	}
	return int16(v)
}

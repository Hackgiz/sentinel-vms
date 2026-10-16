package playback

// A synthetic recording segment in the layout MediaMTX's fMP4 recorder
// writes, for tests (here and in the server package) and dev tools.

import (
	"bytes"
	"encoding/binary"
)

// SynthSegment returns a segment of n one-second fragments of 25 fps video
// (plus an audio track), with a keyframe every gop seconds.
func SynthSegment(n, gop int) []byte { return segmentFile(n, gop, 0) }

// SynthSegmentPCM is SynthSegment with the audio stored as raw PCM (ipcm), as
// MediaMTX writes a camera's G.711 audio.
func SynthSegmentPCM(n, gop int) []byte {
	b := initSectionAudio("ipcm")
	for i := 0; i < n; i++ {
		b = append(b, fragment(uint32(i+1), uint64(i)*90000, 25, i%gop == 0)...)
	}
	return b
}

func box(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	return append(append(b, typ...), body...)
}

func u32(v ...uint32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.BigEndian.AppendUint32(b, x)
	}
	return b
}

func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// initSection: an audio track (id 1) listed before the video track (id 2),
// so the parser has to pick the video one for timing.
func initSection() []byte { return initSectionAudio("mp4a") }

// initSectionAudio: initSection with the audio track's sample entry set to
// the given fourcc (mp4a = AAC; ipcm = G.711 as raw PCM, which MSE rejects).
func initSectionAudio(audioCodec string) []byte {
	trak := func(id uint32, handler string, timescale uint32) []byte {
		codec := "avc1"
		if handler == "soun" {
			codec = audioCodec
		}
		stsd := box("stsd", u32(0, 1), box(codec, make([]byte, 8)))
		minf := box("minf", box("stbl", stsd))
		tkhd := box("tkhd", u32(0, 0, 0, id, 0)) // v0: flags, ctime, mtime, id, reserved
		mdhd := box("mdhd", u32(0, 0, 0, timescale, 0))
		hdlr := box("hdlr", u32(0, 0), []byte(handler), make([]byte, 13))
		return box("trak", tkhd, box("mdia", mdhd, hdlr, minf))
	}
	mvex := box("mvex", box("trex", u32(0, 1, 1, 1024, 0, 0)), box("trex", u32(0, 2, 1, 0, 0, 0x10000)))
	return append(box("ftyp", []byte("iso5"), u32(512), []byte("iso5iso6mp41")),
		box("moov", box("mvhd", make([]byte, 100)), trak(1, "soun", 48000), trak(2, "vide", 90000), mvex)...)
}

// fragment: frames video samples of 3600 ticks (25 fps @ 90 kHz) from tfdt
// base; the first is a keyframe when key. Audio traf first, like MediaMTX.
// Each 100-byte video frame starts with its decode time in ticks (uint64), so
// tests can tell which frames a cut kept; audio samples are 10 bytes of 'a'.
func fragment(seq uint32, base uint64, frames int, key bool) []byte {
	if key {
		return fragmentKeyAt(seq, base, frames, 0)
	}
	return fragmentKeyAt(seq, base, frames, -1)
}

// fragmentKeyAt: like fragment, with the keyframe at sample keyAt (-1 = none),
// the way MediaMTX parts usually start mid-GOP.
func fragmentKeyAt(seq uint32, base uint64, frames, keyAt int) []byte {
	build := func(audioOff, videoOff uint32) []byte {
		audio := box("traf", box("tfhd", u32(0x020000, 1)), box("tfdt", []byte{1, 0, 0, 0}, u64(base/90*48)),
			box("trun", u32(0x000301, 2, audioOff), u32(1024, 10, 1024, 10)))
		var samples []byte
		for i := 0; i < frames; i++ {
			flags := uint32(0x10000)
			if i == keyAt {
				flags = 0x2000000
			}
			samples = append(samples, u32(3600, 100, flags, 0)...)
		}
		video := box("traf", box("tfhd", u32(0x020000, 2)), box("tfdt", []byte{1, 0, 0, 0}, u64(base)),
			box("trun", u32(0x000f01, uint32(frames), videoOff), samples))
		return box("moof", box("mfhd", u32(0, seq)), audio, video)
	}
	n := uint32(len(build(0, 0)))
	moof := build(n+8, n+8+20)
	data := bytes.Repeat([]byte{'a'}, 20)
	for i := 0; i < frames; i++ {
		frame := make([]byte, 100)
		binary.BigEndian.PutUint64(frame, base+uint64(i)*3600)
		data = append(data, frame...)
	}
	return append(moof, box("mdat", data)...)
}

// segmentFile: n one-second fragments (25 frames), a keyframe every gop seconds.
func segmentFile(n, gop int, base uint64) []byte {
	b := initSection()
	for i := 0; i < n; i++ {
		b = append(b, fragment(uint32(i+1), base+uint64(i)*90000, 25, i%gop == 0)...)
	}
	return b
}

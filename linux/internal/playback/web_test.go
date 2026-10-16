package playback

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// initTracks lists the track ids of an init section's traks and trexes.
func initTracks(init []byte) (traks, trexes []uint32) {
	boxes(init, func(typ string, _, body []byte) {
		if typ != "moov" {
			return
		}
		boxes(body, func(typ string, _, body []byte) {
			switch typ {
			case "trak":
				traks = append(traks, trackID(body))
			case "mvex":
				boxes(body, func(typ string, _, body []byte) {
					if typ == "trex" {
						trexes = append(trexes, binary.BigEndian.Uint32(body[4:]))
					}
				})
			}
		})
	})
	return
}

// segmentWith builds a segment like segmentFile with the given audio codec.
func segmentWith(audio string, n, gop int) []byte {
	b := initSectionAudio(audio)
	for i := 0; i < n; i++ {
		b = append(b, fragment(uint32(i+1), uint64(i)*90000, 25, i%gop == 0)...)
	}
	return b
}

func TestAACRecordingIsNotFiltered(t *testing.T) {
	data := segmentWith("mp4a", 6, 2)
	ix, err := ReadIndex(bytes.NewReader(data), int64(len(data)))
	if err != nil || ix.WebFiltered() {
		t.Fatalf("AAC recording filtered: %v %+v", err, ix.dropped)
	}
}

func TestPCMAudioIsLeftOutOfBrowserPlayback(t *testing.T) {
	data := segmentWith("ipcm", 12, 2)
	ix, err := ReadIndex(bytes.NewReader(data), int64(len(data)))
	if err != nil || !ix.Playable() || !ix.WebFiltered() || !ix.dropped[1] || ix.dropped[2] {
		t.Fatalf("index %v %+v", err, ix)
	}
	// The original init section advertises both tracks…
	orig := data[:ix.InitSize]
	if traks, trexes := initTracks(orig); len(traks) != 2 || len(trexes) != 2 {
		t.Fatalf("original init: %v %v", traks, trexes)
	}
	// …the browser one only the video track, and still parses as a recording.
	init, err := ix.WebInit(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if traks, trexes := initTracks(init); len(traks) != 1 || traks[0] != 2 || len(trexes) != 1 || trexes[0] != 2 {
		t.Fatalf("web init: %v %v", traks, trexes)
	}
	if bytes.Contains(init, []byte("ipcm")) || !bytes.Contains(init, []byte("avc1")) || !bytes.HasPrefix(init, orig[:20]) {
		t.Fatal("web init content")
	}

	// A piece cut for browsers carries video only; the export cut keeps the PCM.
	for _, web := range []bool{true, false} {
		cut, wantTracks := ix.Cut, 2
		if web {
			cut, wantTracks = ix.CutWeb, 1
		}
		b, err := cut(bytes.NewReader(data), 2, 6, true)
		if err != nil {
			t.Fatal(err)
		}
		hdr := orig
		if web {
			hdr = init
		}
		whole := append(append([]byte{}, hdr...), b...)
		wix, err := ReadIndex(bytes.NewReader(whole), int64(len(whole)))
		if err != nil || len(wix.Fragments) != 1 || !wix.Fragments[0].Key || wix.Fragments[0].Dur != 4*time.Second {
			t.Fatalf("web=%v: %v %+v", web, err, wix)
		}
		tracks, _ := wix.readSamples(b, 0, 0)
		if len(tracks) != wantTracks || len(tracks[2].samples) != 100 || (!web && tracks[1] == nil) {
			t.Fatalf("web=%v: %d tracks", web, len(tracks))
		}
	}
}

func TestPlaylistMapsFilteredInitWithoutByteRange(t *testing.T) {
	l, dir := lib(t)
	writeSeg(t, dir, t0, segmentWith("ipcm", 10, 2))
	writeSeg(t, dir, t0.Add(time.Minute), segmentWith("mp4a", 10, 2))
	files, _ := l.Files("CAM", t0, t0.Add(time.Hour))
	p := BuildPlan(files, t0, t0.Add(time.Hour))
	m3u8 := p.M3U8(func(f *File) (string, bool) {
		if f.Index.WebFiltered() {
			return "/init/" + f.Name, false
		}
		return "/rec/" + f.Name, true
	}, func(f *File, pc Piece) string { return "/p/" + f.Name })
	var maps []string
	for _, line := range strings.Split(m3u8, "\n") {
		if strings.HasPrefix(line, "#EXT-X-MAP") {
			maps = append(maps, line)
		}
	}
	if len(maps) != 2 || strings.Contains(maps[0], "BYTERANGE") || !strings.Contains(maps[1], "BYTERANGE") {
		t.Fatalf("maps %q", maps)
	}
}

package playback

// Index sidecars (WAVE23 stream M): each recording segment's fragment table
// is kept on disk next to it as <name>.mp4.idx, so a server restart, a
// recordings-folder change or a cache eviction never re-reads a finished
// segment from the start (a day of 15-min files from a USB disk took ~50 s
// to parse on a Core 2 Duo).
//
// Format, version 1, little-endian:
//
//	off  size  field
//	  0     4  magic "SNIX"
//	  4     2  version (1)
//	  6     2  flags (0)
//	  8     8  segment size in bytes the table describes
//	 16     8  segment mtime, Unix ns (informational)
//	 24     4  head length h: min(64 KiB, segment size)
//	 28    32  SHA-256 of the segment's first h bytes
//	 60     8  InitSize (ftyp+moov)
//	 68     4  Timescale of the timing track
//	 72     8  parsed: bytes covered by whole boxes (where Extend resumes)
//	 80     4  fragment count n
//	 84  41·n  fragments: Offset i64, Size i64, Time i64 ns, Dur i64 ns,
//	           KeyOff i64 ns, Key u8
//	  …     4  CRC-32 (IEEE) of everything before it
//
// A sidecar is valid for a segment whose first h bytes still hash the same
// and which is at least as long as the size recorded: equal means it is
// used as is, longer (the segment kept growing) means parsing resumes from
// `parsed`. Anything else (shorter, other head, bad CRC, other version) is
// ignored and the segment is parsed again, and the sidecar rewritten. The
// mtime is not checked: a copy to another disk (recordings-folder move) may
// round it.
//
// The init section's track table is not stored: it is re-read from the
// segment's moov (a few KiB) when the sidecar is loaded.

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// SidecarExt is appended to a segment's file name.
	SidecarExt = ".idx"

	sidecarMagic   = "SNIX"
	sidecarVersion = 1
	sidecarHeader  = 84
	sidecarFrag    = 41
	sidecarHeadLen = 64 << 10
	sidecarMaxFrag = 4 << 20 // ~1000 h of 1 s parts: anything more is junk
)

// SidecarPath is where the index sidecar of the segment at path lives.
func SidecarPath(path string) string { return path + SidecarExt }

// IsSidecarName reports whether name is a segment's index sidecar or a
// temporary file of one being written.
func IsSidecarName(name string) bool {
	if strings.HasSuffix(name, ".mp4"+SidecarExt) {
		return true
	}
	return strings.HasPrefix(name, ".") && strings.Contains(name, ".mp4"+SidecarExt+"-")
}

var errSidecar = errors.New("index sidecar does not match its segment")

// headHash hashes the first n bytes of r.
func headHash(r io.ReaderAt, n int64) ([32]byte, error) {
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, 0); err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(buf), nil
}

func sidecarHead(size int64) int64 { return min(size, sidecarHeadLen) }

// encodeSidecar serialises ix, which describes the first size bytes of a
// segment whose head hashes to sum.
func encodeSidecar(ix *Index, size int64, mod time.Time, headLen int64, sum [32]byte) []byte {
	b := make([]byte, sidecarHeader, sidecarHeader+sidecarFrag*len(ix.Fragments)+4)
	le := binary.LittleEndian
	copy(b[0:4], sidecarMagic)
	le.PutUint16(b[4:], sidecarVersion)
	le.PutUint16(b[6:], 0)
	le.PutUint64(b[8:], uint64(size))
	le.PutUint64(b[16:], uint64(mod.UnixNano()))
	le.PutUint32(b[24:], uint32(headLen))
	copy(b[28:60], sum[:])
	le.PutUint64(b[60:], uint64(ix.InitSize))
	le.PutUint32(b[68:], ix.Timescale)
	le.PutUint64(b[72:], uint64(ix.parsed))
	le.PutUint32(b[80:], uint32(len(ix.Fragments)))
	var fr [sidecarFrag]byte
	for _, f := range ix.Fragments {
		le.PutUint64(fr[0:], uint64(f.Offset))
		le.PutUint64(fr[8:], uint64(f.Size))
		le.PutUint64(fr[16:], uint64(f.Time))
		le.PutUint64(fr[24:], uint64(f.Dur))
		le.PutUint64(fr[32:], uint64(f.KeyOff))
		fr[40] = 0
		if f.Key {
			fr[40] = 1
		}
		b = append(b, fr[:]...)
	}
	return le.AppendUint32(b, crc32.ChecksumIEEE(b))
}

// sidecarData is a decoded sidecar, before it is checked against its segment.
type sidecarData struct {
	size, initSize, parsed int64
	headLen                int64
	sum                    [32]byte
	timescale              uint32
	frags                  []Fragment
}

func decodeSidecar(b []byte) (*sidecarData, error) {
	le := binary.LittleEndian
	if len(b) < sidecarHeader+4 || string(b[0:4]) != sidecarMagic || le.Uint16(b[4:]) != sidecarVersion {
		return nil, errSidecar
	}
	if crc32.ChecksumIEEE(b[:len(b)-4]) != le.Uint32(b[len(b)-4:]) {
		return nil, errSidecar
	}
	n := int(le.Uint32(b[80:]))
	if n > sidecarMaxFrag || len(b) != sidecarHeader+sidecarFrag*n+4 {
		return nil, errSidecar
	}
	d := &sidecarData{
		size:      int64(le.Uint64(b[8:])),
		headLen:   int64(le.Uint32(b[24:])),
		initSize:  int64(le.Uint64(b[60:])),
		timescale: le.Uint32(b[68:]),
		parsed:    int64(le.Uint64(b[72:])),
		frags:     make([]Fragment, n),
	}
	copy(d.sum[:], b[28:60])
	if d.size <= 0 || d.headLen != sidecarHead(d.size) || d.initSize <= 0 || d.parsed < d.initSize || d.parsed > d.size || d.timescale == 0 {
		return nil, errSidecar
	}
	p := b[sidecarHeader:]
	for i := range d.frags {
		q := p[i*sidecarFrag:]
		d.frags[i] = Fragment{
			Offset: int64(le.Uint64(q[0:])),
			Size:   int64(le.Uint64(q[8:])),
			Time:   time.Duration(le.Uint64(q[16:])),
			Dur:    time.Duration(le.Uint64(q[24:])),
			KeyOff: time.Duration(le.Uint64(q[32:])),
			Key:    q[40] == 1,
		}
		if f := d.frags[i]; f.Offset < d.initSize || f.Size <= 0 || f.Offset+f.Size > d.parsed {
			return nil, errSidecar
		}
	}
	return d, nil
}

// readSidecar loads the sidecar of the segment open as f (size bytes now).
// It returns the index and the segment size the sidecar described (≤ size;
// the caller extends the index when it is less).
func readSidecar(path string, f io.ReaderAt, size int64) (*Index, int64, error) {
	sc, err := os.Open(SidecarPath(path))
	if err != nil {
		return nil, 0, err
	}
	defer sc.Close()
	info, err := sc.Stat()
	if err != nil {
		return nil, 0, err
	}
	if info.Size() > sidecarHeader+sidecarFrag*sidecarMaxFrag+4 {
		return nil, 0, errSidecar
	}
	b, err := io.ReadAll(sc)
	if err != nil {
		return nil, 0, err
	}
	d, err := decodeSidecar(b)
	if err != nil {
		return nil, 0, err
	}
	if d.size > size {
		return nil, 0, errSidecar // truncated or replaced
	}
	sum, err := headHash(f, d.headLen)
	if err != nil || sum != d.sum {
		return nil, 0, errSidecar
	}
	// The track table comes from the segment's own moov.
	ix := &Index{pending: -1}
	if err := ix.Extend(f, d.initSize); err != nil || !ix.hasMoov || ix.InitSize != d.initSize || ix.Timescale != d.timescale {
		return nil, 0, errSidecar
	}
	ix.Fragments = d.frags
	ix.parsed = d.parsed
	return ix, d.size, nil
}

// writeSidecar stores ix (describing the first size bytes of the segment
// open as f) next to the segment: a temporary file renamed into place, so a
// reader never sees half a sidecar.
func writeSidecar(path string, f io.ReaderAt, ix *Index, size int64, mod time.Time) error {
	h := sidecarHead(size)
	sum, err := headHash(f, h)
	if err != nil {
		return err
	}
	b := encodeSidecar(ix, size, mod, h, sum)
	dir, base := filepath.Split(path)
	tmp, err := os.CreateTemp(dir, "."+base+SidecarExt+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	_ = os.Chmod(name, 0o640)
	if err := os.Rename(name, SidecarPath(path)); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// sidecarAllowed: the events-mode rolling buffer is never indexed for
// playback, so it never gets sidecars (its segments move or go within
// minutes).
func sidecarAllowed(path string) bool {
	sep := string(filepath.Separator)
	return !strings.Contains(path, sep+BufferDir+sep)
}

// RemoveSidecar deletes the sidecar of the segment at path, if any.
func RemoveSidecar(path string) { _ = os.Remove(SidecarPath(path)) }

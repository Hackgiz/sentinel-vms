package recorder

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"sentinel-linux/internal/link"
)

var (
	cameraIDPattern  = regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}$`)
	segmentPattern   = regexp.MustCompile(`^(\d{8}-\d{6})-\d+\.mp4$`)
	hlsFilePattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.(m3u8|ts|mp4|m4s)$`)
	vaultFilePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.mp4$`)
)

// listSegments returns a camera's recorded segments, newest first.
func (r *Recorder) listSegments(id string) ([]link.Segment, error) {
	entries, err := os.ReadDir(filepath.Join(r.recordingsRoot(), id))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	out := []link.Segment{}
	for _, e := range entries {
		m := segmentPattern.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		info, err := os.Stat(filepath.Join(r.recordingsRoot(), id, e.Name())) // follows imported links (import_mac.go)
		if err != nil || info.Size() == 0 {
			continue
		}
		start, err := time.ParseInLocation("20060102-150405", m[1], time.Local)
		if err != nil {
			continue
		}
		out = append(out, link.Segment{
			Name:    e.Name(),
			Start:   start.Unix(),
			End:     info.ModTime().Unix(),
			Size:    info.Size(),
			Writing: time.Since(info.ModTime()) < 30*time.Second,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start > out[j].Start })
	return out, nil
}

// segmentAt finds the recording that covers t (with a little slack).
func (r *Recorder) segmentAt(cameraID string, t int64) *link.Segment {
	segs, err := r.listSegments(cameraID)
	if err != nil {
		return nil
	}
	for i := range segs {
		if segs[i].Start-2 <= t && t <= segs[i].End+5 {
			return &segs[i]
		}
	}
	return nil
}

// serveRecording streams one segment with Range support so players can seek.
func (r *Recorder) serveRecording(w http.ResponseWriter, req *http.Request) {
	id, file := req.PathValue("id"), req.PathValue("file")
	if !cameraIDPattern.MatchString(id) || !segmentPattern.MatchString(file) {
		writeError(w, http.StatusBadRequest, "bad recording path")
		return
	}
	f, err := os.Open(filepath.Join(r.recordingsRoot(), id, file))
	if err != nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, req, file, info.ModTime(), f) // Range requests → seeking works
}

// lockEvidence copies the recording covering a.At into the evidence vault
// (outside the retention-managed recordings directory) as <caseID>.mp4,
// hashes it and makes it read-only.
func (r *Recorder) lockEvidence(a link.EvidenceLockArgs) (link.EvidenceFile, string, string) {
	if !cameraIDPattern.MatchString(a.CameraID) || !vaultFilePattern.MatchString(a.CaseID+".mp4") {
		return link.EvidenceFile{}, link.CodeBadRequest, "bad evidence request"
	}
	seg := r.segmentAt(a.CameraID, a.At)
	if seg == nil {
		return link.EvidenceFile{}, link.CodeNoClip, "No recording is linked to this alarm yet, or it has already been cleaned up."
	}
	if err := os.MkdirAll(r.evidenceDir(), 0o750); err != nil {
		return link.EvidenceFile{}, link.CodeInternal, err.Error()
	}
	dst := filepath.Join(r.evidenceDir(), a.CaseID+".mp4")
	// A segment still being recorded (they run 15 min) is copied up to its
	// last complete MP4 box, so the alarm can be preserved right away.
	size, sum, err := copyHashed(filepath.Join(r.recordingsRoot(), a.CameraID, seg.Name), dst, seg.Writing)
	if err != nil {
		return link.EvidenceFile{}, link.CodeInternal, err.Error()
	}
	if size == 0 {
		_ = os.Remove(dst)
		return link.EvidenceFile{}, link.CodeClipWriting, "The recording for this alarm is still being written. Try again in a minute."
	}
	_ = os.Chmod(dst, 0o440)
	return link.EvidenceFile{File: filepath.Base(dst), Size: size, SHA256: sum, Start: seg.Start, End: seg.End, Writing: seg.Writing}, "", ""
}

// deleteEvidence removes a vault file the management side couldn't index;
// true if there was one.
func (r *Recorder) deleteEvidence(file string) bool {
	if !vaultFilePattern.MatchString(file) {
		return false
	}
	p := filepath.Join(r.evidenceDir(), file)
	_ = os.Chmod(p, 0o640)
	return os.Remove(p) == nil
}

func copyHashed(src, dst string, completeBoxesOnly bool) (int64, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()
	var rd io.Reader = in
	if completeBoxesOnly {
		info, err := in.Stat()
		if err != nil {
			return 0, "", err
		}
		n, err := completeMP4Prefix(in, info.Size())
		if err != nil {
			return 0, "", err
		}
		if _, err := in.Seek(0, io.SeekStart); err != nil {
			return 0, "", err
		}
		rd = io.LimitReader(in, n)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), rd)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// completeMP4Prefix returns the length of the file made of whole top-level
// MP4 boxes (size+type headers), dropping a fragment that's half written.
func completeMP4Prefix(f io.ReadSeeker, fileSize int64) (int64, error) {
	var off int64
	hdr := make([]byte, 16)
	for off+8 <= fileSize {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return 0, err
		}
		if _, err := io.ReadFull(f, hdr[:8]); err != nil {
			return off, nil
		}
		size := int64(uint32(hdr[0])<<24 | uint32(hdr[1])<<16 | uint32(hdr[2])<<8 | uint32(hdr[3]))
		switch size {
		case 0: // box runs to end of file — still open
			return off, nil
		case 1: // 64-bit size follows the type
			if _, err := io.ReadFull(f, hdr[8:16]); err != nil {
				return off, nil
			}
			size = 0
			for _, b := range hdr[8:16] {
				size = size<<8 | int64(b)
			}
		}
		if size < 8 || off+size > fileSize {
			return off, nil
		}
		off += size
	}
	return off, nil
}

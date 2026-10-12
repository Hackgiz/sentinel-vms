package recorder

// What a recording server does for the Mac importer (PLATFORM.md,
// "Decision 1"): index a camera's recordings from a Sentinel for Mac
// install read-only, and take imported evidence files into the vault.

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"sentinel-linux/internal/link"
)

// MARK: - Imported recordings

// The Mac app records with the same file names (MediaMTX,
// <root>/<camera>/YYYYmmdd-HHMMSS-micro.mp4). Each file in a camera's
// imported folder gets a hard link (a symbolic link across volumes) in
// recordings/<camera>/, so listing, playback, export and evidence see it
// like any other segment while the Mac folder is never written. Retention
// applies to the links as to every recording; removing one leaves the Mac
// file alone.

// importedLinked remembers folders already linked in this process
// ("<recordings root>\x00<camera>\x00<folder>"): the Mac app no longer
// writes there, so once is enough.
var importedLinked sync.Map

// linkImportedRecordings links each camera's imported recordings folder,
// noting failures in applied.Errors.
func (r *Recorder) linkImportedRecordings(cams []link.CameraConfig, applied *link.Applied) {
	for _, c := range cams {
		src := c.ImportedRecordingsPath
		if src == "" || !cameraIDPattern.MatchString(c.ID) {
			continue
		}
		key := r.recordingsRoot() + "\x00" + c.ID + "\x00" + src
		if _, done := importedLinked.Load(key); done {
			continue
		}
		n, err := linkSegments(src, filepath.Join(r.recordingsRoot(), c.ID))
		if err != nil {
			slog.Warn("imported recordings not indexed", "camera", c.Name, "folder", src, "err", err)
			if applied.Errors == nil {
				applied.Errors = map[string]string{}
			}
			applied.Errors[c.ID] = "imported recordings: " + err.Error()
			continue
		}
		importedLinked.Store(key, true)
		if n > 0 {
			slog.Info("imported recordings indexed", "camera", c.Name, "folder", src, "files", n)
		}
	}
}

// linkSegments links every recording file in src into dst that isn't
// there yet, and returns how many it linked.
func linkSegments(src, dst string) (int, error) {
	if !filepath.IsAbs(src) || filepath.Clean(src) != src {
		return 0, errors.New("the folder must be an absolute path")
	}
	info, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a folder", src)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.Type().IsRegular() || !segmentPattern.MatchString(e.Name()) {
			continue
		}
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if _, err := os.Lstat(to); err == nil {
			continue
		}
		if err := os.Link(from, to); err != nil {
			if err := os.Symlink(from, to); err != nil {
				return n, err
			}
		}
		n++
	}
	return n, nil
}

// MARK: - Imported evidence

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// evidenceImportMu serializes imported evidence uploads (one part file
// per case; the management server sends one upload per case at a time).
var evidenceImportMu sync.Mutex

// importEvidence runs one step of an imported evidence upload. The file
// is written to .import-<case>.part in the vault (never served: the vault
// serves *.mp4 only), checked against its size and hash, and then linked
// into place as <case>.mp4, read-only, without ever overwriting a file.
func (r *Recorder) importEvidence(a link.EvidenceImportArgs) (link.EvidenceImportResult, string, string) {
	var none link.EvidenceImportResult
	if strings.HasPrefix(a.CaseID, ".") || !vaultFilePattern.MatchString(a.CaseID+".mp4") || a.Size <= 0 || !sha256Pattern.MatchString(a.SHA256) {
		return none, link.CodeBadRequest, "bad evidence import request"
	}
	evidenceImportMu.Lock()
	defer evidenceImportMu.Unlock()
	if err := os.MkdirAll(r.evidenceDir(), 0o750); err != nil {
		return none, link.CodeInternal, err.Error()
	}
	dst := filepath.Join(r.evidenceDir(), a.CaseID+".mp4")
	part := filepath.Join(r.evidenceDir(), ".import-"+a.CaseID+".part")
	switch a.Op {
	case link.ImportAbort:
		_ = os.Remove(part)
		return none, "", ""

	case link.ImportWrite:
		if a.Offset < 0 || a.Offset+int64(len(a.Data)) > a.Size {
			return none, link.CodeBadRequest, "more data than the file's size"
		}
		var f *os.File
		var err error
		if a.Offset == 0 {
			if res, code, msg, ok := vaultHas(dst, a); ok {
				return res, code, msg
			}
			f, err = os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
		} else {
			f, err = os.OpenFile(part, os.O_WRONLY, 0)
			if err == nil {
				if info, serr := f.Stat(); serr != nil || info.Size() != a.Offset {
					f.Close()
					return none, link.CodeBadRequest, "upload out of order; start again"
				}
			}
		}
		if errors.Is(err, os.ErrNotExist) {
			return none, link.CodeBadRequest, "no upload in progress; start again"
		} else if err != nil {
			return none, link.CodeInternal, err.Error()
		}
		_, err = f.WriteAt(a.Data, a.Offset)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(part)
			return none, link.CodeInternal, err.Error()
		}
		return none, "", ""

	case link.ImportCommit:
		if res, code, msg, ok := vaultHas(dst, a); ok {
			_ = os.Remove(part)
			return res, code, msg
		}
		size, sum, err := hashFile(part)
		if errors.Is(err, os.ErrNotExist) {
			return none, link.CodeBadRequest, "no upload in progress; start again"
		} else if err != nil {
			return none, link.CodeInternal, err.Error()
		}
		if size != a.Size || sum != a.SHA256 {
			_ = os.Remove(part)
			return none, link.CodeBadRequest, "the uploaded file doesn't match its size and SHA-256"
		}
		if err := os.Link(part, dst); err != nil {
			_ = os.Remove(part)
			if res, code, msg, ok := vaultHas(dst, a); ok {
				return res, code, msg
			}
			return none, link.CodeInternal, err.Error()
		}
		_ = os.Remove(part)
		_ = os.Chmod(dst, 0o440)
		return link.EvidenceImportResult{Done: true, File: link.EvidenceFile{File: filepath.Base(dst), Size: size, SHA256: sum}}, "", ""
	}
	return none, link.CodeBadRequest, "unknown evidence import step " + a.Op
}

// vaultHas checks an existing vault file: the same file is done (ok), a
// different one is CodeExists; ok is false when there is none.
func vaultHas(dst string, a link.EvidenceImportArgs) (link.EvidenceImportResult, string, string, bool) {
	size, sum, err := hashFile(dst)
	if errors.Is(err, os.ErrNotExist) {
		return link.EvidenceImportResult{}, "", "", false
	} else if err != nil {
		return link.EvidenceImportResult{}, link.CodeInternal, err.Error(), true
	}
	if size != a.Size || sum != a.SHA256 {
		return link.EvidenceImportResult{}, link.CodeExists, "the evidence vault already has a different file for case " + a.CaseID, true
	}
	return link.EvidenceImportResult{Done: true, File: link.EvidenceFile{File: filepath.Base(dst), Size: size, SHA256: sum}}, "", "", true
}

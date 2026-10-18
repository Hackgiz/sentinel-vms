package server

// API v2: the self-verifying evidence package (WAVE8-DETECT-EVIDENCE.md,
// Stream C). GET /api/v2/evidence/{id}/package streams a zip of the case's
// media, manifest.json, any notes and an offline player.html. The manifest
// reuses the vault's recorded hash and size and the audit entries that
// mention the case (internal/evidencepkg).

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/evidencepkg"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2EvidencePackage(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/evidence/{id}/package", auth.ExportEvidence, s.handleV2EvidencePackage)
}

const maxPackageChain = 200

// fileResponse writes a video-plane response body to a file.
type fileResponse struct {
	h    http.Header
	code int
	f    *os.File
	err  error
}

func (w *fileResponse) Header() http.Header { return w.h }
func (w *fileResponse) WriteHeader(c int) {
	if w.code == 0 {
		w.code = c
	}
}
func (w *fileResponse) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	if w.code != http.StatusOK { // an error body is not media
		return len(b), nil
	}
	n, err := w.f.Write(b)
	if err != nil {
		w.err = err
	}
	return n, err
}

func (s *Server) handleV2EvidencePackage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ev, err := s.scopedEvidenceCase(ctx, r.PathValue("id"))
	if err != nil {
		writeV2Fail(w, err, "evidence")
		return
	}
	// Fetch the media into a temp file (it may be big) and hash it, so a
	// vault file that changed since lock time is refused rather than
	// packaged under a hash it doesn't have.
	tmp, err := os.CreateTemp("", "evidence-package-*.mp4")
	if err != nil {
		writeV2Err(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	fr := &fileResponse{h: http.Header{}, f: tmp}
	vr := r.Clone(ctx)
	vr.Header = http.Header{}
	vp := "/rs/evidence/" + url.PathEscape(filepath.Base(ev.File))
	vr.URL = &url.URL{Path: vp}
	vr.RequestURI = vp
	vr.Method = http.MethodGet
	s.recByID(ev.RecorderID).Video().ServeHTTP(fr, vr)
	switch {
	case fr.code == http.StatusNotFound || fr.code == 0:
		writeV2Error(w, http.StatusNotFound, codeNotFound, "the evidence file is missing on its recording server")
		return
	case fr.code != http.StatusOK || fr.err != nil:
		writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "the recording server holding this evidence could not supply it")
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeV2Err(w, err)
		return
	}
	h := sha256.New()
	size, err := io.Copy(h, tmp)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != ev.SHA256 {
		s.audit(r, "Evidence", "Evidence package refused", ev.CaseID+": file no longer matches its recorded SHA-256")
		writeV2Error(w, http.StatusConflict, codeConflict, "the evidence file no longer matches its recorded SHA-256; no package was made")
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeV2Err(w, err)
		return
	}
	chain, err := s.Store.AuditEntriesFor(ctx, maxPackageChain, ev.CaseID, ev.SHA256)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	m := &evidencepkg.Manifest{
		CaseID: ev.CaseID, EvidenceID: ev.ID, Title: ev.Title,
		Camera:   evidencepkg.CameraInfo{ID: ev.CameraID, Name: ev.CameraName},
		Range:    evidencepkg.RangeInfo{Label: ev.Range, FromMs: ev.FromMs, ToMs: ev.ToMs},
		Media:    evidencepkg.MediaInfo{File: evidencepkg.MediaFileName(ev.CaseID), Size: size, SHA256: ev.SHA256},
		Note:     ev.Note,
		LockedAt: ev.LockedAt, LockedBy: ev.LockedBy,
		ExportedBy: currentUser(r).Name, CreatedAt: time.Now().Unix(),
		Chain: chainEntries(chain),
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.TrimSuffix(evidencepkg.MediaFileName(ev.CaseID), ".mp4")+`-package.zip"`)
	w.Header().Set("X-Content-SHA256", ev.SHA256)
	// Audited before the stream starts: once bytes are out there is no
	// taking the export back.
	s.audit(r, "Evidence", "Exported evidence package", ev.CaseID+", sha256 "+ev.SHA256[:16]+"…")
	if err := evidencepkg.Write(w, m, tmp); err != nil {
		// Headers are gone; the client sees a truncated zip.
		return
	}
}

func chainEntries(in []store.AuditEntry) []evidencepkg.ChainEntry {
	out := make([]evidencepkg.ChainEntry, 0, len(in))
	for _, e := range in {
		out = append(out, evidencepkg.ChainEntry{ID: e.ID, Time: e.Time, User: e.User, Area: e.Area, Action: e.Action,
			Detail: e.Detail, PreviousHash: e.PreviousHash, ChainHash: e.ChainHash})
	}
	return out
}

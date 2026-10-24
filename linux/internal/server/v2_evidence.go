package server

// API v2: the Exports workspace. Evidence cases cut from any camera and
// time range (not only alarms), listed together with the alarm-created
// ones, downloaded, re-verified and released. The clip itself is cut by
// the same plumbing as the alarm lock and the v1 export: the owning
// recording server runs link.CmdExportClip and moves the result into its
// evidence vault with link.CmdEvidenceAdopt; the local server is just the
// recorder with id "local" (s.command).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/playback"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

func (s *Server) registerV2Evidence(mux *http.ServeMux) {
	s.v2Route(mux, "POST /api/v2/evidence", auth.ExportEvidence, s.handleV2ExportEvidence)
	s.v2Route(mux, "GET /api/v2/evidence", auth.ExportEvidence, s.handleV2ListEvidence)
	s.v2Route(mux, "GET /api/v2/evidence/{id}/download", auth.ExportEvidence, s.handleV2EvidenceDownload)
	s.v2Route(mux, "POST /api/v2/evidence/{id}/verify", auth.ExportEvidence, s.handleV2VerifyEvidence)
	s.v2Route(mux, "DELETE /api/v2/evidence/{id}", auth.ManageUsers, s.handleV2DeleteEvidence)
}

const (
	maxEvidenceTitle = 120
	maxEvidenceNote  = 500
)

// v2EvidenceCase is a v2Evidence with the export's range and note.
type v2EvidenceCase struct {
	v2Evidence
	Note       string `json:"note"`
	FromMs     int64  `json:"fromMs"` // 0: unknown (a lock cut from an alarm)
	ToMs       int64  `json:"toMs"`
	RecorderID string `json:"recorderId"`
	// WAVE23: "export" (cut from recordings) or "upload" (a video uploaded
	// from a phone, with who uploaded it, from which device, when it was
	// filmed and the event it was attached to).
	Source     string `json:"source"`
	UploadedBy string `json:"uploadedBy,omitempty"`
	Device     string `json:"device,omitempty"`
	RecordedAt int64  `json:"recordedAt,omitempty"`
	EventID    string `json:"eventId,omitempty"`
}

func toV2EvidenceCase(e *store.EvidenceCase) v2EvidenceCase {
	v := v2EvidenceCase{v2Evidence: toV2Evidence(&e.Evidence), Note: e.Note, FromMs: e.FromMs, ToMs: e.ToMs, RecorderID: e.RecorderID,
		Source: "export", UploadedBy: e.UploadedBy, Device: e.Device, RecordedAt: e.RecordedAt, EventID: e.EventID}
	if e.Source == store.EvidenceSourceUpload {
		v.Source = store.EvidenceSourceUpload
	}
	return v
}

// scopedEvidenceCase reads a case whose camera the caller may see
// (store.ErrNotFound otherwise).
func (s *Server) scopedEvidenceCase(ctx context.Context, id string) (*store.EvidenceCase, error) {
	e, err := s.Store.EvidenceCaseByID(ctx, id)
	if err == nil && !s.cameraScope(ctx).camera(e.CameraID) {
		return nil, store.ErrNotFound
	}
	return e, err
}

// cutEvidence has the recording server holding [fromMs, toMs) of a camera
// cut one clip into its evidence vault (hashed, read-only) and records the
// case. The caller has checked scope and the range.
func (s *Server) cutEvidence(ctx context.Context, cameraID string, fromMs, toMs int64, title, note, who string) (*store.EvidenceCase, error) {
	return s.cutEvidenceWith(ctx, cameraID, fromMs, toMs, title, note, who, exportOpts{})
}

// cutEvidenceWith is cutEvidence with export options (export_options.go).
// A watermark carries the case id, so the id is reserved before the cut.
func (s *Server) cutEvidenceWith(ctx context.Context, cameraID string, fromMs, toMs int64, title, note, who string, opts exportOpts) (*store.EvidenceCase, error) {
	task := tasks.Default.Start(tasks.KindExport, "Evidence export: "+s.cameraLabel(ctx, cameraID))
	defer task.Finish()
	rid := s.recorderAt(ctx, cameraID, fromMs/1000) // the server with the footage
	args := link.ExportArgs{CameraID: cameraID, From: fromMs, To: toMs, Blur: opts.blur}
	var caseID string
	if opts.watermark {
		var release func()
		caseID, release = s.reserveCaseID(ctx)
		defer release()
		args.Watermark = opts.watermarkText(who, time.Now(), caseID)
	}
	var res link.ExportResult
	if err := s.command(ctx, rid, link.CmdExportClip, args, &res); err != nil {
		task.Fail(err)
		return nil, err
	}
	task.Progress(60)
	if caseID == "" {
		caseID = s.Store.NextCaseID(ctx, time.Now())
	}
	var f link.EvidenceFile
	if err := s.command(ctx, rid, link.CmdEvidenceAdopt, link.EvidenceAdoptArgs{Token: res.Token, CaseID: caseID}, &f); err != nil {
		task.Fail(err)
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Clip Export"
	} else if len(title) > maxEvidenceTitle {
		title = title[:maxEvidenceTitle]
	}
	ev := &store.EvidenceCase{
		Evidence: store.Evidence{
			ID: store.NewID(), CaseID: caseID, CameraID: cameraID, CameraName: s.cameraLabel(ctx, cameraID), Title: title,
			Range: rangeLabel(time.UnixMilli(fromMs), time.UnixMilli(toMs)),
			File:  f.File, Size: f.Size, SHA256: f.SHA256, LockedAt: time.Now().Unix(), LockedBy: who,
		},
		Note: note, FromMs: fromMs, ToMs: toMs, RecorderID: rid,
	}
	if err := s.Store.AddEvidenceCase(ctx, ev); err != nil {
		_ = s.command(ctx, rid, link.CmdEvidenceDelete, link.FileArgs{File: f.File}, nil)
		task.Fail(err)
		return nil, err
	}
	return ev, nil
}

func (s *Server) handleV2ExportEvidence(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CameraID string `json:"cameraId"`
		FromMs   int64  `json:"fromMs"`
		ToMs     int64  `json:"toMs"`
		Title    string `json:"title"`
		Note     string `json:"note"`
		exportOptsIn
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	if !cameraIDPattern.MatchString(in.CameraID) || !s.cameraScope(ctx).camera(in.CameraID) {
		writeScopeNotFound(w, r)
		return
	}
	if _, err := s.Store.Camera(ctx, in.CameraID); err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	opts, oerr := parseExportOptions(in.exportOptsIn)
	if oerr != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, oerr.Error())
		return
	}
	switch {
	case in.FromMs <= 0 || in.ToMs <= in.FromMs:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "fromMs must be before toMs (Unix ms)")
		return
	case time.Duration(in.ToMs-in.FromMs)*time.Millisecond > playback.MaxClip:
		// TODO(wave5): chunked long exports
		writeV2Error(w, http.StatusBadRequest, codeBadRequest,
			fmt.Sprintf("an export can cover at most %d minutes; choose a shorter range", int(playback.MaxClip.Minutes())))
		return
	}
	note := strings.TrimSpace(in.Note)
	if len(note) > maxEvidenceNote {
		note = note[:maxEvidenceNote]
	}
	ctx, cancel := context.WithTimeout(ctx, exportTimeout+30*time.Second)
	defer cancel()
	ev, err := s.cutEvidenceWith(ctx, in.CameraID, in.FromMs, in.ToMs, in.Title, note, currentUser(r).Name, opts)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "Evidence", "Locked evidence", ev.CaseID+" clip "+ev.CameraName+" "+ev.Range+", sha256 "+ev.SHA256[:16]+"…"+opts.auditNote())
	writeJSON(w, http.StatusCreated, toV2EvidenceCase(ev))
}

func (s *Server) handleV2ListEvidence(w http.ResponseWriter, r *http.Request) {
	f := store.EvidenceCaseFilter{}
	if sc := s.cameraScope(r.Context()); !sc.all {
		f.CameraIDs = make([]string, 0, len(sc.ids))
		for id := range sc.ids {
			f.CameraIDs = append(f.CameraIDs, id)
		}
	}
	list, err := s.Store.EvidenceCases(r.Context(), f)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	out := make([]v2EvidenceCase, 0, len(list))
	for i := range list {
		out = append(out, toV2EvidenceCase(&list[i]))
	}
	v2Items(w, out)
}

func (s *Server) handleV2EvidenceDownload(w http.ResponseWriter, r *http.Request) {
	ev, err := s.scopedEvidenceCase(r.Context(), r.PathValue("id"))
	if err != nil {
		writeV2Fail(w, err, "evidence")
		return
	}
	s.serveVideoPath(w, r, ev.RecorderID, "/rs/evidence/"+filepath.Base(ev.File), func(h http.Header) {
		if r.Header.Get("Range") == "" {
			s.audit(r, "Evidence", "Downloaded evidence", ev.CaseID)
		}
		setContentType(h, "video/mp4")
		h.Set("Content-Disposition", `attachment; filename="`+ev.CaseID+`.mp4"`)
		h.Set("X-Content-SHA256", ev.SHA256)
	})
}

// handleV2VerifyEvidence has the recording server holding the file hash it
// again and compares with the hash recorded at lock time.
func (s *Server) handleV2VerifyEvidence(w http.ResponseWriter, r *http.Request) {
	ev, err := s.scopedEvidenceCase(r.Context(), r.PathValue("id"))
	if err != nil {
		writeV2Fail(w, err, "evidence")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	var res link.EvidenceVerifyResult
	if err := s.command(ctx, ev.RecorderID, link.CmdEvidenceVerify, link.FileArgs{File: ev.File}, &res); err != nil {
		if commandCode(err) == link.CodeNotFound {
			s.audit(r, "Evidence", "Verified evidence", ev.CaseID+": FAILED, file missing")
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "sha256": "", "message": "the evidence file is missing on its recording server"})
			return
		}
		writeV2Err(w, err)
		return
	}
	ok := strings.EqualFold(res.SHA256, ev.SHA256)
	result := "OK"
	if !ok {
		result = "FAILED, file changed"
	}
	s.audit(r, "Evidence", "Verified evidence", ev.CaseID+": "+result)
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "sha256": res.SHA256})
}

// handleV2DeleteEvidence releases a case: its file is deleted on the
// recording server and its row removed.
func (s *Server) handleV2DeleteEvidence(w http.ResponseWriter, r *http.Request) {
	ev, err := s.scopedEvidenceCase(r.Context(), r.PathValue("id"))
	if err != nil {
		writeV2Fail(w, err, "evidence")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	// Never orphan a file: if its server can't be reached the case stays.
	if err := s.command(ctx, ev.RecorderID, link.CmdEvidenceDelete, link.FileArgs{File: ev.File}, nil); err != nil {
		if errors.Is(err, link.ErrNoRecorder) {
			writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "the recording server holding this evidence isn't connected")
			return
		}
		writeV2Err(w, err)
		return
	}
	if err := s.Store.DeleteEvidenceCase(r.Context(), ev.ID); err != nil {
		writeV2Fail(w, err, "evidence")
		return
	}
	s.audit(r, "Evidence", "Released evidence", ev.CaseID+" "+ev.CameraName+" "+ev.Range+", sha256 "+ev.SHA256)
	w.WriteHeader(http.StatusNoContent)
}

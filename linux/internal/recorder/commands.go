package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/onvif"
)

// Command runs one request/response operation for the management side.
func (r *Recorder) Command(ctx context.Context, c link.Command) (link.Result, error) {
	bad := func(err error) link.Result {
		return link.Fail(c, link.CodeBadRequest, "invalid arguments: "+err.Error())
	}
	switch c.Type {
	case link.CmdRecordings:
		var a link.RecordingsArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		if !cameraIDPattern.MatchString(a.CameraID) {
			return link.Fail(c, link.CodeBadRequest, "bad camera id"), nil
		}
		segs, err := r.listSegments(a.CameraID)
		if err != nil {
			return link.Fail(c, link.CodeInternal, err.Error()), nil
		}
		return link.OK(c, segs), nil

	case link.CmdCovers:
		var a link.CoversArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		out := link.CoversResult{Covered: make([]bool, len(a.Items))}
		for i, q := range a.Items {
			out.Covered[i] = cameraIDPattern.MatchString(q.CameraID) && r.segmentAt(q.CameraID, q.At) != nil
		}
		return link.OK(c, out), nil

	case link.CmdTimeline:
		var a link.RangeArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		if !cameraIDPattern.MatchString(a.CameraID) {
			return link.Fail(c, link.CodeBadRequest, "bad camera id"), nil
		}
		tl, err := r.timeline(a)
		if err != nil {
			return link.Fail(c, link.CodeInternal, err.Error()), nil
		}
		return link.OK(c, tl), nil

	case link.CmdEvidenceLock:
		var a link.EvidenceLockArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		f, code, msg := r.lockEvidence(a)
		if code != "" {
			return link.Fail(c, code, msg), nil
		}
		r.auditCommand("Evidence", "Wrote evidence file", fmt.Sprintf("%s from %s at %s, sha256 %s",
			f.File, r.cameraName(a.CameraID), time.Unix(a.At, 0).UTC().Format(time.RFC3339), f.SHA256))
		return link.OK(c, f), nil

	case link.CmdEvidenceDelete:
		var a link.FileArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		if r.deleteEvidence(a.File) {
			r.auditCommand("Evidence", "Deleted evidence file", a.File)
		}
		return link.OK(c, nil), nil

	case link.CmdExportClip:
		var a link.ExportArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		res, code, msg := r.exportClip(ctx, a)
		if code != "" {
			return link.Fail(c, code, msg), nil
		}
		r.auditCommand("Recordings", "Exported clip", fmt.Sprintf("%s %s – %s, sha256 %s", r.cameraName(a.CameraID),
			time.UnixMilli(a.From).UTC().Format(time.RFC3339), time.UnixMilli(a.To).UTC().Format(time.RFC3339), res.SHA256))
		return link.OK(c, res), nil

	case link.CmdExportLong: // exportlong.go (WAVE7(C))
		var a link.ExportLongArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		st, code, msg := r.exportLong(a)
		if code != "" {
			return link.Fail(c, code, msg), nil
		}
		return link.OK(c, st), nil

	case link.CmdEdgeRecovery: // edgefill.go (WAVE9(C))
		var a link.EdgeRecoveryArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		st, code, msg := r.edgeRecovery(a)
		if code != "" {
			return link.Fail(c, code, msg), nil
		}
		return link.OK(c, st), nil

	case link.CmdEvidenceAdopt:
		var a link.EvidenceAdoptArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		f, code, msg := r.adoptEvidence(a)
		if code != "" {
			return link.Fail(c, code, msg), nil
		}
		r.auditCommand("Evidence", "Wrote evidence file", fmt.Sprintf("%s from an exported clip, sha256 %s", f.File, f.SHA256))
		return link.OK(c, f), nil

	case link.CmdEvidenceVerify: // evidence_verify.go
		var a link.FileArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		res, code, msg := r.verifyEvidence(a.File)
		if code != "" {
			return link.Fail(c, code, msg), nil
		}
		return link.OK(c, res), nil

	case link.CmdSnapshot:
		var a link.SnapshotArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		jpg, code, msg := r.snapshot(ctx, a)
		if code != "" {
			return link.Fail(c, code, msg), nil
		}
		return link.OK(c, link.SnapshotResult{JPEG: jpg}), nil

	case link.CmdEvidenceImport: // import_mac.go
		var a link.EvidenceImportArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		res, code, msg := r.importEvidence(a)
		if code != "" {
			return link.Fail(c, code, msg), nil
		}
		return link.OK(c, res), nil

	case link.CmdStorageInfo:
		return link.OK(c, r.storageInfo()), nil

	case link.CmdHealth:
		return link.OK(c, r.healthResult()), nil

	case link.CmdHWAccelStatus:
		return link.OK(c, r.hwAccelStatus()), nil

	case link.CmdLog:
		var a link.LogArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		if a.Lines < 1 || a.Lines > link.LogMaxLines {
			return link.Fail(c, link.CodeBadRequest, fmt.Sprintf("lines must be 1 to %d", link.LogMaxLines)), nil
		}
		if r.Log == nil {
			return link.Fail(c, link.CodeUnavailable, "this server keeps no log"), nil
		}
		return link.OK(c, link.LogResult{Text: r.Log(a.Lines)}), nil

	case link.CmdDiscover:
		var a link.DiscoverArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		if !r.scanMu.TryLock() {
			return link.Fail(c, link.CodeBusy, "a network scan is already running"), nil
		}
		defer r.scanMu.Unlock()
		devices, subnets, warnings := onvif.Scan(ctx, a.Sweep)
		raw, err := json.Marshal(devices)
		if err != nil {
			return link.Fail(c, link.CodeInternal, err.Error()), nil
		}
		detail := fmt.Sprintf("%d found", len(devices))
		if a.Sweep {
			detail += " (with subnet sweep)"
		}
		r.auditCommand("Cameras", "Scanned network for cameras", detail)
		return link.OK(c, link.DiscoverResult{Devices: raw, Subnets: subnets, Warnings: warnings}), nil

	case link.CmdProbe:
		var a link.ProbeArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		host := strings.TrimSpace(a.Host)
		if err := onvif.ValidateProbeTarget(host, strings.TrimSpace(a.ServiceURL)); err != nil {
			return link.Fail(c, link.CodeBadRequest, err.Error()), nil
		}
		d, mainURL, subURL, err := onvif.Probe(ctx, host, strings.TrimSpace(a.ServiceURL), a.Username, a.Password)
		if err != nil {
			if errors.Is(err, onvif.ErrAuth) {
				r.auditCommand("Cameras", "Camera sign-in failed during discovery", host)
			}
			return link.OK(c, link.ProbeResult{Error: err.Error(), AuthFailed: errors.Is(err, onvif.ErrAuth), SuggestedURL: onvif.SuggestedRTSPURL(host)}), nil
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return link.Fail(c, link.CodeInternal, err.Error()), nil
		}
		return link.OK(c, link.ProbeResult{Details: raw, MainURL: mainURL, SubURL: subURL}), nil

	case link.CmdEncoderGet: // encoder.go
		var a link.EncoderGetArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		return r.encoderGet(ctx, c, a), nil

	case link.CmdEncoderSet:
		var a link.EncoderSetArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		return r.encoderSet(ctx, c, a), nil

	case link.CmdStreamsDiscover: // encoder.go
		var a link.StreamsDiscoverArgs
		if err := c.Decode(&a); err != nil {
			return bad(err), nil
		}
		return r.streamsDiscover(ctx, c, a), nil
	}
	return link.Fail(c, link.CodeUnknownCommand, "unknown command "+c.Type), nil
}

// hwAccelStatus reports decode capabilities, per-camera decode state and
// the detection engine.
func (r *Recorder) hwAccelStatus() link.HWAccelStatus {
	var out link.HWAccelStatus
	if r.Detect == nil {
		return out
	}
	out.Available = true
	caps, cams := r.Detect.DecodeStatus()
	if cams == nil {
		cams = []detect.CameraDecode{}
	}
	out.Decode, _ = json.Marshal(caps)
	out.Cameras, _ = json.Marshal(cams)
	if r.Detect.Objects != nil {
		out.Detection, _ = json.Marshal(r.Detect.Objects.Info())
	}
	return out
}

// MARK: - Video plane

func (r *Recorder) videoHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /live/{id}/{file}", r.handleLive)
	mux.HandleFunc("POST /webrtc/{id}/whep", r.handleWHEPOffer)
	mux.HandleFunc("PATCH /webrtc/{id}/whep/{session}", r.handleWHEPSession)
	mux.HandleFunc("DELETE /webrtc/{id}/whep/{session}", r.handleWHEPSession)
	mux.HandleFunc("GET /recordings/{id}/{file}", r.serveRecording)
	mux.HandleFunc("GET /api/v1/cameras/{id}/playback.m3u8", r.handlePlaybackPlaylist)
	mux.HandleFunc("GET /api/v1/cameras/{id}/pieces/{file}", r.handlePlaybackPiece)
	mux.HandleFunc("GET /api/v1/cameras/{id}/init/{file}", r.handlePlaybackInit)
	mux.HandleFunc("POST /push/{id}/sessions", r.handlePushStart) // push.go (WAVE23)
	mux.HandleFunc("PUT /push/{id}/sessions/{session}/init", r.handlePushInit)
	mux.HandleFunc("PUT /push/{id}/sessions/{session}/segments/{seq}", r.handlePushSegment)
	mux.HandleFunc("DELETE /push/{id}/sessions/{session}", r.handlePushStop)
	mux.HandleFunc("GET /rs/exports/{token}", r.handleExportFile)
	mux.HandleFunc("GET /rs/evidence/{file}", r.handleEvidenceFile)
	mux.HandleFunc("GET /rs/events/{file}", r.handleEventSnapshot)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { writeError(w, http.StatusNotFound, "not found") })
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

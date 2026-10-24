package server

// API v2: video uploads into the evidence vault (WAVE23-PHONE-CAMERA.md
// section B and the first addendum: Sentinel Cloud only).
//
//	POST   /api/v2/evidence/uploads                       declare → 201 {uploadId, partSize, partCount, expiresAt}
//	PUT    /api/v2/evidence/uploads/{uploadId}/parts/{n}  one part (n from 0, each partSize but the last) → 204, idempotent
//	POST   /api/v2/evidence/uploads/{uploadId}/complete   verify (size, SHA-256, ftyp) and file it → 201 evidence item
//	                                                      (202 {"status":"processing"} if it takes longer: ask again)
//	DELETE /api/v2/evidence/uploads/{uploadId}            abort → 204
//
// Every route needs a signed-in user with acknowledgeAlarms (the right that
// adds incident items) and a paid Sentinel Cloud plan (402 cloud_required
// otherwise). An upload belongs to the user who started it. Parts are kept
// in <data>/uploads/<id>/ until the upload completes, is aborted or is
// swept 24 h after it started. A completed video is moved into the evidence
// vault of a recording server (the camera's, else the local one) with the
// same chunked import the Mac importer uses (link.CmdEvidenceImport), so it
// is listed, downloaded, shared and verified like any other evidence and
// never sits under recordings, where retention would delete it.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
)

const (
	uploadPartSize  = 8 << 20 // 8 MiB: Sentinel Cloud carries bodies up to that
	maxUploadSize   = 4 << 30 // 4 GiB
	uploadLifetime  = 24 * time.Hour
	uploadWait      = 60 * time.Second // complete answers 202 after this
	uploadSweepTick = 30 * time.Minute
	uploadHeader    = "X-Sentinel-Upload" // cookie callers set it (a cross-site form can't)
	uploadsPrefix   = "/api/v2/evidence/uploads/"
)

var (
	uploadIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	sha256Hex       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (s *Server) registerV2Uploads(mux *http.ServeMux) {
	p := auth.AcknowledgeAlarms
	s.v2Route(mux, "POST /api/v2/evidence/uploads", p, s.cloudOnly(s.handleV2UploadCreate))
	s.v2Route(mux, "PUT /api/v2/evidence/uploads/{uploadId}/parts/{n}", p, s.cloudOnly(s.handleV2UploadPart))
	s.v2Route(mux, "POST /api/v2/evidence/uploads/{uploadId}/complete", p, s.cloudOnly(s.handleV2UploadComplete))
	s.v2Route(mux, "DELETE /api/v2/evidence/uploads/{uploadId}", p, s.cloudOnly(s.handleV2UploadAbort))
}

// isUploadPart reports whether a request is a part upload the CSRF guard
// may let through without a JSON body: bearer calls, or cookie calls with
// the custom header.
func isUploadPart(r *http.Request) bool {
	return r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, uploadsPrefix) && strings.Contains(r.URL.Path, "/parts/") &&
		(bearerToken(r) != "" || r.Header.Get(uploadHeader) != "")
}

func (s *Server) cloudOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.requireCloud(w, r, msgUploadsCloud) {
			h(w, r)
		}
	}
}

// MARK: - State

// uploadState is per server: completions running or done (the row is gone
// once the video is filed, so a repeated complete finds its result here).
type uploadState struct {
	mu   sync.Mutex
	runs map[string]*uploadRun
}

type uploadRun struct {
	done     chan struct{}
	userID   string
	ev       *store.EvidenceCase
	err      error
	finished time.Time
}

var uploadStates sync.Map // *Server → *uploadState

func (s *Server) uploads() *uploadState {
	v, _ := uploadStates.LoadOrStore(s, &uploadState{runs: map[string]*uploadRun{}})
	return v.(*uploadState)
}

func (s *Server) uploadDir(id string) string {
	return filepath.Join(s.Store.DataDir(), "uploads", id)
}

func partCount(size int64) int { return int((size + uploadPartSize - 1) / uploadPartSize) }

func partLen(size int64, n int) int64 {
	if n == partCount(size)-1 {
		return size - int64(n)*uploadPartSize
	}
	return uploadPartSize
}

func partPath(dir string, n int) string { return filepath.Join(dir, fmt.Sprintf("%06d.part", n)) }

// MARK: - Handlers

func (s *Server) handleV2UploadCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FileName    string  `json:"fileName"`
		Size        int64   `json:"size"`
		SHA256      string  `json:"sha256"`
		ContentType string  `json:"contentType"`
		Title       *string `json:"title"`
		Note        *string `json:"note"`
		RecordedAt  *int64  `json:"recordedAt"`
		CameraID    *string `json:"cameraId"`
		EventID     *string `json:"eventId"`
		AlarmID     *string `json:"alarmId"`
		IncidentID  *string `json:"incidentId"`
		Device      *string `json:"device"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	bad := func(msg string) { writeV2Error(w, http.StatusBadRequest, codeBadRequest, msg) }
	name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(in.FileName, "\\", "/")))
	sum := strings.ToLower(strings.TrimSpace(in.SHA256))
	switch {
	case name == "" || name == "." || name == "/" || len(name) > 255:
		bad("fileName is required (up to 255 characters)")
		return
	case in.Size <= 0 || in.Size > maxUploadSize:
		bad("size must be from 1 byte to 4 GiB")
		return
	case !sha256Hex.MatchString(sum):
		bad("sha256 must be the file's SHA-256 in hex")
		return
	case in.ContentType != "video/mp4" && in.ContentType != "video/quicktime":
		bad("contentType must be video/mp4 or video/quicktime")
		return
	case in.RecordedAt != nil && *in.RecordedAt < 0:
		bad("recordedAt must be Unix milliseconds")
		return
	}
	u := currentUser(r)
	up := &store.Upload{
		UserID: u.ID, UserName: u.Name, FileName: name, Size: in.Size, SHA256: sum, ContentType: in.ContentType,
		Title: strings.TrimSuffix(name, filepath.Ext(name)), CreatedAt: time.Now().Unix(),
	}
	up.ExpiresAt = up.CreatedAt + int64(uploadLifetime/time.Second)
	if in.Title != nil && strings.TrimSpace(*in.Title) != "" {
		up.Title = strings.TrimSpace(*in.Title)
	}
	if len(up.Title) > maxEvidenceTitle {
		up.Title = up.Title[:maxEvidenceTitle]
	}
	if in.Note != nil {
		up.Note = strings.TrimSpace(*in.Note)
		if len(up.Note) > maxEvidenceNote {
			up.Note = up.Note[:maxEvidenceNote]
		}
	}
	if in.RecordedAt != nil {
		up.RecordedAt = *in.RecordedAt
	}
	up.Device = uploadDevice(in.Device, r.UserAgent())
	sc := s.cameraScope(ctx)
	// What the video is attached to; the camera defaults to the event's or
	// the alarm's.
	if in.EventID != nil && *in.EventID != "" {
		e, err := s.Store.Event(ctx, *in.EventID)
		if err != nil || !sc.camera(e.CameraID) {
			writeV2Error(w, http.StatusNotFound, codeNotFound, "event not found")
			return
		}
		up.EventID, up.CameraID = e.ID, e.CameraID
	}
	if in.AlarmID != nil && *in.AlarmID != "" {
		a, err := s.Store.Alarm(ctx, *in.AlarmID)
		if err != nil || !sc.alarm(a) {
			writeV2Error(w, http.StatusNotFound, codeNotFound, "alarm not found")
			return
		}
		up.AlarmID = a.ID
		if up.CameraID == "" {
			up.CameraID = a.CameraID
		}
	}
	if in.IncidentID != nil && *in.IncidentID != "" {
		if _, err := s.Store.Incident(ctx, *in.IncidentID); err != nil {
			writeV2Error(w, http.StatusNotFound, codeNotFound, "incident not found")
			return
		}
		up.IncidentID = *in.IncidentID
	}
	if in.CameraID != nil && *in.CameraID != "" {
		if !sc.camera(*in.CameraID) {
			writeV2Error(w, http.StatusNotFound, codeNotFound, "camera not found")
			return
		}
		if _, err := s.Store.Camera(ctx, *in.CameraID); err != nil {
			writeV2Fail(w, err, "camera")
			return
		}
		up.CameraID = *in.CameraID
	}
	if up.CameraID == "" && !sc.all {
		bad("choose the camera the video belongs to (cameraId)")
		return
	}
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	up.ID = hex.EncodeToString(raw)
	if err := os.MkdirAll(s.uploadDir(up.ID), 0o700); err != nil {
		writeV2Err(w, err)
		return
	}
	if err := s.Store.CreateUpload(ctx, up); err != nil {
		_ = os.RemoveAll(s.uploadDir(up.ID))
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"uploadId": up.ID, "partSize": uploadPartSize, "partCount": partCount(up.Size), "expiresAt": up.ExpiresAt * 1000,
	})
}

// uploadDevice is the device an upload came from: the body's, else the
// app's User-Agent (its first product token), printable and short.
func uploadDevice(body *string, ua string) string {
	d := ""
	if body != nil {
		d = *body
	}
	if strings.TrimSpace(d) == "" {
		// "Sentinel Mobile/3.6.0 (iPhone17,2; iOS 18.1)": the model inside the
		// parentheses, else the first product token.
		if _, rest, ok := strings.Cut(ua, "("); ok {
			d, _, _ = strings.Cut(rest, ";")
			d = strings.TrimSuffix(strings.TrimSpace(d), ")")
		}
		if d == "" {
			d, _, _ = strings.Cut(ua, " ")
		}
	}
	d = strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return -1
		}
		return c
	}, strings.TrimSpace(d))
	if rs := []rune(d); len(rs) > 64 {
		d = string(rs[:64])
	}
	return d
}

// ownUpload reads the path's upload if the caller started it (404 otherwise).
func (s *Server) ownUpload(w http.ResponseWriter, r *http.Request) (*store.Upload, bool) {
	id := r.PathValue("uploadId")
	if uploadIDPattern.MatchString(id) {
		if up, err := s.Store.Upload(r.Context(), id); err == nil && up.UserID == currentUser(r).ID && up.ExpiresAt > time.Now().Unix() {
			return up, true
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeV2Err(w, err)
			return nil, false
		}
	}
	writeV2Error(w, http.StatusNotFound, codeNotFound, "upload not found (finished, aborted or expired)")
	return nil, false
}

func (s *Server) handleV2UploadPart(w http.ResponseWriter, r *http.Request) {
	up, ok := s.ownUpload(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n >= partCount(up.Size) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, fmt.Sprintf("part must be from 0 to %d", partCount(up.Size)-1))
		return
	}
	if s.uploadRunning(up.ID) {
		writeV2Error(w, http.StatusConflict, codeConflict, "this upload is being completed")
		return
	}
	want := partLen(up.Size, n)
	if r.ContentLength > want {
		writeV2Error(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("part %d must be %d bytes", n, want))
		return
	}
	dir := s.uploadDir(up.ID)
	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		writeV2Err(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	got, err := io.Copy(tmp, http.MaxBytesReader(w, r.Body, want))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeV2Error(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("part %d must be %d bytes", n, want))
		return
	case err != nil:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "the part didn't arrive whole: "+err.Error())
		return
	case got != want:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, fmt.Sprintf("part %d must be %d bytes (got %d)", n, want, got))
		return
	}
	// A repeat replaces the part in one step (idempotent).
	if err := os.Rename(tmp.Name(), partPath(dir, n)); err != nil {
		writeV2Err(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleV2UploadAbort(w http.ResponseWriter, r *http.Request) {
	up, ok := s.ownUpload(w, r)
	if !ok {
		return
	}
	if s.uploadRunning(up.ID) {
		writeV2Error(w, http.StatusConflict, codeConflict, "this upload is being completed")
		return
	}
	s.dropUpload(r.Context(), up.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) uploadRunning(id string) bool {
	st := s.uploads()
	st.mu.Lock()
	defer st.mu.Unlock()
	run := st.runs[id]
	if run == nil {
		return false
	}
	select {
	case <-run.done:
		return false
	default:
		return true
	}
}

func (s *Server) dropUpload(ctx context.Context, id string) {
	_ = os.RemoveAll(s.uploadDir(id))
	if err := s.Store.DeleteUpload(ctx, id); err != nil {
		slog.Warn("upload row not removed", "upload", id, "err", err)
	}
}

func (s *Server) handleV2UploadComplete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uploadId")
	st := s.uploads()
	st.mu.Lock()
	run := st.runs[id]
	st.mu.Unlock()
	if run == nil {
		up, ok := s.ownUpload(w, r)
		if !ok {
			return
		}
		st.mu.Lock()
		if run = st.runs[id]; run == nil {
			run = &uploadRun{done: make(chan struct{}), userID: currentUser(r).ID}
			st.runs[id] = run
			who, ip := currentUser(r), clientIP(r)
			go func() {
				ev, err := s.completeUpload(s.ctxOrBackground(), up, who, ip)
				st.mu.Lock()
				run.ev, run.err, run.finished = ev, err, time.Now()
				if err != nil {
					delete(st.runs, id) // the client may fix the parts and try again
				}
				st.mu.Unlock()
				close(run.done)
			}()
		}
		st.mu.Unlock()
	}
	if run.userID != currentUser(r).ID {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "upload not found (finished, aborted or expired)")
		return
	}
	select {
	case <-run.done:
	case <-time.After(uploadWait):
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "processing"})
		return
	case <-r.Context().Done():
		return
	}
	st.mu.Lock()
	ev, err := run.ev, run.err
	st.mu.Unlock()
	if err != nil {
		writeV2Fail(w, err, "upload")
		return
	}
	writeJSON(w, http.StatusCreated, toV2EvidenceCase(ev))
}

// completeUpload checks the parts against the declared size and SHA-256
// and that the file is an MP4/QuickTime movie (ftyp), moves it into an
// evidence vault, records and attaches the evidence and audits it.
func (s *Server) completeUpload(ctx context.Context, up *store.Upload, who *store.User, ip string) (*store.EvidenceCase, error) {
	dir := s.uploadDir(up.ID)
	n := partCount(up.Size)
	var missing []string
	for i := 0; i < n; i++ {
		if fi, err := os.Stat(partPath(dir, i)); err != nil || fi.Size() != partLen(up.Size, i) {
			missing = append(missing, strconv.Itoa(i))
		}
	}
	if len(missing) > 0 {
		if len(missing) > 20 {
			missing = append(missing[:20], "…")
		}
		return nil, inputError{"parts missing: " + strings.Join(missing, ", ")}
	}
	parts := &partsReader{dir: dir, n: n}
	head := make([]byte, 8)
	if _, err := io.ReadFull(parts, head); err != nil {
		return nil, err
	}
	if string(head[4:8]) != "ftyp" {
		parts.Close()
		return nil, inputError{"this isn't an MP4 or QuickTime video (no ftyp box at the start)"}
	}
	h := sha256.New()
	h.Write(head)
	size, err := io.Copy(h, parts)
	parts.Close()
	if err != nil {
		return nil, err
	}
	if size+8 != up.Size || hex.EncodeToString(h.Sum(nil)) != up.SHA256 {
		return nil, inputError{"the uploaded file doesn't match its sha256; send the parts again or start over"}
	}

	rid := s.uploadRecorder(ctx, up.CameraID)
	if rid == "" {
		return nil, link.ErrNoRecorder
	}
	caseID := s.Store.NextCaseID(ctx, time.Now())
	f, err := s.importUpload(ctx, rid, caseID, up)
	if err != nil {
		return nil, err
	}
	cameraName := "Uploaded video"
	if up.CameraID != "" {
		cameraName = s.cameraLabel(ctx, up.CameraID)
	}
	rangeText := "Uploaded " + time.Now().Format("Jan 2 15:04")
	if up.RecordedAt > 0 {
		rangeText = "Filmed " + time.UnixMilli(up.RecordedAt).Format("Jan 2 15:04:05")
	}
	ev := &store.EvidenceCase{
		Evidence: store.Evidence{
			ID: store.NewID(), CaseID: caseID, AlarmID: up.AlarmID, CameraID: up.CameraID, CameraName: cameraName, Title: up.Title,
			Range: rangeText, File: f.File, Size: f.Size, SHA256: f.SHA256, LockedAt: time.Now().Unix(), LockedBy: who.Name,
		},
		Note: up.Note, RecorderID: rid, Source: store.EvidenceSourceUpload, UploadedBy: who.Name, Device: up.Device,
		RecordedAt: up.RecordedAt, EventID: up.EventID,
	}
	if err := s.Store.AddEvidenceCase(ctx, ev); err != nil {
		_ = s.command(ctx, rid, link.CmdEvidenceDelete, link.FileArgs{File: f.File}, nil)
		return nil, err
	}
	detail := ev.CaseID + ", " + humanBytes(ev.Size) + ", sha256 " + ev.SHA256[:16] + "…"
	if ev.Device != "" {
		detail += ", from " + ev.Device
	}
	if up.IncidentID != "" {
		if _, err := s.Store.AddIncidentItem(ctx, up.IncidentID, "evidence", ev.ID, up.Note, who.Name); err != nil {
			slog.Warn("uploaded video not attached to its incident", "incident", up.IncidentID, "err", err)
		} else {
			detail += ", added to incident " + up.IncidentID
		}
	}
	if up.AlarmID != "" {
		_ = s.Store.AddAlarmNote(ctx, up.AlarmID, "Video uploaded by "+who.Name+": "+up.Title+" ("+ev.CaseID+")")
	}
	if err := s.Store.Audit(ctx, who.Name, "Evidence", "Uploaded video "+up.Title, detail+auditFrom(ip)); err != nil {
		slog.Error("audit failed", "err", err)
	}
	s.dropUpload(ctx, up.ID)
	return ev, nil
}

func auditFrom(ip string) string {
	if ip == "" {
		return ""
	}
	return " (" + ip + ")"
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (n+1023)/1024)
}

// uploadRecorder picks the recording server whose vault takes an upload:
// the camera's, else the local one, else any connected one.
func (s *Server) uploadRecorder(ctx context.Context, cameraID string) string {
	if cameraID != "" {
		if c, err := s.Store.Camera(ctx, cameraID); err == nil && s.recorderConnected(c.RecorderID) {
			return c.RecorderID
		}
	}
	if s.recorderConnected(store.LocalRecorderID) {
		return store.LocalRecorderID
	}
	ids := s.ConnectedRecorders()
	sort.Strings(ids)
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// importUpload streams the parts to a recording server's vault in
// link.ImportChunk steps (in-process or over the control link).
func (s *Server) importUpload(ctx context.Context, rid, caseID string, up *store.Upload) (link.EvidenceFile, error) {
	parts := &partsReader{dir: s.uploadDir(up.ID), n: partCount(up.Size)}
	defer parts.Close()
	buf := make([]byte, link.ImportChunk)
	var off int64
	abort := func() {
		_ = s.command(context.Background(), rid, link.CmdEvidenceImport, link.EvidenceImportArgs{Op: link.ImportAbort, CaseID: caseID, Size: up.Size, SHA256: up.SHA256}, nil)
	}
	for off < up.Size {
		k, err := io.ReadFull(parts, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			abort()
			return link.EvidenceFile{}, err
		}
		if k == 0 {
			break
		}
		var res link.EvidenceImportResult
		if err := s.command(ctx, rid, link.CmdEvidenceImport, link.EvidenceImportArgs{Op: link.ImportWrite, CaseID: caseID, Offset: off, Data: buf[:k], Size: up.Size, SHA256: up.SHA256}, &res); err != nil {
			abort()
			return link.EvidenceFile{}, err
		}
		off += int64(k)
	}
	var res link.EvidenceImportResult
	if err := s.command(ctx, rid, link.CmdEvidenceImport, link.EvidenceImportArgs{Op: link.ImportCommit, CaseID: caseID, Size: up.Size, SHA256: up.SHA256}, &res); err != nil {
		abort()
		return link.EvidenceFile{}, err
	}
	return res.File, nil
}

// partsReader reads an upload's parts in order as one stream.
type partsReader struct {
	dir string
	n   int
	i   int
	f   *os.File
}

func (p *partsReader) Read(b []byte) (int, error) {
	for {
		if p.f == nil {
			if p.i >= p.n {
				return 0, io.EOF
			}
			f, err := os.Open(partPath(p.dir, p.i))
			if err != nil {
				return 0, err
			}
			p.f, p.i = f, p.i+1
		}
		k, err := p.f.Read(b)
		if errors.Is(err, io.EOF) {
			p.f.Close()
			p.f = nil
			if k > 0 {
				return k, nil
			}
			continue
		}
		return k, err
	}
}

func (p *partsReader) Close() {
	if p.f != nil {
		p.f.Close()
		p.f = nil
	}
}

// MARK: - Sweep

// runUploadSweep removes uploads not finished within 24 h, and part
// folders without an upload, every 30 minutes. Blocks until ctx ends.
func (s *Server) runUploadSweep(ctx context.Context) {
	t := time.NewTicker(uploadSweepTick)
	defer t.Stop()
	for {
		s.sweepUploads(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) sweepUploads(ctx context.Context, now time.Time) int {
	ids, err := s.Store.ExpiredUploads(ctx, now.Unix())
	if err != nil {
		return 0
	}
	swept := 0
	for _, id := range ids {
		if s.uploadRunning(id) {
			continue
		}
		s.dropUpload(ctx, id)
		swept++
	}
	// Folders whose row is gone (a crash between the two).
	if live, err := s.Store.UploadIDs(ctx); err == nil {
		if entries, err := os.ReadDir(filepath.Join(s.Store.DataDir(), "uploads")); err == nil {
			for _, e := range entries {
				if e.IsDir() && !live[e.Name()] && !s.uploadRunning(e.Name()) {
					if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > time.Hour {
						_ = os.RemoveAll(filepath.Join(s.Store.DataDir(), "uploads", e.Name()))
					}
				}
			}
		}
	}
	// Forget finished completions after an hour.
	st := s.uploads()
	st.mu.Lock()
	for id, run := range st.runs {
		if !run.finished.IsZero() && now.Sub(run.finished) > time.Hour {
			delete(st.runs, id)
		}
	}
	st.mu.Unlock()
	if swept > 0 {
		slog.Info("removed unfinished video uploads", "count", swept)
	}
	return swept
}

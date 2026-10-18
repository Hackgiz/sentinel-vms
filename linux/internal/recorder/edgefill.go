package recorder

// Edge recording recovery (WAVE9-REACH.md, stream C): link.CmdEdgeRecovery.
// After an outage the camera's own SD card (ONVIF Profile G) usually holds
// what we missed. A job finds the gaps in this server's recordings of a
// camera over a range (the playback index's spans, playback.BuildPlan),
// asks the camera which of its recordings cover them, and pulls each gap
// in segment-sized pieces over RTSP replay (onvif.ReplayProxy → the
// bundled ffmpeg, stream copy into fragmented MP4). Each piece becomes an
// ordinary segment in the camera's recording directory, named for its
// start like MediaMTX's, with a "<segment>.edge" marker beside it; it then
// plays back, exports and expires like any footage, and the timeline
// hatches it. An existing file is never replaced: segments are created
// exclusively (hard link or O_EXCL copy), and a piece is dropped if local
// footage turned up meanwhile.
//
// The camera's edgeRecording flag (link.CameraStatus) comes from a probe
// run when a camera's ONVIF address or login is applied, refreshed from
// the regular status report (no loop of its own).

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/playback"
)

const (
	edgePieceMax   = 15 * time.Minute // one MediaMTX segment (recordSegmentDuration)
	edgeMinGap     = 10 * time.Second // shorter holes are keyframe slack between segments
	edgeMinPiece   = 2 * time.Second
	edgeKeep       = time.Hour // a finished job's status is remembered this long
	edgeMaxJobs    = 2         // recoveries running at once on this server
	edgeOpenBudget = 45 * time.Second
	edgeMarkerExt  = ".edge"

	edgeProbeTTL     = 6 * time.Hour
	edgeProbeRetry   = 10 * time.Minute // after an unreachable camera
	edgeProbeTimeout = 20 * time.Second
)

type edgeState struct {
	mu      sync.Mutex
	jobs    map[string]*edgeJob
	probes  map[string]*edgeProbe
	placeMu sync.Mutex // one segment placement at a time (re-check, then create)
}

type edgeJob struct {
	cancel   context.CancelFunc
	cameraID string
	work     string

	mu      sync.Mutex
	st      link.EdgeRecoveryStatus
	endedAt time.Time
}

func (j *edgeJob) status() link.EdgeRecoveryStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st
}

func (j *edgeJob) update(fn func(st *link.EdgeRecoveryStatus)) {
	j.mu.Lock()
	fn(&j.st)
	j.mu.Unlock()
}

// edgeWorkDir holds the pieces of running recoveries until they are placed.
func (r *Recorder) edgeWorkDir() string { return filepath.Join(r.DataDir, "edge-work") }

// MARK: - Gaps and pieces

// edgeSpan is a stretch of wall time.
type edgeSpan struct{ Start, End time.Time }

// edgePiece is one fetch: [Start, End) of camera recording Recording.
type edgePiece struct {
	Start, End time.Time
	Recording  string
}

// edgeGaps returns the stretches of [from, to) with no local recording,
// oldest first, each at least edgeMinGap long. Playable footage counts
// from the playback plan (the same spans the timeline shows); a segment
// that can't be indexed yet counts from its name to its end so a file
// being written, or a damaged one, is never mistaken for a hole.
func edgeGaps(files []playback.File, from, to time.Time) []edgeSpan {
	var covered []edgeSpan
	for _, sp := range playback.BuildPlan(files, from, to).Spans {
		covered = append(covered, edgeSpan{sp.Start, sp.End})
	}
	for i := range files {
		f := &files[i]
		if !f.Index.Playable() {
			covered = append(covered, edgeSpan{f.Start, f.End()})
		}
	}
	sort.Slice(covered, func(i, j int) bool { return covered[i].Start.Before(covered[j].Start) })
	var gaps []edgeSpan
	cur := from
	add := func(end time.Time) {
		if end.After(to) {
			end = to
		}
		if end.Sub(cur) >= edgeMinGap {
			gaps = append(gaps, edgeSpan{cur, end})
		}
	}
	for _, c := range covered {
		if !c.End.After(cur) {
			continue
		}
		if c.Start.After(cur) {
			add(c.Start)
		}
		cur = c.End
		if !cur.Before(to) {
			return gaps
		}
	}
	add(to)
	return gaps
}

// planEdgePieces intersects the gaps with the camera's recordings (for
// each gap the recording covering most of it) and cuts the result into
// pieces of at most edgePieceMax.
func planEdgePieces(gaps []edgeSpan, recs []onvif.EdgeRecording) []edgePiece {
	var out []edgePiece
	for _, g := range gaps {
		best, bestDur := -1, time.Duration(0)
		var bs, be time.Time
		for i, rec := range recs {
			rf, rt := rec.Coverage()
			s, e := later(g.Start, rf), earlier(g.End, rt)
			if d := e.Sub(s); d > bestDur {
				best, bestDur, bs, be = i, d, s, e
			}
		}
		if best < 0 || bestDur < edgeMinPiece {
			continue
		}
		for s := bs; be.Sub(s) >= edgeMinPiece; {
			e := earlier(s.Add(edgePieceMax), be)
			out = append(out, edgePiece{Start: s, End: e, Recording: recs[best].Token})
			s = e
		}
	}
	return out
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// edgeSegmentName names a segment like MediaMTX's recordPath
// (%Y%m%d-%H%M%S-%f, local time, microseconds).
func edgeSegmentName(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	t = t.In(loc)
	return fmt.Sprintf("%s-%06d.mp4", t.Format("20060102-150405"), t.Nanosecond()/1000)
}

// MARK: - Command

func (r *Recorder) edgeRecovery(a link.EdgeRecoveryArgs) (link.EdgeRecoveryStatus, string, string) {
	switch a.Op {
	case link.EdgeOpStart:
		return r.startEdgeRecovery(a)
	case link.EdgeOpStatus, link.EdgeOpCancel:
		r.edge.mu.Lock()
		j := r.edge.jobs[a.JobID]
		r.edge.mu.Unlock()
		if j == nil {
			return link.EdgeRecoveryStatus{}, link.CodeNotFound, "no such recovery job (the server may have restarted)"
		}
		if a.Op == link.EdgeOpCancel {
			j.cancel()
			for i := 0; i < 100 && j.status().State == link.EdgeRunning; i++ {
				time.Sleep(20 * time.Millisecond)
			}
		}
		return j.status(), "", ""
	}
	return link.EdgeRecoveryStatus{}, link.CodeBadRequest, "unknown recovery operation"
}

func (r *Recorder) startEdgeRecovery(a link.EdgeRecoveryArgs) (link.EdgeRecoveryStatus, string, string) {
	none := link.EdgeRecoveryStatus{}
	if !cameraIDPattern.MatchString(a.CameraID) {
		return none, link.CodeBadRequest, "bad camera id"
	}
	cam, ok := r.camera(a.CameraID)
	if !ok {
		return none, link.CodeNotFound, "this recording server doesn't have that camera"
	}
	if cam.ONVIFURL == "" {
		return none, link.CodeBadRequest, "this camera was added without ONVIF, so its recordings can't be searched"
	}
	now := time.Now()
	from, to := time.UnixMilli(a.From), time.UnixMilli(a.To)
	switch {
	case a.From <= 0 || !to.After(from):
		return none, link.CodeBadRequest, "from must be before to (Unix ms)"
	case a.To-a.From > link.MaxEdgeRecoveryMs:
		return none, link.CodeBadRequest, "a recovery can cover at most 24 hours; choose a shorter range"
	case !from.Before(now):
		return none, link.CodeBadRequest, "the range is in the future"
	}
	if to.After(now) {
		to = now
	}
	ffmpeg := r.FFmpegPath()
	if ffmpeg == "" {
		return none, link.CodeUnavailable, playback.ErrNoFFmpeg.Error()
	}
	files, err := r.library().Files(a.CameraID, from, to)
	if err != nil {
		return none, link.CodeInternal, err.Error()
	}
	gaps := edgeGaps(files, from, to)
	if err := os.MkdirAll(r.edgeWorkDir(), 0o750); err != nil {
		return none, link.CodeInternal, err.Error()
	}

	r.edge.mu.Lock()
	defer r.edge.mu.Unlock()
	if r.edge.jobs == nil {
		r.edge.jobs = map[string]*edgeJob{}
	}
	running := 0
	for k, o := range r.edge.jobs {
		st := o.status()
		switch {
		case st.State == link.EdgeRunning && o.cameraID == a.CameraID:
			return none, link.CodeBusy, "a recovery is already running for this camera"
		case st.State == link.EdgeRunning:
			running++
		case time.Since(o.endedAt) > edgeKeep:
			delete(r.edge.jobs, k)
		}
	}
	if running >= edgeMaxJobs {
		return none, link.CodeBusy, "other recoveries are running — try again when they finish"
	}
	r.sweepEdgeWorkLocked()
	work, err := os.MkdirTemp(r.edgeWorkDir(), "job-")
	if err != nil {
		return none, link.CodeInternal, err.Error()
	}
	ctx, cancel := context.WithCancel(r.ctx())
	id := randomHex(8)
	job := &edgeJob{cancel: cancel, cameraID: a.CameraID, work: work,
		st: link.EdgeRecoveryStatus{JobID: id, State: link.EdgeRunning, Gaps: len(gaps)}}
	r.edge.jobs[id] = job

	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		defer cancel()
		defer os.RemoveAll(work)
		r.runEdgeRecovery(ctx, job, ffmpeg, cam, from, to, gaps)
	}()
	return job.status(), "", ""
}

// sweepEdgeWorkLocked removes work directories no running job owns. The
// caller holds edge.mu.
func (r *Recorder) sweepEdgeWorkLocked() {
	entries, err := os.ReadDir(r.edgeWorkDir())
	if err != nil {
		return
	}
	active := map[string]bool{}
	for _, j := range r.edge.jobs {
		if j.status().State == link.EdgeRunning {
			active[j.work] = true
		}
	}
	for _, e := range entries {
		if p := filepath.Join(r.edgeWorkDir(), e.Name()); !active[p] {
			_ = os.RemoveAll(p)
		}
	}
}

func edgeCode(err error) string {
	switch {
	case errors.Is(err, onvif.ErrNoEdgeRecording), errors.Is(err, playback.ErrNoFFmpeg):
		return link.CodeUnavailable
	case errors.Is(err, onvif.ErrAuth):
		return link.CodeBadRequest
	case errors.Is(err, context.DeadlineExceeded):
		return link.CodeTimeout
	}
	return link.CodeInternal
}

func (r *Recorder) runEdgeRecovery(ctx context.Context, job *edgeJob, ffmpeg string, cam link.CameraConfig, from, to time.Time, gaps []edgeSpan) {
	finish := func(fn func(st *link.EdgeRecoveryStatus)) {
		job.mu.Lock()
		fn(&job.st)
		job.endedAt = time.Now()
		job.mu.Unlock()
	}
	fail := func(err error) {
		if ctx.Err() != nil {
			finish(func(st *link.EdgeRecoveryStatus) { st.State, st.Error = link.EdgeCanceled, "canceled" })
			return
		}
		slog.Warn("edge recovery failed", "camera", cam.Name, "err", err)
		finish(func(st *link.EdgeRecoveryStatus) {
			st.State, st.Error, st.Code = link.EdgeFailed, err.Error(), edgeCode(err)
		})
	}
	r.sweepEdgeMarkers(cam.ID)
	if len(gaps) == 0 {
		finish(func(st *link.EdgeRecoveryStatus) { st.State = link.EdgeDone })
		return
	}

	octx, cancel := context.WithTimeout(ctx, edgeOpenBudget)
	edge, err := onvif.OpenEdge(octx, cam.ONVIFURL, cam.Username, cam.Password)
	var recs []onvif.EdgeRecording
	if err == nil {
		recs, err = edge.Recordings(octx)
	}
	cancel()
	if err != nil {
		fail(err)
		return
	}
	pieces := planEdgePieces(gaps, recs)
	job.update(func(st *link.EdgeRecoveryStatus) { st.Total = len(pieces) })

	uris := map[string]string{}
	var lastErr error
	failed := 0
	for i, pc := range pieces {
		if ctx.Err() != nil {
			break
		}
		uri, ok := uris[pc.Recording]
		if !ok {
			uctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			uri, err = edge.ReplayURI(uctx, pc.Recording)
			cancel()
			if err != nil {
				lastErr, failed = err, failed+1
				job.update(func(st *link.EdgeRecoveryStatus) { st.Done++ })
				continue
			}
			uris[pc.Recording] = uri
		}
		got, err := r.recoverPiece(ctx, job.work, ffmpeg, cam, uri, pc, i)
		job.update(func(st *link.EdgeRecoveryStatus) {
			st.Done++
			switch {
			case err != nil:
			case got > 0:
				st.Recovered++
				st.Seconds += got.Seconds()
			default:
				st.Skipped++
			}
		})
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			slog.Warn("edge recovery piece failed", "camera", cam.Name, "from", pc.Start, "err", err)
			lastErr, failed = err, failed+1
		}
	}
	st := job.status()
	if st.Recovered > 0 {
		r.auditCommand("Recordings", "Recovered footage from camera", fmt.Sprintf("%s: %d segment(s), %s, from the camera's card for %s – %s",
			r.cameraName(cam.ID), st.Recovered, time.Duration(st.Seconds*float64(time.Second)).Round(time.Second).String(), from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)))
	}
	switch {
	case ctx.Err() != nil:
		finish(func(st *link.EdgeRecoveryStatus) { st.State, st.Error = link.EdgeCanceled, "canceled" })
	case len(pieces) > 0 && failed == len(pieces):
		fail(lastErr)
	default:
		finish(func(st *link.EdgeRecoveryStatus) {
			st.State = link.EdgeDone
			if failed > 0 {
				st.Error = fmt.Sprintf("%d of %d pieces failed: %v", failed, len(pieces), lastErr)
			}
		})
	}
}

// edgeFFmpegArgs pulls a replay into fragmented MP4 like MediaMTX's
// segments: video copied, audio (G.711 on most cameras, which MP4 can't
// carry) as AAC, ~1 s fragments.
func edgeFFmpegArgs(input, out string, dur time.Duration) []string {
	return []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-rtsp_transport", "tcp", "-timeout", "15000000", "-i", input,
		"-map", "0:v:0", "-map", "0:a:0?", "-c:v", "copy", "-c:a", "aac", "-b:a", "64k",
		"-t", fmt.Sprintf("%.3f", dur.Seconds()),
		"-f", "mp4", "-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-frag_duration", "1000000",
		"-y", out}
}

// recoverPiece fetches one piece and places it. It returns how much
// footage was recovered (0 when the camera had none there, or local
// footage turned up meanwhile).
func (r *Recorder) recoverPiece(ctx context.Context, work, ffmpeg string, cam link.CameraConfig, uri string, pc edgePiece, n int) (time.Duration, error) {
	proxy, err := onvif.StartReplayProxy(uri, cam.Username, cam.Password, pc.Start, pc.End)
	if err != nil {
		return 0, err
	}
	defer proxy.Close()
	dur := pc.End.Sub(pc.Start)
	tmp := filepath.Join(work, fmt.Sprintf("piece-%04d.mp4", n))
	pctx, cancel := context.WithTimeout(ctx, dur*3/2+2*time.Minute)
	out, runErr := exec.CommandContext(pctx, ffmpeg, edgeFFmpegArgs(proxy.URL(), tmp, dur)...).CombinedOutput()
	cancel()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	// ffmpeg often exits non-zero when a replay simply ends; the file decides.
	f, err := os.Open(tmp)
	if err != nil {
		if runErr != nil {
			return 0, fmt.Errorf("ffmpeg: %v: %s", runErr, lastLine(out))
		}
		return 0, nil
	}
	info, _ := f.Stat()
	ix, _ := playback.ReadIndex(f, info.Size())
	f.Close()
	if !ix.Playable() {
		if runErr != nil && info.Size() == 0 {
			return 0, fmt.Errorf("ffmpeg: %v: %s", runErr, lastLine(out))
		}
		return 0, nil // the camera had nothing there
	}
	got := ix.Duration()
	if got > dur {
		got = dur
	}
	return r.placeEdgeSegment(cam.ID, tmp, pc, got)
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

// edgeMarker is what a .edge file records.
type edgeMarker struct {
	Recording   string `json:"recording"` // the camera's recording token
	From        int64  `json:"from"`      // Unix ms requested
	To          int64  `json:"to"`
	RecoveredAt int64  `json:"recoveredAt"`
}

// placeEdgeSegment moves a fetched piece into the camera's recording
// directory, unless local footage now covers that time or a file of that
// name exists.
func (r *Recorder) placeEdgeSegment(cameraID, tmp string, pc edgePiece, got time.Duration) (time.Duration, error) {
	end := pc.Start.Add(got)
	r.edge.placeMu.Lock()
	defer r.edge.placeMu.Unlock()
	lib := r.library()
	files, err := lib.Files(cameraID, pc.Start, end)
	if err != nil {
		return 0, err
	}
	if g := edgeGaps(files, pc.Start, end); len(g) != 1 || g[0].Start.After(pc.Start) || g[0].End.Before(end) {
		return 0, nil // something was recorded there meanwhile
	}
	dir := filepath.Join(r.recordingsRoot(), cameraID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, err
	}
	final := filepath.Join(dir, edgeSegmentName(pc.Start, lib.Loc))
	if err := createExclusive(tmp, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return 0, nil
		}
		return 0, err
	}
	mark, _ := json.Marshal(edgeMarker{Recording: pc.Recording, From: pc.Start.UnixMilli(), To: pc.End.UnixMilli(), RecoveredAt: time.Now().UnixMilli()})
	if err := writeExclusive(final+edgeMarkerExt, mark); err != nil {
		_ = os.Remove(final) // ours: created a moment ago
		return 0, err
	}
	// The modification time stands for a segment's end (Recordings list,
	// Library.Files' window filter).
	_ = os.Chtimes(final, end, end)
	return got, nil
}

// createExclusive makes dst a copy of src without ever replacing dst: a
// hard link when both sit on one filesystem, else an O_EXCL copy.
func createExclusive(src, dst string) error {
	err := os.Link(src, dst)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

func writeExclusive(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// isEdgeSegment reports whether a segment came from the camera's card.
func (r *Recorder) isEdgeSegment(cameraID, name string) bool {
	_, err := os.Stat(filepath.Join(r.recordingsRoot(), cameraID, name+edgeMarkerExt))
	return err == nil
}

// sweepEdgeMarkers removes markers whose segment is gone (retention).
func (r *Recorder) sweepEdgeMarkers(cameraID string) {
	dir := filepath.Join(r.recordingsRoot(), cameraID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, edgeMarkerExt) || !segmentPattern.MatchString(strings.TrimSuffix(name, edgeMarkerExt)) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, strings.TrimSuffix(name, edgeMarkerExt))); errors.Is(err, fs.ErrNotExist) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// MARK: - Capability probe

type edgeProbe struct {
	key       [32]byte
	supported bool
	failed    bool
	running   bool
	at        time.Time
}

func edgeProbeKey(c link.CameraConfig) [32]byte {
	return sha256.Sum256([]byte(c.ONVIFURL + "\x00" + c.Username + "\x00" + c.Password))
}

// syncEdgeProbes probes cameras whose ONVIF address or login is new and
// forgets cameras that are gone.
func (r *Recorder) syncEdgeProbes(cams []link.CameraConfig) {
	st := &r.edge
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.probes == nil {
		st.probes = map[string]*edgeProbe{}
	}
	keep := map[string]bool{}
	for _, c := range cams {
		if c.ONVIFURL == "" {
			continue
		}
		keep[c.ID] = true
		key := edgeProbeKey(c)
		if p := st.probes[c.ID]; p != nil && p.key == key {
			continue
		}
		p := &edgeProbe{key: key}
		st.probes[c.ID] = p
		r.startEdgeProbeLocked(c, p)
	}
	for id := range st.probes {
		if !keep[id] {
			delete(st.probes, id)
		}
	}
}

// startEdgeProbeLocked runs one probe in the background. The caller holds edge.mu.
func (r *Recorder) startEdgeProbeLocked(c link.CameraConfig, p *edgeProbe) {
	p.running = true
	go func() {
		ctx, cancel := context.WithTimeout(r.ctx(), edgeProbeTimeout)
		ok, err := onvif.EdgeRecordingSupported(ctx, c.ONVIFURL, c.Username, c.Password)
		cancel()
		r.edge.mu.Lock()
		p.running, p.at = false, time.Now()
		p.supported, p.failed = ok, err != nil
		r.edge.mu.Unlock()
		if err == nil && ok {
			slog.Info("camera offers Profile G recording search and replay", "camera", c.Name)
		}
	}()
}

// edgeRecordingStatus is the camera's edgeRecording flag. A stale probe is
// refreshed in the background (the status report calls this every 15 s).
func (r *Recorder) edgeRecordingStatus(cameraID string) bool {
	st := &r.edge
	st.mu.Lock()
	defer st.mu.Unlock()
	p := st.probes[cameraID]
	if p == nil {
		return false
	}
	ttl := edgeProbeTTL
	if p.failed {
		ttl = edgeProbeRetry
	}
	if !p.running && time.Since(p.at) > ttl {
		if c, ok := r.camera(cameraID); ok && c.ONVIFURL != "" && edgeProbeKey(c) == p.key {
			r.startEdgeProbeLocked(c, p)
		}
	}
	return p.supported
}

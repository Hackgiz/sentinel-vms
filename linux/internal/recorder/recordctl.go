package recorder

// Events-only and dual recording (WAVE22-RECORDING-MODES.md §1–2).
//
// MediaMTX always records the main stream of an events/dual camera into a
// rolling buffer, <root>/.buffer/<id>/, in 60 s segments. Activity (motion,
// Sentinel detections, camera analytics) opens a keep window from
// first − preRoll to last + postRoll. The keeper moves every buffer segment
// that overlaps a window into <root>/<id>/ (a rename on the same
// filesystem; a segment still being written keeps its open file handle) and
// deletes buffer segments older than bufferKeep that no window wanted.
//
// Windows live in memory and in the recorder database (keep_windows), so a
// restart doesn't lose a window whose segments are still in the buffer.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
)

const (
	// bufferKeep is how long an unkept buffer segment stays (after it was
	// last written): far longer than the longest pre-roll.
	bufferKeep = 10 * time.Minute
	// keepEvery is the keeper's default interval.
	keepEvery = 10 * time.Second
	// windowSlack covers the whole-second segment names and clock jitter
	// when matching segments to windows.
	windowSlack = time.Second
	// recentWrite: a file modified this recently may still be written.
	recentWrite = 2 * time.Minute
)

const keepWindowsSchema = `
CREATE TABLE IF NOT EXISTS keep_windows (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	camera_id  TEXT NOT NULL,
	start_ms   INTEGER NOT NULL,
	end_ms     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS keep_windows_end ON keep_windows(end_ms);
`

type keepWindow struct {
	id         int64
	start, end time.Time
	dirty      bool // end/start changed since it was written
}

// recordState is the keeper's and sweeper's state. The intervals and the
// clock/disk seams are for tests; zero = the defaults.
type recordState struct {
	mu       sync.Mutex
	windows  map[string][]*keepWindow // camera id → windows, oldest first
	loadOnce sync.Once

	// configured: a snapshot has been applied. Until then the recorder
	// doesn't know which cameras exist or their retention, so the sweeper
	// waits (it would otherwise treat every folder as a deleted camera's).
	configured bool

	keepEvery  time.Duration
	sweepEvery time.Duration
	now        func() time.Time
	freeSpace  func(path string) (pct float64, free uint64, ok bool)
}

func (st *recordState) clock() time.Time {
	if st.now != nil {
		return st.now()
	}
	return time.Now()
}

// recordingDefaults are the server-wide recording settings of the applied
// snapshot (an older management server sends only DefaultRetentionDays).
func (r *Recorder) recordingDefaults() link.RecordingDefaults {
	s := r.config()
	d := s.RecordingDefaults
	if d.DefaultRetentionDays <= 0 {
		d.DefaultRetentionDays = s.DefaultRetentionDays
	}
	return d.Normalized()
}

// effectiveRecording is a camera's resolved recording settings.
func (r *Recorder) effectiveRecording(c link.CameraConfig) link.EffectiveRecording {
	return c.Effective(r.recordingDefaults())
}

func keepsOnEvents(mode string) bool { return mode == link.RecordEvents || mode == link.RecordDual }

// MARK: - Activity

// Activity tells recordctl something happened on a camera at a time: kind
// is the detection kind ("Motion", "Person", "Vehicle", "Line crossing",
// a camera analytics kind, an audio kind). For an events/dual camera whose
// trigger accepts it, a keep window opens or is extended.
func (r *Recorder) Activity(cameraID, kind string, at time.Time) {
	r.keepActivity(cameraID, kind, "", at)
}

// keepActivity is Activity with the object label when there is one
// ("person", "car"): a line crossing by a person counts for the
// people-and-vehicles trigger.
func (r *Recorder) keepActivity(cameraID, kind, label string, at time.Time) {
	cam, ok := r.camera(cameraID)
	if !ok {
		return
	}
	eff := r.effectiveRecording(cam)
	if !keepsOnEvents(eff.Mode) || !triggerAccepts(eff.Trigger, kind, label) {
		return
	}
	if at.IsZero() {
		at = r.rec.clock()
	}
	r.extendWindow(cameraID, at.Add(-time.Duration(eff.PreRollSeconds)*time.Second), at.Add(time.Duration(eff.PostRollSeconds)*time.Second))
}

// triggerAccepts reports whether activity of this kind starts a recording
// under the trigger: motion takes everything; objects only people and
// vehicles (Sentinel's or the camera's), never pixel motion or sound.
func triggerAccepts(trigger, kind, label string) bool {
	if trigger != link.TriggerObjects {
		return true
	}
	switch kind {
	case "Person", "Vehicle", "Loitering", "Face", "License plate":
		return true
	}
	l := strings.ToLower(label)
	if l == "" {
		return false
	}
	for _, w := range []string{"person", "people", "human", "pedestrian", "vehicle", "car", "truck", "bus", "van", "motor", "bike", "bicycle"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// extendWindow merges [start, end] into the camera's newest window when
// they touch, else opens a new one (written to the database right away).
func (r *Recorder) extendWindow(cameraID string, start, end time.Time) {
	r.loadWindows()
	st := &r.rec
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.windows == nil {
		st.windows = map[string][]*keepWindow{}
	}
	ws := st.windows[cameraID]
	if n := len(ws); n > 0 && !start.After(ws[n-1].end) {
		w := ws[n-1]
		if end.After(w.end) {
			w.end, w.dirty = end, true
		}
		if start.Before(w.start) {
			w.start, w.dirty = start, true
		}
		return
	}
	w := &keepWindow{start: start, end: end}
	if r.store != nil {
		res, err := r.store.DB.ExecContext(r.ctx(), `INSERT INTO keep_windows(camera_id, start_ms, end_ms) VALUES(?,?,?)`,
			cameraID, start.UnixMilli(), end.UnixMilli())
		if err != nil {
			slog.Warn("keep window not saved", "camera", cameraID, "err", err)
			w.dirty = true
		} else {
			w.id, _ = res.LastInsertId()
		}
	}
	st.windows[cameraID] = append(ws, w)
}

// loadWindows reads the windows a previous run left (once).
func (r *Recorder) loadWindows() {
	r.rec.loadOnce.Do(func() {
		if r.store == nil {
			return
		}
		since := r.rec.clock().Add(-bufferKeep - time.Hour).UnixMilli()
		rows, err := r.store.DB.QueryContext(r.ctx(), `SELECT id, camera_id, start_ms, end_ms FROM keep_windows WHERE end_ms >= ? ORDER BY start_ms`, since)
		if err != nil {
			slog.Warn("keep windows not loaded", "err", err)
			return
		}
		defer rows.Close()
		loaded := map[string][]*keepWindow{}
		for rows.Next() {
			var id, s, e int64
			var cam string
			if rows.Scan(&id, &cam, &s, &e) == nil {
				loaded[cam] = append(loaded[cam], &keepWindow{id: id, start: time.UnixMilli(s), end: time.UnixMilli(e)})
			}
		}
		r.rec.mu.Lock()
		if r.rec.windows == nil {
			r.rec.windows = map[string][]*keepWindow{}
		}
		for cam, ws := range loaded {
			r.rec.windows[cam] = append(ws, r.rec.windows[cam]...)
		}
		r.rec.mu.Unlock()
	})
}

// windowsOf copies a camera's windows (start, end pairs).
func (r *Recorder) windowsOf(cameraID string) [][2]time.Time {
	r.rec.mu.Lock()
	defer r.rec.mu.Unlock()
	ws := r.rec.windows[cameraID]
	out := make([][2]time.Time, len(ws))
	for i, w := range ws {
		out[i] = [2]time.Time{w.start, w.end}
	}
	return out
}

// flushWindows writes changed windows and forgets those no buffer segment
// can overlap any more.
func (r *Recorder) flushWindows(now time.Time) {
	st := &r.rec
	type upd struct {
		cam        string
		w          *keepWindow
		id         int64
		start, end int64
	}
	var writes []upd
	var gone []int64
	st.mu.Lock()
	forget := now.Add(-bufferKeep - 5*time.Minute)
	for cam, ws := range st.windows {
		kept := ws[:0]
		for _, w := range ws {
			if w.end.Before(forget) {
				if w.id != 0 {
					gone = append(gone, w.id)
				}
				continue
			}
			if w.dirty {
				writes = append(writes, upd{cam: cam, w: w, id: w.id, start: w.start.UnixMilli(), end: w.end.UnixMilli()})
				w.dirty = false
			}
			kept = append(kept, w)
		}
		if len(kept) == 0 {
			delete(st.windows, cam)
		} else {
			st.windows[cam] = kept
		}
	}
	st.mu.Unlock()
	if r.store == nil {
		return
	}
	ctx := r.ctx()
	for _, u := range writes {
		if u.id != 0 {
			_, _ = r.store.DB.ExecContext(ctx, `UPDATE keep_windows SET start_ms = ?, end_ms = ? WHERE id = ?`, u.start, u.end, u.id)
			continue
		}
		res, err := r.store.DB.ExecContext(ctx, `INSERT INTO keep_windows(camera_id, start_ms, end_ms) VALUES(?,?,?)`, u.cam, u.start, u.end)
		if err == nil {
			id, _ := res.LastInsertId()
			st.mu.Lock()
			u.w.id = id
			st.mu.Unlock()
		}
	}
	for _, id := range gone {
		_, _ = r.store.DB.ExecContext(ctx, `DELETE FROM keep_windows WHERE id = ?`, id)
	}
	_, _ = r.store.DB.ExecContext(ctx, `DELETE FROM keep_windows WHERE end_ms < ?`, forget.UnixMilli())
}

// MARK: - Keeper

func (r *Recorder) runKeeper(ctx context.Context) {
	every := r.rec.keepEvery
	if every <= 0 {
		every = keepEvery
	}
	r.loadWindows()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		r.keepOnce()
		select {
		case <-ctx.Done():
			r.flushWindows(r.rec.clock())
			return
		case <-tick.C:
		}
	}
}

// bufferSegment is one file in a camera's buffer.
type bufferSegment struct {
	name       string
	start, end time.Time // from the name; last write
}

// listDirSegments lists the recording segments in dir (name = start, mtime
// = end), oldest first.
func listDirSegments(dir string) []bufferSegment {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []bufferSegment
	for _, e := range entries {
		m := segmentPattern.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		start, err := time.ParseInLocation("20060102-150405", m[1], time.Local)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		end := info.ModTime()
		if end.Before(start) {
			end = start
		}
		out = append(out, bufferSegment{name: e.Name(), start: start, end: end})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func overlapsAny(seg bufferSegment, windows [][2]time.Time) bool {
	for _, w := range windows {
		if !seg.start.After(w[1].Add(windowSlack)) && !seg.end.Before(w[0].Add(-windowSlack)) {
			return true
		}
	}
	return false
}

// keepOnce moves buffer segments that overlap a keep window into the
// camera's folder and deletes the old ones nobody wanted.
func (r *Recorder) keepOnce() {
	now := r.rec.clock()
	r.flushWindows(now)
	root := r.recordingsRoot()
	bufRoot := filepath.Join(root, media.BufferDir)
	cams, err := os.ReadDir(bufRoot)
	if err != nil {
		return
	}
	for _, c := range cams {
		id := c.Name()
		if !c.IsDir() || !cameraIDPattern.MatchString(id) {
			continue
		}
		dir := filepath.Join(bufRoot, id)
		windows := r.windowsOf(id)
		for _, seg := range listDirSegments(dir) {
			src := filepath.Join(dir, seg.name)
			if overlapsAny(seg, windows) {
				if err := keepSegment(src, filepath.Join(root, id, seg.name)); err != nil {
					slog.Warn("buffer segment not kept", "camera", id, "file", seg.name, "err", err)
				}
				continue
			}
			if now.Sub(seg.end) > bufferKeep {
				if err := os.Remove(src); err != nil && !errors.Is(err, os.ErrNotExist) {
					slog.Warn("old buffer segment not deleted", "camera", id, "file", seg.name, "err", err)
				}
			}
		}
		if _, ok := r.camera(id); !ok {
			_ = os.Remove(dir) // only when empty: a camera that is gone
		}
	}
}

// keepSegment moves a buffer segment into the camera's kept footage.
func keepSegment(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return errors.New("a recording with that name is already kept")
	}
	return os.Rename(src, dst)
}

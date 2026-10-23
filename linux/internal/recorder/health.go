package recorder

// link.CmdHealth: the System Monitor's reading of this recording server.
// Nothing here probes cameras: online/recording come from the media
// layer's state, the last segment from one directory listing per camera,
// and ingest kbps from the media layer's byte counter between two readings.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"sentinel-linux/internal/hwinfo"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
)

type healthState struct {
	mu    sync.Mutex
	probe hwinfo.HealthProbe // zero = the real host (tests set seams)
	last  map[string]byteSample
}

type byteSample struct {
	bytes int64
	at    time.Time
	kbps  *float64
}

func (r *Recorder) healthResult() link.HealthResult {
	now := time.Now()
	root := r.recordingsRoot()
	dir := root
	if _, err := os.Stat(dir); err != nil {
		dir = r.DataDir
	}
	host := hwinfo.CollectHealth(r.health.probe, dir)
	raw, _ := json.Marshal(host)
	res := link.HealthResult{
		ServerID: r.ServerID, Version: r.Version, UptimeSec: int64(hwinfo.Uptime().Seconds()),
		DBBytes: r.dbBytes(), Host: raw, Cameras: []link.HealthCamera{},
	}

	r.cfgMu.RLock()
	ids := make([]string, 0, len(r.cams))
	cams := make([]link.CameraConfig, 0, len(r.cams))
	for id, c := range r.cams {
		ids = append(ids, id)
		cams = append(cams, c)
	}
	r.cfgMu.RUnlock()
	modes := map[string]string{} // effective recording modes (recordctl.go)
	for _, c := range cams {
		modes[c.ID] = r.effectiveRecording(c).Mode
	}
	sort.Strings(ids)

	r.health.mu.Lock()
	defer r.health.mu.Unlock()
	if r.health.last == nil {
		r.health.last = map[string]byteSample{}
	}
	for _, id := range ids {
		mode := modes[id]
		hc := link.HealthCamera{ID: id, RecordingMode: mode}
		if r.Media != nil {
			ms := r.Media.State(id)
			hc.Online = ms.Ready
			hc.Recording = ms.Ready && mode != link.RecordOff
			hc.KBPS = r.sampleKBPS(id, ms.BytesReceived, now)
		}
		// The stream that always records (WAVE22 section 5): the main
		// stream, the rolling buffer of an events camera, the low-res
		// stream of a dual one.
		if age, ok := segmentAgeIn(r.liveRecordingDir(id, mode), now); ok {
			hc.LastSegmentAgeSec = &age
		}
		if keepsOnEvents(mode) {
			if at, ok := newestSegmentTime(filepath.Join(root, id)); ok {
				hc.LastKeptClipAt = at.UTC().Format(time.RFC3339)
			}
		}
		res.Cameras = append(res.Cameras, hc)
	}
	for id := range r.health.last { // forget cameras that are gone
		if _, ok := modes[id]; !ok {
			delete(r.health.last, id)
		}
	}
	return res
}

// sampleKBPS turns the cumulative byte counter into a rate. Readings less
// than a second apart, or a counter that went backwards (stream restart),
// keep the previous rate. Caller holds r.health.mu.
func (r *Recorder) sampleKBPS(id string, bytes int64, now time.Time) *float64 {
	prev, had := r.health.last[id]
	cur := byteSample{bytes: bytes, at: now}
	if had {
		dt := now.Sub(prev.at).Seconds()
		switch {
		case bytes < prev.bytes:
			// restart: no rate until the next reading
		case dt >= 1:
			k := float64(bytes-prev.bytes) * 8 / 1000 / dt
			cur.kbps = &k
		default:
			cur.kbps, cur.bytes, cur.at = prev.kbps, prev.bytes, prev.at
		}
	}
	r.health.last[id] = cur
	return cur.kbps
}

// lastSegmentAge is how old the newest recording file of a camera is (by
// its modification time, so a segment still being written reads ~0).
func (r *Recorder) lastSegmentAge(id string, now time.Time) (int64, bool) {
	return segmentAgeIn(filepath.Join(r.recordingsRoot(), id), now)
}

// liveRecordingDir is the folder a camera in this mode always records into.
func (r *Recorder) liveRecordingDir(id, mode string) string {
	root := r.recordingsRoot()
	switch mode {
	case link.RecordEvents:
		return filepath.Join(root, media.BufferDir, id)
	case link.RecordDual:
		return filepath.Join(root, media.LowResDir, id+"-sub")
	}
	return filepath.Join(root, id)
}

// segmentAgeIn is how old the newest segment in dir is.
func segmentAgeIn(dir string, now time.Time) (int64, bool) {
	at, ok := newestSegmentTime(dir)
	if !ok {
		return 0, false
	}
	age := int64(now.Sub(at).Seconds())
	if age < 0 {
		age = 0
	}
	return age, true
}

// newestSegmentTime is the modification time (the end) of the newest
// segment in dir.
func newestSegmentTime(dir string) (time.Time, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, false
	}
	newest := ""
	for _, e := range entries { // names are YYYYMMDD-HHMMSS-n.mp4: lexical = chronological
		if e.IsDir() || !segmentPattern.MatchString(e.Name()) {
			continue
		}
		if e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		return time.Time{}, false
	}
	fi, err := os.Stat(filepath.Join(dir, newest))
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// dbBytes is the size of this server's database files (main + WAL).
func (r *Recorder) dbBytes() int64 {
	path := filepath.Join(r.DataDir, "sentinel.db") // shared database in --role all
	if r.store != nil && r.store.path != "" {
		path = r.store.path
	}
	var n int64
	for _, p := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

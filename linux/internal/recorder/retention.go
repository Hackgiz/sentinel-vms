package recorder

// Retention (WAVE22-RECORDING-MODES.md §3): the recorder, not MediaMTX,
// deletes old footage. Every sweepEvery it
//
//   - deletes segments older than each tier's retention: full-res
//     <root>/<id>/ (retentionDays) and low-res <root>/.lowres/<id>-sub/
//     (lowResRetentionDays); folders of cameras this server no longer has
//     follow the server defaults and are removed once empty;
//   - when minFreePercent is set and free space on the recordings disk is
//     under it, deletes the oldest footage until it isn't: low-res first,
//     then unkept buffer segments, then full-res.
//
// It only ever deletes recording segments (YYYYMMDD-HHMMSS-n.mp4) in those
// folders under the recordings root, with their playback index sidecars
// (<name>.mp4.idx, which also go when orphaned): never anything outside it (the
// evidence vault lives elsewhere), never a buffer segment inside a keep
// window, and never a file still being written.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"sentinel-linux/internal/media"
	"sentinel-linux/internal/playback"
)

const sweepEvery = 10 * time.Minute

func (r *Recorder) runSweeper(ctx context.Context) {
	every := r.rec.sweepEvery
	if every <= 0 {
		every = sweepEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		r.sweepOnce()
	}
}

// sweepResult counts what one sweep deleted (tests, logs).
type sweepResult struct {
	Expired  int // past retention
	ForSpace int // minFreePercent
}

// sweepOnce runs one retention pass. Nothing happens before the first
// configuration arrives.
func (r *Recorder) sweepOnce() sweepResult {
	var res sweepResult
	r.rec.mu.Lock()
	configured := r.rec.configured
	r.rec.mu.Unlock()
	if !configured {
		return res
	}
	now := r.rec.clock()
	root := r.recordingsRoot()
	d := r.recordingDefaults()

	// Full-res tier: <root>/<id>/.
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			id := e.Name()
			if !e.IsDir() || !cameraIDPattern.MatchString(id) {
				continue
			}
			days := d.DefaultRetentionDays
			cam, known := r.camera(id)
			if known {
				days = r.effectiveRecording(cam).RetentionDays
			}
			dir := filepath.Join(root, id)
			res.Expired += deleteOlder(dir, now.Add(-time.Duration(days)*24*time.Hour))
			if !known {
				_ = os.Remove(dir) // only when empty
			}
		}
	}
	// Low-res tier: <root>/.lowres/<id>-sub/.
	lowRoot := filepath.Join(root, media.LowResDir)
	if entries, err := os.ReadDir(lowRoot); err == nil {
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), "-sub")
			if !e.IsDir() || !ok || !cameraIDPattern.MatchString(id) {
				continue
			}
			days := d.DefaultLowResRetentionDays
			cam, known := r.camera(id)
			if known {
				days = r.effectiveRecording(cam).LowResRetentionDays
			}
			dir := filepath.Join(lowRoot, e.Name())
			res.Expired += deleteOlder(dir, now.Add(-time.Duration(days)*24*time.Hour))
			if !known {
				_ = os.Remove(dir)
			}
		}
	}
	if d.MinFreePercent > 0 {
		res.ForSpace = r.freeSpace(root, float64(d.MinFreePercent), now)
	}
	if res.Expired > 0 || res.ForSpace > 0 {
		slog.Info("recordings cleaned up", "expired", res.Expired, "forFreeSpace", res.ForSpace)
	}
	return res
}

// deleteOlder removes the segments in dir that started before cutoff and
// returns how many.
func deleteOlder(dir string, cutoff time.Time) int {
	n := 0
	for _, seg := range listDirSegments(dir) { // oldest first
		if !seg.start.Before(cutoff) {
			break
		}
		p := filepath.Join(dir, seg.name)
		if err := os.Remove(p); err == nil {
			n++
			playback.RemoveSidecar(p)
		} else if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("old recording not deleted", "file", p, "err", err)
		}
	}
	removeOrphanSidecars(dir)
	return n
}

// removeOrphanSidecars deletes playback index sidecars (<name>.mp4.idx)
// whose segment is gone, and temporary ones a crash left behind.
func removeOrphanSidecars(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !playback.IsSidecarName(name) {
			continue
		}
		p := filepath.Join(dir, name)
		if seg, ok := strings.CutSuffix(name, playback.SidecarExt); ok && !strings.HasPrefix(name, ".") {
			if _, err := os.Lstat(filepath.Join(dir, seg)); errors.Is(err, os.ErrNotExist) {
				_ = os.Remove(p)
			}
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(p) // a temporary file that was never renamed
		}
	}
}

// spaceCandidate is a segment the free-space pass may delete.
type spaceCandidate struct {
	path  string
	start time.Time
	tier  int // 0 low-res, 1 buffer, 2 full-res: deleted in this order
}

// freeSpace deletes the oldest footage while free space on root's disk is
// under minPct, and returns how many segments it deleted.
func (r *Recorder) freeSpace(root string, minPct float64, now time.Time) int {
	stat := r.rec.freeSpace
	if stat == nil {
		stat = diskFreePct
	}
	pct, _, ok := stat(root)
	if !ok || pct >= minPct {
		return 0
	}
	var cands []spaceCandidate
	add := func(dir string, tier int, skip func(bufferSegment) bool) {
		for _, seg := range listDirSegments(dir) {
			if now.Sub(seg.end) < recentWrite || (skip != nil && skip(seg)) {
				continue // still being written, or wanted
			}
			p := filepath.Join(dir, seg.name)
			if !ownedFile(p) {
				continue // a link to imported footage: deleting it frees nothing
			}
			cands = append(cands, spaceCandidate{path: p, start: seg.start, tier: tier})
		}
	}
	tierDirs := func(base string, tier int, suffix string, skip func(id string) func(bufferSegment) bool) {
		entries, err := os.ReadDir(base)
		if err != nil {
			return
		}
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), suffix)
			if !e.IsDir() || !ok || !cameraIDPattern.MatchString(id) {
				continue
			}
			var f func(bufferSegment) bool
			if skip != nil {
				f = skip(id)
			}
			add(filepath.Join(base, e.Name()), tier, f)
		}
	}
	tierDirs(filepath.Join(root, media.LowResDir), 0, "-sub", nil)
	tierDirs(filepath.Join(root, media.BufferDir), 1, "", func(id string) func(bufferSegment) bool {
		windows := r.windowsOf(id)
		return func(seg bufferSegment) bool { return overlapsAny(seg, windows) }
	})
	tierDirs(root, 2, "", nil)
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].tier != cands[j].tier {
			return cands[i].tier < cands[j].tier
		}
		return cands[i].start.Before(cands[j].start)
	})
	n := 0
	for _, c := range cands {
		if err := os.Remove(c.path); err != nil {
			continue
		}
		playback.RemoveSidecar(c.path)
		n++
		if pct, _, ok = stat(root); !ok || pct >= minPct {
			break
		}
	}
	if n > 0 {
		slog.Warn("recordings disk under the minimum free space: deleted the oldest footage", "minFreePercent", minPct,
			"deleted", n, "freePct", fmt.Sprintf("%.1f", pct))
	}
	return n
}

// ownedFile reports whether deleting p frees its space: a regular file
// with no other hard link (imported Mac recordings are links).
func ownedFile(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		return false
	}
	return true
}

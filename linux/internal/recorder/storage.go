package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"sentinel-linux/internal/link"
)

// Storage health. When free space on the recordings disk drops under 5% the
// recorder reports storage.low once (the management side raises the
// "Storage" alarm, so rules can route it like any other); when it recovers
// above 8% it reports storage.recovered.
const (
	storageLowPct     = 5.0
	storageRecoverPct = 8.0
	storageCheckEvery = 5 * time.Minute
)

type storageState struct {
	mu  sync.Mutex
	low bool
}

func diskFreePct(path string) (pct float64, free uint64, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return 0, 0, false
	}
	free = uint64(st.Bavail) * uint64(st.Bsize)
	total := uint64(st.Blocks) * uint64(st.Bsize)
	return 100 * float64(free) / float64(total), free, true
}

func (r *Recorder) runStorageMonitor(ctx context.Context) {
	tick := time.NewTicker(storageCheckEvery)
	defer tick.Stop()
	var st storageState
	for {
		path := r.recordingsRoot()
		if _, err := os.Stat(path); err != nil {
			path = r.DataDir
		}
		r.checkStorage(ctx, &st, path, diskFreePct)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// checkStorage reports a threshold crossing, if any.
func (r *Recorder) checkStorage(ctx context.Context, st *storageState, path string, stat func(string) (float64, uint64, bool)) {
	pct, free, ok := stat(path)
	if !ok {
		return
	}
	st.mu.Lock()
	var typ string
	switch {
	case !st.low && pct < storageLowPct:
		st.low, typ = true, link.EventStorageLow
		slog.Warn("recordings disk almost full", "freePct", fmt.Sprintf("%.1f", pct))
	case st.low && pct >= storageRecoverPct:
		st.low, typ = false, link.EventStorageRecovered
	}
	st.mu.Unlock()
	if typ == "" {
		return
	}
	msg := link.Event{ID: link.NewID(), Type: typ, ServerID: r.ServerID, Time: time.Now().UnixMilli(),
		Storage: &link.Storage{FreePct: pct, FreeBytes: free}}
	if err := r.store.enqueue(ctx, msg); err != nil {
		slog.Error("outbox", "err", err)
		return
	}
	r.deliver(msg)
}

// storageInfo is the System page's disk card.
func (r *Recorder) storageInfo() link.StorageInfo {
	info := link.StorageInfo{RecordingsDir: r.recordingsRoot()}
	var st unix.Statfs_t
	err := unix.Statfs(r.recordingsRoot(), &st)
	if err != nil {
		err = unix.Statfs(r.DataDir, &st) // folder gone: the data disk
	}
	if err == nil {
		info.HasDisk = true
		info.DiskFreeBytes = uint64(st.Bavail) * uint64(st.Bsize)
		info.DiskTotalBytes = uint64(st.Blocks) * uint64(st.Bsize)
	}
	_ = filepath.WalkDir(r.recordingsRoot(), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				info.RecordingsBytes += fi.Size()
			}
		}
		return nil
	})
	return info
}

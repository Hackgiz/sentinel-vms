package recorder

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"sentinel-linux/internal/link"
)

// Where recordings go. The default is <data>/recordings; the management
// server can point a recording server at another folder (another disk,
// usually) through its configuration snapshot. Switching takes effect for
// new segments at once and moves the existing ones over in the background.

// MoveSettleDelay gives MediaMTX time to close the segment it was writing
// to the old folder before the move starts; files still changing are left
// for the second pass.
var (
	MoveSettleDelay = 5 * time.Second
	MoveRetryDelay  = 60 * time.Second
	MoveRecentFile  = 30 * time.Second
)

func (r *Recorder) defaultRecordingsRoot() string { return filepath.Join(r.DataDir, "recordings") }

func (r *Recorder) recordingsRoot() string {
	r.rootMu.RLock()
	dir := r.rootDir
	r.rootMu.RUnlock()
	if dir != "" {
		return dir
	}
	return r.defaultRecordingsRoot()
}

// applyRecordingsDir switches to the folder the snapshot asks for ("" =
// default). A folder that can't be created or written is reported in
// applied.Errors["storage"] and the current one stays in use.
func (r *Recorder) applyRecordingsDir(want string, applied *link.Applied) {
	if want == "" {
		want = r.defaultRecordingsRoot()
	}
	want = filepath.Clean(want)
	old := r.recordingsRoot()
	if want == old {
		return
	}
	if err := probeWritable(want); err != nil {
		slog.Error("recordings folder unusable; keeping the current one", "folder", want, "err", err)
		if applied.Errors == nil {
			applied.Errors = map[string]string{}
		}
		applied.Errors["storage"] = "recordings folder " + want + ": " + err.Error()
		return
	}
	r.rootMu.Lock()
	if want == r.defaultRecordingsRoot() {
		r.rootDir = ""
	} else {
		r.rootDir = want
	}
	r.rootMu.Unlock()
	if r.Media != nil {
		r.Media.SetRecordingsRoot(want)
	}
	r.library().SetRoot(want)
	slog.Info("recordings folder changed", "from", old, "to", want)
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		r.moveRecordings(old, want)
	}()
}

// probeWritable creates dir if needed and checks a file can be written in it.
func probeWritable(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("not an absolute path")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".sentinel-probe-*")
	if err != nil {
		return fmt.Errorf("not writable: %w", err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// moveRecordings carries every file under from to the same place under to,
// in two passes so a segment still being written is not copied half-done.
// Same-disk moves are renames; across disks files are copied then removed.
func (r *Recorder) moveRecordings(from, to string) {
	if from == to {
		return
	}
	if _, err := os.Stat(from); err != nil {
		return // nothing recorded there yet
	}
	sleep := func(d time.Duration) bool {
		select {
		case <-r.ctx().Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	if !sleep(MoveSettleDelay) {
		return
	}
	n, skipped, err := moveTree(from, to, MoveRecentFile)
	if skipped > 0 && err == nil {
		if !sleep(MoveRetryDelay) {
			return
		}
		var n2 int
		n2, skipped, err = moveTree(from, to, 0)
		n += n2
	}
	removeEmptyDirs(from)
	if err != nil {
		slog.Error("moving recordings stopped", "from", from, "to", to, "moved", n, "err", err)
		return
	}
	slog.Info("recordings moved", "from", from, "to", to, "files", n, "left", skipped)
}

// moveTree moves the files under from to under to, skipping files changed
// within recent (0 = none skipped). Files already present at the
// destination are left alone.
func moveTree(from, to string, recent time.Duration) (moved, skipped int, err error) {
	now := time.Now()
	err = filepath.WalkDir(from, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		rel, rerr := filepath.Rel(from, path)
		if rerr != nil {
			return rerr
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil // vanished (retention)
		}
		if recent > 0 && now.Sub(info.ModTime()) < recent {
			skipped++
			return nil
		}
		dst := filepath.Join(to, rel)
		if _, serr := os.Lstat(dst); serr == nil {
			skipped++
			return nil
		}
		if merr := os.MkdirAll(filepath.Dir(dst), 0o750); merr != nil {
			return merr
		}
		if merr := moveFile(path, dst, info); merr != nil {
			return merr
		}
		moved++
		return nil
	})
	return moved, skipped, err
}

// moveFile renames, or copies then removes when the folders are on
// different disks. Symbolic links (recordings imported from a Mac) are
// recreated, not followed.
func moveFile(src, dst string, info fs.FileInfo) error {
	if os.Rename(src, dst) == nil {
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, dst); err != nil {
			return err
		}
		return os.Remove(src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".moving"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chtimes(tmp, info.ModTime(), info.ModTime())
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}

// removeEmptyDirs prunes empty folders left under root, root included.
func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // fails, harmlessly, when not empty
	}
}

package models

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/modelpath"
)

// Install states (Model.state; "downloading" is the server's, from its job).
const (
	StateMissing     = "missing"
	StateInstalled   = "installed"
	StateCorrupt     = "corrupt"
	StateDownloading = "downloading"
)

// Path is where a model's first file is, or would be installed: the
// bundled detector next to the binary (detect.FindObjectModel) when it is
// not in the models directory; everything else under Dir.
func Path(dataDir, id string) string {
	e, ok := Lookup(id)
	if !ok {
		return ""
	}
	p := filepath.Join(Dir(dataDir), e.File)
	if e.Bundled && !regular(p) {
		if _, m := detect.FindObjectModel(dataDir); m != "" {
			return m
		}
	}
	if !regular(p) {
		if q := modelpath.Find(dataDir, e.File); regular(q) {
			return q // shipped next to the binary
		}
	}
	return p
}

// State reports a model's install state under dataDir: missing (a file is
// absent), corrupt (a file's SHA-256 does not match, or custom.json does
// not parse) or installed. The bundled detector counts when it is found
// (the build verified it). Hashes are cached by size and mtime.
func State(dataDir, id string) string {
	e, ok := Lookup(id)
	if !ok {
		return StateMissing
	}
	return entryState(dataDir, e)
}

func entryState(dataDir string, e Entry) string {
	if e.Bundled {
		if regular(Path(dataDir, e.ID)) {
			return StateInstalled
		}
		return StateMissing
	}
	if e.ID == IDCustom {
		if !regular(filepath.Join(Dir(dataDir), CustomModelFile)) || !regular(filepath.Join(Dir(dataDir), CustomSpecFile)) {
			return StateMissing
		}
		if _, err := ReadCustom(dataDir); err != nil {
			return StateCorrupt
		}
		return StateInstalled
	}
	state := StateInstalled
	for _, f := range e.Files {
		p := modelpath.Find(dataDir, f.Name) // the data dir, else next to the binary (shipped)
		if !regular(p) {
			return StateMissing
		}
		if f.SHA256 == "" {
			continue // not pinned: presence is all we can check
		}
		if sum, err := fileSHA256(p); err != nil || sum != f.SHA256 {
			state = StateCorrupt
		}
	}
	return state
}

// Installed is State(...) == StateInstalled.
func Installed(dataDir, id string) bool { return State(dataDir, id) == StateInstalled }

// Remove deletes a model's files from the models directory (bundled
// models are never removed). Absent files are not an error.
func Remove(dataDir, id string) error {
	e, ok := Lookup(id)
	if !ok {
		return fmt.Errorf("unknown model %q", id)
	}
	if e.Bundled {
		return errors.New("bundled models cannot be removed")
	}
	var first error
	for _, f := range e.Files {
		p := filepath.Join(Dir(dataDir), f.Name)
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
		_ = os.Remove(p + partSuffix)
		forgetHash(p)
	}
	return first
}

func regular(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// MARK: - hash cache

type hashKey struct {
	size  int64
	mtime time.Time
}

var hashes struct {
	mu sync.Mutex
	m  map[string]struct {
		key hashKey
		sum string
	}
}

// fileSHA256 is the file's hex SHA-256, cached by path, size and mtime so
// listing the catalog does not re-read hundreds of megabytes.
func fileSHA256(p string) (string, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	key := hashKey{fi.Size(), fi.ModTime()}
	hashes.mu.Lock()
	if c, ok := hashes.m[p]; ok && c.key == key {
		hashes.mu.Unlock()
		return c.sum, nil
	}
	hashes.mu.Unlock()
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	rememberHash(p, key, sum)
	return sum, nil
}

func rememberHash(p string, key hashKey, sum string) {
	hashes.mu.Lock()
	defer hashes.mu.Unlock()
	if hashes.m == nil {
		hashes.m = map[string]struct {
			key hashKey
			sum string
		}{}
	}
	hashes.m[p] = struct {
		key hashKey
		sum string
	}{key, sum}
}

func forgetHash(p string) {
	hashes.mu.Lock()
	delete(hashes.m, p)
	hashes.mu.Unlock()
}

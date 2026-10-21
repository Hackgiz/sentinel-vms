// Package modelpath finds optional model files. A model may live in the
// server's data directory (<data>/models, where the Models page downloads
// to and where hand installs go) or next to the sentinel binary
// (<bin>/models, where the installer puts the models that ship with a
// release). The data directory wins so a newer download overrides the
// shipped copy.
package modelpath

import (
	"os"
	"path/filepath"
)

// Dirs lists the directories searched, in order.
func Dirs(dataDir string) []string {
	dirs := []string{filepath.Join(dataDir, "models")}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "models"))
	}
	return dirs
}

// Find returns the first existing regular file named name in Dirs, or the
// data-directory path (which does not exist) so messages can name it.
func Find(dataDir, name string) string {
	for _, d := range Dirs(dataDir) {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return filepath.Join(dataDir, "models", name)
}

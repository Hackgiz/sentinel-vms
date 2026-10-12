//go:build !darwin

package main

import (
	"os"
	"path/filepath"
)

// defaultDataDir: SENTINEL_DATA, else /var/lib/sentinel for root (the
// systemd service), else ~/.local/share/sentinel. macOS: datadir_darwin.go.
func defaultDataDir() string {
	if d := os.Getenv("SENTINEL_DATA"); d != "" {
		return d
	}
	if os.Geteuid() == 0 {
		return "/var/lib/sentinel"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "sentinel")
}

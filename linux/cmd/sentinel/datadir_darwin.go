package main

import (
	"os"
	"os/user"

	"sentinel-linux/internal/launchd"
)

// defaultDataDir on macOS: SENTINEL_DATA, else the LaunchDaemon's
// /Library/Application Support/Sentinel for root or the _sentinel service
// user, else ~/Library/Application Support/Sentinel (a per-user run).
func defaultDataDir() string {
	if d := os.Getenv("SENTINEL_DATA"); d != "" {
		return d
	}
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	home, _ := os.UserHomeDir()
	return launchd.DefaultDataDir(os.Geteuid(), name, home)
}

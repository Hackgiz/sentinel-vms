//go:build !linux

package media

import "os/exec"

// configureChild: no parent-death signal outside Linux; exec.CommandContext
// still stops MediaMTX on a clean shutdown.
func configureChild(cmd *exec.Cmd) {}

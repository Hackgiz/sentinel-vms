//go:build !linux && !darwin

package childproc

import "os/exec"

// Configure: nothing extra; exec.CommandContext still stops the child on a
// clean shutdown.
func Configure(cmd *exec.Cmd) {}

// identity is unknown here, so stale children are never killed.
func identity(pid int) (string, bool) { return "", false }

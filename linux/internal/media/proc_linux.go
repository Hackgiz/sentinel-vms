//go:build linux

package media

import (
	"os/exec"
	"syscall"
)

// configureChild makes the kernel kill MediaMTX if Sentinel dies, so a crash
// never leaves an orphan holding the RTSP/HLS ports.
func configureChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

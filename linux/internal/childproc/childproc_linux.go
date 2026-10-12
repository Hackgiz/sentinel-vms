package childproc

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Configure makes the kernel kill the child if Sentinel dies, so a crash
// never leaves an orphan holding ports.
func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

// identity is "<comm>@<start time in clock ticks since boot>" from
// /proc/<pid>/stat, or false when no such process exists (zombies count as gone).
func identity(pid int) (string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	s := string(b)
	open, closing := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || closing < open {
		return "", false
	}
	comm := s[open+1 : closing]
	f := strings.Fields(s[closing+1:]) // f[0] = state (field 3), f[19] = starttime (field 22)
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return "", false
	}
	return comm + "@" + f[19], true
}

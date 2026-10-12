package childproc

import (
	"fmt"
	"os/exec"

	"golang.org/x/sys/unix"
)

// Configure prepares a child before Start. macOS has no Pdeathsig; the child
// deliberately stays in Sentinel's process group (no Setpgid) so launchd's
// process-group cleanup and terminal signals reach it. See the package doc.
func Configure(cmd *exec.Cmd) {}

// identity is "<command>@<start time>" from the kernel's process table, or
// false when no such process exists (zombies count as gone).
func identity(pid int) (string, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || kp.Proc.P_pid != int32(pid) || kp.Proc.P_stat == 5 /* SZOMB */ {
		return "", false
	}
	comm := unix.ByteSliceToString(kp.Proc.P_comm[:])
	st := kp.Proc.P_starttime
	return fmt.Sprintf("%s@%d.%06d", comm, st.Sec, st.Usec), true
}

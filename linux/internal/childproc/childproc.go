// Package childproc keeps Sentinel's long-running helper processes
// (MediaMTX, cloudflared) from outliving it.
//
//   - Linux: the kernel kills the child when Sentinel dies (Pdeathsig).
//   - macOS has no parent-death signal. Children stay in Sentinel's process
//     group, so launchd (which kills a job's whole process group when the job
//     exits, unless AbandonProcessGroup is set) and Ctrl-C in a terminal stop
//     them too. For the cases nothing else covers — Sentinel SIGKILLed or
//     crashing outside launchd — each child's PID and identity (name + start
//     time) are written to a pid file, and the next Sentinel stops a child
//     that is still running before starting its own. The identity check
//     means a recycled PID never gets an unrelated process killed.
//
// A stale MediaMTX would otherwise keep the RTSP/HLS/API ports and make every
// restart fail.
package childproc

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Track records a started child in pidFile.
func Track(pidFile string, pid int) {
	if pidFile == "" {
		return
	}
	id, ok := identity(pid)
	if !ok {
		return
	}
	tmp := pidFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d %s\n", pid, id)), 0o600); err == nil {
		_ = os.Rename(tmp, pidFile)
	}
}

// Untrack removes pidFile if it still names pid (the child has exited).
func Untrack(pidFile string, pid int) {
	if pidFile == "" {
		return
	}
	if p, _, ok := read(pidFile); ok && p == pid {
		_ = os.Remove(pidFile)
	}
}

// KillStale stops the child recorded in pidFile if that exact process is still
// running (SIGTERM, then SIGKILL after grace), removes the file, and returns
// the PID it stopped (0 = none).
func KillStale(pidFile string, grace time.Duration) int {
	if pidFile == "" {
		return 0
	}
	pid, want, ok := read(pidFile)
	defer os.Remove(pidFile)
	if !ok || pid <= 1 || pid == os.Getpid() {
		return 0
	}
	if id, alive := identity(pid); !alive || id != want {
		return 0 // exited, or the PID now belongs to something else
	}
	slog.Warn("stopping a helper process left over from a previous run", "pid", pid, "process", want)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if id, alive := identity(pid); !alive || id != want {
			return pid
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	for i := 0; i < 40; i++ { // up to 2 s for the kernel to reap it and free its ports
		if id, alive := identity(pid); !alive || id != want {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return pid
}

func read(pidFile string) (int, string, bool) {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, "", false
	}
	f := strings.SplitN(strings.TrimSpace(string(b)), " ", 2)
	if len(f) != 2 {
		return 0, "", false
	}
	pid, err := strconv.Atoi(f[0])
	if err != nil {
		return 0, "", false
	}
	return pid, f[1], true
}

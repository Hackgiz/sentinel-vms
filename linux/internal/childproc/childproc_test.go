package childproc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func needIdentity(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process identity is only implemented on Linux and macOS")
	}
}

// startSleep starts `sleep` outside exec.CommandContext so only
// KillStale can stop it, as after a Sentinel crash.
func startSleep(t *testing.T) *exec.Cmd {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd
}

func TestKillStaleStopsTrackedChild(t *testing.T) {
	needIdentity(t)
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	cmd := startSleep(t)
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	Track(pidFile, cmd.Process.Pid)
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("pid file: %v", err)
	}
	if got := KillStale(pidFile, 2*time.Second); got != cmd.Process.Pid {
		t.Fatalf("KillStale = %d, want %d", got, cmd.Process.Pid)
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("child still running")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatal("pid file not removed")
	}
}

func TestKillStaleSparesOtherProcesses(t *testing.T) {
	needIdentity(t)
	dir := t.TempDir()
	cmd := startSleep(t)
	pid := cmd.Process.Pid

	// Same PID, different identity: the PID was recycled by another program.
	pidFile := filepath.Join(dir, "recycled.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)+" mediamtx@1.000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := KillStale(pidFile, time.Second); got != 0 {
		t.Fatalf("killed recycled pid %d", got)
	}
	if _, alive := identity(pid); !alive {
		t.Fatal("unrelated process was killed")
	}

	// Garbage, missing and own-PID files are ignored.
	for name, content := range map[string]string{"garbage.pid": "nonsense", "self.pid": strconv.Itoa(os.Getpid()) + " x@1"} {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(content), 0o600)
		if got := KillStale(p, time.Second); got != 0 {
			t.Fatalf("%s: killed %d", name, got)
		}
	}
	if got := KillStale(filepath.Join(dir, "missing.pid"), time.Second); got != 0 {
		t.Fatal("missing file")
	}
	if KillStale("", time.Second) != 0 {
		t.Fatal("empty path")
	}
}

func TestUntrack(t *testing.T) {
	needIdentity(t)
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	cmd := startSleep(t)
	Track(pidFile, cmd.Process.Pid)
	Untrack(pidFile, cmd.Process.Pid+1) // someone else's: kept
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatal("pid file removed for another pid")
	}
	Untrack(pidFile, cmd.Process.Pid)
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatal("pid file not removed")
	}
}

func TestIdentityOfSelf(t *testing.T) {
	needIdentity(t)
	a, ok := identity(os.Getpid())
	b, ok2 := identity(os.Getpid())
	if !ok || !ok2 || a != b || a == "" {
		t.Fatalf("identity %q %v / %q %v", a, ok, b, ok2)
	}
	if _, ok := identity(1 << 30); ok {
		t.Fatal("nonexistent pid")
	}
}

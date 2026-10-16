//go:build unix

package hwinfo

import "golang.org/x/sys/unix"

// diskUsage is the size and the space available to unprivileged users of
// the file system holding path (Linux and macOS).
func diskUsage(path string) (total, avail uint64, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return 0, 0, false
	}
	return uint64(st.Blocks) * uint64(st.Bsize), uint64(st.Bavail) * uint64(st.Bsize), true
}

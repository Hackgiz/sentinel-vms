package hwinfo

import "golang.org/x/sys/unix"

// hostCPU is the CPU brand, e.g. "Apple M2 Pro" or "Intel(R) Core(TM) i7-…".
func hostCPU() string {
	s, _ := unix.Sysctl("machdep.cpu.brand_string")
	return s
}

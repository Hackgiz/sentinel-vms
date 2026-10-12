//go:build !darwin

package hwinfo

// hostCPU: on Linux, Probe already read /proc/cpuinfo.
func hostCPU() string { return "" }

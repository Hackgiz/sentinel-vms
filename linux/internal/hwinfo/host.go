package hwinfo

import "runtime"

// ProbeHost probes the machine Sentinel runs on: Probe(OS{}) plus the
// operating system and, where /proc doesn't exist (macOS), the CPU name.
func ProbeHost() Info {
	info := Probe(OS{})
	info.OS = runtime.GOOS
	if info.CPU == "" {
		info.CPU = hostCPU()
	}
	return info
}

// IsMac reports a macOS host (VideoToolbox decode, CoreML detection).
func (i Info) IsMac() bool { return i.OS == "darwin" }

package hwinfo

// Live health of the machine Sentinel runs on (System Monitor): CPU load,
// memory and the recordings disk. Reads go through seams (FS for /proc, a
// statfs func) so the collector is testable without a real host.

import (
	"runtime"
	"strconv"
	"strings"
	"time"
)

// processStart approximates the process start (package init).
var processStart = time.Now()

// Uptime is how long this process has been running.
func Uptime() time.Duration { return time.Since(processStart) }

// Health is one reading. Zero totals mean "unknown here" (non-Linux CPU/memory).
type Health struct {
	CPU       float64 `json:"cpu"` // 1-minute load average as a percent of the cores, 0..100; 0 off Linux
	MemUsed   uint64  `json:"memUsed"`
	MemTotal  uint64  `json:"memTotal"` // 0 = unknown
	DiskUsed  uint64  `json:"diskUsed"`
	DiskFree  uint64  `json:"diskFree"`
	DiskTotal uint64  `json:"diskTotal"` // 0 = unknown
}

// HealthProbe is what the collector reads. The zero value reads the real host.
type HealthProbe struct {
	FS     FS                                               // nil = the real filesystem
	GOOS   string                                           // "" = runtime.GOOS
	Cores  int                                              // 0 = runtime.NumCPU()
	Statfs func(path string) (total, avail uint64, ok bool) // nil = statfs(2)
}

// CollectHealth reads CPU load, memory and the disk holding dir.
func CollectHealth(p HealthProbe, dir string) Health {
	fsys, goos, cores, statfs := p.FS, p.GOOS, p.Cores, p.Statfs
	if fsys == nil {
		fsys = OS{}
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if cores <= 0 {
		cores = runtime.NumCPU()
	}
	if statfs == nil {
		statfs = diskUsage
	}
	var h Health
	if goos == "linux" {
		if b, err := fsys.ReadFile("/proc/loadavg"); err == nil {
			if f := strings.Fields(string(b)); len(f) > 0 {
				if l, err := strconv.ParseFloat(f[0], 64); err == nil && cores > 0 {
					h.CPU = clampPct(l / float64(cores) * 100)
				}
			}
		}
		if b, err := fsys.ReadFile("/proc/meminfo"); err == nil {
			h.MemTotal, h.MemUsed = parseMeminfo(string(b))
		}
	}
	if total, avail, ok := statfs(dir); ok && total > 0 {
		h.DiskTotal, h.DiskFree = total, avail
		if avail <= total {
			h.DiskUsed = total - avail
		}
	}
	return h
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// parseMeminfo returns total and used bytes (used = total - MemAvailable).
func parseMeminfo(s string) (total, used uint64) {
	var avail uint64
	var haveAvail bool
	for _, line := range strings.Split(s, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		switch name {
		case "MemTotal":
			total = kb * 1024
		case "MemAvailable":
			avail, haveAvail = kb*1024, true
		}
	}
	if total == 0 || !haveAvail || avail > total {
		return total, 0
	}
	return total, total - avail
}

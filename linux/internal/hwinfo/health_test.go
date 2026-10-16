package hwinfo

import "testing"

func TestCollectHealthLinux(t *testing.T) {
	fs := MapFS{Files: map[string]string{
		"/proc/loadavg": "2.00 1.00 0.50 1/400 999\n",
		"/proc/meminfo": "MemTotal:       8000000 kB\nMemFree: 100 kB\nMemAvailable:   2000000 kB\n",
	}}
	h := CollectHealth(HealthProbe{FS: fs, GOOS: "linux", Cores: 4, Statfs: func(p string) (uint64, uint64, bool) {
		if p != "/rec" {
			t.Fatalf("statfs path %q", p)
		}
		return 1000, 250, true
	}}, "/rec")
	if h.CPU != 50 {
		t.Fatalf("cpu %v", h.CPU)
	}
	if h.MemTotal != 8000000*1024 || h.MemUsed != 6000000*1024 {
		t.Fatalf("mem %+v", h)
	}
	if h.DiskTotal != 1000 || h.DiskFree != 250 || h.DiskUsed != 750 {
		t.Fatalf("disk %+v", h)
	}
}

func TestCollectHealthClampsAndDegrades(t *testing.T) {
	fs := MapFS{Files: map[string]string{"/proc/loadavg": "40.0 1 1 1/1 1"}}
	h := CollectHealth(HealthProbe{FS: fs, GOOS: "linux", Cores: 4, Statfs: func(string) (uint64, uint64, bool) { return 0, 0, false }}, "/x")
	if h.CPU != 100 || h.MemTotal != 0 || h.DiskTotal != 0 {
		t.Fatalf("%+v", h)
	}
	// Off Linux the proc files are not consulted.
	h = CollectHealth(HealthProbe{FS: fs, GOOS: "darwin", Cores: 4, Statfs: func(string) (uint64, uint64, bool) { return 10, 4, true }}, "/x")
	if h.CPU != 0 || h.MemTotal != 0 || h.DiskUsed != 6 {
		t.Fatalf("%+v", h)
	}
	// The real statfs works on this host.
	if h := CollectHealth(HealthProbe{}, t.TempDir()); h.DiskTotal == 0 {
		t.Fatalf("real statfs: %+v", h)
	}
}

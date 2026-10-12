package media

import "testing"

func TestParsePorts(t *testing.T) {
	p, sig, err := ParsePorts("28554, 28888,29997")
	if err != nil || p != (Ports{RTSP: 28554, HLS: 28888, API: 29997}) || sig != 0 {
		t.Fatalf("%+v %d %v", p, sig, err)
	}
	p, sig, err = ParsePorts("28554,28888,29997,28889")
	if err != nil || p.API != 29997 || sig != 28889 {
		t.Fatalf("%+v %d %v", p, sig, err)
	}
	for _, bad := range []string{"", "1,2", "1,2,3,4,5", "a,2,3", "0,2,3", "1,2,70000", "5,5,6"} {
		if _, _, err := ParsePorts(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestPIDFileDefault(t *testing.T) {
	s := &Supervisor{ConfigPath: "/data/mediamtx.yml"}
	if got := s.pidFile(); got != "/data/mediamtx.pid" {
		t.Fatalf("pidFile %q", got)
	}
	s.PIDFile = "/run/x.pid"
	if got := s.pidFile(); got != "/run/x.pid" {
		t.Fatalf("pidFile %q", got)
	}
	if (&Supervisor{}).pidFile() != "" {
		t.Fatal("no config, no pid file")
	}
}

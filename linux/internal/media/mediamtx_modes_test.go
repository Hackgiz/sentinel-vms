package media

import (
	"strings"
	"testing"
)

// pathBlock is the YAML of one MediaMTX path in a rendered config.
func pathBlock(t *testing.T, cfg, name string) string {
	t.Helper()
	lines := strings.Split(cfg, "\n")
	for i, l := range lines {
		if l != "  "+name+":" {
			continue
		}
		var b strings.Builder
		for _, m := range lines[i+1:] {
			if !strings.HasPrefix(m, "    ") {
				break
			}
			b.WriteString(m + "\n")
		}
		return b.String()
	}
	t.Fatalf("no path %s in config:\n%s", name, cfg)
	return ""
}

func TestRenderPerRecordingMode(t *testing.T) {
	s := &Supervisor{RecordingsRoot: "/rec", Ports: DefaultPorts}
	cfg, err := s.render([]Source{
		{ID: "CONT", URL: "rtsp://h/1", SubURL: "rtsp://h/1s", Mode: ModeContinuous},
		{ID: "EVT", URL: "rtsp://h/2", Mode: ModeEvents},
		{ID: "DUAL", URL: "rtsp://h/3", SubURL: "rtsp://h/3s", Mode: ModeDual},
		{ID: "DUALNOSUB", URL: "rtsp://h/4", Mode: ModeDual},
		{ID: "OFF", URL: "rtsp://h/5", SubURL: "rtsp://h/5s", Mode: ModeOff},
		{ID: "LEGACY", URL: "rtsp://h/6", Record: true},
		{ID: "LEGACYOFF", URL: "rtsp://h/7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path      string
		want, not []string
	}{
		{"CONT", []string{"sourceOnDemand: no", "record: yes", "recordPath: '/rec/%path/%Y%m%d-%H%M%S-%f'", "recordSegmentDuration: 15m", "recordDeleteAfter: 0s"}, nil},
		{"CONT-sub", []string{"sourceOnDemand: yes", "record: no"}, []string{"recordPath"}},
		{"EVT", []string{"record: yes", "recordPath: '/rec/.buffer/%path/%Y%m%d-%H%M%S-%f'", "recordSegmentDuration: 60s", "recordDeleteAfter: 1h"}, nil},
		{"DUAL", []string{"record: yes", "recordPath: '/rec/.buffer/%path/", "recordSegmentDuration: 60s", "recordDeleteAfter: 1h"}, nil},
		{"DUAL-sub", []string{"sourceOnDemand: no", "record: yes", "recordPath: '/rec/.lowres/%path/%Y%m%d-%H%M%S-%f'", "recordSegmentDuration: 15m", "recordDeleteAfter: 0s"}, nil},
		{"DUALNOSUB", []string{"record: yes", "recordPath: '/rec/.buffer/%path/"}, nil}, // falls back to events
		{"OFF", []string{"record: no"}, nil},
		{"OFF-sub", []string{"sourceOnDemand: yes", "record: no"}, nil},
		{"LEGACY", []string{"record: yes", "recordPath: '/rec/%path/", "recordSegmentDuration: 15m"}, nil},
		{"LEGACYOFF", []string{"record: no"}, nil},
	}
	for _, c := range cases {
		b := pathBlock(t, cfg, c.path)
		for _, w := range c.want {
			if !strings.Contains(b, w) {
				t.Errorf("%s: missing %q in\n%s", c.path, w, b)
			}
		}
		for _, w := range c.not {
			if strings.Contains(b, w) {
				t.Errorf("%s: unexpected %q in\n%s", c.path, w, b)
			}
		}
	}
	if strings.Contains(cfg, "DUALNOSUB-sub") {
		t.Error("a camera without a sub-stream got a -sub path")
	}
}

// A timestamp-crash reconnect (hlswatch.go) still changes a recorded
// low-res path.
func TestRenderRestartLineOnDualSub(t *testing.T) {
	s := &Supervisor{RecordingsRoot: "/rec", Ports: DefaultPorts, restarts: map[string]int{"D-sub": 1}}
	cfg, err := s.render([]Source{{ID: "D", URL: "rtsp://h/3", SubURL: "rtsp://h/3s", Mode: ModeDual}})
	if err != nil {
		t.Fatal(err)
	}
	if b := pathBlock(t, cfg, "D-sub"); !strings.Contains(b, "sourceOnDemandCloseAfter: 11s") {
		t.Fatalf("restart line missing:\n%s", b)
	}
}

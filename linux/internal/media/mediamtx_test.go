package media

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRenderCarriesMediaMTX121Lessons(t *testing.T) {
	s := &Supervisor{RecordingsRoot: "/var/lib/sentinel/recordings", Ports: DefaultPorts, DefaultRetain: 7}
	cfg, err := s.render([]Source{
		{ID: "CAM-A", URL: "rtsp://u:p%40ss@10.0.0.2/main", SubURL: "rtsp://u:p%40ss@10.0.0.2/sub", Record: true, RetentionDays: 3},
		{ID: "CAM-B", URL: "rtsp://10.0.0.3/main", Record: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"recordPath: '/var/lib/sentinel/recordings/%path/%Y%m%d-%H%M%S-%f'", // MediaMTX >= 1.21 requires %path
		"rtspTransports: [tcp]",
		"rtspTransport: tcp",
		"moq: no",
		"apiAddress: 127.0.0.1:",
		"hlsAddress: 127.0.0.1:",
		"recordDeleteAfter: 72h",  // camera override
		"recordDeleteAfter: 168h", // server default
		"  CAM-A-sub:\n",
		"sourceOnDemand: yes",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config missing %q", want)
		}
	}
	for _, bad := range []string{"sourceProtocol", "\nprotocols:"} {
		if strings.Contains(cfg, bad) {
			t.Errorf("config uses removed key %q", bad)
		}
	}
}

func TestYAMLQuote(t *testing.T) {
	got, err := yamlQuote("rtsp://u:it's!#:@h/x")
	if err != nil || got != "'rtsp://u:it''s!#:@h/x'" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := yamlQuote("rtsp://h/x\nrecord: no"); err == nil {
		t.Fatal("line breaks must be rejected (config injection)")
	}
}

func TestWithCredentialsEscapes(t *testing.T) {
	got, err := WithCredentials("rtsp://10.0.0.2:554/s1", "admin", "p@ss/w:rd!")
	if err != nil || got != "rtsp://admin:p%40ss%2Fw%3Ard%21@10.0.0.2:554/s1" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := WithCredentials("http://10.0.0.2/x", "", ""); err == nil {
		t.Fatal("non-rtsp URL must be rejected")
	}
}

// Apply must rewrite the config file in place: MediaMTX watches the file,
// and on macOS a rename-over would leave the watcher on the old inode, so
// camera changes would never reach a running engine.
func TestApplyKeepsTheConfigFilesInode(t *testing.T) {
	dir := t.TempDir()
	s := &Supervisor{ConfigPath: filepath.Join(dir, "mediamtx.yml"), RecordingsRoot: filepath.Join(dir, "rec"), Ports: DefaultPorts}
	if err := s.Apply([]Source{{ID: "a", URL: "rtsp://10.0.0.1/s"}}); err != nil {
		t.Fatal(err)
	}
	st1, err := os.Stat(s.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply([]Source{{ID: "b", URL: "rtsp://10.0.0.2/s"}}); err != nil {
		t.Fatal(err)
	}
	st2, err := os.Stat(s.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	ino := func(fi os.FileInfo) uint64 { return fi.Sys().(*syscall.Stat_t).Ino }
	if ino(st1) != ino(st2) {
		t.Fatal("Apply replaced the config file instead of rewriting it in place")
	}
	b, _ := os.ReadFile(s.ConfigPath)
	if !strings.Contains(string(b), "10.0.0.2") {
		t.Fatal("second Apply didn't write the new source")
	}
}

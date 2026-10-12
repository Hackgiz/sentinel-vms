package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"sentinel-linux/internal/launchd"
)

func TestServicePlistCommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := serviceCommand([]string{"plist", "-program", "/opt/s/sentinel", "-listen", ":9090", "-env", "SENTINEL_MEDIAMTX_PORTS=1,2,3", "-env", "TZ=UTC"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"<string>" + launchd.Label + "</string>",
		"<string>/opt/s/sentinel</string>", "<string>:9090</string>",
		"<string>/Library/Application Support/Sentinel</string>",
		"<key>UserName</key>\n\t<string>_sentinel</string>",
		"<key>SENTINEL_MEDIAMTX_PORTS</key>\n\t\t<string>1,2,3</string>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if serviceCommand(nil, &out, &errb) != 2 || serviceCommand([]string{"plist", "-env", "novalue"}, &out, &errb) != 2 {
		t.Fatal("bad usage must exit 2")
	}
	if serviceCommand([]string{"plist", "-program", "relative"}, &out, &errb) != 1 {
		t.Fatal("invalid config must exit 1")
	}
}

func TestDefaultDataDirDarwin(t *testing.T) {
	t.Setenv("SENTINEL_DATA", "/tmp/x")
	if defaultDataDir() != "/tmp/x" {
		t.Fatal("SENTINEL_DATA")
	}
	t.Setenv("SENTINEL_DATA", "")
	home, _ := os.UserHomeDir()
	want := home + "/Library/Application Support/Sentinel"
	if os.Geteuid() == 0 {
		want = launchd.DaemonDataDir
	}
	if got := defaultDataDir(); got != want {
		t.Fatalf("defaultDataDir() = %q, want %q", got, want)
	}
}

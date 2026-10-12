package launchd

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultDataDir(t *testing.T) {
	cases := []struct {
		euid       int
		user, home string
		want       string
	}{
		{0, "root", "/var/root", DaemonDataDir},
		{299, "_sentinel", "/var/empty", DaemonDataDir},
		{501, "alex", "/Users/alex", "/Users/alex/Library/Application Support/Sentinel"},
		{501, "ej", "", DaemonDataDir},
	}
	for _, c := range cases {
		if got := DefaultDataDir(c.euid, c.user, c.home); got != c.want {
			t.Errorf("DefaultDataDir(%d, %q, %q) = %q, want %q", c.euid, c.user, c.home, got, c.want)
		}
	}
}

// plistDict is a minimal plist decoder for the shapes Plist emits.
func decodePlist(t *testing.T, b []byte) map[string]any {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(string(b)))
	d.Strict = true
	var stack []any
	var keys []string
	var root map[string]any
	var text strings.Builder
	put := func(v any) {
		if len(stack) == 0 {
			return
		}
		switch parent := stack[len(stack)-1].(type) {
		case map[string]any:
			parent[keys[len(keys)-1]] = v
		case *[]any:
			*parent = append(*parent, v)
		}
	}
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch e := tok.(type) {
		case xml.StartElement:
			text.Reset()
			switch e.Name.Local {
			case "dict":
				m := map[string]any{}
				if root == nil {
					root = m
				} else {
					put(m)
				}
				stack = append(stack, m)
				keys = append(keys, "")
			case "array":
				a := &[]any{}
				put(a)
				stack = append(stack, a)
				keys = append(keys, "")
			case "true":
				put(true)
			case "false":
				put(false)
			}
		case xml.CharData:
			text.Write(e)
		case xml.EndElement:
			switch e.Name.Local {
			case "dict", "array":
				stack, keys = stack[:len(stack)-1], keys[:len(keys)-1]
			case "key":
				keys[len(keys)-1] = text.String()
			case "string", "integer":
				put(text.String())
			}
		}
	}
	if root == nil {
		t.Fatalf("no dict in plist:\n%s", b)
	}
	return root
}

func TestDaemonPlist(t *testing.T) {
	c := DaemonConfig()
	c.Env = map[string]string{"SENTINEL_MEDIAMTX_PORTS": "28554,28888,29997", "TZ": "Europe/Berlin"}
	b, err := Plist(c)
	if err != nil {
		t.Fatal(err)
	}
	p := decodePlist(t, b)
	if p["Label"] != Label || p["UserName"] != "_sentinel" || p["GroupName"] != "_sentinel" {
		t.Fatalf("identity %v", p)
	}
	args := *(p["ProgramArguments"].(*[]any))
	want := []any{"/usr/local/sentinel/sentinel", "-data", "/Library/Application Support/Sentinel", "-listen", ":8090"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args %v", args)
	}
	if p["RunAtLoad"] != true || p["KeepAlive"] != true || p["AbandonProcessGroup"] != false {
		t.Fatalf("lifecycle %v", p)
	}
	if p["StandardErrorPath"] != LogFile || p["WorkingDirectory"] != DaemonDataDir {
		t.Fatalf("paths %v", p)
	}
	if lim := p["SoftResourceLimits"].(map[string]any); lim["NumberOfFiles"] != "8192" {
		t.Fatalf("limits %v", lim)
	}
	env := p["EnvironmentVariables"].(map[string]any)
	if env["TZ"] != "Europe/Berlin" || env["SENTINEL_MEDIAMTX_PORTS"] != "28554,28888,29997" || env["PATH"] == nil {
		t.Fatalf("env %v", env)
	}

	// The real parser agrees (macOS only).
	if runtime.GOOS == "darwin" {
		f := filepath.Join(t.TempDir(), Label+".plist")
		if err := os.WriteFile(f, b, 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("plutil", "-lint", f).CombinedOutput(); err != nil {
			t.Fatalf("plutil -lint: %v %s", err, out)
		}
		out, err := exec.Command("plutil", "-convert", "json", "-o", "-", f).Output()
		if err != nil {
			t.Fatal(err)
		}
		var j map[string]any
		if err := json.Unmarshal(out, &j); err != nil || j["Label"] != Label || j["ThrottleInterval"] != float64(10) {
			t.Fatalf("plutil json %v %s", err, out)
		}
	}
}

func TestPlistEscapesAndValidates(t *testing.T) {
	c := DaemonConfig()
	c.DataDir = "/Volumes/NVR & Co/<data>"
	c.UserName, c.Group, c.LogFile, c.MaxFiles = "", "", "", 0
	b, err := Plist(c)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "/Volumes/NVR &amp; Co/&lt;data&gt;") || strings.Contains(s, "UserName") ||
		strings.Contains(s, "StandardOutPath") || strings.Contains(s, "ResourceLimits") {
		t.Fatalf("plist:\n%s", s)
	}
	p := decodePlist(t, b)
	if args := *(p["ProgramArguments"].(*[]any)); args[2] != "/Volumes/NVR & Co/<data>" {
		t.Fatalf("args %v", args)
	}
	for name, bad := range map[string]Config{
		"relative program": {Program: "sentinel", DataDir: "/d", Listen: ":1"},
		"relative data":    {Program: "/s", DataDir: "d", Listen: ":1"},
		"relative log":     {Program: "/s", DataDir: "/d", Listen: ":1", LogFile: "x.log"},
		"no listen":        {Program: "/s", DataDir: "/d"},
		"bad env":          {Program: "/s", DataDir: "/d", Listen: ":1", Env: map[string]string{"A=B": "c"}},
	} {
		if _, err := Plist(bad); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// Package launchd describes Sentinel's macOS service: where it lives, which
// user it runs as, and its launchd property list. It is plain data (no macOS
// APIs) so it builds and is tested on any OS; packaging/macos/install.sh
// writes the plist with `sentinel service plist`.
//
// Layout (LaunchDaemon, the default):
//
//	/usr/local/sentinel/                    program: sentinel, mediamtx, ffmpeg, cloudflared, lib/, models/
//	/Library/Application Support/Sentinel/  data: sentinel.db, install.key, recordings/ (owner _sentinel, 0750)
//	/Library/Logs/Sentinel/sentinel.log     stdout+stderr (rotated by newsyslog)
//	/Library/LaunchDaemons/com.handoffgrid.sentinel.server.plist
//
// Why a LaunchDaemon running as a dedicated _sentinel user, not a per-user
// LaunchAgent: a recorder must start at boot and keep recording with nobody
// logged in (an agent only runs inside a login session); a hidden role
// account can't read anyone's home folder or Keychain, the same least-
// privilege model as the Linux `sentinel` user; and launchd daemons are not
// subject to macOS's Local Network privacy prompt, which a background agent
// can't answer. Sentinel Operator can later host the same server by
// registering an identical daemon from its bundle (SMAppService.daemon with
// BundleProgram), keeping the label, user and data directory.
package launchd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// Label is the launchd job label (and the plist's file name without .plist).
	Label = "com.handoffgrid.sentinel.server"
	// ServiceUser / ServiceGroup: hidden role account created by install.sh.
	ServiceUser  = "_sentinel"
	ServiceGroup = "_sentinel"
	// DaemonDataDir is the LaunchDaemon's data directory.
	DaemonDataDir = "/Library/Application Support/Sentinel"
	// InstallDir holds the program files.
	InstallDir = "/usr/local/sentinel"
	// LogDir / LogFile receive the server's stdout and stderr.
	LogDir  = "/Library/Logs/Sentinel"
	LogFile = LogDir + "/sentinel.log"
	// PlistPath is where install.sh puts the daemon's property list.
	PlistPath = "/Library/LaunchDaemons/" + Label + ".plist"
)

// DefaultDataDir is the data directory when -data and SENTINEL_DATA are not
// given: the daemon's for root and the service user, else the user's own
// ~/Library/Application Support/Sentinel.
func DefaultDataDir(euid int, username, home string) string {
	if euid == 0 || username == ServiceUser || home == "" {
		return DaemonDataDir
	}
	return filepath.Join(home, "Library", "Application Support", "Sentinel")
}

// Config is what goes into the plist.
type Config struct {
	Program  string            // absolute path of the sentinel binary
	DataDir  string            // -data
	Listen   string            // -listen, e.g. ":8090"
	UserName string            // "" = run as root (not recommended)
	Group    string            // "" = the user's primary group
	LogFile  string            // stdout+stderr; "" = none (launchd discards output)
	Env      map[string]string // e.g. SENTINEL_WEBRTC_PORT, TZ
	// MaxFiles raises the open-file limit (launchd's default soft limit is
	// 256; each camera needs several descriptors in MediaMTX and Sentinel).
	MaxFiles int
}

// DaemonConfig is the standard LaunchDaemon configuration.
func DaemonConfig() Config {
	return Config{
		Program:  InstallDir + "/sentinel",
		DataDir:  DaemonDataDir,
		Listen:   ":8090",
		UserName: ServiceUser,
		Group:    ServiceGroup,
		LogFile:  LogFile,
		MaxFiles: 8192,
	}
}

func (c Config) validate() error {
	if !filepath.IsAbs(c.Program) {
		return errors.New("program must be an absolute path")
	}
	if !filepath.IsAbs(c.DataDir) {
		return errors.New("data directory must be an absolute path")
	}
	if c.LogFile != "" && !filepath.IsAbs(c.LogFile) {
		return errors.New("log file must be an absolute path")
	}
	if c.Listen == "" {
		return errors.New("listen address is required")
	}
	for k := range c.Env {
		if k == "" || strings.ContainsAny(k, "= \t\n") {
			return fmt.Errorf("bad environment variable name %q", k)
		}
	}
	return nil
}

// Plist renders the launchd property list for c.
func Plist(c Config) ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	key := func(k string) { fmt.Fprintf(&b, "\t<key>%s</key>\n", esc(k)) }
	str := func(k, v string) { key(k); fmt.Fprintf(&b, "\t<string>%s</string>\n", esc(v)) }
	boolean := func(k string, v bool) {
		key(k)
		if v {
			b.WriteString("\t<true/>\n")
		} else {
			b.WriteString("\t<false/>\n")
		}
	}
	integer := func(k string, v int) { key(k); fmt.Fprintf(&b, "\t<integer>%d</integer>\n", v) }

	str("Label", Label)
	key("ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, a := range []string{c.Program, "-data", c.DataDir, "-listen", c.Listen} {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", esc(a))
	}
	b.WriteString("\t</array>\n")
	if c.UserName != "" {
		str("UserName", c.UserName)
	}
	if c.Group != "" {
		str("GroupName", c.Group)
	}
	str("WorkingDirectory", c.DataDir)
	// Start at boot, restart whenever it exits (crash, or a fatal startup
	// error, at most every ThrottleInterval seconds).
	boolean("RunAtLoad", true)
	boolean("KeepAlive", true)
	integer("ThrottleInterval", 10)
	// SIGTERM, then SIGKILL after this many seconds (Sentinel stops in ~10 s).
	integer("ExitTimeOut", 30)
	// launchd kills the job's whole process group when it exits, which takes
	// MediaMTX, ffmpeg and cloudflared with it even after a crash. Explicit
	// so nobody "fixes" it to true.
	boolean("AbandonProcessGroup", false)
	str("ProcessType", "Standard")
	if c.MaxFiles > 0 {
		for _, k := range []string{"SoftResourceLimits", "HardResourceLimits"} {
			key(k)
			fmt.Fprintf(&b, "\t<dict>\n\t\t<key>NumberOfFiles</key>\n\t\t<integer>%d</integer>\n\t</dict>\n", c.MaxFiles)
		}
	}
	if c.LogFile != "" {
		str("StandardOutPath", c.LogFile)
		str("StandardErrorPath", c.LogFile)
	}
	env := map[string]string{"PATH": "/usr/bin:/bin:/usr/sbin:/sbin"}
	for k, v := range c.Env {
		env[k] = v
	}
	key("EnvironmentVariables")
	b.WriteString("\t<dict>\n")
	for _, k := range sortedKeys(env) {
		fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", esc(k), esc(env[k]))
	}
	b.WriteString("\t</dict>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes(), nil
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

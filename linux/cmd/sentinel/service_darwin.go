package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sentinel-linux/internal/launchd"
)

// `sentinel service plist [flags]` prints the launchd property list that
// packaging/macos/install.sh installs (and Sentinel Operator can reuse). It
// is dispatched from init so main's flag handling stays untouched; any other
// first argument runs the server as usual.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "service" {
		os.Exit(serviceCommand(os.Args[2:], os.Stdout, os.Stderr))
	}
}

type envFlags map[string]string

func (e envFlags) String() string { return "" }
func (e envFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("want NAME=value, got %q", v)
	}
	e[k] = val
	return nil
}

func serviceCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "plist" {
		fmt.Fprintln(stderr, "usage: sentinel service plist [-program PATH] [-data DIR] [-listen ADDR] [-user NAME] [-group NAME] [-log FILE] [-env NAME=value ...]")
		return 2
	}
	def := launchd.DaemonConfig()
	if exe, err := os.Executable(); err == nil {
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			def.Program = p
		}
	}
	fs := flag.NewFlagSet("sentinel service plist", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := def
	env := envFlags{}
	c.Env = env
	fs.StringVar(&c.Program, "program", def.Program, "absolute path of the sentinel binary")
	fs.StringVar(&c.DataDir, "data", def.DataDir, "data directory")
	fs.StringVar(&c.Listen, "listen", def.Listen, "dashboard/API address")
	fs.StringVar(&c.UserName, "user", def.UserName, `user to run as ("" = root)`)
	fs.StringVar(&c.Group, "group", def.Group, "group to run as")
	fs.StringVar(&c.LogFile, "log", def.LogFile, `stdout/stderr file ("" = none)`)
	fs.IntVar(&c.MaxFiles, "max-files", def.MaxFiles, "open-file limit")
	fs.Var(env, "env", "environment variable NAME=value (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	b, err := launchd.Plist(c)
	if err != nil {
		fmt.Fprintln(stderr, "sentinel service plist:", err)
		return 1
	}
	_, _ = stdout.Write(b)
	return 0
}

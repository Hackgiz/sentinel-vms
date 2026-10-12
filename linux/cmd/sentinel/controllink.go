package main

// The control link (PLATFORM.md, "Control link wire protocol"): the
// management server's mTLS listener, and a recording server's connection
// to its management server (enrolling first when given a token).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"sentinel-linux/internal/link/remote"
	"sentinel-linux/internal/recorder"
	"sentinel-linux/internal/server"
	"sentinel-linux/internal/videogw"
)

// linkOptions are the control link flags.
type linkOptions struct {
	ControlListen string   // management: listen address; "" = off
	ControlHosts  []string // management: extra names in the listener's certificate
	Management    string   // recording: https://host:8091
	Enroll        string   // recording: one-time enrollment token
	VideoURL      string   // recording: where its video plane is reached; "" = derived from --listen
}

// controlListenFor resolves --control-listen: explicit (or env) wins, "off"
// turns it off, else :8091 for --role management and off for all.
func controlListenFor(role, value string, set bool) string {
	if set {
		if strings.EqualFold(value, "off") {
			return ""
		}
		return value
	}
	if role == roleManagement {
		return ":" + remote.DefaultControlPort
	}
	return ""
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// startControlListener runs the control listener and makes it the
// server's ControlLink.
func startControlListener(ctx context.Context, srv *server.Server, dataDir string, o linkOptions) error {
	l, err := remote.NewListener(srv, dataDir, o.ControlListen, o.ControlHosts)
	if err != nil {
		return fmt.Errorf("control link CA: %w", err)
	}
	ln, err := net.Listen("tcp", o.ControlListen)
	if err != nil {
		return fmt.Errorf("control link: %w", err)
	}
	srv.Control = l
	go func() {
		if err := l.Serve(ctx, ln); err != nil {
			slog.Error("control link listener stopped", "err", err)
		}
	}()
	slog.Info("control link listening", "addr", o.ControlListen, "url", l.ManagementURL(), "ca", l.CA.Fingerprint())
	return nil
}

// recordingIdentity loads this recording server's identity, enrolling
// first when --enroll is given (unless it is already enrolled with that
// management server). nil without error = not enrolled: it runs on its own.
func recordingIdentity(ctx context.Context, dataDir string, o linkOptions) (*remote.Identity, error) {
	id, err := remote.LoadIdentity(dataDir)
	if err != nil && !errors.Is(err, remote.ErrNotEnrolled) {
		return nil, err
	}
	if o.Enroll != "" {
		if id != nil && (o.Management == "" || sameManagement(id.Management, o.Management)) {
			slog.Info("already enrolled; ignoring --enroll", "server", id.ServerID, "management", id.Management)
			return id, nil
		}
		if o.Management == "" {
			return nil, errors.New("--enroll needs --management https://host:8091")
		}
		id, err = remote.Enroll(ctx, dataDir, remote.EnrollOptions{Management: o.Management, Token: o.Enroll, Version: Version})
		if err != nil {
			return nil, err
		}
		slog.Info("enrolled with the management server", "server", id.ServerID, "management", id.Management)
		return id, nil
	}
	if id == nil {
		slog.Warn(remote.ErrNotEnrolled.Error())
		return nil, nil
	}
	if o.Management != "" && !sameManagement(id.Management, o.Management) {
		slog.Warn("--management differs from the enrolled management server; using the enrolled one (enroll again to move)", "enrolled", id.Management, "flag", o.Management)
	}
	return id, nil
}

func sameManagement(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "https://"), "/")
		if _, _, err := net.SplitHostPort(s); err != nil {
			s = net.JoinHostPort(s, remote.DefaultControlPort)
		}
		return s
	}
	return norm(a) == norm(b)
}

// videoURLFor is where the management server reaches this recording
// server's video plane: --video-url, else this machine's first LAN
// address on the --listen port.
func videoURLFor(flagValue, listen string) string {
	if flagValue != "" {
		return strings.TrimSuffix(flagValue, "/")
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = lanAddress()
	}
	return "http://" + net.JoinHostPort(host, port)
}

// lanAddress is the first non-loopback IPv4 address, else the hostname.
func lanAddress() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
			return ipn.IP.String()
		}
	}
	return "127.0.0.1"
}

// recordingHandler is a recording server's HTTP surface: its video plane
// at /v/{token}/… (tokens signed by the management server) and the health
// check.
func recordingHandler(rec *recorder.Recorder, client *remote.Client) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]any{"role": roleRecording, "recorder": rec.Status(), "pendingEvents": rec.Pending()}
		if client != nil {
			up, err := client.Connected()
			l := map[string]any{"serverId": client.ID.ServerID, "management": client.ID.Management, "connected": up, "videoUrl": client.VideoURL}
			if err != nil {
				l["error"] = err.Error()
			}
			out["link"] = l
		}
		writeJSON(w, out)
	})
	mux.Handle(videogw.Prefix, remote.VideoPlane(rec.Video(), rec.VideoTokens))
	return mux
}

// Command sentinel is Sentinel VMS for Linux: a headless video management
// server with a web dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/logring"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/server"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tunnel"
	"sentinel-linux/web"
)

// Version is set at build time: -ldflags "-X main.Version=1.0.0".
var Version = "dev"

var recentLog func() string

func main() {
	listen := flag.String("listen", ":8090", "address for the dashboard and API")
	dataDir := flag.String("data", defaultDataDir(), "data directory (database, recordings, keys)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(Version)
		return
	}
	recent := logring.New(300) // last log lines for bug reports (secrets stripped)
	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, recent), &slog.HandlerOptions{Level: slog.LevelInfo})))
	recentLog = recent.Text
	if err := run(*listen, *dataDir); err != nil {
		slog.Error("sentinel stopped", "err", err)
		os.Exit(1)
	}
}

func defaultDataDir() string {
	if d := os.Getenv("SENTINEL_DATA"); d != "" {
		return d
	}
	if os.Geteuid() == 0 {
		return "/var/lib/sentinel"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "sentinel")
}

func run(listen, dataDir string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(dataDir)
	if err != nil {
		return fmt.Errorf("open data dir %s: %w", dataDir, err)
	}
	defer st.Close()

	bin, err := media.FindBinary(dataDir)
	if err != nil {
		return err
	}
	recordings := filepath.Join(dataDir, "recordings")
	if err := os.MkdirAll(recordings, 0o750); err != nil {
		return err
	}
	sup := &media.Supervisor{
		Binary:         bin,
		ConfigPath:     filepath.Join(dataDir, "mediamtx.yml"),
		LogPath:        filepath.Join(dataDir, "mediamtx.log"),
		RecordingsRoot: recordings,
		Ports:          media.DefaultPorts,
	}

	srv := server.New(st, sup, web.FS(), Version)
	srv.Ctx = ctx
	srv.RecentLog = recentLog
	if _, port, err := net.SplitHostPort(listen); err == nil {
		srv.ListenPort = port
	}

	// iPhone remote access: the Cloudflare tunnel's origin is a loopback
	// listener serving ONLY the phone API, never the dashboard.
	originLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("companion origin: %w", err)
	}
	originSrv := &http.Server{Handler: srv.CompanionHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	go func() { _ = originSrv.Serve(originLn) }()
	defer originSrv.Close()
	srv.Tunnel = &tunnel.Tunnel{
		Binary:  tunnel.Find(dataDir),
		Origin:  "http://" + originLn.Addr().String(),
		WorkDir: dataDir,
	}
	if srv.Tunnel.Binary == "" {
		slog.Info("remote access unavailable: cloudflared not found (iPhone works on the local network only)")
	} else if srv.RemoteEnabled() {
		_ = srv.Tunnel.Start(ctx)
	}
	srv.Detect = &detect.Manager{FFmpeg: detect.Find(dataDir), OnMotion: srv.OnMotion}
	if !srv.Detect.Available() {
		slog.Warn("motion detection unavailable: ffmpeg not found")
	} else if lib, model := detect.FindObjectModel(dataDir); lib != "" && model != "" {
		threads := runtime.NumCPU() / 2
		if threads < 1 {
			threads = 1
		} else if threads > 4 {
			threads = 4
		}
		if od, err := detect.NewObjectDetector(lib, model, threads, 2); err != nil {
			slog.Warn("person/vehicle detection unavailable", "err", err)
		} else {
			srv.Detect.Objects = od
			srv.Detect.OnObjects = srv.OnObjects
			srv.Detect.SourceSize = func(id string) (int, int) {
				st := sup.State(id)
				return st.Width, st.Height
			}
			slog.Info("person/vehicle detection ready", "model", filepath.Base(model), "threads", threads)
		}
	} else {
		slog.Info("person/vehicle detection not installed (onnxruntime or model missing); motion only")
	}
	go srv.RunMonitors(ctx)
	if err := srv.ApplyMedia(ctx); err != nil {
		return fmt.Errorf("write media config: %w", err)
	}
	go sup.Run(ctx)
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				st.PruneSessions(ctx)
			}
		}
	}()

	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	slog.Info("sentinel listening", "addr", listen, "data", dataDir, "version", Version, "mediamtx", bin)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

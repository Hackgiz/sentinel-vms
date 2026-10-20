// Command sentinel is the Sentinel VMS server: a headless video management
// server with a web dashboard.
//
// --role picks what this process runs (PLATFORM.md):
//
//	all         the management server and a local recording server in one
//	            process, joined by the in-process link (the default; what
//	            every install ran before roles existed)
//	management  users, cameras, alarms, rules, the APIs and the dashboard;
//	            recording servers connect to its control link (:8091)
//	recording   cameras, recording and detection only, configured by the
//	            management server it is enrolled with (--management,
//	            --enroll); events wait in the outbox while it is away
package main

import (
	"context"
	"encoding/json"
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
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // TZ works even where the OS has no zoneinfo (containers)

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/homekit"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/link/inproc"
	"sentinel-linux/internal/link/remote"
	"sentinel-linux/internal/logring"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/recorder"
	"sentinel-linux/internal/server"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tunnel"
	"sentinel-linux/web"
)

// Version is set at build time: -ldflags "-X main.Version=1.0.0".
var Version = "dev"

// recentLog keeps the last log lines, secrets stripped: for bug reports
// and for Sentinel Admin's log viewer (link.CmdLog).
var recentLog = logring.New(link.LogMaxLines)

// feedbackLogLines is how much of it a bug report includes.
const feedbackLogLines = 300

func main() {
	listen := flag.String("listen", ":8090", "address for the dashboard and API")
	dataDir := flag.String("data", defaultDataDir(), "data directory (database, recordings, keys)")
	showVersion := flag.Bool("version", false, "print version and exit")
	role := flag.String("role", envString("SENTINEL_ROLE", roleAll), "what this server runs: all, management or recording; env SENTINEL_ROLE")
	healthcheck := flag.Bool("healthcheck", false, "probe the server at -listen on localhost and exit 0 if healthy (Docker HEALTHCHECK)")
	flag.IntVar(&webrtcPort, "webrtc-port", envInt("SENTINEL_WEBRTC_PORT", media.DefaultWebRTCICEPort), "UDP+TCP port for WebRTC live video (0 = off, HLS only); env SENTINEL_WEBRTC_PORT")
	flag.StringVar(&webrtcHosts, "webrtc-hosts", os.Getenv("SENTINEL_WEBRTC_HOSTS"), "extra comma-separated IPs/host names browsers use to reach this server (Docker/NAT); env SENTINEL_WEBRTC_HOSTS")
	controlListen := flag.String("control-listen", os.Getenv("SENTINEL_CONTROL_LISTEN"), "control link listener for recording servers (default :8091 for --role management, off for all; \"off\" turns it off); env SENTINEL_CONTROL_LISTEN")
	controlHosts := flag.String("control-hosts", os.Getenv("SENTINEL_CONTROL_HOSTS"), "comma-separated names/IPs recording servers use to reach the control link (added to its certificate); env SENTINEL_CONTROL_HOSTS")
	management := flag.String("management", os.Getenv("SENTINEL_MANAGEMENT"), "recording: the management server's control link, https://host:8091; env SENTINEL_MANAGEMENT")
	enroll := flag.String("enroll", "", "recording: one-time enrollment token from Sentinel Admin (first start only)")
	videoURL := flag.String("video-url", os.Getenv("SENTINEL_VIDEO_URL"), "recording: where the management server reaches this server's video plane (default http://<LAN address>:<listen port>); env SENTINEL_VIDEO_URL")
	flag.Parse()
	exitIfVerifyExport() // verifyexport.go (WAVE8(C))
	if *showVersion {
		fmt.Println(Version)
		return
	}
	if *healthcheck {
		if err := probe(*listen); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}
	logLevel := slog.LevelInfo
	if os.Getenv("SENTINEL_DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, recentLog), &slog.HandlerOptions{Level: logLevel})))
	controlSet := os.Getenv("SENTINEL_CONTROL_LISTEN") != ""
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "control-listen" {
			controlSet = true
		}
	})
	lo := linkOptions{
		ControlListen: controlListenFor(*role, *controlListen, controlSet),
		ControlHosts:  splitList(*controlHosts),
		Management:    *management,
		Enroll:        strings.TrimSpace(*enroll),
		VideoURL:      *videoURL,
	}
	if err := run(*listen, *dataDir, *role, lo); err != nil {
		slog.Error("sentinel stopped", "err", err)
		os.Exit(1)
	}
}

// probe GETs /api/v1/status on the loopback address for listen. Distroless
// images have no curl/wget, so the binary checks itself.
func probe(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/api/v1/status")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %s", resp.Status)
	}
	return nil
}

const (
	roleAll        = "all"
	roleManagement = "management"
	roleRecording  = "recording"
)

func envString(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func run(listen, dataDir, role string, lo linkOptions) error {
	switch role {
	case roleAll, roleManagement, roleRecording:
	default:
		return fmt.Errorf("unknown role %q (want all, management or recording)", role)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Management side.
	var st *store.Store
	var srv *server.Server
	if role != roleRecording {
		var err error
		st, err = store.Open(dataDir)
		if err != nil {
			return fmt.Errorf("open data dir %s: %w", dataDir, err)
		}
		defer st.Close()
		srv = server.New(st, web.FS(), Version)
		srv.Ctx = ctx
		srv.RecentLog = func() string { return recentLog.Tail(feedbackLogLines) }
		srv.Logs = recentLog.Tail
		srv.UpdateURL = os.Getenv("SENTINEL_UPDATE_URL") // "" = sentvms.com
		if _, port, err := net.SplitHostPort(listen); err == nil {
			srv.ListenPort = port
		}
		closeOrigin, err := startRemoteAccess(ctx, srv, dataDir)
		if err != nil {
			return err
		}
		defer closeOrigin()
		if lo.ControlListen != "" {
			if err := startControlListener(ctx, srv, dataDir, lo); err != nil {
				return err
			}
		}
	}

	// Recording side. In --role all it shares sentinel.db (one events table).
	var rec *recorder.Recorder
	var sup *media.Supervisor
	if role != roleManagement {
		var rst *recorder.Store
		var err error
		accel := detect.AccelAuto
		if st != nil {
			accel = st.Setting(ctx, "hwaccel", detect.AccelAuto)
			rst, err = recorder.NewStore(st.DB)
		} else {
			rst, err = recorder.OpenStore(filepath.Join(dataDir, "recorder.db"))
			if err == nil {
				defer rst.DB.Close()
			}
		}
		if err != nil {
			return fmt.Errorf("open recorder store: %w", err)
		}
		rec, sup, err = newRecorder(ctx, dataDir, rst, accel)
		if err != nil {
			return err
		}
		defer rec.Wait() // background snapshots and deliveries, before the stores close
	}

	// A recording server alone connects to its management server.
	var client *remote.Client
	if role == roleRecording {
		id, err := recordingIdentity(ctx, dataDir, lo)
		if err != nil {
			return err
		}
		if id != nil {
			rec.ServerID = id.ServerID // before any snapshot: video tokens name it
			client = &remote.Client{ID: id, Recorder: rec, Version: Version, VideoURL: videoURLFor(lo.VideoURL, listen)}
		}
	}

	if srv != nil && rec != nil {
		ms, rs := inproc.Connect(rec, srv.Sink())
		srv.AttachRecorder(ms)
		rec.Attach(rs)
	}
	var hkBridge *homekit.Bridge // Apple Home: --role all only (HOMEKIT.md)
	if srv != nil && sup != nil {
		b, err := homekit.New(homekit.Deps{Store: st, Sup: sup, Bus: srv.Bus, FFmpeg: rec.FFmpegPath(),
			Accel: st.Setting(ctx, "hwaccel", detect.AccelAuto), Version: Version})
		if err != nil {
			slog.Warn("apple home bridge unavailable", "err", err)
		} else {
			hkBridge = b
			srv.HomeKit = homekitAdapter{b}
		}
	}
	if srv != nil {
		if hkBridge != nil {
			go func() { _ = hkBridge.Run(ctx) }()
		}
		go srv.RunMonitors(ctx)
		go srv.RunRules(ctx) // alarm rules: push queue
		go srv.RunUpdateChecks(ctx)
		// appearance.go (WAVE9(A)): optional re-id model for appearance search
		setupAppearance(dataDir, srv)
		if err := srv.ApplyMedia(ctx); err != nil { // the recorder's first config
			return fmt.Errorf("write media config: %w", err)
		}
		go func() {
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					st.PruneSessions(ctx)
					st.PruneTokens(ctx)
				}
			}
		}()
	} else if client != nil {
		go client.Run(ctx) // applies the saved snapshot, then connects
	} else {
		slog.Warn("recording server without a management server: no cameras until it is enrolled")
		if _, err := rec.ApplyConfig(ctx, link.Snapshot{}); err != nil {
			return fmt.Errorf("write media config: %w", err)
		}
	}
	if rec != nil {
		go rec.Run(ctx) // status reports, outbox retries, low-disk watch
		go sup.Run(ctx)
	}

	var handler http.Handler
	if srv != nil {
		handler = srv.Handler()
	} else {
		handler = recordingHandler(rec, client)
	}
	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	logArgs := []any{"addr", listen, "role", role, "data", dataDir, "version", Version}
	if sup != nil {
		logArgs = append(logArgs, "mediamtx", sup.Binary)
	}
	slog.Info("sentinel listening", logArgs...)

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

// startRemoteAccess sets up remote access: the loopback origin that the
// Cloudflare tunnel and the Sentinel Cloud relay forward to serves only
// Server.RemoteHandler, never the dashboard or /api/v1 (WAVE12(C)). The
// returned func closes the origin.
func startRemoteAccess(ctx context.Context, srv *server.Server, dataDir string) (func(), error) {
	originLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("companion origin: %w", err)
	}
	originSrv := &http.Server{Handler: srv.RemoteHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second} // WAVE12(C): v2 + /v/ + phone API
	go func() { _ = originSrv.Serve(originLn) }()
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
	srv.StartCloud(ctx, "http://"+originLn.Addr().String()) // WAVE12(C): Sentinel Cloud (management roles only)
	return func() { originSrv.Close() }, nil
}

// newRecorder sets up the recording side: MediaMTX, motion and
// person/vehicle detection. accel is the "hwaccel" setting at startup.
func newRecorder(ctx context.Context, dataDir string, rst *recorder.Store, accel string) (*recorder.Recorder, *media.Supervisor, error) {
	bin, err := media.FindBinary(dataDir)
	if err != nil {
		return nil, nil, err
	}
	recordings := filepath.Join(dataDir, "recordings")
	if err := os.MkdirAll(recordings, 0o750); err != nil {
		return nil, nil, err
	}
	sup := &media.Supervisor{
		Binary:         bin,
		ConfigPath:     filepath.Join(dataDir, "mediamtx.yml"),
		LogPath:        filepath.Join(dataDir, "mediamtx.log"),
		RecordingsRoot: recordings,
		Ports:          mediaPorts(), // SENTINEL_MEDIAMTX_PORTS (ports.go)
	}
	sup.WebRTC = webrtcConfig() // webrtc.go

	rec := recorder.New(dataDir, sup, rst, Version, accel)
	rec.Ctx = ctx
	rec.Log = recentLog.Tail
	rec.Detect = &detect.Manager{FFmpeg: detect.Find(dataDir), OnMotion: rec.OnMotion}
	setupDecodeAccel(ctx, accel, rec.Detect, sup) // VAAPI/QSV/VideoToolbox decode (accel.go)
	if !rec.Detect.Available() {
		slog.Warn("motion detection unavailable: ffmpeg not found")
	} else if lib, model := findObjectModel(dataDir); lib != "" && model != "" {
		threads := runtime.NumCPU() / 2
		if threads < 1 {
			threads = 1
		} else if threads > 4 {
			threads = 4
		}
		if od, err := newObjectDetector(accel, dataDir, lib, model, threads); err != nil { // CPU, OpenVINO or CoreML (accel.go)
			slog.Warn("person/vehicle detection unavailable", "err", err)
		} else {
			rec.Detect.Objects = od
			rec.Detect.OnObjects = rec.OnObjects
			rec.Detect.OnLineCross = rec.OnLineCross
			rec.Detect.SourceSize = func(id string) (int, int) {
				st := sup.State(id)
				return st.Width, st.Height
			}
			slog.Info("person/vehicle detection ready", "model", filepath.Base(model), "threads", threads)
		}
	} else {
		slog.Info("person/vehicle detection not installed (onnxruntime or model missing); motion only")
	}
	setupAudio(dataDir, rec) // audio.go
	return rec, sup, nil
}

// homekitAdapter lets the server API use the bridge without importing
// internal/homekit (which imports server for its bus topics).
type homekitAdapter struct{ b *homekit.Bridge }

func (a homekitAdapter) Status() server.HomeKitStatus {
	return server.HomeKitStatus(a.b.Status())
}

func (a homekitAdapter) ResetPairings(ctx context.Context) error { return a.b.ResetPairings(ctx) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

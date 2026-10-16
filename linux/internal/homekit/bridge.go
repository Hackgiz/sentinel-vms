// Package homekit exposes Sentinel's cameras to Apple Home as a HomeKit
// Accessory Protocol bridge: a bridge accessory (aid 1) with one IP camera
// accessory per camera, each with a motion sensor driven by Sentinel's
// detection. See HOMEKIT.md for the contract.
//
// The HAP server itself is github.com/brutella/hap (Apache License 2.0),
// whose camera services cover Camera RTP Stream Management; accessory.go
// follows the structure of the reference camera app github.com/brutella/hkcam
// (also Apache 2.0). Pairing state lives in the homekit_state table, never
// in files.
//
// The bridge runs only where the media supervisor and the management store
// share a process (--role all). It idles while the homekit_enabled setting is
// "0", and flipping the setting takes effect without a restart.
//
// Snapshots and live video are produced by the ffmpeg pipeline in stream.go,
// which installs itself into captureJPEG and startStream.
package homekit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brutella/hap"
	"github.com/brutella/hap/accessory"
	haplog "github.com/brutella/hap/log"

	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/server"
)

// DefaultPort is the HAP TCP port; SENTINEL_HOMEKIT_PORT overrides it.
const DefaultPort = 18093

// Seam to stream.go. The defaults make the bridge pairable and testable
// without ffmpeg: snapshots fall back to the offline placeholder and live
// view does not start.
var captureJPEG = func(ctx context.Context, d *Deps, camID string, w, h int) ([]byte, error) {
	return nil, ErrStreamingUnavailable
}

var startStream = func(ctx context.Context, d *Deps, camID string, p StreamParams) (*Session, error) {
	return nil, ErrStreamingUnavailable
}

// serveHAP runs a HAP server until ctx ends. A variable so tests can run the
// bridge without opening sockets or announcing on the network.
var serveHAP = func(ctx context.Context, srv *hap.Server) error { return srv.ListenAndServe(ctx) }

// Bridge is the HomeKit bridge.
type Bridge struct {
	d    Deps
	db   *sql.DB
	hs   *sqlStore
	aids *aidRegistry
	port int

	now       func() time.Time
	pollEvery time.Duration // how often the enabled setting and camera list are re-read

	motion *motionTracker
	snaps  *snapshotCache
	wake   chan struct{}

	opMu sync.Mutex // serializes reconcile and ResetPairings

	mu      sync.Mutex
	baseCtx context.Context // Run's context; nil before Run
	cur     *running
	lastErr string
	online  map[string]bool // camera id → last reported reachability
}

// running is one live HAP server and the accessories it was built with.
type running struct {
	key    string
	cancel context.CancelFunc
	done   chan struct{}
	cams   map[string]*camAccessory
	byAID  map[uint64]string
}

func (r *running) exited() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

var hapLogOnce sync.Once

// New prepares the bridge. It reads nothing from the network and starts
// nothing; Run does the work.
func New(d Deps) (*Bridge, error) {
	if d.Store == nil {
		return nil, errors.New("homekit: no store")
	}
	hapLogOnce.Do(func() {
		haplog.Info.SetOutput(slogWriter{})
		if os.Getenv("SENTINEL_DEBUG") != "" {
			haplog.Debug.SetOutput(slogWriter{})
		} else {
			haplog.Debug.SetOutput(io.Discard)
		}
	})
	b := &Bridge{
		d:         d,
		db:        d.Store.DB,
		hs:        newSQLStore(d.Store.DB),
		aids:      newAIDRegistry(d.Store.DB),
		port:      portFromEnv(),
		now:       time.Now,
		pollEvery: 3 * time.Second,
		wake:      make(chan struct{}, 1),
		online:    map[string]bool{},
	}
	b.motion = newMotionTracker(func() time.Time { return b.now() }, b.setMotion)
	b.snaps = newSnapshotCache(func() time.Time { return b.now() }, func(ctx context.Context, camID string, w, h int) ([]byte, error) {
		return captureJPEG(ctx, &b.d, camID, w, h)
	})
	return b, nil
}

func portFromEnv() int {
	if v := strings.TrimSpace(os.Getenv("SENTINEL_HOMEKIT_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < 65536 {
			return n
		}
		slog.Warn("homekit: ignoring invalid SENTINEL_HOMEKIT_PORT", "value", v)
	}
	return DefaultPort
}

// slogWriter routes the HAP library's own log lines into slog.
type slogWriter struct{}

func (slogWriter) Write(p []byte) (int, error) {
	slog.Debug("homekit: hap", "msg", strings.TrimSpace(string(p)))
	return len(p), nil
}

// Run serves HAP and mDNS until ctx ends. It subscribes to
// server.TopicConfigChanged (rebuild the accessory set),
// server.TopicCameraActivity (motion) and server.TopicCameraOnline
// (reachability). While homekit_enabled is "0" it idles; the setting is
// re-read every few seconds, so flipping it needs no restart.
func (b *Bridge) Run(ctx context.Context) error {
	b.mu.Lock()
	b.baseCtx = ctx
	b.mu.Unlock()

	if b.d.Bus != nil {
		sub := b.d.Bus.Subscribe("homekit", 256)
		bus.Handle(sub, server.TopicConfigChanged, func(server.ConfigChanged) { b.kick() })
		bus.Handle(sub, server.TopicCameraActivity, func(a server.CameraActivity) { b.onActivity(a.CameraID) })
		bus.Handle(sub, server.TopicCameraOnline, func(o server.CameraOnline) { b.onOnline(o.CameraID, o.Online) })
		defer sub.Close()
	}
	defer func() {
		b.opMu.Lock()
		b.stopRunning()
		b.opMu.Unlock()
		b.mu.Lock()
		b.baseCtx = nil
		b.mu.Unlock()
	}()

	b.reconcile(ctx)
	sweep := time.NewTicker(time.Second)
	defer sweep.Stop()
	poll := time.NewTicker(b.pollEvery)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sweep.C:
			b.motion.Sweep()
		case <-poll.C:
			b.reconcile(ctx)
		case <-b.wake:
			b.reconcile(ctx)
		}
	}
}

// kick asks Run to reconcile now.
func (b *Bridge) kick() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// settingOn reports whether HomeKit is switched on in settings.
func (b *Bridge) settingOn(ctx context.Context) bool {
	switch strings.ToLower(strings.TrimSpace(b.d.Store.Setting(ctx, "homekit_enabled", "0"))) {
	case "1", "true", "on":
		return true
	}
	return false
}

func (b *Bridge) bridgeName(ctx context.Context) string {
	if n := strings.TrimSpace(b.d.Store.Setting(ctx, "homekit_name", "Sentinel")); n != "" {
		return n
	}
	return "Sentinel"
}

// exposed lists the cameras the bridge publishes, with their accessory ids.
// Cameras on remote recording servers are left out: their streams are not in
// this process's mediamtx.
func (b *Bridge) exposed(ctx context.Context) ([]camSpec, error) {
	rows, err := b.db.QueryContext(ctx, `SELECT id, name FROM cameras
		WHERE homekit = 1 AND recorder_id = 'local' ORDER BY sort_order, created_at`)
	if err != nil {
		return nil, err
	}
	var specs []camSpec
	for rows.Next() {
		var s camSpec
		if err := rows.Scan(&s.ID, &s.Name); err != nil {
			rows.Close()
			return nil, err
		}
		specs = append(specs, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range specs {
		aid, err := b.aids.Assign(ctx, specs[i].ID)
		if err != nil {
			return nil, err
		}
		specs[i].AID = aid
	}
	return specs, nil
}

// reconcile brings the HAP server in line with the settings and the camera
// list: stopped when disabled, restarted when the accessory set changed.
func (b *Bridge) reconcile(ctx context.Context) {
	b.opMu.Lock()
	defer b.opMu.Unlock()
	b.reconcileLocked(ctx)
}

func (b *Bridge) reconcileLocked(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if !b.settingOn(ctx) {
		b.stopRunning()
		b.setErr("")
		return
	}
	if b.d.FFmpeg == "" {
		b.stopRunning()
		b.setErr("ffmpeg not found; HomeKit needs it for snapshots and live video")
		return
	}
	name := b.bridgeName(ctx)
	specs, err := b.exposed(ctx)
	if err != nil {
		b.setErr("reading cameras: " + err.Error())
		return
	}
	key := configKey(name, b.port, specs)

	b.mu.Lock()
	cur := b.cur
	b.mu.Unlock()
	if cur != nil && cur.key == key && !cur.exited() {
		return
	}
	b.stopRunning()
	if err := b.startRunning(ctx, name, key, specs); err != nil {
		slog.Warn("homekit: bridge failed to start", "err", err)
		b.setErr(err.Error())
		return
	}
	b.setErr("")
}

// configKey identifies an accessory set: anything that changes what a
// controller should see changes the key.
func configKey(name string, port int, specs []camSpec) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s|%d", name, port)
	for _, s := range specs {
		fmt.Fprintf(&sb, "|%s=%d=%s", s.ID, s.AID, s.Name)
	}
	return sb.String()
}

func (b *Bridge) setErr(s string) {
	b.mu.Lock()
	b.lastErr = s
	b.mu.Unlock()
}

// startRunning builds the accessories and starts a HAP server.
func (b *Bridge) startRunning(ctx context.Context, name, key string, specs []camSpec) (err error) {
	defer func() { // hap panics on some internal errors
		if r := recover(); r != nil {
			err = fmt.Errorf("hap: %v", r)
		}
	}()
	pin, setupID, err := b.setupCodes()
	if err != nil {
		return err
	}
	b.bumpConfigNumber(key)

	runCtx, cancel := context.WithCancel(ctx)
	env := &accEnv{ctx: runCtx, deps: &b.d, version: b.d.Version}
	hub := accessory.NewBridge(accessory.Info{
		Name:         name,
		SerialNumber: "SENTINEL-" + setupID,
		Manufacturer: "Handoffgrid",
		Model:        "Sentinel VMS",
		Firmware:     b.d.Version,
	})
	hub.Id = bridgeAID

	r := &running{key: key, cancel: cancel, done: make(chan struct{}), cams: map[string]*camAccessory{}, byAID: map[uint64]string{}}
	var as []*accessory.A
	for _, s := range specs {
		c := newCamAccessory(s, env)
		r.cams[s.ID] = c
		r.byAID[s.AID] = s.ID
		as = append(as, c.a)
	}
	srv, err := hap.NewServer(b.hs, hub.A, as...)
	if err != nil {
		cancel()
		return err
	}
	srv.Pin = pin
	srv.SetupId = setupID
	srv.Addr = fmt.Sprintf(":%d", b.port)
	srv.ServeMux().HandleFunc("/resource", b.resourceHandler(srv, r))

	// The new accessories start from the live state, not from defaults.
	b.mu.Lock()
	for id, c := range r.cams {
		if b.motion.Active(id) {
			c.SetMotion(true)
		}
		if on, ok := b.online[id]; ok {
			c.SetReachable(on)
		}
	}
	b.cur = r
	b.mu.Unlock()

	serve := serveHAP
	go func() {
		defer close(r.done)
		defer func() {
			if p := recover(); p != nil {
				slog.Error("homekit: HAP server panicked", "panic", p)
			}
		}()
		if err := serve(runCtx, srv); err != nil && runCtx.Err() == nil {
			slog.Warn("homekit: HAP server stopped", "err", err)
			b.setErr(err.Error())
		}
	}()
	slog.Info("homekit: bridge running", "name", name, "cameras", len(specs), "port", b.port)
	return nil
}

// stopRunning shuts the HAP server down and ends every live stream.
func (b *Bridge) stopRunning() {
	b.mu.Lock()
	r := b.cur
	b.cur = nil
	b.mu.Unlock()
	if r == nil {
		return
	}
	r.cancel()
	for _, c := range r.cams {
		c.Close()
	}
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		slog.Warn("homekit: HAP server did not stop in time")
	}
}

// bumpConfigNumber raises the HAP configuration number (the c# in the mDNS
// record) when the accessory set changed. The library only does that when
// the accessory structure changes, not for a rename, and controllers
// re-read the accessory database only when the number moves.
func (b *Bridge) bumpConfigNumber(key string) {
	const stateKey = "sentinel.config_key"
	prev, err := b.hs.Get(stateKey)
	if err == nil && string(prev) == key {
		return
	}
	_ = b.hs.Set(stateKey, []byte(key))
	if err != nil {
		return // first run: the library starts at 1
	}
	n := 1
	if v, err := b.hs.Get("version"); err == nil {
		if x, err := strconv.Atoi(string(v)); err == nil {
			n = x
		}
	}
	n++
	if n > 65535 {
		n = 1
	}
	_ = b.hs.Set("version", []byte(strconv.Itoa(n)))
}

// setupCodes returns the setup code and setup id, creating them on first use.
func (b *Bridge) setupCodes() (pin, setupID string, err error) {
	p, perr := b.hs.Get(keySetupPin)
	s, serr := b.hs.Get(keySetupID)
	if perr == nil && serr == nil && len(p) == 8 && len(s) == 4 {
		return string(p), string(s), nil
	}
	if pin, err = newSetupPin(); err != nil {
		return "", "", err
	}
	if setupID, err = newSetupID(); err != nil {
		return "", "", err
	}
	if err = b.hs.Set(keySetupPin, []byte(pin)); err != nil {
		return "", "", err
	}
	if err = b.hs.Set(keySetupID, []byte(setupID)); err != nil {
		return "", "", err
	}
	return pin, setupID, nil
}

// resourceHandler answers the controller's POST /resource snapshot requests.
func (b *Bridge) resourceHandler(srv *hap.Server, r *running) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !srv.IsAuthorized(req) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var q struct {
			Type   string `json:"resource-type"`
			Width  int    `json:"image-width"`
			Height int    `json:"image-height"`
			AID    uint64 `json:"aid"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<16)).Decode(&q); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if q.Type != "image" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		camID, ok := r.byAID[q.AID]
		if !ok && q.AID == 0 && len(r.cams) == 1 {
			for id := range r.cams {
				camID, ok = id, true
			}
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		img := b.snaps.Get(req.Context(), camID, q.Width, q.Height)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = hap.NewChunkedWriter(w, 2048).Write(img)
	}
}

// onActivity handles TopicCameraActivity: any kind of detection counts.
func (b *Bridge) onActivity(camID string) {
	b.mu.Lock()
	active := b.cur != nil
	b.mu.Unlock()
	if active {
		b.motion.Activity(camID)
	}
}

// setMotion is the motion tracker's output: it flips the camera's sensor.
func (b *Bridge) setMotion(camID string, on bool) {
	b.mu.Lock()
	var c *camAccessory
	if b.cur != nil {
		c = b.cur.cams[camID]
	}
	b.mu.Unlock()
	if c != nil {
		c.SetMotion(on)
	}
}

// onOnline handles TopicCameraOnline.
func (b *Bridge) onOnline(camID string, online bool) {
	b.mu.Lock()
	b.online[camID] = online
	var c *camAccessory
	if b.cur != nil {
		c = b.cur.cams[camID]
	}
	b.mu.Unlock()
	if c != nil {
		c.SetReachable(online)
	}
}

// Status reports the bridge for the API and the settings card.
func (b *Bridge) Status() Status {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var st Status
	on := b.settingOn(ctx)
	st.Enabled = on && b.d.FFmpeg != ""

	b.mu.Lock()
	st.Err = b.lastErr
	if b.cur != nil {
		st.Cameras = len(b.cur.cams)
	}
	b.mu.Unlock()
	if on && b.d.FFmpeg == "" && st.Err == "" {
		st.Err = "ffmpeg not found; HomeKit needs it for snapshots and live video"
	}
	if st.Cameras == 0 && st.Enabled {
		if specs, err := b.exposedCount(ctx); err == nil {
			st.Cameras = specs
		}
	}

	pairs, _ := b.hs.KeysWithSuffix(".pairing")
	st.Paired = len(pairs) > 0
	if st.Enabled && !st.Paired {
		if pin, id, err := b.setupCodes(); err == nil {
			st.SetupCode = formatPin(pin)
			st.SetupURI = setupURI(pin, id)
		}
	}
	return st
}

// exposedCount is how many cameras would be published, without allocating ids.
func (b *Bridge) exposedCount(ctx context.Context) (int, error) {
	var n int
	err := b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cameras WHERE homekit = 1 AND recorder_id = 'local'`).Scan(&n)
	return n, err
}

// ResetPairings drops every controller pairing and the long-term keypair
// and generates a fresh setup code. The device id goes too, so Home sees a
// new accessory rather than a known one with the wrong keys. If the bridge
// is enabled it restarts straight away.
func (b *Bridge) ResetPairings(ctx context.Context) error {
	b.opMu.Lock()
	defer b.opMu.Unlock()
	b.stopRunning()

	pairs, err := b.hs.KeysWithSuffix(".pairing")
	if err != nil {
		return err
	}
	for _, k := range pairs {
		if err := b.hs.Delete(k); err != nil {
			return err
		}
	}
	for _, k := range []string{"keypair", "uuid", keySetupPin, keySetupID} {
		if err := b.hs.Delete(k); err != nil {
			return err
		}
	}
	if _, _, err := b.setupCodes(); err != nil {
		return err
	}
	slog.Info("homekit: pairings reset")

	b.mu.Lock()
	base := b.baseCtx
	b.mu.Unlock()
	if base != nil {
		b.reconcileLocked(base)
	}
	return nil
}

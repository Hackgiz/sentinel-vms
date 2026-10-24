// Package recorder is Sentinel's recording side (PLATFORM.md, "Components
// and ownership"): MediaMTX, recordings and their playback, clip export and
// evidence files, live video (HLS, WebRTC), motion and person/vehicle
// detection, camera analytics subscriptions, event snapshots, hardware
// acceleration and storage health.
//
// It is configured only through ApplyConfig(link.Snapshot) and reports only
// through a link.Sink; it implements link.Recorder. It never imports the
// management side (internal/server, internal/store).
package recorder

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"sentinel-linux/internal/audiodetect"
	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/videotoken"
)

// LocalServerID names the recorder that runs inside an all-in-one install.
const LocalServerID = "local"

type Recorder struct {
	DataDir  string
	Media    *media.Supervisor
	Detect   *detect.Manager      // nil/unavailable = no detection (ffmpeg missing)
	Audio    *audiodetect.Manager // audio events (audio.go); nil = off
	Version  string
	ServerID string
	// Ctx is the recorder's lifetime (subscriptions, readers, background
	// snapshots). Defaults to context.Background().
	Ctx context.Context
	// Log returns the last n lines of this server's log, secrets stripped
	// (link.CmdLog); nil = none kept.
	Log func(n int) string
	// DescribeStream checks guessed second-stream addresses
	// (link.CmdStreamsDiscover); nil = onvif.DescribeVideo. Tests fake it.
	DescribeStream onvif.DescribeFunc

	store *Store

	sinkMu sync.RWMutex
	sink   link.Sink

	cfgMu     sync.RWMutex
	cfg       link.Snapshot
	cams      map[string]link.CameraConfig
	accelMode string // applied hardware acceleration mode
	cfgDigest string // snapshotDigest of cfg
	tokens    *videotoken.Verifier

	deliverMu sync.Mutex // one delivery at a time: keeps outbox order, no double sends
	scanMu    sync.Mutex // one network scan at a time (link.CmdDiscover)
	bg        sync.WaitGroup

	detect    detectState
	camEvents cameraEventsState
	playback  playbackState
	longExp   longExportState // exportlong.go (WAVE7(C))
	health    healthState     // health.go
	edge      edgeState       // edgefill.go (WAVE9(C))
	rec       recordState     // recordctl.go, retention.go (WAVE22)
	push      pushState       // push.go: phone cameras (WAVE23)
	video     http.Handler
	videoOnce sync.Once

	// rootDir overrides where recordings go; "" = <DataDir>/recordings (storagedir.go).
	rootMu  sync.RWMutex
	rootDir string
}

// New creates a recorder over a data directory, a MediaMTX supervisor and
// its store. accelMode is the hardware acceleration mode the detector was
// set up with at startup ("auto" or "off").
func New(dataDir string, sup *media.Supervisor, st *Store, version, accelMode string) *Recorder {
	if accelMode == "" {
		accelMode = detect.AccelAuto
	}
	return &Recorder{DataDir: dataDir, Media: sup, store: st, Version: version, ServerID: LocalServerID, accelMode: accelMode}
}

// Attach sets where events and status go. Queued events are delivered on
// the next Run tick (or right away by Replay).
func (r *Recorder) Attach(sink link.Sink) {
	r.sinkMu.Lock()
	r.sink = sink
	r.sinkMu.Unlock()
}

func (r *Recorder) currentSink() link.Sink {
	r.sinkMu.RLock()
	defer r.sinkMu.RUnlock()
	return r.sink
}

func (r *Recorder) ctx() context.Context {
	if r.Ctx != nil {
		return r.Ctx
	}
	return context.Background()
}

func (r *Recorder) eventsDir() string   { return filepath.Join(r.DataDir, "events") }
func (r *Recorder) evidenceDir() string { return filepath.Join(r.DataDir, "evidence") }
func (r *Recorder) exportsDir() string  { return filepath.Join(r.DataDir, "exports") }

// RecordingsRoot is where camera recordings live (<root>/<camera id>/…).
func (r *Recorder) RecordingsRoot() string { return r.recordingsRoot() }

// EvidenceDir is the evidence vault.
func (r *Recorder) EvidenceDir() string { return r.evidenceDir() }

// ExportsDir holds prepared clip downloads and export scratch space.
func (r *Recorder) ExportsDir() string { return r.exportsDir() }

// FFmpegPath is the ffmpeg used for snapshots, exports and detection; "" =
// none installed.
func (r *Recorder) FFmpegPath() string {
	if r.Detect != nil && r.Detect.FFmpeg != "" {
		return r.Detect.FFmpeg
	}
	return detect.Find(r.DataDir)
}

func (r *Recorder) camera(id string) (link.CameraConfig, bool) {
	r.cfgMu.RLock()
	defer r.cfgMu.RUnlock()
	c, ok := r.cams[id]
	return c, ok
}

func (r *Recorder) config() link.Snapshot {
	r.cfgMu.RLock()
	defer r.cfgMu.RUnlock()
	return r.cfg
}

// MARK: - link.Recorder

// ApplyConfig turns the snapshot into the MediaMTX config, the detection
// readers and the camera analytics subscriptions. Called at startup and
// after any camera, retention, live-view, acceleration or AI change.
func (r *Recorder) ApplyConfig(ctx context.Context, s link.Snapshot) (link.Applied, error) {
	applied := link.Applied{Rev: s.Rev}
	cams := map[string]link.CameraConfig{}
	for _, c := range s.Cameras {
		cams[c.ID] = c
	}
	tokens := r.tokenVerifier(s.VideoTokenKeys)
	digest := snapshotDigest(s)
	r.cfgMu.Lock()
	changed := digest != r.cfgDigest
	r.cfg, r.cams, r.tokens, r.cfgDigest = s, cams, tokens, digest
	accelChanged := s.HWAccel != "" && s.HWAccel != r.accelMode
	if accelChanged {
		r.accelMode = s.HWAccel
	}
	r.cfgMu.Unlock()

	r.rec.mu.Lock()
	r.rec.configured = true // the retention sweeper may run (retention.go)
	r.rec.mu.Unlock()

	r.applyRecordingsDir(s.RecordingsDir, &applied) // storagedir.go
	if r.Media != nil {
		r.Media.SetWebRTCExtraHosts(s.WebRTCExtraHosts)
		sources := make([]media.Source, 0, len(s.Cameras))
		for _, c := range s.Cameras {
			if c.IsPhone() { // WAVE23: the push ingest publishes it (push.go)
				mode := r.effectiveRecording(c).Mode
				sources = append(sources, media.Source{ID: c.ID, Publisher: true, Mode: mode, Record: mode != link.RecordOff})
				continue
			}
			main, err := media.WithCredentials(c.RTSPURL, c.Username, c.Password)
			if err != nil {
				slog.Error("bad camera URL; skipping", "camera", c.Name, "err", err)
				if applied.Errors == nil {
					applied.Errors = map[string]string{}
				}
				applied.Errors[c.ID] = "bad camera URL: " + err.Error()
				continue
			}
			// The effective mode (recordctl.go): "default" resolved, dual
			// without a usable sub-stream falls back to events.
			mode := r.effectiveRecording(c).Mode
			src := media.Source{ID: c.ID, URL: main, Mode: mode, Record: mode != link.RecordOff}
			if c.SubRTSPURL != "" {
				if sub, err := media.WithCredentials(c.SubRTSPURL, c.Username, c.Password); err == nil {
					src.SubURL = sub
				}
			}
			sources = append(sources, src)
		}
		if err := r.Media.Apply(sources); err != nil {
			return applied, err
		}
	}
	if accelChanged && r.Detect.Available() {
		r.Detect.SetAccelMode(s.HWAccel)
	}
	if r.Detect.Available() && r.Ctx != nil {
		r.Detect.Sync(r.Ctx, r.detectionSources(s.Cameras)) // after Apply: LivePath depends on it
	}
	if r.Audio.Available() && r.Ctx != nil {
		r.Audio.Sync(r.Ctx, r.audioSources(s.Cameras)) // audio.go
	}
	r.syncPush(s.Cameras) // push.go (WAVE23)
	r.syncCameraAnalytics(s.Cameras)
	r.syncEdgeProbes(s.Cameras) // WAVE9(C): edgefill.go

	r.linkImportedRecordings(s.Cameras, &applied) // import_mac.go
	if changed {
		r.auditApplied(s, applied)
	}
	return applied, nil
}

// Status is the recorder's current state.
func (r *Recorder) Status() link.ServerStatus {
	st := link.ServerStatus{
		ServerID: r.ServerID, Version: r.Version, Time: time.Now().UnixMilli(),
		Cameras: map[string]link.CameraStatus{},
	}
	r.cfgMu.RLock()
	st.ConfigRev = r.cfg.Rev
	ids := make([]string, 0, len(r.cams))
	for id := range r.cams {
		ids = append(ids, id)
	}
	r.cfgMu.RUnlock()
	sort.Strings(ids)
	st.Capabilities.Detection = r.Detect.Available()
	st.Capabilities.Objects = r.Detect != nil && r.Detect.Objects != nil
	if r.Media != nil {
		st.Media.Running, st.Media.Error = r.Media.Status()
		st.Capabilities.WebRTC = link.WebRTCStatus{Enabled: r.Media.WebRTCEnabled(), ICEPort: r.Media.WebRTC.ICEPort, ConfigHosts: r.Media.WebRTC.Hosts}
		for _, id := range ids {
			ms := r.Media.State(id)
			st.Cameras[id] = link.CameraStatus{Ready: ms.Ready, Readers: ms.Readers, Width: ms.Width, Height: ms.Height, Analytics: r.AnalyticsStatus(id), Audio: r.audioStatus(id), EdgeRecording: r.edgeRecordingStatus(id)}
		}
	} else {
		for _, id := range ids {
			st.Cameras[id] = link.CameraStatus{Analytics: r.AnalyticsStatus(id), Audio: r.audioStatus(id), EdgeRecording: r.edgeRecordingStatus(id)}
		}
	}
	for _, id := range ids { // WAVE23: phone cameras' push state (push.go)
		if c, ok := r.camera(id); ok && c.IsPhone() {
			cs := st.Cameras[id]
			cs.Push = r.pushStatus(id)
			st.Cameras[id] = cs
		}
	}
	return st
}

// Video is the recorder's video plane (see link.VideoPaths).
func (r *Recorder) Video() http.Handler {
	r.videoOnce.Do(func() { r.video = r.videoHandler() })
	return r.video
}

// MARK: - Background work

const (
	statusEvery     = 15 * time.Second
	eventsRetention = 30 * 24 * time.Hour
	outboxRetry     = 30 * time.Second
	outboxMaxAge    = 7 * 24 * time.Hour
	outboxMaxRows   = 100000
)

// Run reports status every 15 s (the management side raises Camera Offline
// alarms from it), prunes events older than 30 days, watches free disk
// space and retries undelivered events. Blocks until ctx ends.
func (r *Recorder) Run(ctx context.Context) {
	defer r.endAllPush() // push.go: ffmpeg ingests end with the recorder
	go r.runStorageMonitor(ctx)
	go r.runKeeper(ctx)                         // recordctl.go: events/dual buffer → kept footage
	go r.runSweeper(ctx)                        // retention.go: retention and minFreePercent
	r.startPlaybackWarm(ctx, playbackWarmDelay) // playback.go: index the last day before anyone asks
	status := time.NewTicker(statusEvery)
	defer status.Stop()
	retry := time.NewTicker(outboxRetry)
	defer retry.Stop()
	r.Replay(ctx)
	lastPrune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-retry.C:
			r.Replay(ctx)
		case <-status.C:
			if sink := r.currentSink(); sink != nil {
				sink.Status(r.Status())
			}
			if time.Since(lastPrune) > time.Hour {
				lastPrune = time.Now()
				r.pruneEvents(ctx)
			}
		}
	}
}

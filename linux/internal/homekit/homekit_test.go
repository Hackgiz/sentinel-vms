package homekit

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brutella/hap"

	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/server"
	"sentinel-linux/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addCamera(t *testing.T, s *store.Store, name string) string {
	t.Helper()
	c, err := s.SaveCamera(context.Background(), "", store.CameraInput{Name: name, RTSPURL: "rtsp://10.0.0.9/" + name})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// fakeServe stands in for the HAP server: it blocks until its context ends
// and counts how many servers ran.
type fakeServe struct {
	started, stopped atomic.Int32
}

func (f *fakeServe) install(t *testing.T) {
	t.Helper()
	old := serveHAP
	serveHAP = func(ctx context.Context, srv *hap.Server) error {
		f.started.Add(1)
		<-ctx.Done()
		f.stopped.Add(1)
		return nil
	}
	t.Cleanup(func() { serveHAP = old })
}

func newTestBridge(t *testing.T) (*Bridge, *store.Store) {
	t.Helper()
	st := openStore(t)
	b, err := New(Deps{Store: st, Bus: bus.New(), FFmpeg: "/usr/bin/ffmpeg", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	b.port = 0
	t.Cleanup(func() {
		b.opMu.Lock()
		b.stopRunning()
		b.opMu.Unlock()
	})
	return b, st
}

func setSetting(t *testing.T, st *store.Store, k, v string) {
	t.Helper()
	if err := st.SetSetting(context.Background(), k, v); err != nil {
		t.Fatal(err)
	}
}

func TestSQLStoreAdapter(t *testing.T) {
	st := openStore(t)
	hs := newSQLStore(st.DB)

	if _, err := hs.Get("keypair"); err == nil {
		t.Fatal("missing key must be an error")
	}
	if err := hs.Set("keypair", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := hs.Set("keypair", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if v, err := hs.Get("keypair"); err != nil || string(v) != "two" {
		t.Fatalf("get = %q, %v", v, err)
	}
	if err := hs.Set("empty", nil); err != nil {
		t.Fatalf("nil value: %v", err)
	}

	for _, k := range []string{"a.pairing", "b.pairing", "c%.pairing", "uuid", "pairing"} {
		hs.Set(k, []byte("x"))
	}
	tests := []struct {
		suffix string
		want   []string
	}{
		{".pairing", []string{"a.pairing", "b.pairing", "c%.pairing"}},
		{"%.pairing", []string{"c%.pairing"}},
		{"_", nil},
		{"uuid", []string{"uuid"}},
	}
	for _, tt := range tests {
		got, err := hs.KeysWithSuffix(tt.suffix)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("KeysWithSuffix(%q) = %v, want %v", tt.suffix, got, tt.want)
		}
	}

	if err := hs.Delete("a.pairing"); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.Get("a.pairing"); err == nil {
		t.Fatal("deleted key still readable")
	}
	if err := hs.Delete("never-existed"); err != nil {
		t.Fatalf("deleting a missing key must not fail: %v", err)
	}
}

func TestSQLStoreSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	a, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	newSQLStore(a.DB).Set("keypair", []byte("secret"))
	a.Close()
	b, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if v, err := newSQLStore(b.DB).Get("keypair"); err != nil || string(v) != "secret" {
		t.Fatalf("after reopen = %q, %v", v, err)
	}
}

func TestAIDRegistry(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	reg := newAIDRegistry(st.DB)

	c1, c2, c3 := addCamera(t, st, "Front"), addCamera(t, st, "Back"), addCamera(t, st, "Garage")
	a1, _ := reg.Assign(ctx, c1)
	a2, _ := reg.Assign(ctx, c2)
	if a1 != 2 || a2 != 3 {
		t.Fatalf("first aids = %d, %d; cameras start at 2", a1, a2)
	}
	if again, _ := reg.Assign(ctx, c1); again != a1 {
		t.Fatalf("aid changed on re-assign: %d -> %d", a1, again)
	}

	// Deleting the highest-numbered camera must not free its number.
	if err := st.DeleteCamera(ctx, c2); err != nil {
		t.Fatal(err)
	}
	a3, err := reg.Assign(ctx, c3)
	if err != nil {
		t.Fatal(err)
	}
	if a3 != 4 {
		t.Fatalf("aid after delete = %d, want 4 (3 is retired)", a3)
	}
	// And a new camera reusing the deleted one's id from the registry's point
	// of view still gets a fresh number.
	c4 := addCamera(t, st, "Shed")
	if a4, _ := reg.Assign(ctx, c4); a4 != 5 {
		t.Fatalf("a4 = %d, want 5", a4)
	}
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM homekit_aids WHERE camera_id = ?`, c2).Scan(&n)
	if n != 1 {
		t.Fatal("a deleted camera's aid row must outlive it")
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestMotionClearsAfterThirtySeconds(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	var mu sync.Mutex
	var events []string
	m := newMotionTracker(clk.Now, func(id string, on bool) {
		mu.Lock()
		events = append(events, id+map[bool]string{true: "+", false: "-"}[on])
		mu.Unlock()
	})
	got := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(events, " ")
	}

	m.Activity("cam")
	m.Activity("cam") // burst: still one "on"
	if got() != "cam+" || !m.Active("cam") {
		t.Fatalf("events = %q", got())
	}
	clk.Advance(29 * time.Second)
	m.Sweep()
	if !m.Active("cam") {
		t.Fatal("cleared early")
	}
	m.Activity("cam") // more activity extends the hold
	clk.Advance(29 * time.Second)
	m.Sweep()
	if !m.Active("cam") {
		t.Fatal("activity must restart the 30 s hold")
	}
	clk.Advance(time.Second)
	m.Sweep()
	if m.Active("cam") || got() != "cam+ cam-" {
		t.Fatalf("not cleared at 30 s: events = %q", got())
	}
	m.Sweep()
	if got() != "cam+ cam-" {
		t.Fatalf("clear must fire once: %q", got())
	}
	m.Activity("cam")
	if got() != "cam+ cam- cam+" {
		t.Fatalf("re-trigger after clear: %q", got())
	}
}

func TestSnapshotCache(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	var calls atomic.Int32
	var fail atomic.Bool
	c := newSnapshotCache(clk.Now, func(ctx context.Context, cam string, w, h int) ([]byte, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("ffmpeg timed out")
		}
		return []byte{0xff, 0xd8, byte(calls.Load())}, nil
	})
	ctx := context.Background()

	first := c.Get(ctx, "cam", 640, 360)
	if again := c.Get(ctx, "cam", 640, 360); !bytes.Equal(first, again) || calls.Load() != 1 {
		t.Fatalf("a frame under 3 s old must be served from cache (calls=%d)", calls.Load())
	}
	c.Get(ctx, "cam", 1920, 1080)
	if calls.Load() != 2 {
		t.Fatalf("a different size is its own capture (calls=%d)", calls.Load())
	}
	c.Get(ctx, "other", 640, 360)
	if calls.Load() != 3 {
		t.Fatalf("cameras are cached separately (calls=%d)", calls.Load())
	}

	clk.Advance(3 * time.Second)
	if fresh := c.Get(ctx, "cam", 640, 360); bytes.Equal(fresh, first) || calls.Load() != 4 {
		t.Fatalf("expired frame must be recaptured (calls=%d)", calls.Load())
	}

	// Failure: the last good frame is served while it is under a minute old.
	good := c.Get(ctx, "cam", 640, 360)
	fail.Store(true)
	clk.Advance(10 * time.Second)
	if got := c.Get(ctx, "cam", 640, 360); !bytes.Equal(got, good) {
		t.Fatal("failed capture must fall back to the cached frame")
	}
	clk.Advance(51 * time.Second) // 61 s since the last good frame
	got := c.Get(ctx, "cam", 640, 360)
	img, err := jpeg.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("placeholder is not a JPEG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 640 || b.Dy() != 360 {
		t.Fatalf("placeholder is %v, want 640x360", b)
	}
}

func TestSnapshotSingleFlight(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	c := newSnapshotCache(nil, func(ctx context.Context, cam string, w, h int) ([]byte, error) {
		calls.Add(1)
		<-release
		return []byte("jpeg"), nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 9; i++ { // HomeView shows nine feeds
		wg.Add(1)
		go func() { defer wg.Done(); c.Get(context.Background(), "cam", 640, 360) }()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("%d concurrent requests ran %d captures, want 1", 9, calls.Load())
	}
}

func TestStubSeamServesPlaceholder(t *testing.T) {
	b, _ := newTestBridge(t)
	got := b.snaps.Get(context.Background(), "cam", 320, 180)
	if _, err := jpeg.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("stub capture must yield the placeholder JPEG: %v", err)
	}
	if _, err := startStream(context.Background(), &b.d, "cam", StreamParams{}); !errors.Is(err, ErrStreamingUnavailable) {
		t.Fatalf("stub startStream = %v", err)
	}
}

func TestSetupCodeAndURI(t *testing.T) {
	for i := 0; i < 200; i++ {
		pin, err := newSetupPin()
		if err != nil || len(pin) != 8 || hap.InvalidPins[pin] {
			t.Fatalf("bad pin %q (%v)", pin, err)
		}
	}
	if got := formatPin("03145154"); got != "031-45-154" {
		t.Fatalf("formatPin = %q", got)
	}
	uri := setupURI("03145154", "abcd")
	if !strings.HasPrefix(uri, "X-HM://") || len(uri) != len("X-HM://")+9+4 || !strings.HasSuffix(uri, "ABCD") {
		t.Fatalf("uri = %q", uri)
	}
	// Decode the payload back: category, IP flag, setup code.
	var n uint64
	for _, r := range uri[len("X-HM://") : len("X-HM://")+9] {
		d := strings.IndexRune("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ", r)
		if d < 0 {
			t.Fatalf("non-base36 digit in %q", uri)
		}
		n = n*36 + uint64(d)
	}
	if n&(1<<27-1) != 3145154 || (n>>27)&0xf != 2 || (n>>31)&0xff != categoryBridge || n>>39 != 0 {
		t.Fatalf("payload %#x does not decode to pin/ip/bridge", n)
	}
}

func goroutinesSettle(base int) int {
	n := runtime.NumGoroutine()
	for i := 0; i < 50 && n > base; i++ {
		time.Sleep(20 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

func TestEnableDisableIdles(t *testing.T) {
	var f fakeServe
	f.install(t)
	b, st := newTestBridge(t)
	ctx := context.Background()
	addCamera(t, st, "Front")
	addCamera(t, st, "Back")

	b.reconcile(ctx)
	if f.started.Load() != 0 || b.Status().Enabled || b.Status().SetupCode != "" {
		t.Fatal("bridge must idle while homekit_enabled is 0")
	}

	base := runtime.NumGoroutine()
	for cycle := 0; cycle < 3; cycle++ {
		setSetting(t, st, "homekit_enabled", "1")
		b.reconcile(ctx)
		s := b.Status()
		if !s.Enabled || s.Cameras != 2 || s.Paired || s.Err != "" {
			t.Fatalf("enabled status = %+v", s)
		}
		if !strings.Contains(s.SetupURI, "X-HM://") || len(s.SetupCode) != 10 {
			t.Fatalf("pairing details missing: %+v", s)
		}
		if int(f.started.Load()) != cycle+1 {
			t.Fatalf("cycle %d: %d servers started", cycle, f.started.Load())
		}
		b.reconcile(ctx) // unchanged: no restart
		if int(f.started.Load()) != cycle+1 {
			t.Fatal("reconcile restarted an unchanged bridge")
		}

		setSetting(t, st, "homekit_enabled", "0")
		b.reconcile(ctx)
		if f.stopped.Load() != f.started.Load() {
			t.Fatalf("disable left a server running (%d/%d)", f.stopped.Load(), f.started.Load())
		}
		if s := b.Status(); s.Enabled || s.SetupCode != "" {
			t.Fatalf("disabled status = %+v", s)
		}
	}
	if n := goroutinesSettle(base); n > base {
		t.Fatalf("goroutines grew from %d to %d over enable/disable cycles", base, n)
	}
}

func TestEnableWithoutFFmpegRefuses(t *testing.T) {
	var f fakeServe
	f.install(t)
	st := openStore(t)
	b, _ := New(Deps{Store: st})
	setSetting(t, st, "homekit_enabled", "1")
	b.reconcile(context.Background())
	s := b.Status()
	if s.Enabled || s.Err == "" || f.started.Load() != 0 {
		t.Fatalf("status = %+v started=%d", s, f.started.Load())
	}
}

func TestPerCameraSwitchAndRemoteCameras(t *testing.T) {
	var f fakeServe
	f.install(t)
	b, st := newTestBridge(t)
	ctx := context.Background()
	a := addCamera(t, st, "Front")
	addCamera(t, st, "Back")
	setSetting(t, st, "homekit_enabled", "1")
	b.reconcile(ctx)
	if b.Status().Cameras != 2 {
		t.Fatalf("cameras = %d", b.Status().Cameras)
	}
	st.DB.Exec(`UPDATE cameras SET homekit = 0 WHERE id = ?`, a)
	b.reconcile(ctx)
	if b.Status().Cameras != 1 || f.started.Load() != 2 {
		t.Fatalf("flag flip: cameras=%d started=%d", b.Status().Cameras, f.started.Load())
	}
	st.DB.Exec(`UPDATE cameras SET recorder_id = 'rec-2' WHERE homekit = 1`)
	b.reconcile(ctx)
	if b.Status().Cameras != 0 {
		t.Fatalf("a camera on a remote recorder must not be exposed: %d", b.Status().Cameras)
	}
}

func TestRunReactsToBusAndSettings(t *testing.T) {
	var f fakeServe
	f.install(t)
	b, st := newTestBridge(t)
	b.pollEvery = 20 * time.Millisecond
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	b.motion.now = clk.Now // before Run: the tracker reads it from other goroutines
	id := addCamera(t, st, "Front")
	setSetting(t, st, "homekit_enabled", "1")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	waitFor(t, "bridge to start", func() bool { return f.started.Load() == 1 })

	// Motion drives the sensor and the tracker's clock clears it.
	bus.Publish(b.d.Bus, server.TopicCameraActivity, server.CameraActivity{CameraID: id, Kind: "Person"})
	waitFor(t, "motion on", func() bool { return b.motion.Active(id) })
	b.mu.Lock()
	cam := b.cur.cams[id]
	b.mu.Unlock()
	waitFor(t, "sensor on", func() bool { return cam.motion.MotionDetected.Value() })
	clk.Advance(30 * time.Second)
	b.motion.Sweep()
	if cam.motion.MotionDetected.Value() {
		t.Fatal("sensor still on after 30 s")
	}

	// Reachability.
	bus.Publish(b.d.Bus, server.TopicCameraOnline, server.CameraOnline{CameraID: id, Online: false})
	waitFor(t, "unreachable", func() bool { return !cam.active.Value() })

	// A rename arrives as ConfigChanged and rebuilds the accessory set.
	st.SaveCamera(context.Background(), id, store.CameraInput{Name: "Driveway", RTSPURL: "rtsp://10.0.0.9/x"})
	bus.Publish(b.d.Bus, server.TopicConfigChanged, server.ConfigChanged{})
	waitFor(t, "rebuild", func() bool { return f.started.Load() == 2 })
	b.mu.Lock()
	renamed := b.cur.cams[id]
	b.mu.Unlock()
	if renamed.a.Name() != "Driveway" || renamed.active.Value() {
		t.Fatalf("rebuilt accessory: name=%q reachable=%v (state must carry over)", renamed.a.Name(), renamed.active.Value())
	}

	// Disabling takes effect without a restart.
	setSetting(t, st, "homekit_enabled", "0")
	waitFor(t, "idle", func() bool { return f.stopped.Load() == 2 })

	cancel()
	<-done
}

func TestRenameBumpsConfigNumber(t *testing.T) {
	var f fakeServe
	f.install(t)
	b, st := newTestBridge(t)
	ctx := context.Background()
	id := addCamera(t, st, "Front")
	setSetting(t, st, "homekit_enabled", "1")
	b.reconcile(ctx)
	v1, _ := b.hs.Get("version")
	st.SaveCamera(ctx, id, store.CameraInput{Name: "Porch", RTSPURL: "rtsp://10.0.0.9/x"})
	b.reconcile(ctx)
	v2, _ := b.hs.Get("version")
	if string(v1) == "" || string(v1) == string(v2) {
		t.Fatalf("config number %q -> %q: a rename must bump it", v1, v2)
	}
	b.reconcile(ctx)
	if v3, _ := b.hs.Get("version"); string(v3) != string(v2) {
		t.Fatalf("an unchanged set bumped the number: %q -> %q", v2, v3)
	}
}

func TestResetPairings(t *testing.T) {
	var f fakeServe
	f.install(t)
	b, st := newTestBridge(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addCamera(t, st, "Front")
	setSetting(t, st, "homekit_enabled", "1")
	b.mu.Lock()
	b.baseCtx = ctx
	b.mu.Unlock()
	b.reconcile(ctx)

	before := b.Status()
	b.hs.Set("keypair", []byte("kp"))
	b.hs.Set("uuid", []byte("AA:BB"))
	b.hs.Set("ios-device.pairing", []byte("{}"))
	if !b.Status().Paired || b.Status().SetupCode != "" {
		t.Fatalf("paired status must hide the setup code: %+v", b.Status())
	}

	if err := b.ResetPairings(ctx); err != nil {
		t.Fatal(err)
	}
	after := b.Status()
	if after.Paired || after.SetupCode == "" || after.SetupCode == before.SetupCode || after.SetupURI == before.SetupURI {
		t.Fatalf("reset: before=%+v after=%+v", before, after)
	}
	for _, k := range []string{"keypair", "ios-device.pairing"} {
		if _, err := b.hs.Get(k); err == nil {
			t.Errorf("%s survived reset", k)
		}
	}
	if u, _ := b.hs.Get("uuid"); string(u) == "AA:BB" {
		t.Error("device id survived reset")
	}
	if f.started.Load() != 2 || f.stopped.Load() != 1 {
		t.Fatalf("reset must restart the bridge: started=%d stopped=%d", f.started.Load(), f.stopped.Load())
	}
	b.stopRunning()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

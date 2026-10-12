package remote

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/pki"
	"sentinel-linux/internal/recorder"
	"sentinel-linux/internal/server"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/videogw"
	"sentinel-linux/internal/videotoken"
)

// MARK: - Fixtures

// site is a management server with its control listener on 127.0.0.1.
type site struct {
	t    *testing.T
	dir  string
	st   *store.Store
	srv  *server.Server
	l    *Listener
	addr string

	stop func()
}

func newSite(t *testing.T) *site {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := server.New(st, nil, "test")
	srv.Ctx = context.Background()
	l, err := NewListener(srv, dir, "127.0.0.1:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.Control = l
	s := &site{t: t, dir: dir, st: st, srv: srv, l: l}
	s.start("127.0.0.1:0")
	t.Cleanup(func() { s.stop() })
	return s
}

// start runs the listener on addr; stop takes it down (connections too).
func (s *site) start(addr string) {
	s.t.Helper()
	var ln net.Listener
	var err error
	for i := 0; i < 50; i++ { // the port may linger briefly after a stop
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		s.t.Fatal(err)
	}
	s.addr = ln.Addr().String()
	s.l.Addr = s.addr
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.l.Serve(ctx, ln)
	}()
	s.stop = func() {
		cancel()
		<-done
	}
}

func (s *site) url() string { return "https://" + s.addr }

func (s *site) token(ttl time.Duration) string {
	s.t.Helper()
	secret, _, err := s.st.CreateEnrollmentToken(context.Background(), "Warehouse", "admin", ttl)
	if err != nil {
		s.t.Fatal(err)
	}
	return s.l.EnrollmentToken(secret)
}

// recSide is a recording server: a recorder (MediaMTX config only, not
// running), its video plane on an httptest server, and the link client.
type recSide struct {
	dir    string
	rec    *recorder.Recorder
	id     *Identity
	client *Client
	video  *httptest.Server
	cancel func()
	done   chan struct{}
}

func newRecorder(t *testing.T, dir string, serverID string) *recorder.Recorder {
	t.Helper()
	rst, err := recorder.OpenStore(filepath.Join(dir, "recorder.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rst.DB.Close() })
	sup := &media.Supervisor{ConfigPath: filepath.Join(dir, "mediamtx.yml"), RecordingsRoot: filepath.Join(dir, "recordings"), Ports: media.DefaultPorts}
	rec := recorder.New(dir, sup, rst, "test-rs", "")
	rec.Ctx = context.Background()
	rec.ServerID = serverID
	t.Cleanup(rec.Wait)
	return rec
}

func enroll(t *testing.T, s *site, dir string) *Identity {
	t.Helper()
	id, err := Enroll(context.Background(), dir, EnrollOptions{Management: s.url(), Token: s.token(time.Hour), Version: "test-rs"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// startRec loads the identity in dir and runs a recording server.
func startRec(t *testing.T, dir string) *recSide { return startRecWith(t, dir, nil) }

// startRecWith runs one with wrap around the recorder the client drives.
func startRecWith(t *testing.T, dir string, wrap func(LocalRecorder) LocalRecorder) *recSide {
	t.Helper()
	id, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := &recSide{dir: dir, id: id, rec: newRecorder(t, dir, id.ServerID)}
	mux := http.NewServeMux()
	mux.Handle(videogw.Prefix, VideoPlane(r.rec.Video(), r.rec.VideoTokens))
	r.video = httptest.NewServer(mux)
	t.Cleanup(r.video.Close)
	var lr LocalRecorder = r.rec
	if wrap != nil {
		lr = wrap(lr)
	}
	r.client = &Client{ID: id, Recorder: lr, Version: "test-rs", VideoURL: r.video.URL}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		r.client.Run(ctx)
	}()
	t.Cleanup(r.stop)
	return r
}

func (r *recSide) stop() {
	r.cancel()
	<-r.done
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func connected(s *site, id string) bool {
	for _, c := range s.srv.ConnectedRecorders() {
		if c == id {
			return true
		}
	}
	return false
}

// MARK: - Enrollment

func TestEnrollment(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	dir := t.TempDir()
	tok := s.token(time.Hour)
	id, err := Enroll(ctx, dir, EnrollOptions{Management: s.url(), Token: tok, Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	rs, err := s.st.RecordingServer(ctx, id.ServerID)
	if err != nil || rs.Name != "Warehouse" || rs.Version != "1.2.3" || rs.CertFingerprint != pki.Fingerprint(id.Cert().Raw) || rs.SealPublicKey == "" {
		t.Fatalf("registry %+v %v", rs, err)
	}
	for _, f := range []string{"identity.json", "client.key", "client.crt", "ca.crt", "seal.key"} {
		if _, err := os.Stat(filepath.Join(dir, "link", f)); err != nil {
			t.Error(err)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "link", "seal.key")); fi.Mode().Perm() != 0o600 {
		t.Errorf("seal.key mode %v", fi.Mode())
	}
	again, err := LoadIdentity(dir)
	if err != nil || again.ServerID != id.ServerID || again.seal.Public != id.seal.Public {
		t.Fatalf("reload %v", err)
	}
	if _, err := LoadIdentity(t.TempDir()); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("empty dir: %v", err)
	}

	// The token is single use.
	var he *HTTPError
	if _, err := Enroll(ctx, t.TempDir(), EnrollOptions{Management: s.url(), Token: tok}); !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
		t.Fatalf("reused token: %v", err)
	}
	// Expired.
	if _, err := Enroll(ctx, t.TempDir(), EnrollOptions{Management: s.url(), Token: s.token(-time.Minute)}); !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
		t.Fatalf("expired token: %v", err)
	}
	// A token pinning another CA is refused before it is sent, and isn't
	// burned.
	good := s.token(time.Hour)
	secret, _, _ := pki.ParseToken(good)
	other, _ := pki.NewCA()
	_, err = Enroll(ctx, t.TempDir(), EnrollOptions{Management: s.url(), Token: pki.FormatToken(secret, other.Pin())})
	if err == nil || !strings.Contains(err.Error(), "doesn't match the enrollment token") {
		t.Fatalf("pin mismatch: %v", err)
	}
	if _, err := Enroll(ctx, t.TempDir(), EnrollOptions{Management: s.url(), Token: good}); err != nil {
		t.Fatalf("token after a refused pin: %v", err)
	}
	if cmd := s.l.EnrollCommand(tok); !strings.HasPrefix(cmd, "sentinel --role recording --management https://") || !strings.HasSuffix(cmd, " --enroll "+tok) {
		t.Fatalf("command %q", cmd)
	}
}

// MARK: - Connect

// dialWith connects to /link/v1/connect with cert; it returns the HTTP
// status of a refusal (0 = connected).
func dialWith(t *testing.T, s *site, id *Identity, cert tls.Certificate) int {
	t.Helper()
	cfg := id.tlsConfig()
	cfg.Certificates = []tls.Certificate{cert}
	d := websocket.Dialer{TLSClientConfig: cfg, HandshakeTimeout: 5 * time.Second}
	ws, resp, err := d.Dial("wss://"+s.addr+"/link/v1/connect", nil)
	if err == nil {
		ws.Close()
		return 0
	}
	if resp == nil {
		return -1 // refused during the TLS handshake
	}
	return resp.StatusCode
}

func TestConnectRefusesUnknownAndRevoked(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	dir := t.TempDir()
	id := enroll(t, s, dir)

	// Signed by the CA but never enrolled.
	key, _ := pki.NewKey()
	csr, _ := pki.CreateCSR(key, "x")
	stray, err := s.l.CA.SignCSR(csr, "stray", pki.CertLifetime)
	if err != nil {
		t.Fatal(err)
	}
	if code := dialWith(t, s, id, tls.Certificate{Certificate: [][]byte{stray.Raw}, PrivateKey: key}); code != http.StatusForbidden {
		t.Fatalf("unknown cert: %d", code)
	}
	// From another CA.
	other, _ := pki.NewCA()
	foreign, _ := other.SignCSR(csr, id.ServerID, pki.CertLifetime)
	if code := dialWith(t, s, id, tls.Certificate{Certificate: [][]byte{foreign.Raw}, PrivateKey: key}); code == 0 {
		t.Fatal("foreign cert connected")
	}
	// No certificate.
	if code := dialWith(t, s, id, tls.Certificate{}); code != http.StatusUnauthorized && code != -1 {
		t.Fatalf("no cert: %d", code)
	}

	// Connected, then revoked: the connection drops and stays refused.
	r := startRec(t, dir)
	eventually(t, "connect", func() bool { return connected(s, id.ServerID) })
	if err := s.st.RevokeRecordingServer(ctx, id.ServerID); err != nil {
		t.Fatal(err)
	}
	s.l.Disconnect(id.ServerID)
	eventually(t, "disconnect", func() bool { return !connected(s, id.ServerID) })
	eventually(t, "refusal", func() bool {
		_, err := r.client.Connected()
		return err != nil && strings.Contains(err.Error(), "revoked")
	})
	if connected(s, id.ServerID) {
		t.Fatal("revoked server reconnected")
	}
}

// MARK: - Full loop

const camPass = "p@ss w0rd"

func TestFullLoop(t *testing.T) {
	s := newSite(t)
	ctx := context.Background()
	dir := t.TempDir()
	id := enroll(t, s, dir)

	pass := camPass
	cam, err := s.st.SaveCamera(ctx, "", store.CameraInput{RecorderID: id.ServerID, Name: "Gate", RTSPURL: "rtsp://10.0.0.9/s", Username: "admin", Password: &pass, Recording: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetCameraRecorder(ctx, cam.ID, id.ServerID); err != nil {
		t.Fatal(err)
	}
	r := startRec(t, dir)
	eventually(t, "connect", func() bool { return connected(s, id.ServerID) })

	// The snapshot arrives with the password sealed, is kept sealed on
	// disk and applied unsealed.
	eventually(t, "snapshot applied", func() bool {
		b, _ := os.ReadFile(r.rec.Media.ConfigPath)
		return strings.Contains(string(b), cam.ID) && strings.Contains(string(b), "p%40ss%20w0rd")
	})
	saved, err := os.ReadFile(filepath.Join(dir, "link", "config.json"))
	if err != nil || strings.Contains(string(saved), camPass) || !strings.Contains(string(saved), `"sealedPassword"`) {
		t.Fatalf("saved snapshot: %v %s", err, saved)
	}
	conn := s.l.Recorder(id.ServerID)
	if conn == nil || conn.Hello().VideoURL != r.video.URL {
		t.Fatalf("conn %+v", conn)
	}

	// Status reaches the management side.
	eventually(t, "status", func() bool { return conn.Status().ConfigRev != 0 })
	if st := conn.Status(); st.ServerID != id.ServerID || len(st.Cameras) != 1 {
		t.Fatalf("status %+v", st)
	}

	// Commands.
	c, _ := link.NewCommand(link.CmdStorageInfo, nil)
	res, err := conn.Command(ctx, c)
	var info link.StorageInfo
	if err != nil || res.Decode(&info) != nil || info.RecordingsDir != r.rec.RecordingsRoot() {
		t.Fatalf("storage.info %+v %v %+v", res, err, info)
	}
	c, _ = link.NewCommand("no.such", nil)
	if res, err := conn.Command(ctx, c); err != nil || res.Code != link.CodeUnknownCommand || res.Re != c.ID {
		t.Fatalf("unknown command %+v %v", res, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := conn.Command(cctx, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}

	// Events are delivered and acknowledged.
	motion := func(at time.Time) {
		for i := 0; i < 4; i++ {
			r.rec.OnMotion(detect.Motion{CameraID: cam.ID, Score: 0.2, At: at.Add(time.Duration(i) * 500 * time.Millisecond)})
		}
	}
	motion(time.Now())
	eventually(t, "event delivered", func() bool { return s.st.EventCount(ctx, cam.ID) == 1 && r.rec.Pending() == 0 })

	// Video: the management side's proxy reaches the recorder's /v/ plane
	// with a minted token.
	seg := "20260101-000000-0.mp4"
	if err := os.MkdirAll(filepath.Join(r.rec.RecordingsRoot(), cam.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.rec.RecordingsRoot(), cam.ID, seg), []byte("MP4 BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	conn.Video().ServeHTTP(w, httptest.NewRequest("GET", "/recordings/"+cam.ID+"/"+seg, nil))
	if w.Code != 200 || w.Body.String() != "MP4 BYTES" {
		t.Fatalf("proxy: %d %s", w.Code, w.Body)
	}
	_ = os.MkdirAll(r.rec.EvidenceDir(), 0o755)
	if err := os.WriteFile(filepath.Join(r.rec.EvidenceDir(), "case-1.mp4"), []byte("EVIDENCE"), 0o444); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	conn.Video().ServeHTTP(w, httptest.NewRequest("GET", "/rs/evidence/case-1.mp4", nil))
	if w.Code != 200 || w.Body.String() != "EVIDENCE" {
		t.Fatalf("internal path: %d %s", w.Code, w.Body)
	}
	// And through the management server's own gateway with a client token.
	key, _ := s.st.VideoTokenKey(ctx)
	tok, _, _ := videotoken.NewSigner(key).Sign(videotoken.Claims{Camera: cam.ID, Subject: "u1", Rights: []videotoken.Right{videotoken.Playback}, Audience: id.ServerID}, time.Minute)
	w = httptest.NewRecorder()
	s.srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v/"+tok+"/recordings/"+cam.ID+"/"+seg, nil))
	if w.Code != 200 || w.Body.String() != "MP4 BYTES" {
		t.Fatalf("gateway: %d %s", w.Code, w.Body)
	}
	// The recorder's plane refuses what isn't signed by the management server.
	resp, err := http.Get(r.video.URL + "/v/v1.bogus.token/recordings/" + cam.ID + "/" + seg)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bogus token: %v %v", resp, err)
	}
	resp.Body.Close()
	internal, _, _ := videotoken.NewSigner(key).Sign(videotoken.Claims{Camera: cam.ID, Subject: "u1", Rights: []videotoken.Right{videotoken.Export}, Audience: id.ServerID}, time.Minute)
	resp, _ = http.Get(r.video.URL + "/v/" + internal + "/rs/evidence/x.mp4")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("client token on an internal path: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// The management server goes away: events queue, and are replayed
	// once, in order, when it is back.
	addr := s.addr
	s.stop()
	eventually(t, "disconnect", func() bool { return !connected(s, id.ServerID) })
	eventually(t, "client notices", func() bool { up, _ := r.client.Connected(); return !up })
	motion(time.Now().Add(2 * time.Minute)) // a new episode
	if r.rec.Pending() != 1 {
		t.Fatalf("pending %d", r.rec.Pending())
	}
	s.start(addr)
	eventually(t, "reconnect", func() bool { return connected(s, id.ServerID) })
	eventually(t, "replay", func() bool { return r.rec.Pending() == 0 })
	if n := s.st.EventCount(ctx, cam.ID); n != 2 {
		t.Fatalf("events after replay: %d", n)
	}
	// Re-sending the same events is harmless (the sink dedupes).
	r.rec.Replay(ctx)
	if n := s.st.EventCount(ctx, cam.ID); n != 2 {
		t.Fatalf("events after second replay: %d", n)
	}

	// Certificate renewal: the registry follows, and the new one connects.
	before := id.Cert().Raw
	if err := r.client.Renew(ctx); err != nil {
		t.Fatal(err)
	}
	rs, _ := s.st.RecordingServer(ctx, id.ServerID)
	if pki.Fingerprint(before) == rs.CertFingerprint || rs.CertFingerprint != pki.Fingerprint(r.client.ID.Cert().Raw) {
		t.Fatal("renewal not recorded")
	}
	s.l.Disconnect(id.ServerID)
	eventually(t, "reconnect after renewal", func() bool { up, _ := r.client.Connected(); return up && connected(s, id.ServerID) })

	// With the management server down, a restarted recording server
	// configures itself from the saved snapshot.
	s.stop()
	r.stop()
	r.video.Close()
	if err := os.Remove(r.rec.Media.ConfigPath); err != nil {
		t.Fatal(err)
	}
	r2 := startRec(t, dir)
	eventually(t, "saved snapshot applied", func() bool {
		b, _ := os.ReadFile(r2.rec.Media.ConfigPath)
		return strings.Contains(string(b), cam.ID) && strings.Contains(string(b), "p%40ss%20w0rd")
	})
	if up, _ := r2.client.Connected(); up {
		t.Fatal("connected to a stopped server")
	}
}

// A request in flight when the connection drops fails instead of hanging.
func TestCommandFailsWhenConnectionDrops(t *testing.T) {
	s := newSite(t)
	dir := t.TempDir()
	id := enroll(t, s, dir)
	blocker := &blockingRecorder{started: make(chan struct{})}
	startRecWith(t, dir, func(lr LocalRecorder) LocalRecorder { blocker.LocalRecorder = lr; return blocker })
	eventually(t, "connect", func() bool { return connected(s, id.ServerID) })
	conn := s.l.Recorder(id.ServerID)
	errc := make(chan error, 1)
	go func() {
		c, _ := link.NewCommand(link.CmdStorageInfo, nil)
		_, err := conn.Command(context.Background(), c)
		errc <- err
	}()
	<-blocker.started
	s.l.Disconnect(id.ServerID)
	select {
	case err := <-errc:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command hung")
	}
	close(blocker.release())
}

type blockingRecorder struct {
	LocalRecorder
	once    sync.Once
	started chan struct{}
	mu      sync.Mutex
	rel     chan struct{}
}

func (b *blockingRecorder) release() chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rel == nil {
		b.rel = make(chan struct{})
	}
	return b.rel
}

func (b *blockingRecorder) Command(ctx context.Context, c link.Command) (link.Result, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release():
	case <-ctx.Done():
	}
	return link.Result{}, ctx.Err()
}

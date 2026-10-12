package remote

// The management side: the control listener (HTTPS on --control-listen,
// default :8091) that recording servers enroll with and dial.
//
//	POST /link/v1/enroll   server-auth TLS; the enrollment token is the credential
//	POST /link/v1/renew    mTLS
//	GET  /link/v1/connect  mTLS, WebSocket
//
// A connected recording server becomes a link.Recorder on the
// server.Server (ConnectRecorder + ApplyRecorder); what it reports goes to
// the server's Sink.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/pki"
	"sentinel-linux/internal/server"
	"sentinel-linux/internal/store"
)

// DefaultControlPort is the control listener's port.
const DefaultControlPort = "8091"

const (
	touchEvery    = 15 * time.Second // registry last_seen_at and revocation check
	eventQueue    = 1024
	maxEnrollBody = 64 << 10
	sinkTimeout   = 30 * time.Second
)

// Listener is the management server's end of the control link. It
// implements server.ControlLink.
type Listener struct {
	Server *server.Server
	CA     *pki.CA
	// Hosts are extra names and addresses recording servers use to reach
	// this server (--control-hosts); the listener's certificate covers them
	// and the local ones.
	Hosts []string
	// Addr is the listen address (--control-listen).
	Addr string

	certMu sync.Mutex
	cert   *tls.Certificate

	mu    sync.Mutex
	conns map[string]*Conn

	upgrader websocket.Upgrader
}

// NewListener sets up the control listener for srv with the CA in
// <dataDir>/ca (created on first start).
func NewListener(srv *server.Server, dataDir string, addr string, hosts []string) (*Listener, error) {
	ca, err := pki.LoadOrCreateCA(filepath.Join(dataDir, "ca"))
	if err != nil {
		return nil, err
	}
	return &Listener{Server: srv, CA: ca, Addr: addr, Hosts: hosts}, nil
}

// TLSConfig is the listener's TLS: its certificate from the internal CA
// and an optional client certificate from the same CA (required by every
// endpoint but enroll).
func (l *Listener) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: l.getCertificate,
		ClientAuth:     tls.VerifyClientCertIfGiven,
		ClientCAs:      l.CA.Pool(),
	}
}

// getCertificate issues the listener's certificate on first use and again
// when two thirds of its life are gone.
func (l *Listener) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	l.certMu.Lock()
	defer l.certMu.Unlock()
	if l.cert != nil && !renewDue(l.cert.Leaf, time.Now()) {
		return l.cert, nil
	}
	hosts := append(append([]string{}, l.Hosts...), pki.LocalHosts()...)
	c, err := l.CA.IssueServer(hosts, pki.CertLifetime)
	if err != nil {
		return nil, err
	}
	l.cert = &c
	return l.cert, nil
}

// renewDue reports whether cert has used two thirds of its life.
func renewDue(cert *x509.Certificate, now time.Time) bool {
	life := cert.NotAfter.Sub(cert.NotBefore)
	return now.After(cert.NotBefore.Add(life * 2 / 3))
}

// Handler serves the control link endpoints.
func (l *Listener) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /link/v1/enroll", l.handleEnroll)
	mux.HandleFunc("POST /link/v1/renew", l.handleRenew)
	mux.HandleFunc("GET /link/v1/connect", l.handleConnect)
	return mux
}

// Serve runs the listener on ln until ctx ends.
func (l *Listener) Serve(ctx context.Context, ln net.Listener) error {
	hs := &http.Server{
		Handler:           l.Handler(),
		TLSConfig:         l.TLSConfig(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug), // TLS handshake noise from scanners
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		l.closeAll()
		_ = hs.Close()
	}()
	err := hs.ServeTLS(ln, "", "")
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ListenAndServe listens on Addr and serves until ctx ends.
func (l *Listener) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", l.Addr)
	if err != nil {
		return fmt.Errorf("control link: %w", err)
	}
	return l.Serve(ctx, ln)
}

// MARK: - server.ControlLink

var _ server.ControlLink = (*Listener)(nil)

// EnrollmentToken turns a stored secret into the token an admin hands out.
func (l *Listener) EnrollmentToken(secret string) string { return pki.FormatToken(secret, l.CA.Pin()) }

// EnrollCommand is the command line to run on the new machine.
func (l *Listener) EnrollCommand(token string) string {
	return "sentinel --role recording --management " + l.ManagementURL() + " --enroll " + token
}

// ManagementURL is the URL recording servers dial: the first
// --control-hosts entry (else this machine's name) and the listen port.
func (l *Listener) ManagementURL() string {
	host := ""
	if len(l.Hosts) > 0 {
		host = l.Hosts[0]
	} else if hn, err := os.Hostname(); err == nil {
		host = hn
	}
	port := DefaultControlPort
	if _, p, err := net.SplitHostPort(l.Addr); err == nil && p != "" && p != "0" {
		port = p
	}
	return "https://" + net.JoinHostPort(host, port)
}

// Disconnect drops a server's connection now (after a revoke).
func (l *Listener) Disconnect(serverID string) {
	l.mu.Lock()
	c := l.conns[serverID]
	l.mu.Unlock()
	if c != nil {
		c.p.close()
	}
}

// Recorder is the connected recording server id, or nil.
func (l *Listener) Recorder(id string) *Conn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.conns[id]
}

func (l *Listener) closeAll() {
	l.mu.Lock()
	conns := make([]*Conn, 0, len(l.conns))
	for _, c := range l.conns {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	for _, c := range conns {
		c.p.close()
	}
}

// MARK: - Enrollment

// EnrollRequest is POST /link/v1/enroll's body.
type EnrollRequest struct {
	Token         string `json:"token"`
	Name          string `json:"name"`
	CSRPEM        string `json:"csrPem"`
	SealPublicKey string `json:"sealPublicKey"`
	Version       string `json:"version"`
	Hostname      string `json:"hostname"`
}

// EnrollResponse is its answer.
type EnrollResponse struct {
	ServerID string `json:"serverId"`
	CertPEM  string `json:"certPem"`
	CAPEM    string `json:"caPem"`
}

// RenewRequest / RenewResponse are POST /link/v1/renew's.
type RenewRequest struct {
	CSRPEM string `json:"csrPem"`
}

type RenewResponse struct {
	CertPEM string `json:"certPem"`
}

func (l *Listener) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var in EnrollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEnrollBody)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	secret, pin, err := pki.ParseToken(in.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !pki.MatchesPin(l.CA.Cert, pin) {
		writeError(w, http.StatusForbidden, "this enrollment token is for another management server")
		return
	}
	if _, err := pki.ParseSealPublicKey(in.SealPublicKey); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := store.NewID()
	// Sign before burning the token, so a bad request doesn't use it up.
	cert, err := l.CA.SignCSR([]byte(in.CSRPEM), id, pki.CertLifetime)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	tok, err := l.Server.Store.ConsumeEnrollmentToken(ctx, secret, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "the enrollment token is unknown, expired or already used — create a new one in Sentinel Admin")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := firstNonEmpty(tok.Name, in.Name, in.Hostname, "Recording server")
	rs := &store.RecordingServer{ID: id, Name: name, CertFingerprint: pki.Fingerprint(cert.Raw), SealPublicKey: in.SealPublicKey, Version: in.Version}
	if err := l.Server.Store.AddRecordingServer(ctx, rs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("recording server enrolled", "server", id, "name", name, "host", in.Hostname, "by", tok.CreatedBy)
	writeJSON(w, http.StatusOK, EnrollResponse{ServerID: id, CertPEM: string(pki.CertPEM(cert.Raw)), CAPEM: string(l.CA.PEM())})
}

// identify finds the enrolled, unrevoked server behind the client
// certificate, answering 401/403 if there is none.
func (l *Listener) identify(w http.ResponseWriter, r *http.Request) (*store.RecordingServer, bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		writeError(w, http.StatusUnauthorized, "a recording server certificate is required")
		return nil, false
	}
	fp := pki.Fingerprint(r.TLS.PeerCertificates[0].Raw)
	rs, err := l.Server.Store.RecordingServerByFingerprint(r.Context(), fp)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusForbidden, "unknown recording server certificate")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if rs.Revoked() {
		writeError(w, http.StatusForbidden, "this recording server was revoked")
		return nil, false
	}
	return rs, true
}

func (l *Listener) handleRenew(w http.ResponseWriter, r *http.Request) {
	rs, ok := l.identify(w, r)
	if !ok {
		return
	}
	var in RenewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEnrollBody)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	cert, err := l.CA.SignCSR([]byte(in.CSRPEM), rs.ID, pki.CertLifetime)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rs.CertFingerprint = pki.Fingerprint(cert.Raw)
	if err := l.Server.Store.UpdateRecordingServer(r.Context(), rs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("recording server certificate renewed", "server", rs.ID, "until", cert.NotAfter.Format(time.DateOnly))
	writeJSON(w, http.StatusOK, RenewResponse{CertPEM: string(pki.CertPEM(cert.Raw))})
}

// MARK: - Connection

func (l *Listener) handleConnect(w http.ResponseWriter, r *http.Request) {
	rs, ok := l.identify(w, r)
	if !ok {
		return
	}
	ws, err := l.upgrader.Upgrade(w, r, nil) // not a browser: no Origin check
	if err != nil {
		return
	}
	p := newPeer(ws)
	defer p.close()
	_ = ws.SetReadDeadline(time.Now().Add(helloWait))
	var env link.Envelope
	if err := ws.ReadJSON(&env); err != nil || env.Type != link.TypeHello {
		slog.Warn("recording server did not say hello", "server", rs.ID, "err", err)
		return
	}
	hello, err := decode[link.Hello](env)
	if err != nil {
		return
	}
	if hello.ServerID != "" && hello.ServerID != rs.ID {
		slog.Warn("recording server hello names another id; using its certificate's", "server", rs.ID, "hello", hello.ServerID)
	}
	if hello.VideoURL != "" {
		if u, err := url.Parse(hello.VideoURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			slog.Warn("recording server sent an unusable video URL", "server", rs.ID, "url", hello.VideoURL)
			hello.VideoURL = ""
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &Conn{l: l, id: rs.ID, p: p, hello: hello, sealKey: rs.SealPublicKey, events: make(chan link.Envelope, eventQueue)}
	l.noteHello(ctx, rs, hello)

	l.mu.Lock()
	if l.conns == nil {
		l.conns = map[string]*Conn{}
	}
	old := l.conns[rs.ID]
	l.conns[rs.ID] = c
	l.mu.Unlock()
	if old != nil {
		old.p.close() // a reconnect replaces a stale connection
	}
	srv := l.Server
	srv.ConnectRecorder(rs.ID, c)
	slog.Info("recording server connected", "server", rs.ID, "name", rs.Name, "version", hello.Version, "video", hello.VideoURL)
	defer func() {
		srv.DisconnectRecorder(rs.ID, c)
		l.mu.Lock()
		if l.conns[rs.ID] == c {
			delete(l.conns, rs.ID)
		}
		l.mu.Unlock()
		slog.Info("recording server disconnected", "server", rs.ID)
	}()

	go p.pinger()
	go c.runSink(ctx)
	go func() {
		if err := srv.ApplyRecorder(ctx, rs.ID); err != nil && !p.closed() {
			slog.Warn("recording server configuration not applied", "server", rs.ID, "err", err)
		}
	}()
	c.readLoop(ctx)
}

// noteHello updates the registry's version and last-seen time.
func (l *Listener) noteHello(ctx context.Context, rs *store.RecordingServer, h link.Hello) {
	st := l.Server.Store
	if h.Version != "" && h.Version != rs.Version {
		rs.Version = h.Version
		if err := st.UpdateRecordingServer(ctx, rs); err != nil {
			slog.Warn("recording server registry", "err", err)
		}
	}
	_ = st.TouchRecordingServer(ctx, rs.ID, time.Now())
}

// Conn is one connected recording server, as the management server sees
// it: a link.Recorder over the control link.
type Conn struct {
	l       *Listener
	id      string
	p       *peer
	hello   link.Hello
	sealKey string // X25519 public key, base64

	statusMu   sync.Mutex
	status     link.ServerStatus
	haveStatus bool

	events chan link.Envelope // events and status reports, in order, for the sink

	lastTouch time.Time // read loop only
}

var _ link.Recorder = (*Conn)(nil)

// ID is the recording server's id.
func (c *Conn) ID() string { return c.id }

// Hello is what the server said when it connected.
func (c *Conn) Hello() link.Hello { return c.hello }

func (c *Conn) readLoop(ctx context.Context) {
	defer close(c.events)
	for {
		env, err := c.p.read()
		if err != nil {
			if !c.p.closed() && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Warn("control link read", "server", c.id, "err", err)
			}
			return
		}
		c.touch(ctx)
		switch env.Type {
		case link.TypeResult, link.TypeConfigApplied:
			c.p.resolve(env)
		case link.TypePing:
			_ = c.p.reply(env, link.TypePong, nil)
		case link.TypePong:
		case link.TypeEvent, link.TypeStatus:
			ephemeral := false
			if env.Type == link.TypeStatus {
				ephemeral = true
			} else if e, err := decode[link.Event](env); err == nil && e.Ephemeral {
				ephemeral = true
			}
			if ephemeral {
				select { // high-rate and best effort: drop when the sink is behind
				case c.events <- env:
				default:
				}
			} else {
				select {
				case c.events <- env:
				case <-c.p.done:
					return
				}
			}
		default:
			slog.Warn("unknown control link message", "server", c.id, "type", env.Type)
		}
		if c.p.closed() {
			return
		}
	}
}

// touch records traffic in the registry and drops the connection if the
// server was revoked meanwhile (Disconnect normally does it at once).
func (c *Conn) touch(ctx context.Context) {
	now := time.Now()
	if now.Sub(c.lastTouch) < touchEvery {
		return
	}
	c.lastTouch = now
	st := c.l.Server.Store
	_ = st.TouchRecordingServer(ctx, c.id, now)
	if rs, err := st.RecordingServer(ctx, c.id); err == nil && rs.Revoked() {
		slog.Warn("recording server revoked; dropping its connection", "server", c.id)
		c.p.close()
	}
}

// runSink hands events and status reports to the management server's sink
// one at a time, acknowledging durable events once the sink took them.
func (c *Conn) runSink(ctx context.Context) {
	sink := c.l.Server.Sink()
	for env := range c.events {
		switch env.Type {
		case link.TypeStatus:
			st, err := decode[link.ServerStatus](env)
			if err != nil {
				continue
			}
			st.ServerID = c.id // the certificate says who it is
			c.statusMu.Lock()
			c.status, c.haveStatus = st, true
			c.statusMu.Unlock()
			sink.Status(st)
		case link.TypeEvent:
			e, err := decode[link.Event](env)
			if err != nil {
				_ = c.p.reply(env, link.TypeResult, link.Result{Re: env.ID, Code: link.CodeBadRequest, Error: err.Error()})
				continue
			}
			e.ServerID = c.id
			sctx, cancel := context.WithTimeout(ctx, sinkTimeout)
			err = sink.Event(sctx, e)
			cancel()
			if e.Ephemeral {
				continue
			}
			res := link.Result{Re: env.ID}
			if err != nil {
				res.Code, res.Error = link.CodeUnavailable, err.Error()
			}
			_ = c.p.reply(env, link.TypeResult, res)
		}
	}
}

// MARK: - link.Recorder

// ApplyConfig sends the snapshot with every camera password sealed to the
// server's X25519 key.
func (c *Conn) ApplyConfig(ctx context.Context, s link.Snapshot) (link.Applied, error) {
	sealed, err := c.seal(s)
	if err != nil {
		return link.Applied{}, err
	}
	env, err := link.Wrap(link.TypeConfigSnapshot, link.NewID(), sealed)
	if err != nil {
		return link.Applied{}, err
	}
	env.Rev = s.Rev
	resp, err := c.p.request(ctx, env)
	if err != nil {
		return link.Applied{}, err
	}
	a, err := decode[appliedBody](resp)
	if err != nil {
		return link.Applied{}, err
	}
	if a.Fault != "" {
		return a.Applied, errors.New(a.Fault)
	}
	return a.Applied, nil
}

func (c *Conn) seal(s link.Snapshot) (link.Snapshot, error) {
	key, err := pki.ParseSealPublicKey(c.sealKey)
	if err != nil {
		return s, fmt.Errorf("recording server %s has no usable seal key: %w", c.id, err)
	}
	cams := make([]link.CameraConfig, len(s.Cameras))
	for i, cam := range s.Cameras {
		if cam.Password != "" {
			sp, err := pki.Seal(key, []byte(cam.Password))
			if err != nil {
				return s, err
			}
			cam.SealedPassword, cam.Password = sp, ""
		}
		cams[i] = cam
	}
	s.Cameras = cams
	return s, nil
}

// Command runs one command on the recording server.
func (c *Conn) Command(ctx context.Context, cmd link.Command) (link.Result, error) {
	if err := ctx.Err(); err != nil {
		return link.Result{}, err
	}
	env, err := link.Wrap(link.TypeCommand, link.NewID(), cmd)
	if err != nil {
		return link.Result{}, err
	}
	resp, err := c.p.request(ctx, env)
	if err != nil {
		return link.Result{}, err
	}
	res, err := decode[resultBody](resp)
	if err != nil {
		return link.Result{}, err
	}
	if res.Fault != "" {
		return link.Result{}, errors.New(res.Fault)
	}
	return res.Result, nil
}

// Status is the last status the server reported (what it said in hello
// until the first report).
func (c *Conn) Status() link.ServerStatus {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	if c.haveStatus {
		st := c.status
		st.Cameras = make(map[string]link.CameraStatus, len(c.status.Cameras))
		for k, v := range c.status.Cameras {
			st.Cameras[k] = v
		}
		return st
	}
	return link.ServerStatus{ServerID: c.id, Version: c.hello.Version, ConfigRev: c.hello.LastAppliedRev,
		Capabilities: c.hello.Capabilities, Cameras: map[string]link.CameraStatus{}}
}

// MARK: - helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

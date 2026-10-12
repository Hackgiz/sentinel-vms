package remote

// The recording side: enrollment, the identity in <data>/link/ and the
// Client that keeps one connection to the management server.
//
//	<data>/link/identity.json  server id, management URL
//	<data>/link/client.key     Ed25519 TLS key
//	<data>/link/client.crt     certificate from the management server's CA
//	<data>/link/ca.crt         the pinned CA
//	<data>/link/seal.key       X25519 key camera passwords are sealed to
//	<data>/link/config.json    the last snapshot, passwords still sealed

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
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
)

const (
	identityFile = "identity.json"
	clientKey    = "client.key"
	clientCert   = "client.crt"
	caCert       = "ca.crt"
	sealKeyFile  = "seal.key"
	configFile   = "config.json"

	backoffMin  = time.Second
	backoffMax  = time.Minute
	renewCheck  = time.Hour
	httpTimeout = 30 * time.Second
)

// ErrNotEnrolled means <data>/link holds no identity yet.
var ErrNotEnrolled = errors.New("this recording server is not enrolled with a management server (run it once with --management URL --enroll TOKEN)")

// LinkDir is where the recording server keeps its control-link files.
func LinkDir(dataDir string) string { return filepath.Join(dataDir, "link") }

// Identity is an enrolled recording server's credentials.
type Identity struct {
	ServerID   string `json:"serverId"`
	Management string `json:"management"` // https://host:8091
	EnrolledAt int64  `json:"enrolledAt"` // Unix s

	dir  string
	key  ed25519.PrivateKey
	cert *x509.Certificate
	ca   *x509.Certificate
	seal *pki.SealKey
}

// Cert is the current client certificate.
func (id *Identity) Cert() *x509.Certificate { return id.cert }

// LoadIdentity reads <data>/link. ErrNotEnrolled if there is none.
func LoadIdentity(dataDir string) (*Identity, error) {
	dir := LinkDir(dataDir)
	raw, err := os.ReadFile(filepath.Join(dir, identityFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotEnrolled
	}
	if err != nil {
		return nil, err
	}
	id := &Identity{dir: dir}
	if err := json.Unmarshal(raw, id); err != nil || id.ServerID == "" || id.Management == "" {
		return nil, fmt.Errorf("control link: %s is damaged", identityFile)
	}
	read := func(name string) []byte {
		if err != nil {
			return nil
		}
		var b []byte
		b, err = os.ReadFile(filepath.Join(dir, name))
		return b
	}
	keyPEM, certPEM, caPEM, sealRaw := read(clientKey), read(clientCert), read(caCert), read(sealKeyFile)
	if err != nil {
		return nil, fmt.Errorf("control link identity: %w", err)
	}
	if id.key, err = pki.ParseKeyPEM(keyPEM); err != nil {
		return nil, err
	}
	if id.cert, err = pki.ParseCertPEM(certPEM); err != nil {
		return nil, err
	}
	if id.ca, err = pki.ParseCertPEM(caPEM); err != nil {
		return nil, err
	}
	if id.seal, err = pki.ParseSealKey(strings.TrimSpace(string(sealRaw))); err != nil {
		return nil, err
	}
	return id, nil
}

// EnrollOptions describes this machine to the management server.
type EnrollOptions struct {
	Management string // https://host:8091
	Token      string // sen1.…
	Name       string // "" = the name the admin gave the token, else the hostname
	Version    string
}

// Enroll trades a one-time token for this server's identity and saves it
// in <data>/link. The management server's CA is trusted only if it is the
// one the token pins.
func Enroll(ctx context.Context, dataDir string, o EnrollOptions) (*Identity, error) {
	base, err := managementURL(o.Management)
	if err != nil {
		return nil, err
	}
	_, pin, err := pki.ParseToken(o.Token)
	if err != nil {
		return nil, err
	}
	key, err := pki.NewKey()
	if err != nil {
		return nil, err
	}
	hostname, _ := os.Hostname()
	csr, err := pki.CreateCSR(key, hostname)
	if err != nil {
		return nil, err
	}
	seal, err := pki.NewSealKey()
	if err != nil {
		return nil, err
	}
	req := EnrollRequest{Token: strings.TrimSpace(o.Token), Name: o.Name, CSRPEM: string(csr), SealPublicKey: seal.PublicString(), Version: o.Version, Hostname: hostname}
	client := &http.Client{Timeout: httpTimeout, Transport: &http.Transport{TLSClientConfig: pinnedTLS(pin)}}
	var resp EnrollResponse
	if err := postJSON(ctx, client, base+"/link/v1/enroll", req, &resp); err != nil {
		return nil, fmt.Errorf("enroll with %s: %w", base, err)
	}
	ca, err := pki.ParseCertPEM([]byte(resp.CAPEM))
	if err != nil || !pki.MatchesPin(ca, pin) {
		return nil, errors.New("enroll: the management server sent a CA other than the one the token pins")
	}
	cert, err := pki.ParseCertPEM([]byte(resp.CertPEM))
	if err != nil {
		return nil, fmt.Errorf("enroll: certificate: %w", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: poolOf(ca), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, fmt.Errorf("enroll: certificate: %w", err)
	}
	if !key.Public().(ed25519.PublicKey).Equal(cert.PublicKey) || resp.ServerID == "" {
		return nil, errors.New("enroll: the certificate is not for this server's key")
	}
	id := &Identity{ServerID: resp.ServerID, Management: base, EnrolledAt: time.Now().Unix(),
		dir: LinkDir(dataDir), key: key, cert: cert, ca: ca, seal: seal}
	if err := id.save(); err != nil {
		return nil, err
	}
	_ = os.Remove(filepath.Join(id.dir, configFile)) // a snapshot from a previous enrollment
	return id, nil
}

func (id *Identity) save() error {
	if err := os.MkdirAll(id.dir, 0o700); err != nil {
		return err
	}
	keyPEM, err := pki.EncodeKeyPEM(id.key)
	if err != nil {
		return err
	}
	meta, _ := json.MarshalIndent(id, "", "  ")
	for _, f := range []struct {
		name string
		data []byte
		perm os.FileMode
	}{
		{clientKey, keyPEM, 0o600},
		{sealKeyFile, []byte(id.seal.PrivateString() + "\n"), 0o600},
		{caCert, pki.CertPEM(id.ca.Raw), 0o644},
		{clientCert, pki.CertPEM(id.cert.Raw), 0o644},
		{identityFile, meta, 0o600}, // last: its presence means enrolled
	} {
		if err := pki.WriteFileAtomic(filepath.Join(id.dir, f.name), f.data, f.perm); err != nil {
			return err
		}
	}
	return nil
}

// tlsConfig is mTLS with the pinned CA (and the server's name checked, so
// a recording server certificate from the same CA can't pose as it).
func (id *Identity) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      poolOf(id.ca),
		Certificates: []tls.Certificate{{Certificate: [][]byte{id.cert.Raw}, PrivateKey: id.key, Leaf: id.cert}},
	}
}

// Renew asks the management server for a new certificate for the same key.
func (id *Identity) Renew(ctx context.Context) error {
	csr, err := pki.CreateCSR(id.key, id.ServerID)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: httpTimeout, Transport: &http.Transport{TLSClientConfig: id.tlsConfig()}}
	defer client.CloseIdleConnections()
	var resp RenewResponse
	if err := postJSON(ctx, client, id.Management+"/link/v1/renew", RenewRequest{CSRPEM: string(csr)}, &resp); err != nil {
		return fmt.Errorf("renew certificate: %w", err)
	}
	cert, err := pki.ParseCertPEM([]byte(resp.CertPEM))
	if err != nil {
		return err
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: poolOf(id.ca), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("renewed certificate: %w", err)
	}
	if !id.key.Public().(ed25519.PublicKey).Equal(cert.PublicKey) {
		return errors.New("renewed certificate is not for this server's key")
	}
	if err := pki.WriteFileAtomic(filepath.Join(id.dir, clientCert), pki.CertPEM(cert.Raw), 0o644); err != nil {
		return err
	}
	id.cert = cert
	return nil
}

// pinnedTLS trusts only the CA whose SHA-256 is pin: it must be in the
// chain the server presents and the server certificate must chain to it
// for the name dialed.
func pinnedTLS(pin []byte) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // verified below against the pinned CA
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no server certificate")
			}
			for _, c := range cs.PeerCertificates {
				if !pki.MatchesPin(c, pin) {
					continue
				}
				_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: cs.ServerName, Roots: poolOf(c)})
				return err
			}
			return errors.New("the management server's CA doesn't match the enrollment token (wrong server, or someone in between)")
		},
	}
}

func poolOf(c *x509.Certificate) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c)
	return p
}

func managementURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("management server URL %q: want https://host[:port]", raw)
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), DefaultControlPort)
	}
	return "https://" + u.Host, nil
}

func postJSON(ctx context.Context, client *http.Client, u string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return &HTTPError{Status: resp.StatusCode, Message: e.Error}
		}
		return &HTTPError{Status: resp.StatusCode, Message: resp.Status}
	}
	return json.Unmarshal(raw, out)
}

// HTTPError is a refusal from the management server.
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("%d: %s", e.Status, e.Message) }

// MARK: - Client

// LocalRecorder is the recording server the client connects
// (*recorder.Recorder).
type LocalRecorder interface {
	link.Recorder
	// Attach sets where events and status go.
	Attach(link.Sink)
	// Replay delivers queued events now.
	Replay(ctx context.Context)
}

// Client keeps a recording server connected to its management server. It
// is the recorder's link.Sink: events go over the connection and count as
// delivered only once the management server acknowledged them.
type Client struct {
	ID       *Identity
	Recorder LocalRecorder
	Version  string
	// VideoURL is where this server's video plane listens (sent in hello).
	VideoURL string
	// OnState, if set, is told about connects and disconnects.
	OnState func(connected bool, err error)

	mu         sync.Mutex
	p          *peer
	lastErr    error
	appliedRev int64
	applyMu    sync.Mutex // one snapshot at a time
	idMu       sync.Mutex // the certificate, while it is renewed
}

var _ link.Sink = (*Client)(nil)

// Connected reports whether the control link is up, and the last error.
func (c *Client) Connected() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.p != nil, c.lastErr
}

func (c *Client) current() *peer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.p
}

// Run attaches the client to the recorder, applies the snapshot kept on
// disk (so recording continues while the management server is away),
// then connects and reconnects with backoff until ctx ends.
func (c *Client) Run(ctx context.Context) {
	c.Recorder.Attach(c)
	if err := c.ApplyPersisted(ctx); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("saved configuration not applied", "err", err)
	}
	go c.renewLoop(ctx)
	backoff := backoffMin
	for ctx.Err() == nil {
		start := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		c.lastErr = err
		c.mu.Unlock()
		if c.OnState != nil {
			c.OnState(false, err)
		}
		if time.Since(start) > backoffMax {
			backoff = backoffMin // it was up for a while: retry soon
		}
		slog.Warn("control link down; retrying", "management", c.ID.Management, "in", backoff.Round(time.Second), "err", err)
		wait := backoff/2 + rand.N(backoff/2+1)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, backoffMax)
	}
}

// renewLoop renews the certificate at two thirds of its life.
func (c *Client) renewLoop(ctx context.Context) {
	t := time.NewTicker(renewCheck)
	defer t.Stop()
	for {
		c.idMu.Lock()
		due := renewDue(c.ID.cert, time.Now())
		c.idMu.Unlock()
		if due {
			if err := c.Renew(ctx); err != nil {
				slog.Warn("certificate renewal failed; will retry", "err", err)
			} else {
				slog.Info("control link certificate renewed")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Renew renews the client certificate now (Run does it at two thirds of
// its life); the next connection uses it.
func (c *Client) Renew(ctx context.Context) error {
	c.idMu.Lock()
	defer c.idMu.Unlock()
	return c.ID.Renew(ctx)
}

// session runs one connection until it drops.
func (c *Client) session(ctx context.Context) error {
	c.idMu.Lock()
	tlsCfg := c.ID.tlsConfig()
	c.idMu.Unlock()
	c.mu.Lock()
	lastRev := c.appliedRev
	c.mu.Unlock()
	u := "wss://" + strings.TrimPrefix(c.ID.Management, "https://") + "/link/v1/connect"
	d := websocket.Dialer{TLSClientConfig: tlsCfg, HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	ws, resp, err := d.DialContext(ctx, u, nil)
	if err != nil {
		if resp != nil {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return fmt.Errorf("connect: %s %s", resp.Status, strings.TrimSpace(string(msg)))
		}
		return fmt.Errorf("connect: %w", err)
	}
	p := newPeer(ws)
	defer p.close()
	hello := link.Hello{ServerID: c.ID.ServerID, Version: c.Version, Protocol: link.ProtocolVersion,
		Capabilities: c.Recorder.Status().Capabilities, LastAppliedRev: lastRev, VideoURL: c.VideoURL}
	env, err := link.Wrap(link.TypeHello, link.NewID(), hello)
	if err != nil {
		return err
	}
	if err := p.send(env); err != nil {
		return err
	}
	c.mu.Lock()
	c.p, c.lastErr = p, nil
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.p == p {
			c.p = nil
		}
		c.mu.Unlock()
	}()
	slog.Info("control link connected", "management", c.ID.Management, "server", c.ID.ServerID)
	if c.OnState != nil {
		c.OnState(true, nil)
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-sctx.Done()
		p.close()
	}()
	go p.pinger()
	snapshots := make(chan link.Envelope, 8)
	defer close(snapshots)
	go c.applyLoop(sctx, p, snapshots)
	go func() {
		c.Status(c.Recorder.Status())
		c.Recorder.Replay(sctx) // what queued up while disconnected
	}()

	for {
		env, err := p.read()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		switch env.Type {
		case link.TypeResult, link.TypePong:
			p.resolve(env)
		case link.TypePing:
			_ = p.reply(env, link.TypePong, nil)
		case link.TypeConfigSnapshot:
			select {
			case snapshots <- env:
			case <-p.done:
			}
		case link.TypeCommand:
			go c.runCommand(sctx, p, env)
		default:
			slog.Warn("unknown control link message", "type", env.Type)
		}
	}
}

func (c *Client) runCommand(ctx context.Context, p *peer, env link.Envelope) {
	var body resultBody
	cmd, err := decode[link.Command](env)
	if err != nil {
		body.Result = link.Result{Code: link.CodeBadRequest, Error: err.Error()}
	} else {
		res, err := c.Recorder.Command(ctx, cmd)
		if err != nil {
			body.Fault = err.Error()
		}
		body.Result = res
	}
	_ = p.reply(env, link.TypeResult, body)
}

// applyLoop applies snapshots in the order they came.
func (c *Client) applyLoop(ctx context.Context, p *peer, in <-chan link.Envelope) {
	for env := range in {
		var body appliedBody
		snap, err := decode[link.Snapshot](env)
		if err == nil {
			err = c.persist(env.Body)
			if err != nil {
				slog.Error("saving the configuration failed; applying it anyway", "err", err)
			}
			body.Applied, err = c.apply(ctx, snap)
		}
		if err != nil {
			body.Fault = err.Error()
		}
		_ = p.reply(env, link.TypeConfigApplied, body)
		c.Status(c.Recorder.Status()) // the new camera set, without waiting 15 s
	}
}

// apply opens the sealed passwords in memory and applies the snapshot.
func (c *Client) apply(ctx context.Context, s link.Snapshot) (link.Applied, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	var bad map[string]string
	cams := make([]link.CameraConfig, len(s.Cameras))
	for i, cam := range s.Cameras {
		if cam.SealedPassword != "" {
			pw, err := c.ID.seal.Open(cam.SealedPassword)
			if err != nil {
				if bad == nil {
					bad = map[string]string{}
				}
				bad[cam.ID] = "camera password can't be opened (sealed to another key)"
			}
			cam.Password = string(pw)
			cam.SealedPassword = ""
		}
		cams[i] = cam
	}
	s.Cameras = cams
	a, err := c.Recorder.ApplyConfig(ctx, s)
	if err != nil {
		return a, err
	}
	for id, msg := range bad {
		if a.Errors == nil {
			a.Errors = map[string]string{}
		}
		a.Errors[id] = msg
	}
	c.mu.Lock()
	c.appliedRev = s.Rev
	c.mu.Unlock()
	return a, nil
}

// persist keeps the snapshot (passwords still sealed) for the next start.
func (c *Client) persist(raw json.RawMessage) error {
	return pki.WriteFileAtomic(filepath.Join(c.ID.dir, configFile), raw, 0o600)
}

// ApplyPersisted applies the last snapshot saved on disk;
// os.ErrNotExist if there is none.
func (c *Client) ApplyPersisted(ctx context.Context) error {
	raw, err := os.ReadFile(filepath.Join(c.ID.dir, configFile))
	if err != nil {
		return err
	}
	var s link.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("%s: %w", configFile, err)
	}
	a, err := c.apply(ctx, s)
	if err != nil {
		return err
	}
	for id, msg := range a.Errors {
		slog.Warn("camera not configured", "camera", id, "err", msg)
	}
	slog.Info("applied the saved configuration", "rev", s.Rev, "cameras", len(s.Cameras))
	return nil
}

// MARK: - link.Sink

var errNotConnected = errors.New("not connected to the management server")

// Event sends one event. Durable events wait for the management server's
// acknowledgement (sent once its Sink took the event); ephemeral ones are
// sent and forgotten.
func (c *Client) Event(ctx context.Context, e link.Event) error {
	p := c.current()
	if p == nil {
		return errNotConnected
	}
	env, err := link.Wrap(link.TypeEvent, e.ID, e)
	if err != nil {
		return err
	}
	if env.ID == "" {
		env.ID = link.NewID()
	}
	if e.Ephemeral {
		return p.send(env)
	}
	ctx, cancel := context.WithTimeout(ctx, eventAckTimeout)
	defer cancel()
	resp, err := p.request(ctx, env)
	if err != nil {
		return err
	}
	res, err := decode[link.Result](resp)
	if err != nil {
		return err
	}
	return res.Err()
}

// Status sends a status report if connected.
func (c *Client) Status(st link.ServerStatus) {
	p := c.current()
	if p == nil {
		return
	}
	if env, err := link.Wrap(link.TypeStatus, link.NewID(), st); err == nil {
		_ = p.send(env)
	}
}

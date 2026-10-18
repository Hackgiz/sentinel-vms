package onvif

// RTSP replay relay (ONVIF Streaming Specification, "RTSP replay"): a
// Profile G camera plays a recording back only when PLAY carries
// "Require: onvif-replay" and an absolute "Range: clock=…" — headers
// ffmpeg's RTSP client cannot send. ReplayProxy listens on a loopback port
// ffmpeg opens instead of the camera, forwards its requests with those
// headers added (and its URLs pointed at the camera), answers the camera's
// authentication challenge itself (the digest covers the request URI,
// which it rewrites), and from PLAY on relays the interleaved RTP stream
// untouched. ffmpeg must use -rtsp_transport tcp.

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ReplayProxy relays one camera replay to local RTSP clients.
type ReplayProxy struct {
	ln         net.Listener
	target     *url.URL // the camera's replay address (no credentials)
	user, pass string
	from, to   time.Time

	localPrefix, camPrefix string

	mu     sync.Mutex
	conns  map[net.Conn]bool
	closed bool
	wg     sync.WaitGroup
}

const rtspDialTimeout = 10 * time.Second

// StartReplayProxy listens on 127.0.0.1 (a port the OS picks) and relays
// to replayURI, asking the camera for [from, to). Credentials in the URI
// are used when user is empty.
func StartReplayProxy(replayURI, user, pass string, from, to time.Time) (*ReplayProxy, error) {
	u, err := url.Parse(replayURI)
	if err != nil || !strings.EqualFold(u.Scheme, "rtsp") || u.Host == "" {
		return nil, fmt.Errorf("invalid replay address %q", replayURI)
	}
	if u.User != nil {
		if user == "" {
			user = u.User.Username()
			pass, _ = u.User.Password()
		}
		u.User = nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &ReplayProxy{ln: ln, target: u, user: strings.TrimSpace(user), pass: pass, from: from, to: to, conns: map[net.Conn]bool{}}
	p.localPrefix = "rtsp://" + ln.Addr().String()
	p.camPrefix = "rtsp://" + u.Host
	p.wg.Add(1)
	go p.accept()
	return p, nil
}

// URL is what to hand ffmpeg: the replay address on the loopback port.
func (p *ReplayProxy) URL() string {
	rest := p.target.EscapedPath()
	if p.target.RawQuery != "" {
		rest += "?" + p.target.RawQuery
	}
	return p.localPrefix + rest
}

// Close stops listening and drops every relayed connection.
func (p *ReplayProxy) Close() error {
	p.mu.Lock()
	p.closed = true
	for c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	err := p.ln.Close()
	p.wg.Wait()
	return err
}

func (p *ReplayProxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = c.Close()
		return false
	}
	p.conns[c] = true
	return true
}

func (p *ReplayProxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
	_ = c.Close()
}

func (p *ReplayProxy) accept() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		if !p.track(c) {
			continue
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer p.untrack(c)
			p.relay(c)
		}()
	}
}

func (p *ReplayProxy) camAddr() string {
	if p.target.Port() != "" {
		return p.target.Host
	}
	return net.JoinHostPort(p.target.Hostname(), "554")
}

// relay serves one client connection.
func (p *ReplayProxy) relay(cli net.Conn) {
	cam, err := dialTimeout("tcp", p.camAddr(), rtspDialTimeout)
	if err != nil {
		writeRTSPError(cli, 503, "Service Unavailable", "camera unreachable")
		return
	}
	if !p.track(cam) {
		return
	}
	defer p.untrack(cam)
	cr, sr := bufio.NewReader(cli), bufio.NewReader(cam)
	var challenge string // the camera's WWW-Authenticate, once it asked
	streaming := false
	for {
		b, err := cr.Peek(1)
		if err != nil {
			return
		}
		if b[0] == '$' { // interleaved RTCP from the client
			frame, err := readInterleaved(cr)
			if err != nil {
				return
			}
			if _, err := cam.Write(frame); err != nil {
				return
			}
			continue
		}
		req, err := readRTSPMessage(cr)
		if err != nil {
			return
		}
		p.rewrite(req)
		if streaming {
			// The camera's answers now arrive inside the relayed stream.
			p.authorize(req, challenge)
			if _, err := cam.Write(req.bytes()); err != nil {
				return
			}
			continue
		}
		resp, ch, err := p.roundTrip(cam, sr, req, challenge)
		if err != nil {
			return
		}
		challenge = ch
		if _, err := cli.Write(resp.bytes()); err != nil {
			return
		}
		if req.method() == "PLAY" && resp.status() == 200 {
			streaming = true
			go func() {
				_, _ = io.Copy(cli, sr) // RTP/RTCP and later answers, as they come
				_ = cli.Close()
			}()
		}
	}
}

// rewrite points the request at the camera and adds the replay headers.
func (p *ReplayProxy) rewrite(m *rtspMessage) {
	parts := strings.SplitN(m.start, " ", 3)
	if len(parts) == 3 && strings.HasPrefix(parts[1], p.localPrefix) {
		parts[1] = p.camPrefix + strings.TrimPrefix(parts[1], p.localPrefix)
		m.start = strings.Join(parts, " ")
	}
	m.del("Authorization")
	switch m.method() {
	case "DESCRIBE", "SETUP", "PLAY", "PAUSE":
		m.set("Require", "onvif-replay")
	}
	if m.method() == "PLAY" {
		m.del("Range")
		m.del("Scale")
		m.set("Range", "clock="+clockTime(p.from)+"-"+clockTime(p.to))
		// As fast as the camera can send; ffmpeg keeps the RTP timestamps.
		m.set("Rate-Control", "no")
	}
}

// clockTime is an RFC 2326 utc-time: 20261016T090000.000Z.
func clockTime(t time.Time) string { return t.UTC().Format("20060102T150405.000Z") }

// authorize adds the camera's requested credentials to a request.
func (p *ReplayProxy) authorize(m *rtspMessage, challenge string) {
	if challenge == "" || p.user == "" {
		return
	}
	uri := m.uri()
	if strings.HasPrefix(strings.ToLower(challenge), "digest") {
		m.set("Authorization", digestAuthorization(challenge, p.user, p.pass, m.method(), uri))
		return
	}
	m.set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.user+":"+p.pass)))
}

// roundTrip sends a request and reads its answer, answering one
// authentication challenge on the way. It returns the challenge to use
// for the next requests.
func (p *ReplayProxy) roundTrip(cam net.Conn, sr *bufio.Reader, req *rtspMessage, challenge string) (*rtspMessage, string, error) {
	for attempt := 0; ; attempt++ {
		p.authorize(req, challenge)
		_ = cam.SetDeadline(time.Now().Add(30 * time.Second))
		if _, err := cam.Write(req.bytes()); err != nil {
			return nil, challenge, err
		}
		resp, err := readRTSPResponse(sr)
		_ = cam.SetDeadline(time.Time{})
		if err != nil {
			return nil, challenge, err
		}
		if resp.status() == 401 && p.user != "" && attempt == 0 {
			if ch := pickReplayChallenge(resp.all("WWW-Authenticate")); ch != "" {
				challenge = ch
				continue
			}
		}
		return resp, challenge, nil
	}
}

// pickReplayChallenge prefers Digest over Basic.
func pickReplayChallenge(list []string) string {
	basic := ""
	for _, c := range list {
		l := strings.ToLower(strings.TrimSpace(c))
		if strings.HasPrefix(l, "digest") {
			return strings.TrimSpace(c)
		}
		if strings.HasPrefix(l, "basic") {
			basic = strings.TrimSpace(c)
		}
	}
	return basic
}

// readRTSPResponse reads the next answer, skipping interleaved frames a
// camera may send early.
func readRTSPResponse(r *bufio.Reader) (*rtspMessage, error) {
	for {
		b, err := r.Peek(1)
		if err != nil {
			return nil, err
		}
		if b[0] == '$' {
			if _, err := readInterleaved(r); err != nil {
				return nil, err
			}
			continue
		}
		m, err := readRTSPMessage(r)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(m.start, "RTSP/") {
			return nil, errors.New("unexpected RTSP request from the camera")
		}
		return m, nil
	}
}

func readInterleaved(r *bufio.Reader) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	n := int(hdr[2])<<8 | int(hdr[3])
	frame := make([]byte, 4+n)
	copy(frame, hdr)
	if _, err := io.ReadFull(r, frame[4:]); err != nil {
		return nil, err
	}
	return frame, nil
}

func writeRTSPError(w io.Writer, code int, reason, body string) {
	_, _ = fmt.Fprintf(w, "RTSP/1.0 %d %s\r\nContent-Length: %d\r\n\r\n%s", code, reason, len(body), body)
}

// rtspMessage is a request or response: start line, headers in order, body.
type rtspMessage struct {
	start   string
	headers [][2]string
	body    []byte
}

const maxRTSPHeaderBytes = 64 << 10

func readRTSPMessage(r *bufio.Reader) (*rtspMessage, error) {
	m := &rtspMessage{}
	total := 0
	readLine := func() (string, error) {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		total += len(line)
		if total > maxRTSPHeaderBytes {
			return "", errors.New("RTSP header too large")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	for m.start == "" { // tolerate stray blank lines between messages
		line, err := readLine()
		if err != nil {
			return nil, err
		}
		m.start = line
	}
	for {
		line, err := readLine()
		if err != nil {
			return nil, err
		}
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		m.headers = append(m.headers, [2]string{strings.TrimSpace(k), strings.TrimSpace(v)})
	}
	if n, err := strconv.Atoi(m.get("Content-Length")); err == nil && n > 0 {
		if n > 4<<20 {
			return nil, errors.New("RTSP body too large")
		}
		m.body = make([]byte, n)
		if _, err := io.ReadFull(r, m.body); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *rtspMessage) get(k string) string {
	for _, h := range m.headers {
		if strings.EqualFold(h[0], k) {
			return h[1]
		}
	}
	return ""
}

func (m *rtspMessage) all(k string) []string {
	var out []string
	for _, h := range m.headers {
		if strings.EqualFold(h[0], k) {
			out = append(out, h[1])
		}
	}
	return out
}

func (m *rtspMessage) set(k, v string) {
	for i, h := range m.headers {
		if strings.EqualFold(h[0], k) {
			m.headers[i][1] = v
			return
		}
	}
	m.headers = append(m.headers, [2]string{k, v})
}

func (m *rtspMessage) del(k string) {
	out := m.headers[:0]
	for _, h := range m.headers {
		if !strings.EqualFold(h[0], k) {
			out = append(out, h)
		}
	}
	m.headers = out
}

func (m *rtspMessage) method() string {
	if i := strings.IndexByte(m.start, ' '); i > 0 && !strings.HasPrefix(m.start, "RTSP/") {
		return m.start[:i]
	}
	return ""
}

func (m *rtspMessage) uri() string {
	parts := strings.SplitN(m.start, " ", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func (m *rtspMessage) status() int {
	parts := strings.SplitN(m.start, " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "RTSP/") {
		return 0
	}
	n, _ := strconv.Atoi(parts[1])
	return n
}

func (m *rtspMessage) bytes() []byte {
	var b strings.Builder
	b.WriteString(m.start)
	b.WriteString("\r\n")
	for _, h := range m.headers {
		if strings.EqualFold(h[0], "Content-Length") {
			continue
		}
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	if len(m.body) > 0 {
		b.WriteString("Content-Length: " + strconv.Itoa(len(m.body)) + "\r\n")
	}
	b.WriteString("\r\n")
	return append([]byte(b.String()), m.body...)
}

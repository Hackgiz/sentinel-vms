package onvif

// ONVIF audio backchannel (ONVIF Streaming Specification, "Back channel
// connection"): a camera with a speaker offers an extra audio track in its
// RTSP DESCRIBE answer when the request carries
// `Require: www.onvif.org/ver20/backchannel`. That track is marked
// `a=sendonly` (the direction is the client's: we send, the camera plays).
//
// This file is the capability probe plus the small RTSP client the talkback
// package drives the backchannel with (DESCRIBE/SETUP/PLAY/TEARDOWN over one
// TCP connection, RTP interleaved on the same connection). Nothing is
// stored: the server keeps the probe result in memory.

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BackchannelRequire is the RTSP Require tag that asks for the backchannel.
const BackchannelRequire = "www.onvif.org/ver20/backchannel"

// ErrNoBackchannel means the camera offers no PCMU backchannel track.
var ErrNoBackchannel = errors.New("the camera does not offer a G.711 µ-law audio backchannel")

// BackchannelTrack is the backchannel audio track from a DESCRIBE answer.
type BackchannelTrack struct {
	Control     string // absolute control URL to SETUP
	PayloadType int    // RTP payload type to send
	Encoding    string // upper case, e.g. "PCMU"
	ClockRate   int
}

// PCMU reports whether the track takes G.711 µ-law at 8 kHz, the only
// encoding Sentinel sends.
func (t *BackchannelTrack) PCMU() bool {
	return t != nil && t.Encoding == "PCMU" && t.ClockRate == 8000
}

// ParseBackchannelSDP finds the backchannel track in an SDP description:
// the first m=audio section marked a=sendonly. base is the URL control
// attributes are relative to (Content-Base, else the DESCRIBE URL). When
// the section offers several payloads, PCMU is preferred. nil means there
// is no backchannel.
func ParseBackchannelSDP(sdp, base string) *BackchannelTrack {
	type section struct {
		media    string
		formats  []string
		control  string
		sendonly bool
		rtpmap   map[string]string // payload → "PCMU/8000"
	}
	var sections []*section
	var cur *section
	sessionControl := ""
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 2 || line[1] != '=' {
			continue
		}
		k, v := line[0], line[2:]
		if k == 'm' {
			f := strings.Fields(v)
			cur = &section{rtpmap: map[string]string{}}
			if len(f) > 0 {
				cur.media = strings.ToLower(f[0])
			}
			if len(f) > 3 {
				cur.formats = f[3:]
			}
			sections = append(sections, cur)
			continue
		}
		if k != 'a' {
			continue
		}
		name, val, _ := strings.Cut(v, ":")
		switch strings.ToLower(name) {
		case "control":
			if cur == nil {
				sessionControl = val
			} else {
				cur.control = val
			}
		case "sendonly":
			if cur != nil {
				cur.sendonly = true
			}
		case "rtpmap":
			if cur != nil {
				pt, enc, _ := strings.Cut(val, " ")
				cur.rtpmap[strings.TrimSpace(pt)] = strings.TrimSpace(enc)
			}
		}
	}
	if sessionControl != "" && sessionControl != "*" {
		base = resolveControl(base, sessionControl)
	}
	for _, s := range sections {
		if s.media != "audio" || !s.sendonly {
			continue
		}
		t := &BackchannelTrack{Control: resolveControl(base, s.control)}
		for i, f := range s.formats {
			enc, rate := codecOf(f, s.rtpmap[f])
			if i == 0 || (enc == "PCMU" && rate == 8000) {
				t.PayloadType, _ = strconv.Atoi(f)
				t.Encoding, t.ClockRate = enc, rate
			}
			if enc == "PCMU" && rate == 8000 {
				break
			}
		}
		return t
	}
	return nil
}

// codecOf names a payload: from its rtpmap ("PCMU/8000/1"), else the static
// RTP/AVP assignment (0 = PCMU, 8 = PCMA).
func codecOf(pt, rtpmap string) (string, int) {
	if rtpmap != "" {
		parts := strings.Split(rtpmap, "/")
		rate := 0
		if len(parts) > 1 {
			rate, _ = strconv.Atoi(parts[1])
		}
		return strings.ToUpper(parts[0]), rate
	}
	switch pt {
	case "0":
		return "PCMU", 8000
	case "8":
		return "PCMA", 8000
	}
	return "", 0
}

// resolveControl turns an SDP control attribute into an absolute URL.
func resolveControl(base, control string) string {
	control = strings.TrimSpace(control)
	switch {
	case control == "" || control == "*":
		return base
	case strings.HasPrefix(strings.ToLower(control), "rtsp://") || strings.HasPrefix(strings.ToLower(control), "rtsps://"):
		return control
	}
	if strings.HasSuffix(base, "/") {
		return base + control
	}
	return base + "/" + control
}

// MARK: - RTSP client

// RTSPResponse is one RTSP answer.
type RTSPResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// RTSPConn is one RTSP control connection. Requests are serialized by the
// caller; once Drain is running, Send and WriteInterleaved may be used from
// several goroutines.
type RTSPConn struct {
	conn       net.Conn
	br         *bufio.Reader
	user, pass string

	wmu       sync.Mutex // writes
	cseq      int
	challenge string // last WWW-Authenticate, answered on every request
	session   string // Session id once SETUP answered

	// Drain mode: responses by CSeq.
	dmu     sync.Mutex
	waiters map[int]chan *RTSPResponse
	drained chan struct{}
}

const rtspUserAgent = "Sentinel talkback"

func rtspDial(ctx context.Context, network, addr string) (net.Conn, error) {
	return Dial(ctx, network, addr)
}

// DialRTSP connects to an rtsp:// URL's host (port 554 by default). The
// URL's own user info, if any, is used when user is empty.
func DialRTSP(ctx context.Context, rawURL, user, pass string) (*RTSPConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "rtsp") || u.Host == "" {
		return nil, fmt.Errorf("invalid RTSP address %q", StripUserInfo(rawURL))
	}
	if user == "" && u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "554")
	}
	conn, err := rtspDial(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach the camera's RTSP service: %w", err)
	}
	return &RTSPConn{conn: conn, br: bufio.NewReaderSize(conn, 16<<10), user: user, pass: pass}, nil
}

// StripUserInfo removes credentials from a URL for logs, messages and
// request lines.
func StripUserInfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// Close closes the connection.
func (c *RTSPConn) Close() error { return c.conn.Close() }

// Session is the session id the camera gave at SETUP ("" before).
func (c *RTSPConn) Session() string { return c.session }

// writeRequest writes one request; register, when set, learns its CSeq
// before it is on the wire (Send's waiter).
func (c *RTSPConn) writeRequest(method, uri string, hdr map[string]string, register func(cseq int)) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.cseq++
	if register != nil {
		register(c.cseq)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s RTSP/1.0\r\nCSeq: %d\r\nUser-Agent: %s\r\n", method, uri, c.cseq, rtspUserAgent)
	if c.session != "" {
		fmt.Fprintf(&b, "Session: %s\r\n", c.session)
	}
	if a := c.authorization(method, uri); a != "" {
		fmt.Fprintf(&b, "Authorization: %s\r\n", a)
	}
	for k, v := range hdr {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n")
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := io.WriteString(c.conn, b.String())
	return c.cseq, err
}

func (c *RTSPConn) authorization(method, uri string) string {
	if c.challenge == "" || c.user == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(c.challenge), "digest") {
		return digestAuthorization(c.challenge, c.user, c.pass, method, uri)
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.user+":"+c.pass))
}

// pickChallenge prefers Digest over Basic when several are offered.
func pickChallenge(h http.Header) string {
	var basic string
	for _, v := range h.Values("Www-Authenticate") {
		l := strings.ToLower(strings.TrimSpace(v))
		if strings.HasPrefix(l, "digest") {
			return strings.TrimSpace(v)
		}
		if strings.HasPrefix(l, "basic") {
			basic = strings.TrimSpace(v)
		}
	}
	return basic
}

// readResponse reads the next RTSP response, skipping interleaved frames.
func (c *RTSPConn) readResponse() (*RTSPResponse, error) {
	for {
		b, err := c.br.Peek(1)
		if err != nil {
			return nil, err
		}
		if b[0] == '$' {
			var hdr [4]byte
			if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
				return nil, err
			}
			if _, err := c.br.Discard(int(hdr[2])<<8 | int(hdr[3])); err != nil {
				return nil, err
			}
			continue
		}
		break
	}
	tp := textproto.NewReader(c.br)
	line, err := tp.ReadLine()
	if err != nil {
		return nil, err
	}
	proto, rest, _ := strings.Cut(line, " ")
	if !strings.HasPrefix(proto, "RTSP/") {
		return nil, fmt.Errorf("not an RTSP answer: %q", line)
	}
	code, _, _ := strings.Cut(rest, " ")
	status, err := strconv.Atoi(code)
	if err != nil {
		return nil, fmt.Errorf("bad RTSP status line %q", line)
	}
	mh, err := tp.ReadMIMEHeader()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	resp := &RTSPResponse{Status: status, Header: http.Header(mh)}
	if n, _ := strconv.Atoi(resp.Header.Get("Content-Length")); n > 0 {
		if n > 1<<20 {
			return nil, errors.New("RTSP answer too large")
		}
		resp.Body = make([]byte, n)
		if _, err := io.ReadFull(c.br, resp.Body); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// Do sends one request and waits for its answer, answering one auth
// challenge (Digest or Basic) along the way. Not for use once Drain runs.
func (c *RTSPConn) Do(ctx context.Context, method, uri string, hdr map[string]string) (*RTSPResponse, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetReadDeadline(dl)
		defer c.conn.SetReadDeadline(time.Time{})
	}
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetReadDeadline(time.Now()) })
	defer stop()
	for attempt := 0; ; attempt++ {
		if _, err := c.writeRequest(method, uri, hdr, nil); err != nil {
			return nil, err
		}
		resp, err := c.readResponse()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		if resp.Status == http.StatusUnauthorized && attempt == 0 && c.user != "" {
			if ch := pickChallenge(resp.Header); ch != "" {
				c.challenge = ch
				continue
			}
		}
		if resp.Status == http.StatusUnauthorized {
			return resp, ErrAuth
		}
		return resp, nil
	}
}

// SetSession records the Session header from a SETUP answer (the part
// before ";timeout=") and returns the camera's session timeout (60 s when
// it gives none).
func (c *RTSPConn) SetSession(h http.Header) time.Duration {
	v := h.Get("Session")
	id, params, _ := strings.Cut(v, ";")
	c.session = strings.TrimSpace(id)
	timeout := 60 * time.Second
	for _, p := range strings.Split(params, ";") {
		if k, val, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(k, "timeout") {
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				timeout = time.Duration(n) * time.Second
			}
		}
	}
	return timeout
}

// Drain starts reading everything the camera sends from now on (RTCP or
// stray interleaved data is discarded; answers are handed to Send's
// waiters). Use after PLAY. The returned channel closes when the
// connection ends.
func (c *RTSPConn) Drain() <-chan struct{} {
	c.dmu.Lock()
	if c.drained != nil {
		c.dmu.Unlock()
		return c.drained
	}
	c.waiters = map[int]chan *RTSPResponse{}
	c.drained = make(chan struct{})
	c.dmu.Unlock()
	go func() {
		defer close(c.drained)
		for {
			resp, err := c.readResponse()
			if err != nil {
				return
			}
			seq, _ := strconv.Atoi(resp.Header.Get("Cseq"))
			c.dmu.Lock()
			ch := c.waiters[seq]
			delete(c.waiters, seq)
			c.dmu.Unlock()
			if ch != nil {
				ch <- resp
			}
		}
	}()
	return c.drained
}

// Send writes a request while Drain runs and waits up to wait for its
// answer (0 = don't wait). A timeout is not an error: keep-alives and
// TEARDOWN are best effort.
func (c *RTSPConn) Send(method, uri string, hdr map[string]string, wait time.Duration) (*RTSPResponse, error) {
	ch := make(chan *RTSPResponse, 1)
	var register func(int)
	if wait > 0 {
		register = func(seq int) {
			c.dmu.Lock()
			if c.waiters != nil {
				c.waiters[seq] = ch
			}
			c.dmu.Unlock()
		}
	}
	if _, err := c.writeRequest(method, uri, hdr, register); err != nil || wait <= 0 {
		return nil, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(wait):
		return nil, nil
	case <-c.drained:
		return nil, io.EOF
	}
}

// WriteInterleaved writes one RTP/RTCP packet on an interleaved channel
// ("$", channel, 16-bit length, packet).
func (c *RTSPConn) WriteInterleaved(channel byte, pkt []byte) error {
	if len(pkt) > 0xFFFF {
		return errors.New("interleaved packet too large")
	}
	buf := make([]byte, 4+len(pkt))
	buf[0], buf[1], buf[2], buf[3] = '$', channel, byte(len(pkt)>>8), byte(len(pkt))
	copy(buf[4:], pkt)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.conn.Write(buf)
	return err
}

// DescribeBackchannel sends DESCRIBE with the backchannel Require tag and
// returns the backchannel track (nil when the camera has none: no
// sendonly audio, or it refused the Require tag) and the base URL the
// aggregate control (PLAY, TEARDOWN) uses.
func (c *RTSPConn) DescribeBackchannel(ctx context.Context, uri string) (*BackchannelTrack, string, error) {
	resp, err := c.Do(ctx, "DESCRIBE", uri, map[string]string{"Accept": "application/sdp", "Require": BackchannelRequire})
	if err != nil {
		return nil, "", err
	}
	switch {
	case resp.Status == 551 || resp.Status == 400 || resp.Status == 454 || resp.Status == 405 || resp.Status == 501:
		return nil, "", nil // "Option not supported" and friends: no backchannel
	case resp.Status < 200 || resp.Status > 299:
		return nil, "", fmt.Errorf("camera answered DESCRIBE with RTSP %d", resp.Status)
	}
	base := firstNonEmpty(resp.Header.Get("Content-Base"), resp.Header.Get("Content-Location"), uri)
	base = StripUserInfo(base)
	return ParseBackchannelSDP(string(resp.Body), base), base, nil
}

// ProbeBackchannel asks one RTSP address whether it offers a PCMU
// backchannel: (false, nil) when the camera answers without one.
func ProbeBackchannel(ctx context.Context, rtspURL, user, pass string) (bool, error) {
	c, err := DialRTSP(ctx, rtspURL, user, pass)
	if err != nil {
		return false, err
	}
	defer c.Close()
	t, _, err := c.DescribeBackchannel(ctx, StripUserInfo(rtspURL))
	if err != nil {
		return false, err
	}
	return t.PCMU(), nil
}

// TalkbackSupported is the talkback capability probe. Candidates are the
// main profile's stream address from ONVIF GetStreamUri (when serviceURL is
// set) and the camera's stored rtspURL; the first that offers a PCMU
// backchannel wins and is returned. (false, "", nil) means the camera
// answered and has none; an error means it could not be asked.
func TalkbackSupported(ctx context.Context, serviceURL, rtspURL, user, pass string) (bool, string, error) {
	var candidates []string
	var firstErr error
	if serviceURL != "" {
		uri, err := mainStreamURI(ctx, serviceURL, user, pass)
		if err != nil {
			if errors.Is(err, ErrAuth) {
				return false, "", err
			}
			firstErr = err
		} else {
			candidates = append(candidates, uri)
		}
	}
	if rtspURL != "" && (len(candidates) == 0 || StripUserInfo(candidates[0]) != StripUserInfo(rtspURL)) {
		candidates = append(candidates, rtspURL)
	}
	answered := false
	for _, u := range candidates {
		ok, err := ProbeBackchannel(ctx, u, user, pass)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		answered = true
		if ok {
			return true, StripUserInfo(u), nil
		}
	}
	if answered {
		return false, "", nil
	}
	if firstErr == nil {
		firstErr = errors.New("the camera has no RTSP address to probe")
	}
	return false, "", firstErr
}

// mainStreamURI is the recording (main) profile's RTSP address via
// GetStreamUri, chosen the way discovery chooses it.
func mainStreamURI(ctx context.Context, serviceURL, user, pass string) (string, error) {
	c := newEncoderClient(user, pass)
	dev, media, err := c.openMedia(ctx, serviceURL)
	if err != nil {
		return "", err
	}
	profiles, err := c.profiles(ctx, media)
	if err != nil && media.String() != dev.String() {
		media = dev
		profiles, err = c.profiles(ctx, media)
	}
	if err != nil {
		return "", err
	}
	main, _ := ChooseStreams(profiles)
	if main == nil {
		return "", errors.New("the camera did not return any media profiles")
	}
	// WAVE9(B): Media2 (ver20) GetStreamUri is not tried; every camera seen so
	// far answers Media1, and the backchannel itself is negotiated in RTSP.
	return c.streamURI(ctx, media, main.Token)
}

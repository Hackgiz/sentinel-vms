package fakecam

// A minimal RTSP server with an ONVIF audio backchannel, for talkback
// tests. DESCRIBE with `Require: www.onvif.org/ver20/backchannel` answers
// an SDP with a video track and a sendonly PCMU track (without the tag,
// or with Backchannel off, only video, or 551 when Refuse551 is set).
// SETUP must be RTP/AVP/TCP interleaved; after PLAY the server reads the
// interleaved RTP and counts packets and checksums their payloads, so tests
// can assert end to end that every 20 ms frame arrived intact.
//
// Point a Stream's RTSPURL at Backchannel.URL() to make the ONVIF camera
// advertise it through GetStreamUri.

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
)

// Backchannel is the fake camera's RTSP endpoint.
type Backchannel struct {
	// User/Pass, when set, require RTSP Digest auth (realm "fakecam").
	User, Pass string
	// Off serves video only even when the backchannel is required.
	Off bool
	// Refuse551 answers a backchannel DESCRIBE with 551 Option not supported
	// (what many cameras without a speaker do) instead of video only.
	Refuse551 bool
	// Encoding of the backchannel track (default "PCMU/8000").
	Encoding string

	ln net.Listener

	mu      sync.Mutex
	stats   BackchannelStats
	crc     uint32
	lastTS  uint32
	lastLen int
}

// BackchannelStats is what the endpoint has seen.
type BackchannelStats struct {
	Describes, Setups, Plays, Teardowns, KeepAlives int
	Packets                                         int    // RTP packets received on the backchannel
	PayloadBytes                                    int    // µ-law bytes received
	Checksum                                        uint32 // CRC-32 (IEEE) of every payload, in arrival order
	Markers                                         int    // packets with the RTP marker bit
	PayloadTypes                                    map[int]int
	FirstSeq                                        uint16
	LastSeq                                         uint16
	SeqGaps                                         int // packets whose sequence number did not follow the last
	TSGaps                                          int // packets whose timestamp did not advance by the last payload length
	SSRCs                                           map[uint32]bool
	Connections                                     int // TCP connections closed so far
	Payload                                         []byte
}

// Start listens on 127.0.0.1 (an OS-chosen free port) and serves until Close.
func (b *Backchannel) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	b.ln = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go b.serve(c)
		}
	}()
	return nil
}

// URL is the stream address ("rtsp://127.0.0.1:port/stream1").
func (b *Backchannel) URL() string { return "rtsp://" + b.ln.Addr().String() + "/stream1" }

// Close stops listening (open sessions run on until their client leaves).
func (b *Backchannel) Close() error { return b.ln.Close() }

// Stats returns a copy of what the endpoint has seen.
func (b *Backchannel) Stats() BackchannelStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stats
	s.Checksum = b.crc
	s.PayloadTypes = map[int]int{}
	for k, v := range b.stats.PayloadTypes {
		s.PayloadTypes[k] = v
	}
	s.SSRCs = map[uint32]bool{}
	for k := range b.stats.SSRCs {
		s.SSRCs[k] = true
	}
	s.Payload = append([]byte(nil), b.stats.Payload...)
	return s
}

// Reset forgets everything seen so far.
func (b *Backchannel) Reset() {
	b.mu.Lock()
	b.stats, b.crc = BackchannelStats{}, 0
	b.mu.Unlock()
}

const bcNonce = "fakecamnonce42"

func (b *Backchannel) sdp(host string, back bool) string {
	enc := b.Encoding
	if enc == "" {
		enc = "PCMU/8000"
	}
	pt := "0"
	if !strings.HasPrefix(enc, "PCMU") {
		pt = "97"
	}
	s := "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=fakecam\r\nc=IN IP4 0.0.0.0\r\nt=0 0\r\na=control:*\r\n" +
		"m=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:track1\r\na=recvonly\r\n" +
		"m=audio 0 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=control:track2\r\na=recvonly\r\n"
	if back {
		s += "m=audio 0 RTP/AVP " + pt + "\r\na=rtpmap:" + pt + " " + enc + "\r\na=control:track3\r\na=sendonly\r\n"
	}
	return s
}

func (b *Backchannel) authorized(method string, h textproto.MIMEHeader) bool {
	if b.User == "" {
		return true
	}
	a := h.Get("Authorization")
	if !strings.HasPrefix(a, "Digest ") {
		return false
	}
	p := map[string]string{}
	for _, part := range strings.Split(a[len("Digest "):], ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			p[k] = strings.Trim(v, `"`)
		}
	}
	md := func(s string) string { x := md5.Sum([]byte(s)); return hex.EncodeToString(x[:]) }
	ha1 := md(b.User + ":fakecam:" + b.Pass)
	ha2 := md(method + ":" + p["uri"])
	want := md(ha1 + ":" + bcNonce + ":" + ha2)
	if p["qop"] != "" {
		want = md(ha1 + ":" + bcNonce + ":" + p["nc"] + ":" + p["cnonce"] + ":" + p["qop"] + ":" + ha2)
	}
	return p["username"] == b.User && p["response"] == want
}

func (b *Backchannel) serve(conn net.Conn) {
	defer func() {
		conn.Close()
		b.mu.Lock()
		b.stats.Connections++
		b.mu.Unlock()
	}()
	br := bufio.NewReader(conn)
	tp := textproto.NewReader(br)
	session := ""
	playing := false
	for {
		if playing {
			if c, err := br.Peek(1); err == nil && c[0] == '$' {
				var hdr [4]byte
				if _, err := io.ReadFull(br, hdr[:]); err != nil {
					return
				}
				pkt := make([]byte, int(hdr[2])<<8|int(hdr[3]))
				if _, err := io.ReadFull(br, pkt); err != nil {
					return
				}
				if hdr[1] == 0 {
					b.rtp(pkt)
				}
				continue
			}
		}
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return
		}
		method := f[0]
		h, err := tp.ReadMIMEHeader()
		if err != nil {
			return
		}
		if n, _ := strconv.Atoi(h.Get("Content-Length")); n > 0 {
			if _, err := io.CopyN(io.Discard, br, int64(n)); err != nil {
				return
			}
		}
		cseq := h.Get("Cseq")
		write := func(status string, extra string, body string) {
			out := "RTSP/1.0 " + status + "\r\nCSeq: " + cseq + "\r\n" + extra
			if body != "" {
				out += "Content-Type: application/sdp\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n"
			}
			_, _ = io.WriteString(conn, out+"\r\n"+body)
		}
		if method != "OPTIONS" && !b.authorized(method, h) {
			write("401 Unauthorized", `WWW-Authenticate: Digest realm="fakecam", nonce="`+bcNonce+`"`+"\r\n", "")
			continue
		}
		back := strings.Contains(h.Get("Require"), "www.onvif.org/ver20/backchannel")
		switch method {
		case "OPTIONS":
			write("200 OK", "Public: OPTIONS, DESCRIBE, SETUP, PLAY, TEARDOWN, GET_PARAMETER\r\n", "")
		case "DESCRIBE":
			b.mu.Lock()
			b.stats.Describes++
			b.mu.Unlock()
			if back && b.Refuse551 {
				write("551 Option not supported", "Unsupported: www.onvif.org/ver20/backchannel\r\n", "")
				continue
			}
			write("200 OK", "Content-Base: "+b.URL()+"/\r\n", b.sdp(conn.LocalAddr().String(), back && !b.Off))
		case "SETUP":
			tr := h.Get("Transport")
			if !back || b.Off || !strings.HasSuffix(f[1], "/track3") || !strings.Contains(tr, "RTP/AVP/TCP") || !strings.Contains(tr, "interleaved=0-1") {
				write("461 Unsupported Transport", "", "")
				continue
			}
			b.mu.Lock()
			b.stats.Setups++
			b.mu.Unlock()
			session = fmt.Sprintf("FAKE%d", len(f[1]))
			write("200 OK", "Session: "+session+";timeout=60\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n", "")
		case "PLAY":
			if session == "" || h.Get("Session") != session {
				write("454 Session Not Found", "", "")
				continue
			}
			b.mu.Lock()
			b.stats.Plays++
			b.mu.Unlock()
			playing = true
			write("200 OK", "Session: "+session+"\r\n", "")
		case "GET_PARAMETER", "SET_PARAMETER":
			b.mu.Lock()
			b.stats.KeepAlives++
			b.mu.Unlock()
			write("200 OK", "Session: "+session+"\r\n", "")
		case "TEARDOWN":
			b.mu.Lock()
			b.stats.Teardowns++
			b.mu.Unlock()
			write("200 OK", "", "")
			return
		default:
			write("501 Not Implemented", "", "")
		}
	}
}

// rtp records one backchannel RTP packet.
func (b *Backchannel) rtp(pkt []byte) {
	if len(pkt) < 12 || pkt[0]>>6 != 2 {
		return
	}
	cc := int(pkt[0] & 0x0F)
	hl := 12 + 4*cc
	if len(pkt) < hl {
		return
	}
	marker := pkt[1]&0x80 != 0
	pt := int(pkt[1] & 0x7F)
	seq := uint16(pkt[2])<<8 | uint16(pkt[3])
	ts := uint32(pkt[4])<<24 | uint32(pkt[5])<<16 | uint32(pkt[6])<<8 | uint32(pkt[7])
	ssrc := uint32(pkt[8])<<24 | uint32(pkt[9])<<16 | uint32(pkt[10])<<8 | uint32(pkt[11])
	payload := pkt[hl:]
	b.mu.Lock()
	defer b.mu.Unlock()
	s := &b.stats
	if s.PayloadTypes == nil {
		s.PayloadTypes, s.SSRCs = map[int]int{}, map[uint32]bool{}
	}
	if s.Packets == 0 {
		s.FirstSeq = seq
	} else {
		if seq != s.LastSeq+1 {
			s.SeqGaps++
		}
		if ts != b.lastTS+uint32(b.lastLen) {
			s.TSGaps++
		}
	}
	s.Packets++
	s.LastSeq = seq
	b.lastTS, b.lastLen = ts, len(payload)
	s.PayloadBytes += len(payload)
	s.PayloadTypes[pt]++
	s.SSRCs[ssrc] = true
	if marker {
		s.Markers++
	}
	s.Payload = append(s.Payload, payload...)
	b.crc = crc32.Update(b.crc, crc32.IEEETable, payload)
}

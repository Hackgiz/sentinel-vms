package talkback

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/onvif"
)

// Session is one open backchannel: DESCRIBE (with the backchannel Require
// tag), SETUP of the backchannel track over RTP/AVP/TCP interleaved, PLAY,
// then RTP on the interleaved channel until Close sends TEARDOWN.
type Session struct {
	conn    *onvif.RTSPConn
	base    string // aggregate control URL (PLAY, keep-alive, TEARDOWN)
	channel byte   // interleaved RTP channel
	pk      *Packetizer

	pending []int16 // samples waiting for a full 20 ms frame
	frame   []byte
	last    time.Time // when the last packet went out
	now     func() time.Time

	stop      chan struct{}
	closeOnce sync.Once
	closeErr  error

	mu      sync.Mutex
	packets int
}

// Talkspurt gap: a pause longer than this between frames starts a new
// talkspurt (marker bit, timestamp moved over the silence). Well above
// network jitter, well below a release-and-press of the talk button.
const spurtGap = 500 * time.Millisecond

// Dial opens the backchannel of the camera at rtspURL.
func Dial(ctx context.Context, rtspURL, user, pass string) (*Session, error) {
	conn, err := onvif.DialRTSP(ctx, rtspURL, user, pass)
	if err != nil {
		return nil, err
	}
	s, err := open(ctx, conn, onvif.StripUserInfo(rtspURL))
	if err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

func open(ctx context.Context, conn *onvif.RTSPConn, uri string) (*Session, error) {
	track, base, err := conn.DescribeBackchannel(ctx, uri)
	if err != nil {
		return nil, err
	}
	if !track.PCMU() {
		return nil, onvif.ErrNoBackchannel
	}
	require := map[string]string{"Require": onvif.BackchannelRequire}
	setup := map[string]string{"Require": onvif.BackchannelRequire, "Transport": "RTP/AVP/TCP;unicast;interleaved=0-1"}
	resp, err := conn.Do(ctx, "SETUP", track.Control, setup)
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		return nil, fmt.Errorf("camera refused the backchannel SETUP (RTSP %d)", resp.Status)
	}
	timeout := conn.SetSession(resp.Header)
	if conn.Session() == "" {
		return nil, errors.New("camera answered SETUP without a session")
	}
	channel := interleavedChannel(resp.Header.Get("Transport"))
	play := map[string]string{"Require": onvif.BackchannelRequire, "Range": "npt=0.000-"}
	if resp, err = conn.Do(ctx, "PLAY", base, play); err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		return nil, fmt.Errorf("camera refused PLAY on the backchannel (RTSP %d)", resp.Status)
	}
	var r [8]byte
	_, _ = rand.Read(r[:])
	s := &Session{
		conn: conn, base: base, channel: channel,
		pk:  NewPacketizer(binary.BigEndian.Uint32(r[:4]), binary.BigEndian.Uint16(r[4:6]), uint32(binary.BigEndian.Uint16(r[6:]))<<8),
		now: time.Now, stop: make(chan struct{}),
	}
	conn.Drain()
	go s.keepAlive(timeout, require)
	return s, nil
}

// interleavedChannel reads "interleaved=a-b" from a Transport header (the
// camera may move the channel); 0 when absent.
func interleavedChannel(transport string) byte {
	for _, p := range strings.Split(transport, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(k, "interleaved") {
			a, _, _ := strings.Cut(v, "-")
			var n int
			if _, err := fmt.Sscanf(a, "%d", &n); err == nil && n >= 0 && n < 256 {
				return byte(n)
			}
		}
	}
	return 0
}

// keepAlive sends GET_PARAMETER at half the camera's session timeout, so a
// quiet operator does not lose the session before our own idle limit.
func (s *Session) keepAlive(timeout time.Duration, hdr map[string]string) {
	every := timeout / 2
	if every < time.Second {
		every = time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			if _, err := s.conn.Send("GET_PARAMETER", s.base, hdr, 0); err != nil {
				return
			}
		}
	}
}

// WritePCM queues 8 kHz 16-bit samples and sends every complete 20 ms
// frame as one PCMU packet. A partial frame waits for more samples.
func (s *Session) WritePCM(samples []int16) error {
	s.pending = append(s.pending, samples...)
	for len(s.pending) >= FrameSamples {
		now := s.now()
		if !s.last.IsZero() {
			if gap := now.Sub(s.last); gap > spurtGap {
				// Account for the silence in whole frames, beyond the frame
				// that was due anyway.
				s.pk.Skip(int(gap/(20*time.Millisecond)-1) * FrameSamples)
			}
		}
		s.frame = EncodeMuLaw(s.frame[:0], s.pending[:FrameSamples])
		if err := s.conn.WriteInterleaved(s.channel, s.pk.Packet(s.frame, FrameSamples)); err != nil {
			return err
		}
		s.last = now
		s.pending = s.pending[FrameSamples:]
		s.mu.Lock()
		s.packets++
		s.mu.Unlock()
	}
	if len(s.pending) == 0 {
		s.pending = s.pending[:0:0]
	}
	return nil
}

// Packets is how many RTP packets have been sent.
func (s *Session) Packets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.packets
}

// Close sends TEARDOWN (waiting briefly for the answer) and closes the
// connection. A trailing partial frame (under 20 ms) is dropped.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		close(s.stop)
		_, s.closeErr = s.conn.Send("TEARDOWN", s.base, nil, 2*time.Second)
		if err := s.conn.Close(); s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

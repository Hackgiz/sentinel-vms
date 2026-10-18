package talkback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"math"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/onvif/fakecam"
)

// Vectors from the ITU-T G.191 reference ulaw_compress. The positive half
// was cross-checked against an independent implementation (CPython's
// audioop.lin2ulaw, which agrees on every non-negative sample; it rounds
// negatives differently, e.g. -1 → 0x7E, so those come from G.191 alone).
func TestMuLawKnownVectors(t *testing.T) {
	vectors := []struct {
		in   int16
		want byte
	}{
		{0, 0xFF}, {1, 0xFF}, {3, 0xFF}, {4, 0xFE}, {-1, 0x7F}, {-4, 0x7F}, {-5, 0x7E},
		{33, 0xFB}, {-33, 0x7B}, {100, 0xF2}, {-100, 0x73}, {123, 0xF0},
		{1000, 0xCE}, {-1000, 0x4E}, {4095, 0xAF}, {-4096, 0x2F}, {5000, 0xAB}, {-5000, 0x2B},
		{8031, 0xA0}, {-8031, 0x20}, {8158, 0x9F}, {16000, 0x90}, {-16000, 0x10},
		{32767, 0x80}, {-32767, 0x00}, {-32768, 0x00},
	}
	for _, v := range vectors {
		if got := MuLaw(v.in); got != v.want {
			t.Errorf("MuLaw(%d) = 0x%02X, want 0x%02X", v.in, got, v.want)
		}
		if got := ulawReference(v.in); got != v.want {
			t.Errorf("reference(%d) = 0x%02X, want 0x%02X", v.in, got, v.want)
		}
	}
	// The whole table, every 16-bit input, against a digest of the G.191
	// reference output (computed outside Go).
	all := make([]int16, 0, 65536)
	for x := -32768; x <= 32767; x++ {
		all = append(all, int16(x))
	}
	sum := sha256.Sum256(EncodeMuLaw(nil, all))
	if got := hex.EncodeToString(sum[:]); got != "90c29de505fb68e766118303bd552a16005dcf810873698bee1d8f3b247ce28c" {
		t.Fatalf("µ-law table digest %s", got)
	}
	// Table lookup equals the algorithm for every input.
	for _, x := range all {
		if MuLaw(x) != ulawReference(x) {
			t.Fatalf("table and reference disagree at %d", x)
		}
	}
}

func TestMuLawDecode(t *testing.T) {
	for b, want := range map[byte]int16{0x00: -32124, 0x80: 32124, 0xFF: 0, 0x7F: 0, 0x0F: -16764, 0x8F: 16764, 0x55: -716} {
		if got := MuLawDecode(b); got != want {
			t.Errorf("decode 0x%02X = %d, want %d", b, got, want)
		}
	}
	// Round trip stays within the quantization step of the segment.
	for x := -32000; x <= 32000; x += 37 {
		d := int(MuLawDecode(MuLaw(int16(x))))
		if diff := math.Abs(float64(d - x)); diff > math.Max(16, math.Abs(float64(x))/16) {
			t.Fatalf("round trip %d → %d", x, d)
		}
	}
}

func tone(freq float64, rate, n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

func rms(x []int16) float64 {
	s := 0.0
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(x)))
}

func TestHalverDownsample16kTo8k(t *testing.T) {
	var h Halver
	in := tone(1000, 16000, 16000, 10000)
	out := h.Process(nil, in)
	if len(out) != 8000 {
		t.Fatalf("1 s at 16 kHz gave %d samples, want 8000", len(out))
	}
	// 1 kHz passes at about unity (skip the filter's warm-up).
	if r := rms(out[100:]) / rms(in[200:]); r < 0.97 || r > 1.03 {
		t.Fatalf("1 kHz gain %.3f", r)
	}
	// 6 kHz would alias to 2 kHz; it must be crushed.
	var h2 Halver
	hi := h2.Process(nil, tone(6000, 16000, 16000, 10000))
	if r := rms(hi[100:]) / 7071; r > 0.01 {
		t.Fatalf("6 kHz leaks at %.4f (-40 dB wanted)", r)
	}
	// DC is kept exactly.
	var h3 Halver
	dc := make([]int16, 400)
	for i := range dc {
		dc[i] = 1234
	}
	if got := h3.Process(nil, dc); got[len(got)-1] != 1234 {
		t.Fatalf("DC 1234 → %d", got[len(got)-1])
	}
	// Chunking doesn't change the result (state carries over), including odd chunk sizes.
	var a, b Halver
	whole := a.Process(nil, in[:999])
	var parts []int16
	for i := 0; i < 999; {
		n := min(1+i%7, 999-i)
		parts = b.Process(parts, in[i:i+n])
		i += n
	}
	if len(whole) != len(parts) {
		t.Fatalf("chunked length %d vs %d", len(parts), len(whole))
	}
	for i := range whole {
		if whole[i] != parts[i] {
			t.Fatalf("chunked output differs at %d", i)
		}
	}
	// Full-scale input clamps instead of wrapping.
	var c Halver
	sq := make([]int16, 200)
	for i := range sq {
		sq[i] = 32767
	}
	// Past the 15-sample warm-up the filter rings slightly above full scale.
	for _, v := range c.Process(nil, sq)[16:] {
		if v < 32000 {
			t.Fatalf("full scale came out as %d (wrapped?)", v)
		}
	}
}

func TestPacketizer(t *testing.T) {
	p := NewPacketizer(0xDEADBEEF, 0xFFFE, 0xFFFFFF00)
	payload := make([]byte, FrameSamples)
	for i := range payload {
		payload[i] = byte(i)
	}
	type hdr struct {
		marker bool
		seq    uint16
		ts     uint32
	}
	parse := func(b []byte) hdr {
		if len(b) != 12+FrameSamples || b[0] != 0x80 || b[1]&0x7F != 0 {
			t.Fatalf("bad header % x", b[:12])
		}
		if b[8] != 0xDE || b[9] != 0xAD || b[10] != 0xBE || b[11] != 0xEF {
			t.Fatalf("ssrc % x", b[8:12])
		}
		if string(b[12:]) != string(payload) {
			t.Fatal("payload mangled")
		}
		return hdr{b[1]&0x80 != 0, uint16(b[2])<<8 | uint16(b[3]), uint32(b[4])<<24 | uint32(b[5])<<16 | uint32(b[6])<<8 | uint32(b[7])}
	}
	got := []hdr{parse(p.Packet(payload, FrameSamples)), parse(p.Packet(payload, FrameSamples)), parse(p.Packet(payload, FrameSamples))}
	p.Skip(5 * FrameSamples)
	got = append(got, parse(p.Packet(payload, FrameSamples)))
	p.Talkspurt()
	got = append(got, parse(p.Packet(payload, FrameSamples)))
	want := []hdr{
		{true, 0xFFFE, 0xFFFFFF00},        // first packet: marker
		{false, 0xFFFF, 0xFFFFFF00 + 160}, //
		{false, 0x0000, 64},               // sequence and timestamp wrap
		{true, 0x0001, 320 + 160*6 - 256}, // skip of 5 frames + marker
		{true, 0x0002, 320 + 160*7 - 256}, // explicit talkspurt
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("packet %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestInterleavedChannel(t *testing.T) {
	for in, want := range map[string]byte{"RTP/AVP/TCP;unicast;interleaved=0-1": 0, "RTP/AVP/TCP;unicast;interleaved=4-5;ssrc=1": 4, "RTP/AVP/TCP": 0} {
		if got := interleavedChannel(in); got != want {
			t.Errorf("%q → %d", in, got)
		}
	}
}

func startCam(t *testing.T, b *fakecam.Backchannel) *fakecam.Backchannel {
	t.Helper()
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSessionEndToEndAgainstFakecam(t *testing.T) {
	cam := startCam(t, &fakecam.Backchannel{User: "admin", Pass: "pw"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Dial(ctx, cam.URL(), "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	pcm := tone(440, 8000, 50*FrameSamples+37, 8000) // one second plus a partial frame
	// Feed in uneven chunks, as a WebSocket would.
	for i := 0; i < len(pcm); {
		n := min(97, len(pcm)-i)
		if err := s.WritePCM(pcm[i : i+n]); err != nil {
			t.Fatal(err)
		}
		i += n
	}
	if s.Packets() != 50 {
		t.Fatalf("sent %d packets, want 50", s.Packets())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "teardown", func() bool { return cam.Stats().Teardowns == 1 })
	st := cam.Stats()
	want := EncodeMuLaw(nil, pcm[:50*FrameSamples])
	if st.Packets != 50 || st.PayloadBytes != 50*FrameSamples || st.Checksum != crc32.ChecksumIEEE(want) {
		t.Fatalf("camera got %d packets, %d bytes, crc %08x; want 50, %d, %08x", st.Packets, st.PayloadBytes, st.Checksum, len(want), crc32.ChecksumIEEE(want))
	}
	if st.SeqGaps != 0 || st.TSGaps != 0 || st.Markers != 1 || st.PayloadTypes[0] != 50 || len(st.SSRCs) != 1 {
		t.Fatalf("stream shape: %+v", st)
	}
	if st.Describes != 1 || st.Setups != 1 || st.Plays != 1 {
		t.Fatalf("handshake: %+v", st)
	}
	if err := s.Close(); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestSessionTalkspurtGap(t *testing.T) {
	cam := startCam(t, &fakecam.Backchannel{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Dial(ctx, cam.URL(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	frame := make([]int16, FrameSamples)
	for i := 0; i < 3; i++ {
		_ = s.WritePCM(frame)
		now = now.Add(20 * time.Millisecond)
	}
	now = now.Add(2 * time.Second) // the operator let go of the button
	_ = s.WritePCM(frame)
	_ = s.Close()
	waitFor(t, "teardown", func() bool { return cam.Stats().Teardowns == 1 })
	st := cam.Stats()
	if st.Packets != 4 || st.Markers != 2 || st.TSGaps != 1 || st.SeqGaps != 0 {
		t.Fatalf("gap handling: %+v", st)
	}
}

func TestDialWithoutBackchannel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, b := range []*fakecam.Backchannel{{Off: true}, {Refuse551: true}, {Encoding: "PCMA/8000"}} {
		cam := startCam(t, b)
		if _, err := Dial(ctx, cam.URL(), "", ""); !errors.Is(err, onvif.ErrNoBackchannel) {
			t.Fatalf("%+v: %v, want ErrNoBackchannel", b, err)
		}
	}
	cam := startCam(t, &fakecam.Backchannel{User: "u", Pass: "p"})
	if _, err := Dial(ctx, cam.URL(), "u", "nope"); !errors.Is(err, onvif.ErrAuth) {
		t.Fatalf("bad password: %v", err)
	}
}

// fakeSink records what a talk sends.
type fakeSink struct {
	mu      sync.Mutex
	samples int
	closed  int
	fail    bool
}

func (f *fakeSink) WritePCM(s []int16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("camera went away")
	}
	f.samples += len(s)
	return nil
}

func (f *fakeSink) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return nil
}

func (f *fakeSink) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.samples, f.closed
}

func TestManagerOnePerCamera(t *testing.T) {
	m := NewManager()
	a, err := m.Reserve("cam1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reserve("cam1", "bob"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second talk: %v, want ErrBusy", err)
	}
	if _, err := m.Reserve("cam2", "bob"); err != nil {
		t.Fatalf("another camera: %v", err)
	}
	if m.Active("cam1") != "alice" {
		t.Fatal("Active wrong")
	}
	sink := &fakeSink{}
	a.Attach(sink, 16000)
	if err := a.Feed(make([]byte, 640)); err != nil { // 320 samples at 16 kHz
		t.Fatal(err)
	}
	if n, _ := sink.counts(); n != 160 {
		t.Fatalf("16 kHz input reached the sink as %d samples, want 160", n)
	}
	_, reason := a.End(EndClosed)
	_, reason2 := a.End(EndIdle)
	if reason != EndClosed || reason2 != EndClosed {
		t.Fatalf("reasons %q %q", reason, reason2)
	}
	if _, c := sink.counts(); c != 1 {
		t.Fatalf("sink closed %d times", c)
	}
	if err := a.Feed(make([]byte, 2)); err == nil {
		t.Fatal("feed after end accepted")
	}
	if _, err := m.Reserve("cam1", "bob"); err != nil {
		t.Fatalf("camera not freed: %v", err)
	}
}

func TestTalkIdleAndLimit(t *testing.T) {
	m := &Manager{IdleTimeout: 80 * time.Millisecond, MaxDuration: time.Hour}
	idle, _ := m.Reserve("a", "u")
	idle.Attach(&fakeSink{}, 8000)
	// Keep talking past the idle timeout: no end.
	for i := 0; i < 5; i++ {
		time.Sleep(30 * time.Millisecond)
		if err := idle.Feed(make([]byte, 320)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-idle.Done():
		t.Fatal("ended while audio was flowing")
	default:
	}
	select {
	case <-idle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("idle timeout never fired")
	}
	if d, r := idle.End(EndClosed); r != EndIdle || d <= 0 {
		t.Fatalf("idle end: %v %q", d, r)
	}

	m2 := &Manager{IdleTimeout: time.Hour, MaxDuration: 100 * time.Millisecond}
	capped, _ := m2.Reserve("a", "u")
	sink := &fakeSink{}
	capped.Attach(sink, 8000)
	stop := time.After(2 * time.Second)
	for {
		select {
		case <-capped.Done():
			if d, r := capped.End(EndClosed); r != EndLimit || d < 100*time.Millisecond {
				t.Fatalf("cap end: %v %q", d, r)
			}
			if _, c := sink.counts(); c != 1 {
				t.Fatal("sink not closed at the cap")
			}
			return
		case <-stop:
			t.Fatal("hard cap never fired")
		default:
			_ = capped.Feed(make([]byte, 320))
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestTalkSinkFailureEnds(t *testing.T) {
	m := NewManager()
	tk, _ := m.Reserve("a", "u")
	tk.Attach(&fakeSink{fail: true}, 8000)
	if err := tk.Feed(make([]byte, 320)); err == nil {
		t.Fatal("expected error")
	}
	<-tk.Done()
	if tk.Reason() != EndError {
		t.Fatalf("reason %q", tk.Reason())
	}
	tk.End(EndClosed)
}

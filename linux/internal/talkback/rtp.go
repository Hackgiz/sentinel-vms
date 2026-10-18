package talkback

// RTP (RFC 3550) packetization for PCMU (RFC 3551): payload type 0, 8 kHz
// clock, one packet per 20 ms (160 samples, 160 bytes). The timestamp
// advances by the samples in each packet; the marker bit flags the first
// packet of a talkspurt (after silence), so the camera can reset its
// jitter buffer.

const (
	PayloadPCMU    = 0
	ClockRate      = 8000
	FrameSamples   = ClockRate / 50 // 20 ms
	rtpHeaderBytes = 12
)

// Packetizer numbers RTP packets for one stream.
type Packetizer struct {
	PayloadType byte
	SSRC        uint32
	Seq         uint16 // of the next packet
	Timestamp   uint32 // of the next packet
	mark        bool
}

// NewPacketizer starts a PCMU stream with the given SSRC and initial
// sequence number and timestamp (RFC 3550 wants them random; tests pin
// them). The first packet carries the marker.
func NewPacketizer(ssrc uint32, seq uint16, ts uint32) *Packetizer {
	return &Packetizer{PayloadType: PayloadPCMU, SSRC: ssrc, Seq: seq, Timestamp: ts, mark: true}
}

// Talkspurt marks the next packet as the start of a talkspurt.
func (p *Packetizer) Talkspurt() { p.mark = true }

// Packet wraps one payload of `samples` samples and advances the sequence
// number and timestamp. Silence the caller skipped is accounted for with
// Skip, which keeps the timestamp honest.
func (p *Packetizer) Packet(payload []byte, samples int) []byte {
	b := make([]byte, rtpHeaderBytes+len(payload))
	b[0] = 0x80 // V=2, no padding, no extension, no CSRC
	b[1] = p.PayloadType & 0x7F
	if p.mark {
		b[1] |= 0x80
		p.mark = false
	}
	b[2], b[3] = byte(p.Seq>>8), byte(p.Seq)
	b[4], b[5], b[6], b[7] = byte(p.Timestamp>>24), byte(p.Timestamp>>16), byte(p.Timestamp>>8), byte(p.Timestamp)
	b[8], b[9], b[10], b[11] = byte(p.SSRC>>24), byte(p.SSRC>>16), byte(p.SSRC>>8), byte(p.SSRC)
	copy(b[rtpHeaderBytes:], payload)
	p.Seq++
	p.Timestamp += uint32(samples)
	return b
}

// Skip advances the timestamp over samples that were not sent (a gap
// between talkspurts) and marks the next packet.
func (p *Packetizer) Skip(samples int) {
	p.Timestamp += uint32(samples)
	p.mark = true
}

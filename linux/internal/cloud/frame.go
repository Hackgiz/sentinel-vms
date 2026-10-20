package cloud

// Relay wire protocol framing (contract section 3). Every WebSocket message
// is binary:
//
//	byte 0      type
//	bytes 1..4  stream id, uint32 big-endian (0 = control)
//	bytes 5..   payload

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Frame types.
const (
	FrameHello   byte = 1  // DO→home: Entitlement JSON
	FrameReq     byte = 2  // DO→home: request head
	FrameRes     byte = 3  // home→DO: response head
	FrameData    byte = 4  // both: body chunk ≤ 1 MiB
	FrameEnd     byte = 5  // both: end of body
	FrameRst     byte = 6  // both: abort the stream
	FrameWSOpen  byte = 7  // DO→home: bridge a client WebSocket
	FrameWSOK    byte = 8  // home→DO: the bridge's answer
	FrameWSMsg   byte = 9  // both: byte 0 = 1 text / 2 binary, then the message
	FrameWSClose byte = 10 // both: {"code","reason"}
)

// Limits and close codes.
const (
	MaxDataChunk  = 1 << 20 // largest DATA payload
	MaxStreams    = 64      // open streams per connection
	frameHeader   = 5
	maxHeadJSON   = 256 << 10 // REQ/RES/WSOPEN/HELLO JSON
	maxFrameBytes = frameHeader + 2*MaxDataChunk

	CloseTokenRevoked = 4001
	ClosePlanInactive = 4003
	CloseReplaced     = 4009
)

// Frame is one relay message.
type Frame struct {
	Type    byte
	Stream  uint32
	Payload []byte
}

var errFrame = errors.New("relay: malformed frame")

// EncodeFrame builds one message.
func EncodeFrame(typ byte, stream uint32, payload []byte) []byte {
	out := make([]byte, frameHeader+len(payload))
	putHeader(out, typ, stream)
	copy(out[frameHeader:], payload)
	return out
}

func putHeader(b []byte, typ byte, stream uint32) {
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], stream)
}

// DecodeFrame parses one message strictly: a known type, HELLO on the
// control stream and everything else on a stream, size limits per type.
// Payload aliases b.
func DecodeFrame(b []byte) (Frame, error) {
	if len(b) < frameHeader {
		return Frame{}, fmt.Errorf("%w: %d bytes", errFrame, len(b))
	}
	f := Frame{Type: b[0], Stream: binary.BigEndian.Uint32(b[1:5]), Payload: b[frameHeader:]}
	if f.Type < FrameHello || f.Type > FrameWSClose {
		return Frame{}, fmt.Errorf("%w: unknown type %d", errFrame, f.Type)
	}
	if (f.Type == FrameHello) != (f.Stream == 0) {
		return Frame{}, fmt.Errorf("%w: type %d on stream %d", errFrame, f.Type, f.Stream)
	}
	switch f.Type {
	case FrameData:
		if len(f.Payload) > MaxDataChunk {
			return Frame{}, fmt.Errorf("%w: DATA of %d bytes", errFrame, len(f.Payload))
		}
	case FrameEnd:
		if len(f.Payload) != 0 {
			return Frame{}, fmt.Errorf("%w: END with a payload", errFrame)
		}
	case FrameWSMsg:
		if len(f.Payload) < 1 || (f.Payload[0] != wsText && f.Payload[0] != wsBinary) {
			return Frame{}, fmt.Errorf("%w: WSMSG without a kind byte", errFrame)
		}
	default:
		if len(f.Payload) > maxHeadJSON {
			return Frame{}, fmt.Errorf("%w: type %d payload of %d bytes", errFrame, f.Type, len(f.Payload))
		}
	}
	return f, nil
}

// WSMSG kinds.
const (
	wsText   byte = 1
	wsBinary byte = 2
)

// Header pairs are lowercase [name, value].
type headerPairs [][2]string

// reqHead is REQ's payload.
type reqHead struct {
	M    string      `json:"m"`
	P    string      `json:"p"`
	H    headerPairs `json:"h"`
	Body bool        `json:"body"`
}

// resHead is RES's payload.
type resHead struct {
	S    int         `json:"s"`
	H    headerPairs `json:"h"`
	Body bool        `json:"body"`
}

// wsOpenHead is WSOPEN's payload.
type wsOpenHead struct {
	P string      `json:"p"`
	H headerPairs `json:"h"`
}

// wsOKHead is WSOK's payload.
type wsOKHead struct {
	S int         `json:"s"`
	H headerPairs `json:"h,omitempty"`
}

// wsCloseBody is WSCLOSE's payload.
type wsCloseBody struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

// rstBody is RST's payload.
type rstBody struct {
	Reason string `json:"reason"`
}

// hopByHop headers never cross the relay (RFC 9110 section 7.6.1), nor do
// the WebSocket handshake headers the bridge regenerates.
var hopByHop = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authenticate": true, "proxy-authorization": true,
	"te": true, "trailer": true, "transfer-encoding": true, "upgrade": true, "proxy-connection": true,
}

var wsHandshake = map[string]bool{
	"sec-websocket-key": true, "sec-websocket-version": true, "sec-websocket-extensions": true, "sec-websocket-accept": true,
}

var methods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true,
}

// validPath accepts an origin-form request target: "/" then no scheme,
// no "//" authority, no control characters.
func validPath(p string) bool {
	if len(p) == 0 || len(p) > 8192 || p[0] != '/' || strings.HasPrefix(p, "//") {
		return false
	}
	for i := 0; i < len(p); i++ {
		if c := p[i]; c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

// validHeader rejects header names and values that would not survive (or
// would smuggle) an HTTP/1.1 hop.
func validHeader(name, value string) bool {
	if name == "" || len(name) > 256 || len(value) > 16<<10 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0) {
			return false
		}
	}
	for i := 0; i < len(value); i++ {
		if c := value[i]; c == '\r' || c == '\n' || c == 0 {
			return false
		}
	}
	return true
}

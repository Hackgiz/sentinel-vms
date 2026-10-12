// Package remote is the control link over the network (PLATFORM.md,
// "Control link wire protocol"): the management server's mTLS listener
// (Listener: enrollment, renewal and one WebSocket per recording server)
// and the recording server's dialer (Client).
//
// Each side sees the other through the same interfaces as link/inproc: the
// management server holds a link.Recorder per connected recording server,
// and the recording server reports to a link.Sink. Every value crosses as
// a link.Envelope of JSON.
package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"sentinel-linux/internal/link"
)

const (
	pingEvery      = 10 * time.Second
	silenceTimeout = 30 * time.Second // offline after 30 s without a message
	writeWait      = 10 * time.Second
	helloWait      = 10 * time.Second
	maxMessage     = 64 << 20 // snapshots, frames and command results with JPEGs
	// defaultRequestTimeout bounds a request whose context has no deadline,
	// so a recording server that stops answering can't hang a caller.
	defaultRequestTimeout = 5 * time.Minute
	eventAckTimeout       = 30 * time.Second
)

// ErrClosed is returned for requests on a connection that went away.
var ErrClosed = errors.New("control link: connection closed")

// appliedBody is config.applied's body: the Applied, or the error
// ApplyConfig returned.
type appliedBody struct {
	link.Applied
	Fault string `json:"fault,omitempty"`
}

// resultBody is a command's answer: the Result, or the transport error
// Command returned.
type resultBody struct {
	link.Result
	Fault string `json:"fault,omitempty"`
}

// peer is one end of a control-link WebSocket: serialized writes, and
// requests waiting for the reply whose re is their id.
type peer struct {
	ws *websocket.Conn

	wmu sync.Mutex

	pmu     sync.Mutex
	pending map[string]chan link.Envelope

	done      chan struct{}
	closeOnce sync.Once
}

func newPeer(ws *websocket.Conn) *peer {
	ws.SetReadLimit(maxMessage)
	return &peer{ws: ws, pending: map[string]chan link.Envelope{}, done: make(chan struct{})}
}

// send writes one message.
func (p *peer) send(env link.Envelope) error {
	select {
	case <-p.done:
		return ErrClosed
	default:
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_ = p.ws.SetWriteDeadline(time.Now().Add(writeWait))
	if err := p.ws.WriteJSON(env); err != nil {
		p.close()
		return fmt.Errorf("control link: send %s: %w", env.Type, err)
	}
	return nil
}

// reply answers msg.
func (p *peer) reply(msg link.Envelope, typ string, body any) error {
	env, err := link.Wrap(typ, link.NewID(), body)
	if err != nil {
		return err
	}
	env.Re = msg.ID
	env.Rev = msg.Rev
	return p.send(env)
}

// request sends env and waits for the message answering it.
func (p *peer) request(ctx context.Context, env link.Envelope) (link.Envelope, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
	ch := make(chan link.Envelope, 1)
	p.pmu.Lock()
	if _, dup := p.pending[env.ID]; dup {
		p.pmu.Unlock()
		return link.Envelope{}, fmt.Errorf("control link: message %s already in flight", env.ID)
	}
	p.pending[env.ID] = ch
	p.pmu.Unlock()
	defer func() {
		p.pmu.Lock()
		delete(p.pending, env.ID)
		p.pmu.Unlock()
	}()
	if err := p.send(env); err != nil {
		return link.Envelope{}, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return link.Envelope{}, ctx.Err()
	case <-p.done:
		return link.Envelope{}, ErrClosed
	}
}

// resolve hands a reply to the request waiting for it; false if none is.
func (p *peer) resolve(env link.Envelope) bool {
	p.pmu.Lock()
	ch, ok := p.pending[env.Re]
	p.pmu.Unlock()
	if ok {
		select {
		case ch <- env:
		default:
		}
	}
	return ok
}

// read returns the next message, allowing silenceTimeout of silence.
func (p *peer) read() (link.Envelope, error) {
	_ = p.ws.SetReadDeadline(time.Now().Add(silenceTimeout))
	var env link.Envelope
	if err := p.ws.ReadJSON(&env); err != nil {
		return env, err
	}
	return env, nil
}

// pinger sends a ping every pingEvery until the connection closes.
func (p *peer) pinger() {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-t.C:
			if err := p.send(link.Envelope{Type: link.TypePing, ID: link.NewID()}); err != nil {
				return
			}
		}
	}
}

// close ends the connection; waiting requests fail with ErrClosed.
func (p *peer) close() {
	p.closeOnce.Do(func() {
		close(p.done)
		_ = p.ws.Close()
	})
}

func (p *peer) closed() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func decode[T any](env link.Envelope) (T, error) {
	var v T
	if err := json.Unmarshal(env.Body, &v); err != nil {
		return v, fmt.Errorf("control link: decode %s: %w", env.Type, err)
	}
	return v, nil
}

// Package wss implements transport.Transport on top of TLS 1.3 + WebSocket.
package wss

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
)

// Default keepalive cadence: ping every 30s, treat the conn as dead if no
// pong arrives within 90s.
const (
	defaultPingInterval = 30 * time.Second
	defaultPongTimeout  = 90 * time.Second
)

// KeepaliveOpts configures WebSocket-layer ping/pong. Zero values yield the
// defaults; set Disable to skip the keepalive goroutine entirely (useful in
// tests where you don't want background ticks).
type KeepaliveOpts struct {
	PingInterval time.Duration
	PongTimeout  time.Duration
	Disable      bool
}

// Conn wraps a *websocket.Conn and speaks proto.Frame on top. Send is
// serialized internally; Recv is single-reader only.
type Conn struct {
	ws        *websocket.Conn
	identity  transport.Identity
	writeMu   sync.Mutex
	lastPong  atomic.Int64 // unix nanos; 0 = none yet
	kaStop    chan struct{}
	closeOnce sync.Once
}

// NewConn wraps an existing *websocket.Conn (typically after upgrade/dial)
// and starts WebSocket-layer keepalive with default cadence. Identity comes
// from the handshake.
func NewConn(ws *websocket.Conn, id transport.Identity) *Conn {
	return NewConnWithKeepalive(ws, id, KeepaliveOpts{})
}

// NewConnWithKeepalive is the same as NewConn but lets callers override the
// ping cadence — or disable it for unit tests.
func NewConnWithKeepalive(ws *websocket.Conn, id transport.Identity, opts KeepaliveOpts) *Conn {
	c := &Conn{ws: ws, identity: id}
	if opts.Disable {
		return c
	}
	pi := opts.PingInterval
	if pi <= 0 {
		pi = defaultPingInterval
	}
	pt := opts.PongTimeout
	if pt <= 0 {
		pt = defaultPongTimeout
	}
	c.kaStop = make(chan struct{})
	ws.SetPongHandler(func(string) error {
		c.lastPong.Store(time.Now().UnixNano())
		return nil
	})
	go c.keepaliveLoop(pi, pt)
	return c
}

// keepaliveLoop sends a WebSocket control-frame Ping every interval and
// closes the underlying conn if no Pong has arrived within timeout. Exits
// when kaStop closes or Send fails (which itself indicates a dead conn).
// lastPong is left at zero until a real pong arrives so callers can
// distinguish "never ponged" from "ponged recently"; staleness checks fall
// back to the conn's start time when lastPong is zero.
func (c *Conn) keepaliveLoop(interval, timeout time.Duration) {
	start := time.Now()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-c.kaStop:
			return
		case <-t.C:
			c.writeMu.Lock()
			err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(interval))
			c.writeMu.Unlock()
			if err != nil {
				return
			}
			last := start
			if v := c.lastPong.Load(); v != 0 {
				last = time.Unix(0, v)
			}
			if time.Since(last) > timeout {
				_ = c.ws.Close()
				return
			}
		}
	}
}

func (c *Conn) lastPongTime() time.Time {
	v := c.lastPong.Load()
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(0, v)
}

func (c *Conn) Send(ctx context.Context, f proto.Frame) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	b, err := f.Encode()
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.WriteMessage(websocket.BinaryMessage, b)
}

var errNotBinary = errors.New("wss: unexpected non-binary frame")

func (c *Conn) Recv(ctx context.Context) (proto.Frame, error) {
	if ctx.Err() != nil {
		return proto.Frame{}, ctx.Err()
	}
	mt, data, err := c.ws.ReadMessage()
	if err != nil {
		return proto.Frame{}, err
	}
	if mt != websocket.BinaryMessage {
		return proto.Frame{}, errNotBinary
	}
	return proto.Decode(data)
}

func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		if c.kaStop != nil {
			close(c.kaStop)
		}
	})
	return c.ws.Close()
}

func (c *Conn) PeerIdentity() transport.Identity { return c.identity }

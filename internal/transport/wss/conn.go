// Package wss implements transport.Transport on top of TLS 1.3 + WebSocket.
package wss

import (
	"context"
	"errors"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
)

// Conn wraps a *websocket.Conn and speaks proto.Frame on top. Send is
// serialized internally; Recv is single-reader only.
type Conn struct {
	ws       *websocket.Conn
	identity transport.Identity
	writeMu  sync.Mutex
}

// NewConn wraps an existing *websocket.Conn (typically after upgrade/dial).
// Identity comes from the handshake.
func NewConn(ws *websocket.Conn, id transport.Identity) *Conn {
	return &Conn{ws: ws, identity: id}
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

func (c *Conn) Close() error                     { return c.ws.Close() }
func (c *Conn) PeerIdentity() transport.Identity { return c.identity }

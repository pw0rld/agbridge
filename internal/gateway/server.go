// Package gateway is the rendezvous server. Phase 1 only echoes Ping → Pong.
// Routing, auth, audit land in Phase 2.
package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"

	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

// Run starts a gateway on addr. It returns the listener's actual address
// (useful when addr ends in ":0"). The server stops when ctx is cancelled.
func Run(ctx context.Context, addr string, tlsCfg *tls.Config) (net.Addr, error) {
	ln, err := wss.Listen(ctx, addr, tlsCfg)
	if err != nil {
		return nil, err
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go func() {
		for {
			c, err := ln.Accept(ctx)
			if err != nil {
				if !errors.Is(err, net.ErrClosed) && ctx.Err() == nil {
					log.Printf("gateway accept: %v", err)
				}
				return
			}
			go handleConn(ctx, c)
		}
	}()
	return ln.Addr(), nil
}

type connIO interface {
	Recv(context.Context) (proto.Frame, error)
	Send(context.Context, proto.Frame) error
	Close() error
}

func handleConn(ctx context.Context, c connIO) {
	defer c.Close()
	for {
		f, err := c.Recv(ctx)
		if err != nil {
			return
		}
		if f.Type == proto.FrameTypePing {
			_ = c.Send(ctx, proto.Frame{Type: proto.FrameTypePong, ReqID: f.ReqID})
		}
	}
}

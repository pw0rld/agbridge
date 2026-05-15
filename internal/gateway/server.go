// Package gateway is the rendezvous server. Phase 2 handles handshake-time
// auth, maintains a registry of online daemons, and routes Route frames from
// bridge to the named daemon target.
package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"

	"github.com/pw0rld/agbridge/internal/audit"
	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

// connIO is the subset of *wss.Conn used by handlers; declared here so
// tests can substitute stubs.
type connIO interface {
	Recv(context.Context) (proto.Frame, error)
	Send(context.Context, proto.Frame) error
	Close() error
}

// Run starts the gateway. It returns the listener's actual address, plus
// the CredRegistry so callers (main / tests) can Replace it on SIGHUP.
func Run(ctx context.Context, tlsCfg *tls.Config, cfg *config.GatewayConfig, aud *audit.Writer) (net.Addr, *CredRegistry, error) {
	ln, err := wss.Listen(ctx, cfg.Listen, tlsCfg)
	if err != nil {
		return nil, nil, err
	}
	reg := NewRegistry()
	creds := NewCredRegistry(cfg)
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
			go handleConn(ctx, c, creds, reg, aud)
		}
	}()
	return ln.Addr(), creds, nil
}

func handleConn(ctx context.Context, c connIO, creds *CredRegistry, reg *Registry, aud *audit.Writer) {
	defer c.Close()
	hello, ok := readHello(ctx, c)
	if !ok {
		_ = aud.Append(map[string]any{"event": "handshake_malformed"})
		return
	}
	switch hello.Role {
	case "daemon":
		handleDaemon(ctx, c, hello, creds, reg, aud)
	case "bridge":
		handleBridge(ctx, c, hello, creds, reg, aud)
	default:
		_ = c.Send(ctx, errFrame("unknown_role"))
		_ = aud.Append(map[string]any{"event": "auth_failed", "reason": "unknown_role", "name": hello.Name})
	}
}

func readHello(ctx context.Context, c connIO) (handshake.Hello, bool) {
	f, err := c.Recv(ctx)
	if err != nil || f.Type != proto.FrameTypeHello {
		return handshake.Hello{}, false
	}
	h, err := handshake.DecodeHello(f.Payload)
	if err != nil {
		return handshake.Hello{}, false
	}
	return h, true
}

func handleDaemon(ctx context.Context, c connIO, h handshake.Hello, creds *CredRegistry, reg *Registry, aud *audit.Writer) {
	entry, ok := creds.LookupDaemon(h.Name)
	if !ok || !auth.SecretMatches(h.Secret, entry.TokenHash) {
		_ = c.Send(ctx, errFrame("auth_failed"))
		_ = aud.Append(map[string]any{"event": "auth_failed", "role": "daemon", "name": h.Name})
		return
	}
	if err := reg.Register(h.Name, c); err != nil {
		_ = c.Send(ctx, errFrame("duplicate_daemon"))
		_ = aud.Append(map[string]any{"event": "auth_failed", "role": "daemon", "name": h.Name, "reason": "duplicate"})
		return
	}
	defer reg.Unregister(h.Name)
	_ = c.Send(ctx, proto.Frame{Type: proto.FrameTypeHelloAck})
	_ = aud.Append(map[string]any{"event": "auth_ok", "role": "daemon", "name": h.Name})
	// Phase 2: the daemon connection is read by bridgeRoutingLoop via the
	// registry. We just block until shutdown.
	<-ctx.Done()
}

func handleBridge(ctx context.Context, c connIO, h handshake.Hello, creds *CredRegistry, reg *Registry, aud *audit.Writer) {
	entry, ok := creds.LookupAgent(h.Name)
	if !ok || !auth.SecretMatches(h.Secret, entry.APIKeyHash) {
		_ = c.Send(ctx, errFrame("auth_failed"))
		_ = aud.Append(map[string]any{"event": "auth_failed", "role": "bridge", "name": h.Name})
		return
	}
	if !contains(entry.AllowedDaemons, h.TargetDaemon) {
		_ = c.Send(ctx, errFrame("daemon_not_allowed"))
		_ = aud.Append(map[string]any{"event": "authz_failed", "role": "bridge", "name": h.Name, "target": h.TargetDaemon})
		return
	}
	_ = c.Send(ctx, proto.Frame{Type: proto.FrameTypeHelloAck})
	_ = aud.Append(map[string]any{"event": "auth_ok", "role": "bridge", "name": h.Name, "target": h.TargetDaemon})

	apiKey := []byte(h.Secret)

	// Look up the daemon once at bridge-connect time. Phase 3 limitation:
	// if the daemon disconnects mid-session, bridge sees stale frames /
	// errors until reconnect (Phase 4 adds dynamic re-lookup + reconnect).
	dconn, ok := reg.Lookup(h.TargetDaemon)
	if !ok {
		_ = c.Send(ctx, errFrame("daemon_offline"))
		_ = aud.Append(map[string]any{"event": "route_failed", "reason": "daemon_offline", "target": h.TargetDaemon})
		return
	}

	// Pump daemon → bridge asynchronously so a single bridge request can
	// receive multiple response frames (chunks + complete).
	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			resp, err := dconn.Recv(bridgeCtx)
			if err != nil {
				return
			}
			if err := c.Send(bridgeCtx, resp); err != nil {
				return
			}
		}
	}()

	// bridge → daemon main loop.
	for {
		f, err := c.Recv(bridgeCtx)
		if err != nil {
			return
		}
		if f.Type != proto.FrameTypeRoute {
			continue
		}
		inner, err := auth.VerifyFrame(apiKey, f.Payload)
		if err != nil {
			_ = c.Send(bridgeCtx, errFrame("bad_mac"))
			_ = aud.Append(map[string]any{"event": "mac_failed", "agent": h.Name, "target": h.TargetDaemon})
			return
		}
		_ = aud.Append(map[string]any{"event": "route", "agent": h.Name, "target": h.TargetDaemon})
		if err := dconn.Send(bridgeCtx, proto.Frame{Type: proto.FrameTypeRoute, Payload: inner}); err != nil {
			_ = c.Send(bridgeCtx, errFrame("daemon_send_failed"))
			return
		}
	}
}

func errFrame(code string) proto.Frame {
	return proto.Frame{Type: proto.FrameTypeError, Payload: []byte(code)}
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

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
	"net/http"
	"time"

	"github.com/pw0rld/agbridge/internal/audit"
	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

// sessionReplacedTimeout caps how long the gateway will wait to push the
// session_replaced error frame down the old bridge's conn before yanking
// it. Kept short so a slow or already-half-dead peer doesn't delay the new
// bridge's setup.
const sessionReplacedTimeout = time.Second

// connIO is the subset of *wss.Conn used by handlers; declared here so
// tests can substitute stubs.
type connIO interface {
	Recv(context.Context) (proto.Frame, error)
	Send(context.Context, proto.Frame) error
	Close() error
}

// Instance bundles everything the caller (cmd/agbridge or tests) needs to
// drive a running gateway: the listener address, the credential registry
// (for hot reload via Replace), and the live-session table (for revoking
// sessions whose creds were removed/rotated).
type Instance struct {
	Addr     net.Addr
	Creds    *CredRegistry
	Sessions *SessionTable
}

// Run starts the gateway and returns an Instance handle.
// Equivalent to RunWithHandler(..., nil).
func Run(ctx context.Context, tlsCfg *tls.Config, cfg *config.GatewayConfig, aud *audit.Writer) (*Instance, error) {
	return RunWithHandler(ctx, tlsCfg, cfg, aud, nil)
}

// RunWithHandler is like Run but mounts extra under non-WS routes on the
// shared TLS:443 listener. Used to expose POST /v1/enroll.
func RunWithHandler(ctx context.Context, tlsCfg *tls.Config, cfg *config.GatewayConfig, aud *audit.Writer, extra http.Handler) (*Instance, error) {
	ln, err := wss.ListenWithHandler(ctx, cfg.Listen, tlsCfg, extra)
	if err != nil {
		return nil, err
	}
	reg := NewRegistry()
	inst := &Instance{
		Addr:     ln.Addr(),
		Creds:    NewCredRegistry(cfg),
		Sessions: NewSessionTable(),
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
			go handleConn(ctx, c, inst, reg, aud)
		}
	}()
	return inst, nil
}

func handleConn(ctx context.Context, c connIO, inst *Instance, reg *Registry, aud *audit.Writer) {
	defer c.Close()
	hello, ok := readHello(ctx, c)
	if !ok {
		_ = aud.Append(map[string]any{"event": "handshake_malformed"})
		return
	}
	switch hello.Role {
	case "daemon":
		handleDaemon(ctx, c, hello, inst, reg, aud)
	case "bridge":
		handleBridge(ctx, c, hello, inst, reg, aud)
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

func handleDaemon(ctx context.Context, c connIO, h handshake.Hello, inst *Instance, reg *Registry, aud *audit.Writer) {
	entry, ok := inst.Creds.LookupDaemon(h.Name)
	if !ok || !auth.SecretMatches(h.Secret, entry.TokenHash) {
		_ = c.Send(ctx, errFrame("auth_failed"))
		_ = aud.Append(map[string]any{"event": "auth_failed", "role": "daemon", "name": h.Name})
		return
	}
	proxy := NewDaemonProxy(c, h.ProtocolVersion)
	if err := reg.Register(h.Name, proxy); err != nil {
		_ = c.Send(ctx, errFrame("duplicate_daemon"))
		_ = aud.Append(map[string]any{"event": "auth_failed", "role": "daemon", "name": h.Name, "reason": "duplicate"})
		return
	}
	defer reg.Unregister(h.Name)
	_, release := inst.Sessions.Register(&Session{
		Role: "daemon", Name: h.Name, KeyHash: entry.TokenHash, Conn: c,
	})
	defer release()
	_ = c.Send(ctx, proto.Frame{Type: proto.FrameTypeHelloAck})
	_ = aud.Append(map[string]any{"event": "auth_ok", "role": "daemon", "name": h.Name})
	// Pair the auth_ok with a conn_close on the way out so the audit log is
	// a complete connect/disconnect ledger — operators consuming the log
	// can detect when a daemon goes offline without polling.
	defer func() {
		_ = aud.Append(map[string]any{"event": "conn_close", "role": "daemon", "name": h.Name})
	}()
	// Read loop owned by handleDaemon (not by any individual bridge): each
	// frame is routed to the currently-subscribed bridge writer or dropped
	// if no bridge is connected. When the conn dies (revocation, network),
	// Recv returns and we unregister.
	for {
		f, err := c.Recv(ctx)
		if err != nil {
			return
		}
		_ = proxy.Deliver(ctx, f)
	}
}

func handleBridge(ctx context.Context, c connIO, h handshake.Hello, inst *Instance, reg *Registry, aud *audit.Writer) {
	entry, ok := inst.Creds.LookupAgent(h.Name)
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
	// "last writer wins" semantics for bridges: a second bridge with the
	// same identity replaces the first. The old bridge's daemon→bridge
	// writer has been rebound to the new conn, so any pending tool call
	// on the old bridge would hang forever waiting for a response that
	// now lands elsewhere. We need to sever the old conn — but a naive
	// Close would make the old bridge's supervisor auto-reconnect and
	// kick the new one out, looping forever. Instead, send a
	// session_replaced error frame first so the old bridge's router can
	// recognise the eviction and exit instead of reconnecting (see
	// runReader + superviseBridgeConn in cmd/agbridge/bridge.go).
	replaced, _, release := inst.Sessions.ReplaceByRoleName(&Session{
		Role: "bridge", Name: h.Name, KeyHash: entry.APIKeyHash, Conn: c,
	})
	defer release()
	if replaced != nil {
		sendCtx, sendCancel := context.WithTimeout(ctx, sessionReplacedTimeout)
		_ = replaced.Send(sendCtx, errFrame("session_replaced"))
		sendCancel()
		_ = replaced.Close()
		_ = aud.Append(map[string]any{
			"event": "bridge_replaced", "name": h.Name,
			"reason": "newer bridge with same identity connected",
		})
	}
	_ = c.Send(ctx, proto.Frame{Type: proto.FrameTypeHelloAck})
	_ = aud.Append(map[string]any{"event": "auth_ok", "role": "bridge", "name": h.Name, "target": h.TargetDaemon})
	// Pair the auth_ok with a conn_close on the way out so the audit log
	// is a complete connect/disconnect ledger (see handleDaemon for the
	// daemon-side equivalent).
	defer func() {
		_ = aud.Append(map[string]any{"event": "conn_close", "role": "bridge", "name": h.Name, "target": h.TargetDaemon})
	}()

	apiKey := []byte(h.Secret)

	proxy, ok := reg.Lookup(h.TargetDaemon)
	if !ok {
		_ = c.Send(ctx, errFrame("daemon_offline"))
		_ = aud.Append(map[string]any{"event": "route_failed", "reason": "daemon_offline", "target": h.TargetDaemon})
		return
	}
	// Cross-generation pairing is refused: a v0.0.3-bridge believes the
	// daemon still enforces path allowlists, and a v0.0.5-bridge would
	// bootstrap a working directory from a daemon that has none. Neither
	// failure is visible to the peers without an explicit rejection.
	if bridgeVer := h.EffectiveVersion(); bridgeVer != proxy.ProtoVersion() {
		_ = c.Send(ctx, errFrame("protocol_mismatch"))
		_ = aud.Append(map[string]any{
			"event": "route_failed", "reason": "protocol_mismatch", "target": h.TargetDaemon,
			"bridge_version": bridgeVer, "daemon_version": proxy.ProtoVersion(),
		})
		return
	}

	// Subscribe to daemon→bridge frames for the duration of this session.
	// Unsubscribe removes exactly this subscription — an old bridge's
	// teardown can never clear a newer bridge's writer (the classic
	// replace-race hang the single writer slot used to cause).
	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sub := proxy.Subscribe(h.Name, func(_ context.Context, f proto.Frame) error {
		return c.Send(bridgeCtx, f)
	})
	defer proxy.Unsubscribe(sub)

	// bridge → daemon main loop.
	for {
		f, err := c.Recv(bridgeCtx)
		if err != nil {
			return
		}
		switch f.Type {
		case proto.FrameTypeRoute:
			inner, err := auth.VerifyFrame(apiKey, f.Payload)
			if err != nil {
				_ = c.Send(bridgeCtx, errFrame("bad_mac"))
				_ = aud.Append(map[string]any{"event": "mac_failed", "agent": h.Name, "target": h.TargetDaemon})
				return
			}
			_ = aud.Append(map[string]any{"event": "route", "agent": h.Name, "target": h.TargetDaemon, "bytes": len(f.Payload)})
			if err := proxy.Send(bridgeCtx, proto.Frame{Type: proto.FrameTypeRoute, Payload: inner}); err != nil {
				_ = c.Send(bridgeCtx, errFrame("daemon_send_failed"))
				return
			}
		case proto.FrameTypeNoiseInit, proto.FrameTypeNoiseRekey:
			// Noise handshake / rekey frames: forward opaquely. Integrity
			// and authenticity are enforced by Noise itself (msg1 carries
			// Poly1305 tags, peer pub revealed for ACL on the daemon
			// side). Gateway only relays bytes and audits the event.
			_ = aud.Append(map[string]any{"event": "noise_relay", "agent": h.Name, "target": h.TargetDaemon, "frame_type": f.Type, "bytes": len(f.Payload)})
			if err := proxy.Send(bridgeCtx, f); err != nil {
				_ = c.Send(bridgeCtx, errFrame("daemon_send_failed"))
				return
			}
		default:
			// ignore stray frames silently
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

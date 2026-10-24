// Package cliclient implements the one-shot bridge connection used by
// the agbridge CLI subcommands. Each Invoke() call dials the gateway,
// completes the Hello handshake (and optional Noise IK), invokes one
// tool handler, and tears the connection down.
//
// This is the v0.0.2 replacement for the long-running MCP stdio bridge.
package cliclient

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/e2e"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

// Options carries the per-invocation bridge config + target.
// Built from on-disk state.json + global flags by the cmd/agbridge layer.
type Options struct {
	GatewayURL          string
	AgentName           string
	APIKey              string
	CertPin             string
	GatewayTLSMode      string
	E2EMode             string
	BridgeStaticKeyPath string
	// DaemonPubkey is the target daemon's X25519 public key, base64-
	// encoded. Same shape as config.BridgeConfig.DaemonPubkey. Decoded
	// at the Noise IK call site via e2e.PubFromBase64.
	DaemonPubkey string
	Target       string
	// TLSConfig optionally overrides the TLS config used to dial the
	// gateway. When nil (production default), dial builds the config
	// from GatewayTLSMode + CertPin via auth.RuntimeTLSConfig.
	// Primarily for tests; advanced users can also inject custom mTLS.
	TLSConfig *tls.Config
}

// OptionsFromBridgeConfig translates an existing v0.0.1 BridgeConfig
// to Options. Used by the CLI when loading state.json + applying a
// target override.
func OptionsFromBridgeConfig(cfg *config.BridgeConfig) Options {
	return Options{
		GatewayURL:          cfg.GatewayURL,
		AgentName:           cfg.AgentName,
		APIKey:              cfg.APIKey,
		CertPin:             cfg.CertPin,
		GatewayTLSMode:      cfg.GatewayTLSMode,
		E2EMode:             cfg.E2EMode,
		BridgeStaticKeyPath: cfg.BridgeStaticKeyPath,
		DaemonPubkey:        cfg.DaemonPubkey,
		Target:              cfg.TargetDaemon,
	}
}

// Handler is the function shape we accept. Matches existing
// (*router).execHandler / readFileHandler / etc signatures.
type Handler func(ctx context.Context, args json.RawMessage) (any, error)

// Invoke dials the gateway, completes the Hello handshake (and Noise
// IK if e2e enabled), calls fn with a ready-to-use Client, then
// closes the connection. Returns the handler's result.
//
// fn is called once. Its job is to build the args JSON and invoke
// one tool handler against client.
func Invoke(ctx context.Context, opts Options, fn func(ctx context.Context, c *Client) (any, error)) (any, error) {
	c, err := dial(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer c.Close()
	return fn(ctx, c)
}

// Client wraps a live, handshook bridge connection. Exposes the
// existing tool handlers (exec, read_file, write_file, etc) bound to
// this connection via the embedded Router.
type Client struct {
	conn      *wss.Conn
	opts      Options
	router    *Router
	cancel    context.CancelFunc
	session   *e2e.Session // nil when E2EMode == "disabled"
	sessionID [8]byte
	closeOnce sync.Once
	closeErr  error
}

// Router returns the live router for callers (CLI subcommands) that
// need to invoke handler methods directly.
func (c *Client) Router() *Router { return c.router }

// Close tears down the runReader goroutine + WSS connection. Safe to
// call concurrently from signal handlers and deferred cleanup paths;
// only the first call performs the teardown.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
			c.cancel = nil
		}
		if c.conn != nil {
			c.closeErr = c.conn.Close()
			c.conn = nil
		}
	})
	return c.closeErr
}

// dial implements the v0.0.2 connect flow:
//  1. wss.Dial with TLS (pin or webpki)
//  2. send Hello{Role:"bridge"}, recv HelloAck
//  3. (e2e) Noise IK XX
//
// Returns a Client ready for one handler call.
func dial(ctx context.Context, opts Options) (*Client, error) {
	var tlsCfg *tls.Config
	if opts.TLSConfig != nil {
		tlsCfg = opts.TLSConfig
	} else {
		var err error
		tlsCfg, err = auth.RuntimeTLSConfig(opts.GatewayTLSMode, opts.CertPin)
		if err != nil {
			return nil, fmt.Errorf("tls config: %w", err)
		}
	}
	conn, err := wss.Dial(ctx, opts.GatewayURL, transport.Credentials{}, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("wss dial: %w", err)
	}
	helloBytes, err := handshake.Hello{
		Role:            "bridge",
		Name:            opts.AgentName,
		Secret:          opts.APIKey,
		TargetDaemon:    opts.Target,
		ProtocolVersion: handshake.ProtocolVersion,
	}.Encode()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("encode hello: %w", err)
	}
	if err := conn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: helloBytes}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("send hello: %w", err)
	}
	ack, err := conn.Recv(ctx)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("recv hello ack: %w", err)
	}
	if ack.Type != proto.FrameTypeHelloAck {
		_ = conn.Close()
		return nil, fmt.Errorf("unexpected ack type: %v", ack.Type)
	}
	c := &Client{conn: conn, opts: opts}
	if opts.E2EMode != "disabled" {
		if err := c.handshakeE2E(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("noise ik: %w", err)
		}
	}

	// Build the Router AFTER Hello and Noise IK complete. runReader
	// reads off conn.Recv and would race the synchronous Noise frames
	// if started any earlier — handshakeE2E.Send/Recv would lose their
	// reply to runReader's loop.
	bcfg := &config.BridgeConfig{
		GatewayURL:          opts.GatewayURL,
		AgentName:           opts.AgentName,
		APIKey:              opts.APIKey,
		GatewayTLSMode:      opts.GatewayTLSMode,
		CertPin:             opts.CertPin,
		TargetDaemon:        opts.Target,
		DaemonPubkey:        opts.DaemonPubkey,
		E2EMode:             opts.E2EMode,
		BridgeStaticKeyPath: opts.BridgeStaticKeyPath,
	}
	readerCtx, cancel := context.WithCancel(ctx)
	rt, err := NewRouter(readerCtx, conn, []byte(opts.APIKey), bcfg)
	if err != nil {
		cancel()
		_ = conn.Close()
		return nil, fmt.Errorf("router: %w", err)
	}
	c.router = rt
	c.cancel = cancel
	// Install the negotiated E2E session on the router so sendInner
	// encrypts subsequent Route frames with the right keys + sid.
	if c.session != nil {
		rt.installSession(c.session, c.sessionID)
	}
	go rt.runReader()
	return c, nil
}

// handshakeE2E runs the Noise IK initiator role over the live conn,
// using the bridge static key from disk and the target daemon's public
// key from opts. On success the session is stored on the Client so
// subsequent tool calls can encrypt/decrypt Route payloads.
//
// Ported from cmd/agbridge/bridge.go (doBridgeNoiseHandshake + the
// e2e setup half of newRouter). The CLI variant skips the router's
// install-session / replace-conn dance — Client owns the conn until
// Close().
func (c *Client) handshakeE2E(ctx context.Context) error {
	bridgeKey, err := e2e.LoadStatic(c.opts.BridgeStaticKeyPath)
	if err != nil {
		return fmt.Errorf("load static key %q: %w", c.opts.BridgeStaticKeyPath, err)
	}
	daemonPub, err := e2e.PubFromBase64(c.opts.DaemonPubkey)
	if err != nil {
		return fmt.Errorf("parse daemon_pubkey: %w", err)
	}
	prologue := []byte(fmt.Sprintf("agbridge/v1|%s|%s", c.opts.GatewayURL, c.opts.Target))

	var sid [8]byte
	if _, err := rand.Read(sid[:]); err != nil {
		return fmt.Errorf("sid rand: %w", err)
	}
	init, err := e2e.NewInitiator(bridgeKey, daemonPub, prologue)
	if err != nil {
		return fmt.Errorf("new initiator: %w", err)
	}
	msg1, err := init.WriteMessage1()
	if err != nil {
		return fmt.Errorf("write msg1: %w", err)
	}
	if err := c.conn.Send(ctx, proto.Frame{
		Type:    proto.FrameTypeNoiseInit,
		ReqID:   hex.EncodeToString(sid[:]),
		Payload: msg1,
	}); err != nil {
		return fmt.Errorf("send NoiseInit: %w", err)
	}
	resp, err := c.conn.Recv(ctx)
	if err != nil {
		return fmt.Errorf("recv NoiseResp: %w", err)
	}
	if resp.Type == proto.FrameTypeError {
		return fmt.Errorf("daemon rejected handshake: %q", resp.Payload)
	}
	if resp.Type != proto.FrameTypeNoiseResp {
		return fmt.Errorf("expected NoiseResp, got type %d", resp.Type)
	}
	sess, err := init.ReadMessage2(resp.Payload)
	if err != nil {
		return fmt.Errorf("read msg2: %w", err)
	}
	c.session = sess
	c.sessionID = sid
	return nil
}

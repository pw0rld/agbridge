// Package cliclient implements the one-shot bridge connection used by
// the agbridge CLI subcommands. Each Invoke() call dials the gateway,
// completes the Hello handshake (and optional Noise IK), invokes one
// tool handler, and tears the connection down.
//
// This is the v0.0.2 replacement for the long-running MCP stdio bridge.
package cliclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
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
// this connection.
type Client struct {
	conn *wss.Conn
	opts Options
}

// Close tears down the WSS connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// dial implements the v0.0.2 connect flow:
//  1. wss.Dial with TLS (pin or webpki)
//  2. send Hello{Role:"bridge"}, recv HelloAck
//  3. (e2e) Noise IK XX
//
// Returns a Client ready for one handler call.
func dial(ctx context.Context, opts Options) (*Client, error) {
	tlsCfg, err := auth.RuntimeTLSConfig(opts.GatewayTLSMode, opts.CertPin)
	if err != nil {
		return nil, fmt.Errorf("tls config: %w", err)
	}
	conn, err := wss.Dial(ctx, opts.GatewayURL, transport.Credentials{}, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("wss dial: %w", err)
	}
	helloBytes, err := handshake.Hello{
		Role:         "bridge",
		Name:         opts.AgentName,
		Secret:       opts.APIKey,
		TargetDaemon: opts.Target,
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
	// Noise IK is added in Task 1.5 — leave a hook here.
	return c, nil
}

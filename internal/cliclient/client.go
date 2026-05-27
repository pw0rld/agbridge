// Package cliclient implements the one-shot bridge connection used by
// the agbridge CLI subcommands. Each Invoke() call dials the gateway,
// completes the Hello handshake (and optional Noise IK), invokes one
// tool handler, and tears the connection down.
//
// This is the v0.0.2 replacement for the long-running MCP stdio bridge.
package cliclient

import (
	"github.com/pw0rld/agbridge/internal/config"
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

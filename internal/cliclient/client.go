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
	// DaemonPubkey is the decoded X25519 public key (32 bytes) of the
	// target daemon. config.BridgeConfig stores the base64-encoded form
	// as a string; the CLI layer is responsible for decoding it before
	// populating this field. OptionsFromBridgeConfig therefore leaves
	// this nil — base64 decoding can fail and this constructor does not
	// return an error.
	DaemonPubkey []byte
	Target       string
}

// OptionsFromBridgeConfig translates an existing v0.0.1 BridgeConfig
// to Options. Used by the CLI when loading state.json + applying a
// target override.
//
// Note: DaemonPubkey is intentionally not copied here because
// config.BridgeConfig.DaemonPubkey is base64-encoded (string) while
// Options.DaemonPubkey is the decoded form ([]byte). Callers that need
// E2E must decode and set Options.DaemonPubkey themselves.
func OptionsFromBridgeConfig(cfg *config.BridgeConfig) Options {
	return Options{
		GatewayURL:          cfg.GatewayURL,
		AgentName:           cfg.AgentName,
		APIKey:              cfg.APIKey,
		CertPin:             cfg.CertPin,
		GatewayTLSMode:      cfg.GatewayTLSMode,
		E2EMode:             cfg.E2EMode,
		BridgeStaticKeyPath: cfg.BridgeStaticKeyPath,
		Target:              cfg.TargetDaemon,
	}
}

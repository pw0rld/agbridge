package cliclient

import (
	"testing"

	"github.com/pw0rld/agbridge/internal/config"
)

func TestNewOptionsFromBridgeConfig(t *testing.T) {
	cfg := &config.BridgeConfig{
		GatewayURL:   "wss://gw.example/",
		AgentName:    "alice",
		APIKey:       "k",
		TargetDaemon: "lab01",
		E2EMode:      "disabled",
	}
	opts := OptionsFromBridgeConfig(cfg)
	if opts.GatewayURL != "wss://gw.example/" || opts.AgentName != "alice" {
		t.Fatalf("OptionsFromBridgeConfig: %+v", opts)
	}
	if opts.Target != "lab01" {
		t.Fatalf("target: %q", opts.Target)
	}
}

package cliclient

import (
	"crypto/tls"
	"testing"

	"github.com/pw0rld/agbridge/internal/config"
)

// TestInvokeSignature is filled in later (Task 2.0 extracts shared
// fixture). For now it just ensures the Invoke signature exists.
func TestInvokeSignature(t *testing.T) {
	_ = Invoke
	_ = (*Client).Close
	_ = Handler(nil)
}

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

func TestOptionsTLSConfigOverride(t *testing.T) {
	cfg := &tls.Config{ServerName: "test"}
	opts := Options{TLSConfig: cfg}
	if opts.TLSConfig != cfg {
		t.Fatalf("TLSConfig field not present on Options")
	}
}

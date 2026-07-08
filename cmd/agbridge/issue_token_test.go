package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pw0rld/agbridge/internal/gateway"
)

// gatewayTokenStore opens the tokens.json that issue-token writes next
// to the gateway config.
func gatewayTokenStore(t *testing.T, cfgPath string) (*gateway.TokenStore, error) {
	t.Helper()
	return gateway.NewTokenStore(filepath.Join(filepath.Dir(cfgPath), "tokens.json"))
}

// writeMinimalGatewayConfig writes a gateway YAML sufficient for
// issue-token to load; issue-token only needs Listen/PublicURL and the
// tokens.json sibling path.
func writeMinimalGatewayConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "gateway.yaml")
	body := "listen: 127.0.0.1:8443\npublic_url: wss://gw.example.com/\naudit_path: " + filepath.Join(dir, "audit.jsonl") + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestIssueTokenBridgeRequiresTarget(t *testing.T) {
	cfg := writeMinimalGatewayConfig(t)
	cmd := newIssueTokenCmd()
	cmd.SetArgs([]string{"--config", cfg, "--role", "bridge", "--name", "laptop"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when bridge has no --target")
	}
}

func TestIssueTokenBridgeAcceptsMultipleTargets(t *testing.T) {
	cfg := writeMinimalGatewayConfig(t)
	cmd := newIssueTokenCmd()
	cmd.SetArgs([]string{"--config", cfg, "--role", "bridge", "--name", "laptop",
		"--target", "lab01", "--target", "lab02", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute with two targets: %v", err)
	}
	// The token file should now hold a bridge token with both targets.
	store, err := gatewayTokenStore(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tk := range store.List() {
		if tk.Name == "laptop" {
			found = true
			if len(tk.Targets) != 2 || tk.Targets[0] != "lab01" || tk.Targets[1] != "lab02" {
				t.Fatalf("stored targets = %v, want [lab01 lab02]", tk.Targets)
			}
		}
	}
	if !found {
		t.Fatal("issued token not found in store")
	}
}
